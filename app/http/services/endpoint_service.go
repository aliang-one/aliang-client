package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/storage"
)

// LLM endpoint registry 的 service 层(spec 2026-11-25 §6 路由算法的实现体):
// 选择(协议/健康/模型过滤 + builtin 兜底)、失败状态机(计数/摘除/429 冷却/
// 懒恢复)、per-endpoint 并发信号量、上游探测(测试连接/拉模型)。
//
// 进程归属:core 进程单例持有(GetEndpointService),56431 管理面与 56432 数据面
// 同进程,健康态/信号量无跨进程同步问题(spec §12)。本包不 import inbound/http
// (import 纪律:路由层在 Task 3 通过包内接口适配本类型)。

// ErrNoEndpoint 无任何可用 endpoint(全部不健康/冷却中/不存在)。路由层映射 503
// no_endpoint_available。
var ErrNoEndpoint = errors.New("endpoint: 无可用 endpoint(候选为空且内置行不健康)")

// ErrEndpointNotAvailable 显式指定的 endpoint 不可用(不存在/未启用/协议不匹配)。
// 与 ErrNoEndpoint 分开成两个哨兵:显式指定失败是调用方的点名错误,路由层映射
// 404 endpoint_not_available;无候选是路由池整体枯竭,映射 503。
var ErrEndpointNotAvailable = errors.New("endpoint: 指定 endpoint 不可用(不存在/未启用/协议不匹配)")

// ErrQueueTimeout 并发排队失败:等待超过 30s 或队列已满(>32 个等待者)。
// 路由层映射 503 endpoint_queue_timeout。
var ErrQueueTimeout = errors.New("endpoint: 并发排队超时或队列已满")

// 并发闸参数(spec §6.3:队列上限 32、等待上限 30s)与探测超时(§8/§11:2s)。
const (
	endpointMaxQueue     = 32
	endpointAcquireWait  = 30 * time.Second
	endpointProbeTimeout = 2 * time.Second
	// endpointModelsTimeout 是 /v1/models 模型列表拉取的 HTTP 超时。与 TCP 拨测
	// 分离:实测有上游网关拉列表要 5-6s(2026-11-26 maids.ink 装机实证),2s 会把
	// 可用网关误判成「拉取失败」;10s 覆盖慢网关,拨不通仍由 TCP 层 2s 快速失败。
	endpointModelsTimeout = 10 * time.Second
)

// getPolicyFallback 是读组策略失败时的保底值:与 store 层默认策略同形
// (失败摘除开、连续 3 次摘除、冷却 300s、429 冷却 60s)。摘除开着是刻意的——
// 策略读不到时宁可摘除保护上游,也不能让失败 endpoint 被无限捶。
func getPolicyFallback() config.EndpointGroupPolicy {
	return config.EndpointGroupPolicy{
		FailureDisable:       true,
		FailureThreshold:     3,
		CooldownSec:          300,
		RateLimitCooldownSec: 60,
	}
}

// endpointGate 是单个 endpoint 的进程内并发闸:buffered chan 当信号量,
// cap = max_concurrency;waiting 记当前排队人数(队列深度硬上限的依据)。
// 语义:acquire = 收令牌,release = 还令牌;通道初始必须预填满 cap 个令牌
// (空通道意味着零可用额度,首个 Acquire 会干等 30s)。
type endpointGate struct {
	sem     chan struct{}
	waiting int
}

// newEndpointGate 造闸并预填令牌。
func newEndpointGate(capacity int) *endpointGate {
	g := &endpointGate{sem: make(chan struct{}, capacity)}
	for i := 0; i < capacity; i++ {
		g.sem <- struct{}{}
	}
	return g
}

// EndpointService 是 endpoint registry 的路由/健康/并发实现。所有公开方法
// 并发安全;健康态的读改写串行在 mu 上,保证失败计数不丢(并发请求同时失败
// 时,read-modify-write 若不串行会互相覆盖,摘除永不触发)。
type EndpointService struct {
	store *storage.EndpointStore

	mu       sync.Mutex
	sems     map[string]*endpointGate
	inflight map[string]int

	// waitTimeout 是排队等待上限;独立成字段只为测试注入短超时(生产恒 30s)。
	waitTimeout time.Duration
	// httpClient 用于上游探测(测试连接/拉模型);测试可注入替换。
	httpClient *http.Client
	// builtinProbeAddr 是 builtin 行测试连接的拨测目标(平台代理入口);
	// 空时用生产默认 127.0.0.1:56432,测试注入假地址断言失败路径。
	builtinProbeAddr string
}

var (
	endpointServiceOnce   sync.Once
	sharedEndpointService *EndpointService
)

// GetEndpointService 返回 core 进程内的 registry 单例:走 auth 共享句柄打开
// aliang.data 两张表并幂等 seed 内置行。构造失败(库打不开/迁移失败)时返回
// nil——调用方必须判 nil 并走降级路径(Task 3 的 aiSelector 不注入、Task 5
// 回退 EffectiveAgentCoreLLM/默认链),56432 AI path 保持原有透明代理行为,
// 不允许 panic。与 getAuthSessionDB 同用 sync.Once:失败也缓存,不重试。
func GetEndpointService() *EndpointService {
	endpointServiceOnce.Do(func() {
		db, err := auth.SharedAuthSessionDB()
		if err != nil {
			logger.Error(fmt.Sprintf("endpoint: 打开共享会话库失败,endpoint 路由降级为不可用: %v", err))
			return
		}
		store, err := storage.NewEndpointStoreFromDB(db)
		if err != nil {
			logger.Error(fmt.Sprintf("endpoint: 初始化 endpoint store 失败,endpoint 路由降级为不可用: %v", err))
			return
		}
		sharedEndpointService = newEndpointServiceWithStore(store)
		logger.Info("endpoint: registry service 单例就绪(内置行已 seed,健康态/信号量由本进程持有)")
	})
	return sharedEndpointService
}

// newEndpointServiceWithStore 是注入构造:测试用它挂 :memory: store,生产走
// GetEndpointService 的共享句柄构造。不对外暴露第二份单例语义。
func newEndpointServiceWithStore(store *storage.EndpointStore) *EndpointService {
	return &EndpointService{
		store:       store,
		sems:        map[string]*endpointGate{},
		inflight:    map[string]int{},
		waitTimeout: endpointAcquireWait,
		httpClient:  &http.Client{Timeout: endpointModelsTimeout},
	}
}

// ---- 选择(spec §6.1)--------------------------------------------------------

// Select 选一个 endpoint:
//   - explicitID 非空:直取该行,只校验存在/启用/协议匹配(健康与 auto_group
//     旁路——显式指定是管理面/内核点名用途),失败返回 ErrEndpointNotAvailable;
//     excludeID 对显式路径不生效(显式点名就是显式,重试排除只作用于自动选择);
//   - 否则:enabled ∧ 协议 toggle ∧ 非 429 冷却 ∧ (ok ∨ 冷却已到期,到期即懒恢复)
//     ∧ 模型过滤 ∧ auto_group ∧ id ∉ excludeIDs,按 priority、created_at 升序取首;
//   - 候选为空 → 回落内置行(若其 enabled/协议/健康可用)→ 否则 ErrNoEndpoint。
//
// excludeIDs 是重试真切换的机制(final-review 必改 2):路由层重试 attempt 把
// 上一失败 attempt 的 endpoint id 传入,Select 排除后天然命中下一候选——不传
// 时单次失败不达摘除阈值,Select 仍会返回同一 endpoint,重试=同 endpoint 两枪。
//
// 错误契约(供 Task 3 路由层映射):两个哨兵 ErrNoEndpoint(503)/
// ErrEndpointNotAvailable(404)可直接 errors.Is 判定;store 读失败返回的是
// wrapped raw error(非哨兵),路由层应映射 500 而非 503——注册表读挂是服务端
// 故障,不是「无可用 endpoint」。
//
// 内置行刻意不进自动候选池:它 priority 恒 0(两个协议 toggle 都开、不限模型),
// 放进池里会永远排在 BYO 前面,BYO 语义整体失效。内置行的角色是兜底(回落),
// 这也是 §6.1「空候选 → builtin 健康则回落」可被触达的唯一一致读法。
//
// 签名演进:旧三参形态 Select(protocol, model, explicitID) → 现四参
// Select(protocol, model, explicitID, excludeIDs ...)。变参尾部追加让既有
// 调用点(agentCoreSelectEndpoint 等单次选择)零改动编译,重试路径显式传 id。
func (s *EndpointService) Select(protocol, model, explicitID string, excludeIDs ...string) (*config.LLMEndpoint, error) {
	if explicitID != "" {
		return s.selectExplicit(protocol, explicitID)
	}
	ep, err := s.selectAuto(protocol, model, excludeIDs)
	if err == nil {
		return ep, nil
	}
	if !errors.Is(err, ErrNoEndpoint) {
		return nil, err
	}
	return s.fallbackBuiltin(protocol)
}

// selectExplicit 显式点名:存在/启用/协议三道校验,其余(健康、auto、模型)全放行。
func (s *EndpointService) selectExplicit(protocol, id string) (*config.LLMEndpoint, error) {
	e, err := s.store.Get(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %s 不存在", ErrEndpointNotAvailable, id)
	}
	if !e.Enabled {
		return nil, fmt.Errorf("%w: %s 已停用", ErrEndpointNotAvailable, id)
	}
	if !endpointProtocolMatch(e, protocol) {
		return nil, fmt.Errorf("%w: %s 未开启 %s 协议", ErrEndpointNotAvailable, id, protocol)
	}
	return e, nil
}

// selectAuto 自动池选择。健康态语义(spec §6.4):cooldown_until <= now 即视为
// 可用并当场懒恢复(回 ok、清零计数),不跑后台定时器。excludeIDs 非空时
// 过滤指定 id(重试真切换;排除后空候选由 Select 统一回落 builtin)。
func (s *EndpointService) selectAuto(protocol, model string, excludeIDs []string) (*config.LLMEndpoint, error) {
	list, err := s.store.List()
	if err != nil {
		return nil, fmt.Errorf("endpoint: 读注册表失败: %w", err)
	}
	excluded := map[string]bool{}
	for _, id := range excludeIDs {
		if id != "" {
			excluded[id] = true
		}
	}
	now := time.Now().Unix()
	var best *config.LLMEndpoint
	for _, e := range list {
		if e.ID == storage.BuiltinEndpointID { // 内置行只做兜底,见 Select 注释
			continue
		}
		if excluded[e.ID] { // 重试排除:失败过的行本轮不再入选
			continue
		}
		if !e.Enabled || !endpointProtocolMatch(e, protocol) || !e.AutoGroup {
			continue
		}
		if e.RateLimitedUntil > now {
			continue
		}
		if e.HealthStatus != "ok" && e.CooldownUntil > now {
			continue
		}
		s.recoverIfNeeded(e, now)
		if !endpointModelAllowed(e.Models, model) {
			continue
		}
		// 严格优先级:priority 升序,同优先级按 created_at 升序(spec §6.1 第 4 步);
		// 再以 id 兜底保证同一秒创建的行选择结果稳定。
		if best == nil || e.Priority < best.Priority ||
			(e.Priority == best.Priority && (e.CreatedAt < best.CreatedAt ||
				(e.CreatedAt == best.CreatedAt && e.ID < best.ID))) {
			best = e
		}
	}
	if best == nil {
		return nil, ErrNoEndpoint
	}
	logger.Info(fmt.Sprintf("endpoint: select id=%s protocol=%s model=%q priority=%d base_url=%s",
		best.ID, protocol, model, best.Priority, best.BaseURL))
	return best, nil
}

// fallbackBuiltin 空候选时的兜底(spec §8):内置行若 enabled/协议/健康可用则
// 放行(模型过滤刻意不查——内置行 models=[] 不限制,且模型不匹配正是回落场景)。
// 回落是静默降级,必须留可查日志(spec §15)。
func (s *EndpointService) fallbackBuiltin(protocol string) (*config.LLMEndpoint, error) {
	b, err := s.store.Get(storage.BuiltinEndpointID)
	if err != nil {
		return nil, ErrNoEndpoint
	}
	now := time.Now().Unix()
	if !b.Enabled || !endpointProtocolMatch(b, protocol) ||
		b.RateLimitedUntil > now || (b.HealthStatus != "ok" && b.CooldownUntil > now) {
		logger.Warn(fmt.Sprintf("endpoint: 无候选且内置行不可用(protocol=%s enabled=%v health=%s cooldown_until=%d rate_limited_until=%d),返回 no_endpoint_available",
			protocol, b.Enabled, b.HealthStatus, b.CooldownUntil, b.RateLimitedUntil))
		return nil, ErrNoEndpoint
	}
	s.recoverIfNeeded(b, now)
	logger.Info(fmt.Sprintf("endpoint: fallback to builtin id=%s protocol=%s(自动池无可用候选)", storage.BuiltinEndpointID, protocol))
	return b, nil
}

// recoverIfNeeded 懒恢复:非 ok 状态且冷却已到期的行,当场清零健康态并回 ok
// (spec §6.4:下次 select 时 cooldown_until<=now 即回 ok,不跑后台定时器)。
// 刻意只在「状态确实非 ok」时触发:ok 但计数>0 的行处于「计数中」阶段
// (连续失败未达阈值),它的计数只能被 ReportSuccess 或下一条失败改写——
// 每次路过 select 都清零会让摘除永远攒不满。
//
// 池模式守卫(final-review finding 5,严格不变量 6):failure_disable=0 时
// 本方法不写任何健康列——「池模式不写任何健康状态」含清理陈旧态(残留
// disabled 原样保留,直到管理员显式重置或切回摘除模式)。行的可用性已由
// 调用点的过滤语义放行(status!=ok 且冷却过期即视为可用),跳过写库不影响
// 本轮路由。
//
// 并发(quality fix Important 1):UpdateHealth 必须在 s.mu 内执行,与
// ReportFailure 同一把锁。冷却到期正是「流量恢复再失败」的 designed operating
// point——恢复写与失败摘除交错时,锁外的读改写会把刚写入的摘除/429 冷却
// 覆盖复活。锁内重读当前行再判定,进一步收窄窗口。
// 调用前提:该行的 rate_limited 已过期(两个调用点都先做了过滤),因此清
// rate_limited_until 不会抹掉仍在生效的 429 冷却。
func (s *EndpointService) recoverIfNeeded(e *config.LLMEndpoint, now int64) {
	if e.HealthStatus == "ok" {
		return
	}
	if e.CooldownUntil > now {
		return // 未到期,不该被恢复(调用点已过滤,双保险)
	}
	if !s.currentPolicy().FailureDisable {
		// 池模式不写库(不变量 6):行在过滤语义上已可用,陈旧健康态原样保留。
		logger.Info(fmt.Sprintf("endpoint: %s 冷却已到期(池模式,健康态不落库,陈旧态保留)", e.ID))
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// 锁内重读:进锁前到现在可能有并发 ReportFailure 改了状态/冷却,
	// 以最新行再判一次,避免用陈旧快照覆盖新写入。
	cur, err := s.store.Get(e.ID)
	if err != nil {
		logger.Warn(fmt.Sprintf("endpoint: %s 懒恢复重读失败(下次 select 重试): %v", e.ID, err))
		return
	}
	if cur.HealthStatus == "ok" || cur.CooldownUntil > now || cur.RateLimitedUntil > now {
		return // 已被并发路径处理(恢复/再摘除/再限流),不覆盖
	}
	if err := s.store.UpdateHealth(e.ID, storage.HealthUpdate{
		Status: "ok", Failures: 0, CooldownUntil: 0, RateLimitedUntil: 0, LastErr: "",
	}); err != nil {
		// 恢复写失败不拦路由:时间语义上它已经可用,下次 select 会再试。
		logger.Warn(fmt.Sprintf("endpoint: %s 懒恢复写库失败(下次 select 重试): %v", e.ID, err))
		return
	}
	e.HealthStatus = "ok"
	e.ConsecutiveFailures = 0
	logger.Info(fmt.Sprintf("endpoint: %s 冷却到期懒恢复(ok,计数清零)", e.ID))
}

// endpointProtocolMatch 按 protocol 字符串查对应 toggle;未知协议不匹配任何行
// (Select 走到 ErrNoEndpoint,而不是静默错路由)。
func endpointProtocolMatch(e *config.LLMEndpoint, protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "anthropic":
		return e.ProtocolAnthropic
	case "openai":
		return e.ProtocolOpenAI
	default:
		return false
	}
}

// endpointModelAllowed 模型过滤(spec §6.1 第 2 步):models=[] 或请求未带模型
// 视为不限;models 非法 JSON 时防御性按「不限制」处理(Validate 已挡非法,这里
// 是路由期不被脏数据 503 的双保险)。
func endpointModelAllowed(modelsJSON, requestedModel string) bool {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return true
	}
	raw := strings.TrimSpace(modelsJSON)
	if raw == "" || raw == "[]" {
		return true
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		logger.Warn(fmt.Sprintf("endpoint: models 非法 JSON(%q),按不限制放行: %v", raw, err))
		return true
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == requestedModel {
			return true
		}
	}
	return false
}

// ---- 失败状态机(spec §6.2)--------------------------------------------------

// ReportFailure 按 §6.2 表格落状态:
//
//	429            → 不计数,rate_limited_until = now + 组策略 429 冷却(不摘除)
//	5xx / 连接失败 → consecutive_failures+1;达 threshold(且 failure_disable=1)
//	                 → disabled + cooldown_until = now + 组策略冷却
//	其它 4xx       → 完全忽略(不计数不落状态)
//
// 池模式(组策略 failure_disable=0,不变量 6):失败只打日志,不写任何健康列。
// 健康态读改写持 mu 串行,防并发失败互相覆盖计数。
func (s *EndpointService) ReportFailure(id string, status int, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	policy := s.currentPolicy()
	e, err := s.store.Get(id)
	if err != nil {
		// 行在失败回报窗口内被删:记日志放行,路由层马上要断开这次请求。
		logger.Warn(fmt.Sprintf("endpoint: ReportFailure 找不到 %s(可能刚被删除), status=%d err=%q", id, status, errMsg))
		return
	}
	now := time.Now().Unix()

	switch {
	case status == http.StatusTooManyRequests: // 429
		if !policy.FailureDisable {
			logger.Warn(fmt.Sprintf("endpoint: %s 429(池模式不落状态) err=%q", id, errMsg))
			return
		}
		until := now + int64(policy.RateLimitCooldownSec)
		if err := s.store.UpdateHealth(id, storage.HealthUpdate{
			Status:           e.HealthStatus, // 429 不改健康状态
			Failures:         e.ConsecutiveFailures,
			CooldownUntil:    e.CooldownUntil,
			RateLimitedUntil: until,
			LastErr:          errMsg,
		}); err != nil {
			logger.Warn(fmt.Sprintf("endpoint: %s 429 冷却写库失败: %v", id, err))
			return
		}
		logger.Warn(fmt.Sprintf("endpoint: %s 429 限流,冷却至 %d(%ds,不计数不摘除) err=%q",
			id, until, policy.RateLimitCooldownSec, errMsg))

	case status >= 500 || status <= 0: // 5xx / 连接失败 / 超时(status<=0 = 无 HTTP 状态)
		if !policy.FailureDisable {
			logger.Warn(fmt.Sprintf("endpoint: %s 上游失败 status=%d(池模式不落状态) err=%q", id, status, errMsg))
			return
		}
		failures := e.ConsecutiveFailures + 1
		u := storage.HealthUpdate{
			Status:           e.HealthStatus,
			Failures:         failures,
			CooldownUntil:    e.CooldownUntil,
			RateLimitedUntil: e.RateLimitedUntil,
			LastErr:          errMsg,
		}
		// threshold<=0 不摘除:0 的「连续失败 0 次就摘」是退化语义,会形成
		// 失败→摘除→秒恢复的循环;用户要立即摘除应设 threshold=1。
		if policy.FailureThreshold > 0 && failures >= policy.FailureThreshold {
			u.Status = "disabled"
			u.CooldownUntil = now + int64(policy.CooldownSec)
			logger.Warn(fmt.Sprintf("endpoint: %s 连续失败 %d 次(阈值 %d)摘除,冷却至 %d status=%d err=%q",
				id, failures, policy.FailureThreshold, u.CooldownUntil, status, errMsg))
		} else {
			logger.Warn(fmt.Sprintf("endpoint: %s 上游失败 status=%d,连续失败 %d/%d err=%q",
				id, status, failures, policy.FailureThreshold, errMsg))
		}
		if err := s.store.UpdateHealth(id, u); err != nil {
			logger.Warn(fmt.Sprintf("endpoint: %s 失败计数写库失败: %v", id, err))
		}

	default:
		// 其它 4xx:透传错误但不计数(spec §6.2 不变量 5)。
		logger.Info(fmt.Sprintf("endpoint: %s 客户端错误 status=%d 不计数 err=%q", id, status, errMsg))
	}
}

// ReportSuccess 成功清零(spec §6.2 最后一行)。全清(状态/计数/两路冷却/摘要):
// 请求真实走通是它可用的最强证据;池模式不写(不变量 6,池模式下本就没有
// 需要清的计数)。
func (s *EndpointService) ReportSuccess(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.currentPolicy().FailureDisable {
		logger.Info(fmt.Sprintf("endpoint: %s 成功(池模式,健康态不落库)", id))
		return
	}
	if err := s.store.UpdateHealth(id, storage.HealthUpdate{
		Status: "ok", Failures: 0, CooldownUntil: 0, RateLimitedUntil: 0, LastErr: "",
	}); err != nil {
		logger.Warn(fmt.Sprintf("endpoint: %s 成功清零写库失败: %v", id, err))
		return
	}
	logger.Info(fmt.Sprintf("endpoint: %s 成功,健康态清零", id))
}

// ResetHealth 手动重置健康(管理面「重置健康」按钮,spec §6.4):立即清零全部
// 健康列并立即可选,不等冷却到期。
//
// 缺失 id 契约:store 的 UpdateHealth 是 UPDATE ... WHERE id=?,0 行受影响也
// 返回 nil,本方法保持返回 nil——调用方(handler)如需区分「重置成功」与
// 「id 不存在」,自行先 Get 判 404;此处不做存在性预查,避免 TOCTOU 双查。
func (s *EndpointService) ResetHealth(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.store.UpdateHealth(id, storage.HealthUpdate{
		Status: "ok", Failures: 0, CooldownUntil: 0, RateLimitedUntil: 0, LastErr: "",
	})
	if err != nil {
		return fmt.Errorf("endpoint: 重置健康失败: %w", err)
	}
	logger.Info(fmt.Sprintf("endpoint: %s 手动重置健康(ok,计数/冷却清零)", id))
	return nil
}

// currentPolicy 读组策略;失败按保底值(摘除开)。持 mu 调用。
func (s *EndpointService) currentPolicy() config.EndpointGroupPolicy {
	p, err := s.store.GetPolicy()
	if err != nil || p == nil {
		logger.Warn(fmt.Sprintf("endpoint: 读组策略失败,按默认(失败摘除开)处理: %v", err))
		return getPolicyFallback()
	}
	return *p
}

// ---- 并发闸(spec §6.3)------------------------------------------------------

// Acquire 拿 endpoint 的并发令牌,返回 release(必须恰好调用一次,重复调用被
// sync.Once 挡住)。maxConcurrency<=0 时以库里该行的 max_concurrency 为准
// (仍为 0 = 不限并发,不建闸)——路由层只需把 Select 返回的行转交,无需自己
// 携带容量值。排队上限 32、等待上限 30s,超限/超时返回 ErrQueueTimeout。
func (s *EndpointService) Acquire(id string, maxConcurrency int) (func(), error) {
	if maxConcurrency <= 0 {
		e, err := s.store.Get(id)
		if err != nil {
			// 读不到行不静默当「不限并发」放行:那会在管理面刚删行的窗口里
			// 悄悄撤掉并发保护。降级为不限并发(可用性优先),但必须留痕。
			logger.Warn(fmt.Sprintf("endpoint: Acquire 读 %s 的 max_concurrency 失败,降级为不限并发: %v", id, err))
		} else {
			maxConcurrency = e.MaxConcurrency
		}
	}

	s.mu.Lock()
	if maxConcurrency <= 0 {
		// 不限并发:不建闸,只记 inflight 供管理面展示用量。
		s.inflight[id]++
		s.mu.Unlock()
		return s.makeRelease(id, nil), nil
	}

	g := s.sems[id]
	if g == nil {
		g = newEndpointGate(maxConcurrency)
		s.sems[id] = g
	}
	// 用户改了 max_concurrency:仅在闸完全空闲(无持有者=通道满,无排队者)时
	// 重建;旧代 release 闭包写的是旧通道,被遗弃无害(容量恰等于其遗留令牌数,
	// send 永不阻塞)。
	if cap(g.sem) != maxConcurrency && g.waiting == 0 && len(g.sem) == cap(g.sem) {
		g = newEndpointGate(maxConcurrency)
		s.sems[id] = g
	}

	select {
	case <-g.sem: // 有空闲令牌,立即拿
		s.inflight[id]++
		s.mu.Unlock()
		return s.makeRelease(id, g), nil
	default:
	}
	if g.waiting >= endpointMaxQueue {
		s.mu.Unlock()
		logger.Warn(fmt.Sprintf("endpoint: %s 并发队列已满(≥%d),直接拒绝 endpoint_queue_timeout", id, endpointMaxQueue))
		return nil, ErrQueueTimeout
	}
	g.waiting++
	wait := s.waitTimeout
	s.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-g.sem:
		s.mu.Lock()
		g.waiting--
		s.inflight[id]++
		s.mu.Unlock()
		return s.makeRelease(id, g), nil
	case <-timer.C:
		// 超时与放行存在赛跑:令牌可能恰好在超时瞬间到达。先非阻塞试探回收,
		// 拿到就当成功(调用方正常使用),拿不到才真超时。
		select {
		case <-g.sem:
			s.mu.Lock()
			g.waiting--
			s.inflight[id]++
			s.mu.Unlock()
			return s.makeRelease(id, g), nil
		default:
			s.mu.Lock()
			g.waiting--
			s.mu.Unlock()
			logger.Warn(fmt.Sprintf("endpoint: %s 并发排队等待超过 %v,拒绝 endpoint_queue_timeout", id, wait))
			return nil, ErrQueueTimeout
		}
	}
}

// makeRelease 构造 release 闭包:减 inflight + 归还令牌(不限并发时 g 为 nil)。
// sync.Once 保证误用(双重释放)不产生多余令牌。
func (s *EndpointService) makeRelease(id string, g *endpointGate) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.inflight[id] > 1 {
				s.inflight[id]--
			} else {
				delete(s.inflight, id)
			}
			s.mu.Unlock()
			if g != nil {
				// 归还令牌永不阻塞:在途令牌(持有+通道内)恒 <= cap。
				g.sem <- struct{}{}
			}
		})
	}
}

// queueDepth 返回某 endpoint 当前排队人数(白盒观测,并发测试与管理面调试用)。
func (s *EndpointService) queueDepth(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g := s.sems[id]; g != nil {
		return g.waiting
	}
	return 0
}

// Inflight 返回 {endpoint id: 在途请求数} 快照(副本,防调用方污染内部状态)。
// 供 REST 列表展示「并发用量 inflight/max」(spec §6.3)。
func (s *EndpointService) Inflight() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.inflight))
	for id, n := range s.inflight {
		out[id] = n
	}
	return out
}

// ---- 上游探测(spec §11 test / fetch-models)---------------------------------

// FetchModels 拉上游 /v1/models(2s 超时,带 Bearer key),兼容 OpenAI
// {data:[{id}]} 与 Anthropic/部分网关 {models:[{id}]} 两种形态。刻意不写库:
// 返回列表交给 handler,由用户在管理面确认后经 PUT 落库(spec §10/§11)。
func (s *EndpointService) FetchModels(id string) ([]string, error) {
	e, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(e.BaseURL) == "" {
		return nil, fmt.Errorf("endpoint: %s 无 base_url(内置行走平台隧道,无独立模型列表可拉)", id)
	}
	_, ids, err := s.probeModels(strings.TrimSpace(e.BaseURL), e.APIKey)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// FetchModelsByBaseURL 按裸 base_url+key 探测模型列表(新建表单「自动获取」
// 用,行尚未落库没有 id 可走 FetchModels)。同款不写库语义;base_url 形态由
// 调用方(handler)先经 Validate 同源规则校验,这里只拒绝空值与探测失败。
func (s *EndpointService) FetchModelsByBaseURL(baseURL, apiKey string) ([]string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return nil, fmt.Errorf("endpoint: base_url 为空,无法探测")
	}
	_, ids, err := s.probeModels(base, strings.TrimSpace(apiKey))
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// Test 连接测试(spec §11):TCP 拨测 + GET /v1/models smoke,返回可达/模型数/
// 错误摘要。401 算可达(链路通、key 问题留给请求本身报,口径同 §8 内核探针)。
func (s *EndpointService) Test(id string) (bool, int, string) {
	e, err := s.store.Get(id)
	if err != nil {
		return false, 0, err.Error()
	}
	base := strings.TrimSpace(e.BaseURL)
	if base == "" {
		// builtin 行真探测:拨本机平台代理入口(56432),与内核 agentCoreEnsureUpstream
		// 对 builtin 的探针同口径——隧道健康以转发链为准,入口活着即可用。通了给
		// 明确「可达」而非误报失败(2026-11-26 装机首测误导实证);不通给可自查文案。
		dialAddr := s.builtinProbeAddr
		if dialAddr == "" {
			dialAddr = "127.0.0.1:56432"
		}
		conn, err := net.DialTimeout("tcp", dialAddr, endpointProbeTimeout)
		if err != nil {
			logger.Warn(fmt.Sprintf("endpoint: %s builtin 入口拨测失败 %s: %v", id, dialAddr, err))
			return false, 0, fmt.Sprintf("平台隧道入口 %s 不可达: %v——请在仪表盘启动代理服务后重试", dialAddr, err)
		}
		_ = conn.Close()
		logger.Info(fmt.Sprintf("endpoint: %s builtin 入口可达 %s(隧道健康以转发链为准)", id, dialAddr))
		return true, 0, "平台隧道入口可达(内置链路健康以实际转发为准)"
	}
	// TCP 拨测:上游端口通不通,2s 封顶,失败摘要直接进管理面。
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return false, 0, fmt.Sprintf("base_url 非法: %q", base)
	}
	hostPort := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			hostPort = net.JoinHostPort(u.Hostname(), "443")
		} else {
			hostPort = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	conn, err := net.DialTimeout("tcp", hostPort, endpointProbeTimeout)
	if err != nil {
		logger.Warn(fmt.Sprintf("endpoint: %s 拨测失败 %s: %v", id, hostPort, err))
		return false, 0, fmt.Sprintf("TCP %s 拨测失败: %v", hostPort, err)
	}
	_ = conn.Close()

	status, ids, err := s.probeModels(base, e.APIKey)
	if err != nil {
		// 401/403 是「链路通、key 被拒」:可达=true,问题留给实际请求报
		// (probeModels 对非 200 一律返回 err,这里按状态码把它接回来)。
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return true, 0, fmt.Sprintf("链路可达,但 /v1/models 返回 %d(key 被拒,问题留给实际请求报)", status)
		}
		return false, 0, err.Error()
	}
	if status != http.StatusOK {
		return false, 0, fmt.Sprintf("/v1/models 返回非预期状态 %d", status)
	}
	return true, len(ids), ""
}

// probeModels 探测 <base>/v1/models,返回 HTTP 状态码与解析出的模型 id 列表。
// 响应体 1MB 封顶(模型列表不是大文件,超限按异常处理)。
func (s *EndpointService) probeModels(baseURL, apiKey string) (int, []string, error) {
	target := strings.TrimRight(baseURL, "/") + "/v1/models"
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("endpoint: 构造探测请求失败: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("endpoint: 探测 %s 失败: %v", target, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("endpoint: 读探测响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, fmt.Errorf("endpoint: 探测 %s 返回 %d", target, resp.StatusCode)
	}

	// 宽松解析:能提取出 id 字符串数组即可(data/models 两个键都试)。
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return resp.StatusCode, nil, fmt.Errorf("endpoint: 探测响应不是可解析 JSON: %w", err)
	}
	seen := map[string]bool{}
	var ids []string
	for _, group := range [][]struct {
		ID string `json:"id"`
	}{payload.Data, payload.Models} {
		for _, m := range group {
			id := strings.TrimSpace(m.ID)
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return resp.StatusCode, ids, nil
}

// ---- 模型并集(Task 3 的 /v1/models 聚合数据源)------------------------------

// ModelOwner 是 ModelsUnion 的条目:ID + 归属 endpoint 的 id(聚合响应里映射
// owned_by)。同一模型 id 被多个 endpoint 声明时取第一个声明者(列表按
// priority,id 排序,即最高优先级者)。
type ModelOwner struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
}

// ModelsUnion 返回自动池内各 endpoint 的 models 并集(去重、字典序,带归属)。
// models=[](不限制)与内置行不贡献条目——前者没有具体清单,后者不该把平台
// 隧道伪装成一个多模型目录;禁用行同样跳过。protocol 维度本期不区分
// (/v1/models 聚合是单端点 OpenAI 风格,spec §18)。
func (s *EndpointService) ModelsUnion() []ModelOwner {
	list, err := s.store.List()
	if err != nil {
		logger.Warn(fmt.Sprintf("endpoint: ModelsUnion 读注册表失败,返回空并集: %v", err))
		return []ModelOwner{}
	}
	seen := map[string]bool{}
	var out []ModelOwner
	for _, e := range list {
		if e.ID == storage.BuiltinEndpointID || !e.Enabled {
			continue
		}
		if !e.ProtocolAnthropic && !e.ProtocolOpenAI {
			continue
		}
		raw := strings.TrimSpace(e.Models)
		if raw == "" || raw == "[]" {
			continue
		}
		var ids []string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			logger.Warn(fmt.Sprintf("endpoint: %s models 非法 JSON(%q),不贡献并集: %v", e.ID, raw, err))
			continue
		}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, ModelOwner{ID: id, Owner: e.ID})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// NewEndpointServiceForTest 是跨包测试注入缝(Task 4 的 REST handler 测试在
// handlers 包,够不到包内 newEndpointServiceWithStore):挂一个 :memory: store
// 的独立 service,与生产单例完全隔离。返回的 EndpointService 自持 store,
// Close 关句柄。生产代码不得调用本函数(命名即契约)。
func NewEndpointServiceForTest() (*EndpointService, error) {
	store, err := storage.NewEndpointStoreForTest()
	if err != nil {
		return nil, err
	}
	return newEndpointServiceWithStore(store), nil
}

// SetBuiltinProbeAddrForTest 注入内置行拨测地址:handlers 层测试与 service 层
// 双分支(真监听→可达文案/死端口→不可达文案)对齐用——本机 core 常驻监听
// 56432 时,不注入的断言会「本地永远红、CI 永远绿」。生产实例不经此入口,
// 拨测地址恒走默认 127.0.0.1:56432。
func (s *EndpointService) SetBuiltinProbeAddrForTest(addr string) { s.builtinProbeAddr = addr }

// Close 释放 service 持有的 store 句柄(测试构造自持 :memory: 库;生产单例
// 复用 auth 共享句柄,store.Close 是 no-op,调用安全)。
func (s *EndpointService) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}

// ---- REST 管理面读写(spec §11,Task 4 handler 的 service 缝)------------------
// 这一组是 store CRUD 的直通包装:管理面是唯一调用方,刻意不做额外状态
// 机(健康/并发的写路径仍只走本文件上面的 Report*/ResetHealth)。

// ListEndpoints 全量列表(store 排序 priority,id 升序)。管理面专用,数据面勿用
// (数据面走 Select/Acquire/Report*)。
func (s *EndpointService) ListEndpoints() ([]*config.LLMEndpoint, error) { return s.store.List() }

// GetEndpoint 单行;不存在返回 wrapped gorm.ErrRecordNotFound(handler 判 404)。
// 管理面专用,数据面勿用(数据面走 Select)。
func (s *EndpointService) GetEndpoint(id string) (*config.LLMEndpoint, error) {
	return s.store.Get(id)
}

// CreateEndpoint 新建(校验/id 冲突错误由 store 归一,handler 映射 400/409)。
// 管理面专用,数据面勿用。
func (s *EndpointService) CreateEndpoint(e *config.LLMEndpoint) error { return s.store.Create(e) }

// UpdateEndpoint 配置列白名单更新;健康列 clobber 由 store 白名单挡住。
// 管理面专用,数据面勿用。
func (s *EndpointService) UpdateEndpoint(e *config.LLMEndpoint) error { return s.store.Update(e) }

// DeleteEndpoint 删除;builtin → storage.ErrBuiltinImmutable(handler 映射 409)。
// 管理面专用,数据面勿用。
func (s *EndpointService) DeleteEndpoint(id string) error { return s.store.Delete(id) }

// TestEndpoint 连接测试直通(探测实现见 Test)。管理面专用,数据面勿用。
func (s *EndpointService) TestEndpoint(id string) (bool, int, string) { return s.Test(id) }

// GetPolicy 组策略读(无行=默认值,不落库)。管理面专用,数据面勿用。
func (s *EndpointService) GetPolicy() (*config.EndpointGroupPolicy, error) {
	return s.store.GetPolicy()
}

// SetPolicy 组策略写(组键恒 auto,store 归一)。管理面专用,数据面勿用。
func (s *EndpointService) SetPolicy(p *config.EndpointGroupPolicy) error { return s.store.SetPolicy(p) }
