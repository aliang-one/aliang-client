package services

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/storage"
)

// kernel_endpoint_resolver.go — 内核(AliangCode/agentcore)的出口解析器
// (spec 二期 2026-11-27 §5.0 每 run 单次解析 / §5.1 黏性 / §5.2 负载 /
// §5.4 出口尊重限流)。
//
// 与 EndpointService 的分工(评审定案「Select 保持无粘性——路由层共享不污染」):
//   - 服务层 Select 只做「协议/健康/模型过滤 + 优先级排序 + builtin 兜底」,
//     56432 路由层与内核共用;粘性(会话→行)、出口指定、层内最少负载是内核
//     专属语义,全部收在本层,服务层不感知 session 概念;
//   - 本文件只做 resolver 本体,不碰 agent_ai_agentcore.go——宿主接线
//     (三解析点收拢/Acquire per-run/OnRequestError)在 Task 4b,agentcore_llm
//     退役在 4c。
//
// 解析顺序(§5.0 伪码):
//  1. 出口指定行(egress_kernel=1)存在:
//     a. enabled=0 → 出口暂停保留,落 auto 调度(Info 日志);
//     b. enabled=1 → 恒返回它,压过健康/模型/并发;唯一例外是限流窗(见 d);
//     d. enabled=1 且 rate_limited_until 未到 → 不发请求,返回本地限流错误
//     (含恢复时刻文案),不 failover——避免每 30s 戳 5h 限额窗;熔断
//     (disabled+cooldown>0)不算例外,仍照走(用户配置压倒一切)。
//  2. auto 调度:黏性优先(命中且行仍满足候选闸→直接用,重刷 TTL)→
//     按 priority 分层,顶层内选 in-flight 最少者(并列随机)→ 选中并重黏;
//     全空回落 builtin(复用 svc.Select 的兜底路径,必打日志),兜底行不黏。
//
// MarkUsed/ReportFailure/Acquire 的调用时机都归宿主(4b 已接线:agentCoreRunEgress
// 每 run Acquire 一次 + MarkUsed 打点,OnRequestError 闭包按行 id ReportFailure),
// 本层只提供选择与 Acquire 包装,不碰计数列——路由层共享的 Select 不能被内核
// 打点污染。

// kernelEndpointProtocol 内核只说 anthropic 协议(出口行的 Validate 双层守卫
// 也只允许 anthropic 行设出口,processor/config/endpoint.go §Validate)。
const kernelEndpointProtocol = "anthropic"

const (
	// kernelStickyTTL 黏性绑定 TTL(秒,spec §5.1:1h 命中刷新)。
	kernelStickyTTL int64 = 3600
	// kernelStickyMaxEntries 粘性 map 容量上限;超过即全量清扫过期项(§5.1)。
	// 上限按「单机会话数」拍:每项几十字节,4096 项不到 1MB。
	kernelStickyMaxEntries = 4096
)

// ErrKernelEgressRateLimited 出口指定行处于 429 限流窗内(spec §5.4):内核
// 不发请求,调用方直接把本地错误透传给用户;4b 可用 errors.Is 判定后组装
// 「出口限流中,恢复于 HH:MM」形态的回复。错误文本本身已含恢复时刻。
var ErrKernelEgressRateLimited = errors.New("出口限流中")

// kernelEndpointSvc 是 resolver 对 EndpointService 的窄依赖面。接口注入让
// 测试能 mock 选择/负载/并发(无需真库与真信号量);生产传 *EndpointService
// (编译期由本文件底部的 var _ 校验)。
type kernelEndpointSvc interface {
	Select(protocol, model, explicitID string, excludeIDs ...string) (*config.LLMEndpoint, error)
	ListEndpoints() ([]*config.LLMEndpoint, error)
	GetEndpoint(id string) (*config.LLMEndpoint, error)
	Inflight() map[string]int
	Acquire(id string, maxConcurrency int) (func(), error)
}

// kernelEndpointResolution 是单次解析的结果(§5.0:run 开始解析一次,行随
// agentcore.Config 传递)。
//
// 契约:err != nil 时 Endpoint 恒为 nil——调用方不得在错误路径使用行;
// RateLimitedUntil/Egress 仍有效,供调用方组装结构化错误与日志。
type kernelEndpointResolution struct {
	// Endpoint 选中的整行(builtin 兜底时即内置行)。nil 仅与 err != nil 同现。
	Endpoint *config.LLMEndpoint
	// Sticky 本次是否命中既有粘性绑定(首黏/重黏/出口/兜底均为 false)。
	Sticky bool
	// Egress 是否来自出口指定行(enabled=1 命中;限流错误路径也为 true)。
	Egress bool
	// RateLimitedUntil 出口行限流截止(unix 秒);仅限流错误路径 >0。
	RateLimitedUntil int64
}

// stickyEntry 是一条会话黏性绑定(§5.1):lastHit 是最近命中 unix 秒,TTL
// 以它为基准(命中刷新),过期在读时懒清除。
type stickyEntry struct {
	endpointID string
	lastHit    int64
}

// kernelEndpointResolver 持有内核专属调度状态(粘性 map),生产进程单例持有
// (4b 接线)。Resolve/Acquire 并发安全:粘性 map 串行在 mu 上,svc 调用各自
// 有锁。同一 session 并发 Resolve 可能各选各行、后写覆写先写——良性(两请求
// 各自成立,下回合统一到后写行)。
type kernelEndpointResolver struct {
	svc kernelEndpointSvc
	// egressRow 出口指定行获取函数;nil 时用默认实现(ListEndpoints 过滤
	// EgressKernel=1,取首个——「全表至多一行=1」由 store.SetEgressKernel
	// 事务守卫)。测试注入假函数免真写库。行不存在返回 (nil, nil)。
	egressRow func() (*config.LLMEndpoint, error)

	mu     sync.Mutex
	sticky map[string]stickyEntry

	// now 时钟注入(测试控制 TTL/限流判定),生产恒 time.Now。
	now func() time.Time
}

// newKernelEndpointResolver 构造内核出口解析器。svc 为 nil 时返回 nil——
// registry 未构造成功(GetEndpointService 失败)的降级形态,调用方(4b)
// 判 nil 后回落 env/settings 继承链,不允许 panic。
func newKernelEndpointResolver(svc kernelEndpointSvc, egressRow func() (*config.LLMEndpoint, error)) *kernelEndpointResolver {
	if svc == nil {
		return nil
	}
	if egressRow == nil {
		egressRow = func() (*config.LLMEndpoint, error) {
			list, err := svc.ListEndpoints()
			if err != nil {
				return nil, err
			}
			for _, e := range list {
				if e.EgressKernel {
					return e, nil
				}
			}
			return nil, nil
		}
	}
	return &kernelEndpointResolver{
		svc:       svc,
		egressRow: egressRow,
		sticky:    map[string]stickyEntry{},
		now:       time.Now,
	}
}

// 编译期契约:生产注入的 *EndpointService 必须满足窄依赖面。
var _ kernelEndpointSvc = (*EndpointService)(nil)

// Resolve 按 §5.0 伪码解析内核出口。sessionID 是粘性键(内存 extract 路径用
// projectPath、compact 用 session.id,由 4b 决定);model 为空表示跳过模型
// 过滤。excludeIDs 是本回合排除的行(4b 宿主:Acquire 排队超时 exclude 后
// 重选,§5.2「满/超时在解析层重选而非 run 内等待」;不计失败不动健康)。
// 失败时错误分三类:ErrKernelEgressRateLimited(出口限流,本地错误不
// failover)、ErrNoEndpoint(池空且 builtin 也不可用)、wrapped raw(注册表读
// 挂,服务端故障)。
func (r *kernelEndpointResolver) Resolve(sessionID, model string, excludeIDs ...string) (kernelEndpointResolution, error) {
	now := r.now().Unix()
	excluded := map[string]bool{}
	for _, id := range excludeIDs {
		if id != "" {
			excluded[id] = true
		}
	}

	// 1) 出口指定行。
	eg, err := r.egressRow()
	if err != nil {
		// 读出口行失败降级为「无出口」继续走 auto:出口是路由优化不是硬依赖,
		// auto 池里的 ListEndpoints 会以同样的方式失败并自然报错,这里不吞。
		logger.Warn(fmt.Sprintf("endpoint: kernel resolver 读出口指定行失败,按无出口处理: %v", err))
		eg = nil
	}
	if eg != nil && excluded[eg.ID] {
		// 出口行被宿主排除(排队超时重选):本回合当无出口,落 auto;下一 run
		// 不带 exclude 时出口恢复首选。
		logger.Info(fmt.Sprintf("endpoint: kernel resolver 出口 %s 被排除(并发排队超时重选),本回合走 auto", eg.ID))
		eg = nil
	}
	if eg != nil {
		if !eg.Enabled {
			// 出口暂停保留(spec §4:enabled=0 与 egress_kernel=1 可共存),
			// 落 auto 调度;enabled 0→1 时出口自动恢复生效。
			logger.Info(fmt.Sprintf("endpoint: kernel resolver 出口 %s 停用中,回退 auto", eg.ID))
		} else {
			// §5.4 唯一例外:限流窗未到,不发请求、不 failover。
			if eg.RateLimitedUntil > now {
				hhmm := time.Unix(eg.RateLimitedUntil, 0).Format("15:04")
				logger.Warn(fmt.Sprintf("endpoint: kernel resolver 出口 %s 限流中(至 %s),不发请求不 failover", eg.ID, hhmm))
				return kernelEndpointResolution{
					Egress:           true,
					RateLimitedUntil: eg.RateLimitedUntil,
				}, fmt.Errorf("%w，恢复于 %s（可稍后重试）", ErrKernelEgressRateLimited, hhmm)
			}
			// 恒返回它:压过健康(锁存/熔断照走)、模型、并发。出口是用户配置,
			// 错误留给上游真实返回(§5.0 1b/§5.3「出口行被锁存仍按出口语义走」)。
			logger.Info(fmt.Sprintf("endpoint: kernel resolver 出口 %s 命中(压过健康/模型/并发)", eg.ID))
			return kernelEndpointResolution{Endpoint: eg, Egress: true}, nil
		}
	}

	// 2) auto 调度:黏性优先。黏住的行被排除(排队超时重选)时视同未黏——
	// 落到下方首黏路径重新选择并覆写绑定。
	if sessionID != "" {
		if id, ok := r.stickyLookup(sessionID, now); ok && !excluded[id] {
			ep, gerr := r.svc.GetEndpoint(id)
			if gerr == nil && r.kernelEndpointCandidate(ep, model, now) {
				// 命中且行仍满足候选闸:直接用,重刷 TTL(§5.1)。
				r.stickyBind(sessionID, id, now)
				logger.Info(fmt.Sprintf("endpoint: kernel resolver 会话 %s 黏住 %s", sessionID, id))
				return kernelEndpointResolution{Endpoint: ep, Sticky: true}, nil
			}
			// 行不满足候选闸(§5.1:enabled=0/熔断/限流/删除/模型漂移):不清
			// 旧绑定,重黏次优并覆写;选不出(池空/兜底/读挂)时不覆写,即等效
			// 「保留旧绑定」——旧行恢复后仍优先回黏。
			reason := kernelStickyUnusableReason(ep, gerr, model, now)
			res, serr := r.resolveAuto(model, id, excludeIDs...)
			if serr == nil && res.Endpoint != nil && !res.Endpoint.Builtin {
				logger.Info(fmt.Sprintf("endpoint: kernel resolver 会话 %s 重黏 %s→%s(原因: %s)",
					sessionID, id, res.Endpoint.ID, reason))
				r.stickyBind(sessionID, res.Endpoint.ID, now)
			}
			return res, serr
		}
	}
	// 首黏:session 带键且选中池内行才写绑定。builtin 兜底刻意不黏——黏上
	// 兜底行会让 BYO 恢复后该会话仍被钉在兜底行一整个 TTL。
	res, serr := r.resolveAuto(model, "", excludeIDs...)
	if serr == nil && sessionID != "" && res.Endpoint != nil && !res.Endpoint.Builtin {
		r.stickyBind(sessionID, res.Endpoint.ID, now)
	}
	return res, serr
}

// resolveAuto 无粘性上下文(或重黏)时的调度:候选枚举 → priority 分层 →
// 层内 in-flight 最少(并列随机)→ 池空复用 svc.Select 的 builtin 兜底。
// stuckID 非空时排除黏住的旧行(重黏语义:选出的必是另一行);excludeIDs
// 是宿主传的本回合排除集(Acquire 排队超时重选,§5.2),同样作用于兜底的
// svc.Select(排除集非空时 builtin 兜底是唯一落点)。
func (r *kernelEndpointResolver) resolveAuto(model, stuckID string, excludeIDs ...string) (kernelEndpointResolution, error) {
	now := r.now().Unix()
	excluded := map[string]bool{}
	for _, id := range excludeIDs {
		if id != "" {
			excluded[id] = true
		}
	}
	// 兜底 Select 的排除集 = 宿主排除集 + 重黏的旧行(Select 的变参尾部语义)。
	fallbackExclude := make([]string, 0, len(excludeIDs)+1)
	fallbackExclude = append(fallbackExclude, excludeIDs...)
	if stuckID != "" {
		fallbackExclude = append(fallbackExclude, stuckID)
	}
	list, err := r.svc.ListEndpoints()
	if err != nil {
		return kernelEndpointResolution{}, fmt.Errorf("endpoint: kernel resolver 读注册表失败: %w", err)
	}
	var cands []*config.LLMEndpoint
	for _, e := range list {
		if e.ID == storage.BuiltinEndpointID { // 与 selectAuto 同口径:内置行只做兜底
			continue
		}
		if stuckID != "" && e.ID == stuckID { // 重黏:排除刚失效的旧行
			continue
		}
		if excluded[e.ID] { // 宿主排除(排队超时重选):本回合不再入选
			continue
		}
		if !r.kernelEndpointCandidate(e, model, now) {
			continue
		}
		cands = append(cands, e)
	}
	if len(cands) == 0 {
		// 池空(或只剩被排除的旧行)→ builtin 兜底:直接走 svc.Select——
		// 它对空池回落 fallbackBuiltin(enabled/协议/健康过滤 + 必打日志),
		// 与路由层共享同一条兜底路径,不在这里复刻内置行的判定。
		if len(fallbackExclude) > 0 {
			ep, serr := r.svc.Select(kernelEndpointProtocol, model, "", fallbackExclude...)
			if serr != nil {
				return kernelEndpointResolution{}, serr
			}
			return kernelEndpointResolution{Endpoint: ep}, nil
		}
		ep, serr := r.svc.Select(kernelEndpointProtocol, model, "")
		if serr != nil {
			return kernelEndpointResolution{}, serr
		}
		return kernelEndpointResolution{Endpoint: ep}, nil
	}

	// 分层:取最高优先级层(数值最小,对齐 selectAuto 的 priority 升序语义)。
	top := cands[0]
	for _, e := range cands[1:] {
		if e.Priority < top.Priority {
			top = e
		}
	}
	var layer []*config.LLMEndpoint
	for _, e := range cands {
		if e.Priority == top.Priority {
			layer = append(layer, e)
		}
	}

	// 层内最少 in-flight(§5.2):负载快照取自 svc.Inflight()——它的真数依赖
	// 「内核选中后统一 Acquire」(4b 接线 per-run 一次),无记录的行按 0。
	inflight := r.svc.Inflight()
	minLoad := -1
	var least []*config.LLMEndpoint
	for _, e := range layer {
		load := inflight[e.ID]
		if minLoad < 0 || load < minLoad {
			minLoad = load
			least = []*config.LLMEndpoint{e}
			continue
		}
		if load == minLoad {
			least = append(least, e)
		}
	}
	pick := least[0]
	if len(least) > 1 { // 层内并列随机(spec §5.2,不加权重不粘请求序)
		pick = least[rand.Intn(len(least))]
	}
	logger.Info(fmt.Sprintf("endpoint: kernel resolver auto 选中 %s(priority=%d, 层内 in-flight=%d, 候选 %d 行)",
		pick.ID, pick.Priority, inflight[pick.ID], len(cands)))
	return kernelEndpointResolution{Endpoint: pick}, nil
}

// kernelEndpointCandidate 是 resolver 侧的候选闸,过滤序列与
// EndpointService.selectAuto 逐条对齐(enabled ∧ protocol ∧ auto_group ∧
// 非 429 冷却 ∧ 健康可用(含冷却到期) ∧ 模型过滤),helper 直接复用服务层的
// 同名私有函数——两边漂移只可能来自手动改代码,注释钉住对应关系。
// 附带与 selectAuto 同款的懒恢复副作用(冷却到期即回 ok),保证「内核走的行」
// 与「路由层走的行」健康态收敛一致;mock 注入(测试)没有具体类型则跳过。
func (r *kernelEndpointResolver) kernelEndpointCandidate(e *config.LLMEndpoint, model string, now int64) bool {
	if e == nil || !e.Enabled || !endpointProtocolMatch(e, kernelEndpointProtocol) || !e.AutoGroup {
		return false
	}
	if e.RateLimitedUntil > now {
		return false
	}
	if !endpointHealthUsable(e, now) {
		return false
	}
	if es, ok := r.svc.(*EndpointService); ok {
		es.recoverIfNeeded(e, now)
	}
	return endpointModelAllowed(e.Models, model)
}

// kernelStickyUnusableReason 把「黏住的行为何不满足候选闸」翻成日志文案
// (§5.1:重黏必打日志,含原因)。判定顺序与 kernelEndpointCandidate 一致,
// 首个命中的闸即原因。
func kernelStickyUnusableReason(e *config.LLMEndpoint, getErr error, model string, now int64) string {
	if getErr != nil {
		return "行已删除"
	}
	if !e.Enabled {
		return "行已停用"
	}
	if e.RateLimitedUntil > now {
		return fmt.Sprintf("限流中(恢复于 %s)", time.Unix(e.RateLimitedUntil, 0).Format("15:04"))
	}
	if e.HealthStatus != "ok" && e.CooldownUntil <= 0 {
		return "熔断锁存(认证失败/欠费)"
	}
	if e.HealthStatus != "ok" && e.CooldownUntil > now {
		return fmt.Sprintf("熔断冷却中(恢复于 %s)", time.Unix(e.CooldownUntil, 0).Format("15:04"))
	}
	if !endpointProtocolMatch(e, kernelEndpointProtocol) {
		return "协议不再匹配"
	}
	if !e.AutoGroup {
		return "已移出 auto 池"
	}
	if !endpointModelAllowed(e.Models, model) {
		return "模型不再匹配"
	}
	return "未知原因"
}

// stickyLookup 读会话绑定,顺带做 TTL 懒清除(§5.1:过期项在读时删,不跑
// 后台定时器)。返回绑定的 endpoint id 与是否存在(未过期)。
func (r *kernelEndpointResolver) stickyLookup(sessionID string, now int64) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ent, ok := r.sticky[sessionID]
	if !ok {
		return "", false
	}
	if now-ent.lastHit > kernelStickyTTL {
		delete(r.sticky, sessionID)
		return "", false
	}
	return ent.endpointID, true
}

// stickyBind 写/刷新会话绑定(命中刷新与首黏/重黏覆写共用写入口)。map 超限
// 时全量清扫过期项(§5.1);全新鲜时允许临时超限一项,下一轮过期再收。
func (r *kernelEndpointResolver) stickyBind(sessionID, endpointID string, now int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sticky) >= kernelStickyMaxEntries {
		for k, v := range r.sticky {
			if now-v.lastHit > kernelStickyTTL {
				delete(r.sticky, k)
			}
		}
	}
	r.sticky[sessionID] = stickyEntry{endpointID: endpointID, lastHit: now}
}

// Acquire 包装 svc.Acquire(§5.0:计数作用域=每 run 一次,run 内多次 LLM 请求
// 共享一个槽位;release 挂 run 终结 defer 全路径)。maxConcurrency<=0 时以
// 行上 max_concurrency 为准(仍 0=不限并发)。宿主 agentCoreRunEgress 每 run
// 调用一次;排队超限返回 ErrQueueTimeout,调用方 exclude 该行重选而非在
// run 内等待(§5.2)。
func (r *kernelEndpointResolver) Acquire(ep *config.LLMEndpoint, maxConcurrency int) (func(), error) {
	if ep == nil {
		return nil, ErrNoEndpoint
	}
	return r.svc.Acquire(ep.ID, maxConcurrency)
}
