package services

// tray_summary.go — GET /api/tray/summary 托盘面板唯一读通道
// (plan 2026-11-26 托盘面板 Task 5,样式 spec §4.3/§4.4/§4.5)。
//
// 契约要点:
//   - 单一 GET 端点聚合四个子源(run/ai/agents/endpoints),面板一次取全;
//   - 子源缺失/panic → 对应字段降级为 null(JSON),不报错、不拖死整体:
//     handler 对每个子源独立 recover,单源炸不掉其余三路;
//   - 回环来源免 cookie(面板与 core dashboard 同进程同源),非回环来源须持
//     dashboard 会话——与聊天 WS 握手「cookie ∥ loopback」双闸同口径;
//   - 依赖方向:本文件只读 processor/config、processor/statistic 与本包既有
//     service(EndpointService/AgentService/RunService),绝不 import app/tray
//     ——app/tray 已依赖本包,反向 import 即成环。
//
// 可测性设计:所有映射逻辑是纯函数(trayRunSummaryFromStatus /
// trayAIProviders / aggregateTrayAgentKernels / trayEndpointSummary),生产
// 采集器(gatherTray*)是几行薄壳,handler 经 serveTraySummaryWith 注入采集器
// ——测试不打全局单例(SharedAuthSessionDB/VibeSessions 磁盘扫),遵守
// 「go test 不触真实 agent」护栏。

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/statistic"
)

// ---- 出参结构(字段名即面板契约,改名=面板渲染崩)--------------------------

// TrayRunBlocked 登录闸拦截原因:空串=允许启动(tooltip 语义与托盘一致)。
type TrayRunBlocked struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

// TrayRunSummary 代理运行态卡片(样式 spec §4.1/§4.2)。
type TrayRunSummary struct {
	IsRunning  bool           `json:"is_running"`
	Mode       string         `json:"mode"`
	StatusDesc string         `json:"status_desc"`
	ListenAddr string         `json:"listen_addr"`
	Blocked    TrayRunBlocked `json:"blocked"`
}

// TrayAIProvider 单个 provider 家族的点亮态(spec §4.3:检测驱动,不可手点)。
type TrayAIProvider struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Detected bool   `json:"detected"`
	Detail   string `json:"detail"`
}

// TrayAISummary AI 加速区:enabled=存在任一启用 provider(全关→行尾「AI 加速
// 已关 · 图标全灰」);providers 只含启用规则,禁用家族不进列表。
type TrayAISummary struct {
	Enabled   bool             `json:"enabled"`
	Providers []TrayAIProvider `json:"providers"`
}

// TrayAgentSession 24h 会话明细行(spec §4.5:状态点+标题+「来源 · 相对时间」)。
type TrayAgentSession struct {
	Title  string `json:"title"`
	Source string `json:"source"`
	Active bool   `json:"active"`
	Ts     int64  `json:"ts"`
}

// TrayAgentKernel 单内核卡片:三枚统计芯片 + 展开明细(cap 20)。
type TrayAgentKernel struct {
	Name     string             `json:"name"`
	Active   int                `json:"active"`
	Idle     int                `json:"idle"`
	Total24h int                `json:"total_24h"`
	Sessions []TrayAgentSession `json:"sessions"`
}

// TrayAgentsSummary AI AGENT · 24H 区。
type TrayAgentsSummary struct {
	Kernels []TrayAgentKernel `json:"kernels"`
}

// TrayEndpointItem endpoint 列表项(spec §4.4:radio+名称+优先级徽标+可用态)。
// rank=组内 priority 升序 1 基排名(P1/P2…);auto_group=0 或 priority=0
// (未设优先级,含内置行)→ rank=0,面板渲染「—」。
type TrayEndpointItem struct {
	Name                 string `json:"name"`
	Rank                 int    `json:"rank"`
	State                string `json:"state"`
	CooldownRemainingSec int64  `json:"cooldown_remaining_sec"`
}

// TrayEndpointGroup 一个协议族(anthropic/openai toggle 各成一组;双 toggle
// 的 endpoint 同时出现在两组)。current=组内 rank 最小的健康候选——即 56432
// 自动调度此刻会选中的行(与 selectAuto 同口径,内置兜底行不抢占组头)。
type TrayEndpointGroup struct {
	Protocol string             `json:"protocol"`
	Current  string             `json:"current"`
	Items    []TrayEndpointItem `json:"items"`
}

// TrayEndpointsSummary endpoint 区:available=false(单例降级/读库失败/空表)
// 时面板整区不渲染,不留空壳(spec §4.4 降级策略)。
type TrayEndpointsSummary struct {
	Available bool                `json:"available"`
	Groups    []TrayEndpointGroup `json:"groups"`
}

// TraySummary 顶层聚合:四个子源指针,nil=该子源缺失(JSON null)。
type TraySummary struct {
	Version   string                `json:"version"`
	Run       *TrayRunSummary       `json:"run"`
	AI        *TrayAISummary        `json:"ai"`
	Agents    *TrayAgentsSummary    `json:"agents"`
	Endpoints *TrayEndpointsSummary `json:"endpoints"`
}

// ---- 子源采集器接口(测试注入点)------------------------------------------

// traySummaryGatherers 四个子源各自一个采集函数;nil 等价于「该子源缺失」。
type traySummaryGatherers struct {
	run       func() *TrayRunSummary
	ai        func() *TrayAISummary
	agents    func() *TrayAgentsSummary
	endpoints func() *TrayEndpointsSummary
}

// buildTraySummary 聚合入口:每个子源独立 recover——单源 panic 降级为 null,
// 其余子源照常输出(契约硬性要求,2026-11-26 plan Task 5)。
func buildTraySummary(versionStr string, g traySummaryGatherers) *TraySummary {
	return &TraySummary{
		Version:   versionStr,
		Run:       recoverTraySubSource("run", g.run),
		AI:        recoverTraySubSource("ai", g.ai),
		Agents:    recoverTraySubSource("agents", g.agents),
		Endpoints: recoverTraySubSource("endpoints", g.endpoints),
	}
}

// recoverTraySubSource 单子源守卫:panic 时吞掉并返回零值(指针=nil→JSON
// null),日志留痕供面板侧归因「为什么这区块是 null」。
func recoverTraySubSource[T any](name string, gather func() T) (out T) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warn(fmt.Sprintf("tray_summary: 子源 %s panic,该字段降级为 null: %v", name, r))
		}
	}()
	if gather == nil {
		return out
	}
	return gather()
}

// ---- HTTP 面 --------------------------------------------------------------

// ServeTraySummary 生产路由入口:回环免 cookie,非回环须持 dashboard 会话
// (与聊天 WS 握手双闸同口径;56431 面板与 core 同进程,面板天然回环)。
func ServeTraySummary(w http.ResponseWriter, r *http.Request) {
	serveTraySummaryWith(w, r, traySummaryGatherers{
		run:       gatherTrayRun,
		ai:        gatherTrayAI,
		agents:    gatherTrayAgents,
		endpoints: gatherTrayEndpoints,
	})
}

// serveTraySummaryWith 注入式 handler 本体:来源闸 + 聚合 + 信封。
func serveTraySummaryWith(w http.ResponseWriter, r *http.Request, g traySummaryGatherers) {
	// 回环 ∥ cookie 双闸:面板(浏览器开 127.0.0.1:56431)免 cookie 直读;
	// LAN/远端来源没有会话不得读——会话标题与 endpoint 名是本机隐私面。
	if !middleware.IsLoopbackRequest(r) && !middleware.ValidateDashboardSession(r) {
		common.ErrorUnauthorized(w, "Authenticated dashboard session is required")
		return
	}
	common.Success(w, buildTraySummary(version.String(), g))
}

// ---- 纯映射函数(测试直打,零全局依赖)-------------------------------------

const (
	// trayAgentWindow24h agents 区时间窗语义(spec §4.5「24h 内发生的对话」):
	// 会话时间戳过滤在聚合层做,扫描层的 cap 只是洪峰兜底,不承担窗口语义。
	trayAgentWindow24h = 24 * time.Hour
	// trayAgentSessionCap 每内核明细上限(展开列表 200px 视口装得下的量级);
	// total_24h 仍按窗口全量计数,cap 只削明细不削统计。
	trayAgentSessionCap = 20
)

// trayRunSummaryFromStatus 把 RunService.GetStatus() 的 map 键映射为出参结构
// (map 键名以 GetStatus 现实现为准,拼错=零值静默,靠 GetStatus 键核对)。
func trayRunSummaryFromStatus(status map[string]interface{}, blocked TrayRunBlocked, listenAddr string) *TrayRunSummary {
	if status == nil {
		return nil
	}
	mode, _ := status["current_mode"].(string)
	running, _ := status["is_running"].(bool)
	desc, _ := status["description"].(string)
	return &TrayRunSummary{
		IsRunning:  running,
		Mode:       mode,
		StatusDesc: desc,
		ListenAddr: listenAddr,
		Blocked:    blocked,
	}
}

// traySourceLabel 会话来源角标(spec §4.5「网页/终端/手机沿用现有会话数据」)。
// origin 全集:dashboard(网页聊天)/phone(手机)/其余(tui 本地/unknown/空)
// 归终端——本地磁盘扫出的原生 claude/codex 会话 origin 恒空,全是终端形态。
func traySourceLabel(origin string) string {
	switch origin {
	case "dashboard":
		return "网页"
	case "phone":
		return "手机"
	default:
		return "终端"
	}
}

// traySessionUnixTs RFC3339 → unix 秒;解析失败=未知新鲜度,不入窗(宁可少
// 显示一条,不把无时间戳的旧会话误当 24h 内活跃)。
func traySessionUnixTs(rfc3339 string) (int64, bool) {
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(rfc3339))
	if err != nil || ts.IsZero() {
		return 0, false
	}
	return ts.Unix(), true
}

// aggregateTrayAgentKernels 按内核(=会话 Provider 字段:claude/codex/agentcore)
// 聚合 24h 窗口。窗口外/坏时间戳剔除;窗口内零会话的内核不产出卡片(没有
// 「探测到的内核」就渲染不出);明细新→旧排序,cap 20。
func aggregateTrayAgentKernels(sessions []models.AgentVibeSession, now time.Time) []TrayAgentKernel {
	cutoff := now.Add(-trayAgentWindow24h).Unix()
	nowUnix := now.Unix()

	type kernelAcc struct {
		active int
		idle   int
		rows   []TrayAgentSession
	}
	byKernel := map[string]*kernelAcc{}
	for _, s := range sessions {
		ts, ok := traySessionUnixTs(s.UpdatedAt)
		// 未来 1 分钟容差:时钟微漂不丢当前正写入的会话。
		if !ok || ts < cutoff || ts > nowUnix+60 {
			continue
		}
		active := strings.EqualFold(strings.TrimSpace(s.Status), "running")
		// 空内核名防御:清单通道理论上恒有 Provider(claude/codex/agentcore),
		// 防上游新通道漏填渲染出无名卡。
		provider := strings.TrimSpace(s.Provider)
		if provider == "" {
			provider = "unknown"
		}
		acc := byKernel[provider]
		if acc == nil {
			acc = &kernelAcc{}
			byKernel[provider] = acc
		}
		if active {
			acc.active++
		} else {
			acc.idle++
		}
		acc.rows = append(acc.rows, TrayAgentSession{
			Title:  s.Title,
			Source: traySourceLabel(s.Origin),
			Active: active,
			Ts:     ts,
		})
	}

	names := make([]string, 0, len(byKernel))
	for name := range byKernel {
		names = append(names, name)
	}
	sort.Strings(names)

	kernels := make([]TrayAgentKernel, 0, len(names))
	for _, name := range names {
		acc := byKernel[name]
		rows := acc.rows
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Ts > rows[j].Ts })
		if len(rows) > trayAgentSessionCap {
			rows = rows[:trayAgentSessionCap]
		}
		kernels = append(kernels, TrayAgentKernel{
			Name:     name,
			Active:   acc.active,
			Idle:     acc.idle,
			Total24h: acc.active + acc.idle,
			Sessions: rows,
		})
	}
	return kernels
}

// trayEndpointStateOK / cooldown / disabled:与 LLMEndpoint.HealthStatus 内部
// 取值同词表(processor/config/endpoint.go),面板按这三个值渲染样式。
const (
	trayEndpointStateOK       = "ok"
	trayEndpointStateCooldown = "cooldown"
	trayEndpointStateDisabled = "disabled"
)

// trayEndpointState 行级可用态:cooldown 附剩余秒(健康时间戳 − now);剩余
// 秒 ≤0 时按 selectAuto 的懒恢复口径回 ok——面板不再显示「冷却 0s」假灰。
func trayEndpointState(e *config.LLMEndpoint, now int64) (string, int64) {
	switch e.HealthStatus {
	case trayEndpointStateDisabled:
		return trayEndpointStateDisabled, 0
	case trayEndpointStateCooldown:
		remaining := e.CooldownUntil - now
		if rl := e.RateLimitedUntil - now; rl > remaining {
			remaining = rl
		}
		if remaining <= 0 {
			return trayEndpointStateOK, 0
		}
		return trayEndpointStateCooldown, remaining
	default:
		// 429 限流刻意不改健康状态(endpoint_service「429 不改健康状态」),
		// 限流窗口内的 ok 行在此按 cooldown 展示——与 selectAuto 的
		// 「RateLimitedUntil>now 跳过」同口径,否则面板会把它当 current。
		if e.RateLimitedUntil > now {
			return trayEndpointStateCooldown, e.RateLimitedUntil - now
		}
		return trayEndpointStateOK, 0
	}
}

// trayEndpointHasProtocol 行是否属于某协议组(组=协议 toggle,双 toggle 双组)。
func trayEndpointHasProtocol(e *config.LLMEndpoint, protocol string) bool {
	switch protocol {
	case "anthropic":
		return e.ProtocolAnthropic
	case "openai":
		return e.ProtocolOpenAI
	default:
		return false
	}
}

// trayEndpointGroup 组装单协议组:items=该组全部启用行(enabled=0 不进——
// 用户手动停用的不出现在面板,管理归仪表盘;spec §4.4);rank 只授给 auto 池
// 成员(auto_group ∧ priority>0,升序 1 基,次级序与 selectAuto 一致);current
// =组内 rank 最小的 ok 行,空池回落内置行(56432 真实兜底语义)。
func trayEndpointGroup(protocol string, list []*config.LLMEndpoint, now int64) TrayEndpointGroup {
	group := TrayEndpointGroup{Protocol: protocol, Items: []TrayEndpointItem{}}

	var pool []*config.LLMEndpoint // rank 授予池:auto 池成员
	var builtin *config.LLMEndpoint
	var itemIDs []string // 与 group.Items 一一对应,rank 关联按 ID(重名行不互串)
	for _, e := range list {
		if e == nil || !e.Enabled || !trayEndpointHasProtocol(e, protocol) {
			continue // enabled=0 不进 items
		}
		state, remaining := trayEndpointState(e, now)
		group.Items = append(group.Items, TrayEndpointItem{
			Name:                 e.Name,
			Rank:                 0,
			State:                state,
			CooldownRemainingSec: remaining,
		})
		itemIDs = append(itemIDs, e.ID)
		if e.AutoGroup && e.Priority > 0 {
			pool = append(pool, e)
		}
		if e.Builtin {
			builtin = e
		}
	}

	// priority 升序排名(同优先级按 created_at、id 兜底,与 selectAuto 同序,
	// 排名与调度器「下一个会选谁」永远自洽)。
	poolSorted := append([]*config.LLMEndpoint(nil), pool...)
	sort.SliceStable(poolSorted, func(i, j int) bool {
		if poolSorted[i].Priority != poolSorted[j].Priority {
			return poolSorted[i].Priority < poolSorted[j].Priority
		}
		if poolSorted[i].CreatedAt != poolSorted[j].CreatedAt {
			return poolSorted[i].CreatedAt < poolSorted[j].CreatedAt
		}
		return poolSorted[i].ID < poolSorted[j].ID
	})
	rankOf := make(map[string]int, len(poolSorted))
	for i, e := range poolSorted {
		rankOf[e.ID] = i + 1
	}
	for i, id := range itemIDs {
		group.Items[i].Rank = rankOf[id]
	}

	// current:rank 最小的健康候选(冷却/摘除跳过——它们不可选)。
	for _, e := range poolSorted {
		if state, _ := trayEndpointState(e, now); state == trayEndpointStateOK {
			group.Current = e.Name
			break
		}
	}
	// 空池回落内置行:56432 无候选时真实回落 builtin,组头如实展示。
	if group.Current == "" && builtin != nil {
		if state, _ := trayEndpointState(builtin, now); state == trayEndpointStateOK {
			group.Current = builtin.Name
		}
	}
	return group
}

// trayEndpointSummary endpoint 子源聚合:registry 不可用(单例 nil/读库失败/
// 空表)→ available=false,面板整区不渲染(spec §4.4 降级策略)。
func trayEndpointSummary(list []*config.LLMEndpoint, listErr error, now int64) *TrayEndpointsSummary {
	if listErr != nil || len(list) == 0 {
		return &TrayEndpointsSummary{Available: false, Groups: []TrayEndpointGroup{}}
	}
	groups := []TrayEndpointGroup{
		trayEndpointGroup("anthropic", list, now),
		trayEndpointGroup("openai", list, now),
	}
	return &TrayEndpointsSummary{Available: true, Groups: groups}
}

// trayAIProviders AI provider 家族映射(spec §4.3 图标行):只列启用规则;
// label 用户自定义优先,preset 兜底;RecentProviderTraffic 命中 → 点亮。
// HitCount 是经此路由的流量命中数——托盘只表达「谁在被使用」,逐次明细归仪表盘。
func trayAIProviders(rules map[string]*config.CustomerAIRuleSetting, presets []config.AIRuleProviderPreset, traffic []*statistic.AIActivityDetection) []TrayAIProvider {
	if len(rules) == 0 {
		return []TrayAIProvider{}
	}

	hits := make(map[string]*statistic.AIActivityDetection, len(traffic))
	for _, d := range traffic {
		if d == nil {
			continue
		}
		hits[config.NormalizeAIProviderKey(d.ProviderKey)] = d
	}

	providers := make([]TrayAIProvider, 0, len(presets))
	for _, preset := range presets {
		key := config.NormalizeAIProviderKey(preset.Key)
		rule, exists := config.FindCustomerAIRule(rules, key)
		if !exists || rule == nil || rule.Enble == nil || !*rule.Enble {
			continue // 未启用家族不进面板(与托盘菜单同口径)
		}
		provider := TrayAIProvider{
			Key:   key,
			Label: rule.Label,
		}
		if provider.Label == "" {
			provider.Label = preset.Label
		}
		if d, ok := hits[key]; ok {
			provider.Detected = true
			provider.Detail = fmt.Sprintf("检测到 %d 次流量经此路由", d.HitCount)
		}
		providers = append(providers, provider)
	}
	return providers
}

// ---- 生产采集器(薄壳:全局单例 → 纯映射函数)------------------------------

// gatherTrayRun run 子源:与 /api/run/status 同源(GetSharedRunService 单例),
// 登录闸复用 ProxyStartBlockedReason(托盘 Start 项同一份 gate),listen_addr
// 取代理面配置端口(HTTPProxyListenAddr)。
func gatherTrayRun() *TrayRunSummary {
	rs := GetSharedRunService()
	if rs == nil {
		return nil
	}
	code, msg := ProxyStartBlockedReason()
	return trayRunSummaryFromStatus(rs.GetStatus(), TrayRunBlocked{Code: code, Msg: msg}, config.HTTPProxyListenAddr())
}

// gatherTrayAI ai 子源:与 app/tray 直连模式同源——同一份 AIActivityTracker
// + 同一份 customer.ai_rules 存储(app/tray/ai_status.go 的 IPC 消费端形态
// 不经此路径;本采集器是 core 进程内直读,数据面一致)。
func gatherTrayAI() *TrayAISummary {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || len(cfg.Customer.AIRules) == 0 {
		return &TrayAISummary{Enabled: false, Providers: []TrayAIProvider{}}
	}
	summary := statistic.GetDefaultAIActivityTracker().Summary()
	if summary == nil {
		summary = &statistic.AIActivitySummary{}
	}
	providers := trayAIProviders(cfg.Customer.AIRules, config.PresetAIRuleProviders, summary.RecentProviderTraffic)
	return &TrayAISummary{Enabled: len(providers) > 0, Providers: providers}
}

// gatherTrayAgents agents 子源:复用仪表盘 agent 会话查询(AgentService.
// VibeSessions——磁盘清单+活跃覆盖层的同一份数据),聚合层做 24h 过滤。
func gatherTrayAgents() *TrayAgentsSummary {
	svc := GetSharedAgentService()
	if svc == nil {
		return nil
	}
	return &TrayAgentsSummary{Kernels: aggregateTrayAgentKernels(svc.VibeSessions(), time.Now())}
}

// gatherTrayEndpoints endpoints 子源:registry 单例(降级=nil)→ ListEndpoints。
func gatherTrayEndpoints() *TrayEndpointsSummary {
	svc := GetEndpointService()
	if svc == nil {
		return &TrayEndpointsSummary{Available: false, Groups: []TrayEndpointGroup{}}
	}
	list, err := svc.ListEndpoints()
	return trayEndpointSummary(list, err, time.Now().Unix())
}
