package handlers

// endpoint_handler.go — LLM endpoint registry REST 面(spec 2026-11-25 §11,
// 56431 管理面、core-only 单注册)。CRUD/掩码/连通测试/拉模型/重置健康/
// 组策略七组端点,全部经 services.EndpointService 读写,handler 不碰 store。
//
// 契约要点(spec §14/§17 不变量 7/8):
//   - GET 永不回显完整 api_key(前4+***+后4,长度≤8 全掩);
//   - PUT 的 api_key 空值 = 不改(保留旧值),实现是 GET→merge→Update,
//     store 的列白名单保证健康列不被回写 clobber;
//   - builtin 行禁删(409)、禁改 base_url/auto_group(400);
//   - enabled 新建默认 true(store 层刻意不默认,避免 gorm 陷阱)。
//
// 鉴权:全部端点走现有 dashboard session 中间件缝(RequireDashboardSession),
// 与 quick-setup CRUD 家族同族;56431 本期不加新来源闸(spec §14)。

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/storage"
)

const endpointRequestMaxBytes = 1 << 20 // 1MB,防呆(models 列表也装得下)

// EndpointHandler 持有 registry service(生产=GetEndpointService 单例,
// 测试=NewEndpointServiceForTest 注入)。
type EndpointHandler struct {
	service *services.EndpointService
}

// NewEndpointHandler 生产构造:service 为 nil(GetEndpointService 降级)时
// handler 仍可构造,所有端点统一 503——管理面如实报告 registry 不可用,
// 不 panic。
func NewEndpointHandler(service *services.EndpointService) *EndpointHandler {
	return &EndpointHandler{service: service}
}

// endpointView 是 GET 列表/单条的出参形状:LLMEndpoint 的 JSON 视图 + 掩码
// api_key + inflight(并发用量,来自 service.Inflight())。
// 二期(spec 2026-11-27 §8):补 cooldown_until/rate_limited_until 两个 REST
// 字段——store 结构体的 json:"-" 刻意把它们挡在三词徽标(停用/熔断恢复于
// HH:MM/限流至 HH:MM)的 REST 面外,视图层显式补上(UI「恢复于 HH:MM」
// tooltip 需要它们);egress_kernel/last_used_at/total_requests/failed_requests
// 四个由嵌入结构体的 json tag 直接出面,此处不重复声明。
type endpointView struct {
	config.LLMEndpoint
	APIKey           string `json:"api_key"`
	Inflight         int    `json:"inflight"`
	CooldownUntil    int64  `json:"cooldown_until"`
	RateLimitedUntil int64  `json:"rate_limited_until"`
}

// maskAPIKey 前4+***+后4;长度≤8 全掩(短 key 泄露比例太高)。
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "********"
	}
	return key[:4] + "***" + key[len(key)-4:]
}

// endpointViews 批量转换 + inflight 快照一次取齐(避免逐行查)。
func (h *EndpointHandler) endpointViews(list []*config.LLMEndpoint) []endpointView {
	inflight := map[string]int{}
	if h.service != nil {
		inflight = h.service.Inflight()
	}
	views := make([]endpointView, 0, len(list))
	for _, e := range list {
		views = append(views, endpointView{
			LLMEndpoint:      *e,
			APIKey:           maskAPIKey(e.APIKey),
			Inflight:         inflight[e.ID],
			CooldownUntil:    e.CooldownUntil,
			RateLimitedUntil: e.RateLimitedUntil,
		})
	}
	return views
}

// endpointServiceUnavailable 统一降级响应:registry 不可用是服务端故障。
func endpointServiceUnavailable(w http.ResponseWriter) {
	common.Error(w, common.CodeServiceUnavailable, "endpoint registry 不可用", nil)
}

// endpointDecodeBody 读并解析 JSON 体(1MB 封顶;超限/非法一律 400)。
func endpointDecodeBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, endpointRequestMaxBytes)
	if err := common.DecodeRequest(r, v); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return false
	}
	return true
}

// endpointRequireSession 会话闸:未登录 401。返回 false 时响应已写。
func endpointRequireSession(w http.ResponseWriter, r *http.Request) bool {
	return middleware.RequireDashboardSession(w, r)
}

// HandleList GET /api/endpoints — 全量列表(priority,id 升序,store 排序)。
func (h *EndpointHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	// service 层没有 List(选择器不需要),经 Inflight 之外的读路径统一走
	// store 的 List 由 service 暴露——这里用 ModelsUnion 同款读法会越层,
	// 所以 handler 依赖 service 的 List 方法(Task 2 已提供)。
	list, err := h.service.ListEndpoints()
	if err != nil {
		common.ErrorInternalServer(w, "读取 endpoint 列表失败", map[string]interface{}{"error": err.Error()})
		return
	}
	common.Success(w, map[string]interface{}{"endpoints": h.endpointViews(list)})
}

// HandleGet GET /api/endpoints/{id} — 单条(api_key 掩码 + inflight,形状与
// 列表条目一致);不存在 → 404。
func (h *EndpointHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	ep, err := h.service.GetEndpoint(id)
	if err != nil {
		common.ErrorNotFound(w, "endpoint "+id+" 不存在")
		return
	}
	common.Success(w, h.endpointViews([]*config.LLMEndpoint{ep})[0])
}

// endpointCreateRequest 是 POST 的请求体:LLMEndpoint 全字段 + enabled 三态。
// Enabled 用 *bool:缺省=nil(默认 true),显式 false=建禁用行。
type endpointCreateRequest struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	BaseURL           string `json:"base_url"`
	APIKey            string `json:"api_key"`
	ProtocolAnthropic bool   `json:"protocol_anthropic"`
	ProtocolOpenAI    bool   `json:"protocol_openai"`
	AutoGroup         bool   `json:"auto_group"`
	Priority          int    `json:"priority"`
	MaxConcurrency    int    `json:"max_concurrency"`
	Models            string `json:"models"`
	Enabled           *bool  `json:"enabled"`
}

// HandleCreate POST /api/endpoints — 校验(400)→ Create(409 id 重复)。
func (h *EndpointHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	var req endpointCreateRequest
	if !endpointDecodeBody(w, r, &req) {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	// id 缺省时由 handler 生成 uuid(final-review 必改 1:UI endpointToPayload
	// 不下发 id,store 又拒空 id——不生成则「新增端点」整个功能 400)。显式 id
	// 仍允许(管理面/测试用)。
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = uuid.NewString()
	}
	ep := &config.LLMEndpoint{
		ID:                id,
		Name:              strings.TrimSpace(req.Name),
		BaseURL:           strings.TrimSpace(req.BaseURL),
		APIKey:            req.APIKey,
		ProtocolAnthropic: req.ProtocolAnthropic,
		ProtocolOpenAI:    req.ProtocolOpenAI,
		AutoGroup:         req.AutoGroup,
		Priority:          req.Priority,
		MaxConcurrency:    req.MaxConcurrency,
		Models:            strings.TrimSpace(req.Models),
		Enabled:           enabled,
	}
	if err := h.service.CreateEndpoint(ep); err != nil {
		// 重复 id:store 归一为 storage.ErrEndpointIDExists(预查重 + sqlite
		// UNIQUE 兜底两路都哨兵化),errors.Is 判定映射 409。
		if errors.Is(err, storage.ErrEndpointIDExists) {
			common.ErrorConflict(w, err.Error())
			return
		}
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	// 成功响应为单视图(final-review finding 4:与 HandleGet/HandleUpdate 形状
	// 一致,调用方不必按数组解包)。
	common.Success(w, h.endpointViews([]*config.LLMEndpoint{ep})[0])
}

// endpointUpdateRequest 是 PUT 的请求体:全字段可选,零值=不改(指针区分
// 「未给」与「显式零值」);api_key 空值=不改(spec §17 不变量 7)。
type endpointUpdateRequest struct {
	Name              *string `json:"name"`
	BaseURL           *string `json:"base_url"`
	APIKey            *string `json:"api_key"`
	ProtocolAnthropic *bool   `json:"protocol_anthropic"`
	ProtocolOpenAI    *bool   `json:"protocol_openai"`
	AutoGroup         *bool   `json:"auto_group"`
	Priority          *int    `json:"priority"`
	MaxConcurrency    *int    `json:"max_concurrency"`
	Models            *string `json:"models"`
	Enabled           *bool   `json:"enabled"`
}

// HandleUpdate PUT /api/endpoints/{id} — GET→merge→Update(不能整行 Save,
// 否则健康列被陈旧快照 clobber);builtin 禁改 base_url/auto_group(400,
// store 层 validateBuiltinInvariants 是第二道);不存在 404。
func (h *EndpointHandler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		common.ErrorBadRequest(w, "endpoint id required", nil)
		return
	}
	var req endpointUpdateRequest
	if !endpointDecodeBody(w, r, &req) {
		return
	}

	current, err := h.service.GetEndpoint(id)
	if err != nil {
		common.ErrorNotFound(w, "endpoint "+id+" 不存在")
		return
	}
	// builtin 不变量在 handler 层先拦(第一层):base_url 非空或 auto_group
	// 显式关,都直接 400,文案比 store 层更贴管理面语境。
	if current.Builtin {
		if req.BaseURL != nil && strings.TrimSpace(*req.BaseURL) != "" {
			common.ErrorBadRequest(w, "内置 endpoint 的 base_url 不可修改(恒走平台隧道)", nil)
			return
		}
		if req.AutoGroup != nil && !*req.AutoGroup {
			common.ErrorBadRequest(w, "内置 endpoint 的 auto_group 不可关闭", nil)
			return
		}
	}

	merged := *current
	if req.Name != nil {
		merged.Name = strings.TrimSpace(*req.Name)
	}
	if req.BaseURL != nil {
		merged.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.APIKey != nil && *req.APIKey != "" {
		merged.APIKey = *req.APIKey // 空值=不改(保留旧值)
	}
	if req.ProtocolAnthropic != nil {
		merged.ProtocolAnthropic = *req.ProtocolAnthropic
	}
	if req.ProtocolOpenAI != nil {
		merged.ProtocolOpenAI = *req.ProtocolOpenAI
	}
	if req.AutoGroup != nil {
		merged.AutoGroup = *req.AutoGroup
	}
	if req.Priority != nil {
		merged.Priority = *req.Priority
	}
	if req.MaxConcurrency != nil {
		merged.MaxConcurrency = *req.MaxConcurrency
	}
	if req.Models != nil {
		merged.Models = strings.TrimSpace(*req.Models)
	}
	if req.Enabled != nil {
		merged.Enabled = *req.Enabled
	}

	if err := h.service.UpdateEndpoint(&merged); err != nil {
		// TOCTOU 删除窗口:GET 成功后行被并发删除 → Update 报 not found → 404。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			common.ErrorNotFound(w, "endpoint "+id+" 不存在")
			return
		}
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	views := h.endpointViews([]*config.LLMEndpoint{&merged})
	common.Success(w, views[0])
}

// HandleDelete DELETE /api/endpoints/{id} — builtin → 409(ErrBuiltinImmutable)。
// 不存在的 id 返回 200 幂等成功(store.Delete 对不命中报 nil,管理面重复
// 删除/重试语义,与 S3 存储删除惯例一致;需要 404 语义的调用方先 GET)。
func (h *EndpointHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		common.ErrorBadRequest(w, "endpoint id required", nil)
		return
	}
	if err := h.service.DeleteEndpoint(id); err != nil {
		if errors.Is(err, storage.ErrBuiltinImmutable) {
			common.ErrorConflict(w, "内置 endpoint 不可删除")
			return
		}
		common.ErrorInternalServer(w, "删除 endpoint 失败", map[string]interface{}{"error": err.Error()})
		return
	}
	common.Success(w, map[string]interface{}{"id": id})
}

// HandleTest POST /api/endpoints/{id}/test — TCP 拨测 + /v1/models smoke。
// 探测结果(ok/model_count/error)是数据不是错误:200 信封携带。
func (h *EndpointHandler) HandleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	ok, modelCount, summary := h.service.TestEndpoint(id)
	common.Success(w, map[string]interface{}{
		"ok":          ok,
		"model_count": modelCount,
		"error":       summary,
	})
}

// HandleFetchModels POST /api/endpoints/{id}/fetch-models — 拉上游模型列表,
// 不落库(落库走 PUT 显式确认,spec §11)。
func (h *EndpointHandler) HandleFetchModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	models, err := h.service.FetchModels(id)
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	common.Success(w, map[string]interface{}{"models": models})
}

// endpointProbeRequest 是「按裸地址探测」的请求体:新建表单里行还没落库,
// 没有 id 可走 {id}/fetch-models,先用用户正在填的 base_url+key 试拉。
type endpointProbeRequest struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// HandleProbeModels POST /api/endpoints/probe-models — 按裸 base_url+key 拉
// 模型列表(不落库,形态校验同源 Create 的 Validate)。新建/编辑表单的
// 「自动获取」按钮走这里;已保存行仍走 {id}/fetch-models。
func (h *EndpointHandler) HandleProbeModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	var req endpointProbeRequest
	if !endpointDecodeBody(w, r, &req) {
		return
	}
	// 形态闸同源 Create:base_url 必须是合法 http(s) URL(带 host);空/畸形 400。
	base := strings.TrimSpace(req.BaseURL)
	if base != "" {
		if u, err := url.Parse(base); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			common.ErrorBadRequest(w, "base_url 必须是合法的 http/https URL(含主机名)", nil)
			return
		}
	} else {
		common.ErrorBadRequest(w, "base_url 为空,无法探测", nil)
		return
	}
	models, err := h.service.FetchModelsByBaseURL(base, req.APIKey)
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	common.Success(w, map[string]interface{}{"models": models})
}

// HandleResetHealth POST /api/endpoints/{id}/reset-health — 清零健康列。
func (h *EndpointHandler) HandleResetHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	id := idFromPath(w, r)
	// service.ResetHealth 对缺失 id 返回 nil(UPDATE 0 行);先 Get 判 404,
	// 避免「重置成功」假象。
	if _, err := h.service.GetEndpoint(id); err != nil {
		common.ErrorNotFound(w, "endpoint "+id+" 不存在")
		return
	}
	if err := h.service.ResetHealth(id); err != nil {
		common.ErrorInternalServer(w, "重置健康失败", map[string]interface{}{"error": err.Error()})
		return
	}
	common.Success(w, map[string]interface{}{"id": id})
}

// endpointSetEgressRequest 是 set-egress-kernel 的请求体(spec 二期 §8):
// set 用 *bool 区分「未给」与显式值——设/取消语义相反,缺字段不猜默认值。
type endpointSetEgressRequest struct {
	Set *bool `json:"set"`
}

// HandleSetEgressKernel POST /api/endpoints/{id}/set-egress-kernel — 出口指定
// 换绑(spec 二期 §8):set=true 经 store 事务方法换绑(全表至多一行=1,唯一
// 写入口;builtin 行允许,停用行允许=出口「暂停保留」),set=false 取消(幂等)。
// 错误映射:不存在 → 404(store 对 set 两方向一致做存在性检查);非 anthropic
// → 400(ErrNotAnthropic 哨兵);其余 → 500。请求体没有该字段的普通 PUT 永远
// 碰不到 egress_kernel(列白名单也不收),换绑只能走这里。
func (h *EndpointHandler) HandleSetEgressKernel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	id := idFromPath(w, r)
	if id == "" {
		return
	}
	var req endpointSetEgressRequest
	if !endpointDecodeBody(w, r, &req) {
		return
	}
	if req.Set == nil {
		common.ErrorBadRequest(w, "set 字段必填(true=设为出口/false=取消)", nil)
		return
	}
	if err := h.service.SetEgressKernel(id, *req.Set); err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			common.ErrorNotFound(w, "endpoint "+id+" 不存在")
		case errors.Is(err, storage.ErrNotAnthropic):
			common.ErrorBadRequest(w, err.Error(), nil)
		default:
			common.ErrorInternalServer(w, "设置出口指定失败", map[string]interface{}{"error": err.Error()})
		}
		return
	}
	common.Success(w, map[string]interface{}{"id": id, "egress_kernel": *req.Set})
}

// HandlePolicy GET/PUT /api/endpoint-groups/auto/policy — 组策略读写。
func (h *EndpointHandler) HandlePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !endpointRequireSession(w, r) {
		return
	}
	if h.service == nil {
		endpointServiceUnavailable(w)
		return
	}
	if r.Method == http.MethodGet {
		policy, err := h.service.GetPolicy()
		if err != nil {
			common.ErrorInternalServer(w, "读取组策略失败", map[string]interface{}{"error": err.Error()})
			return
		}
		common.Success(w, policy)
		return
	}
	var policy config.EndpointGroupPolicy
	if !endpointDecodeBody(w, r, &policy) {
		return
	}
	if err := h.service.SetPolicy(&policy); err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	common.Success(w, policy)
}

// idFromPath 取 {id} 路径参数;空=400(响应已写,调用方判空直接 return)。
func idFromPath(w http.ResponseWriter, r *http.Request) string {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		common.ErrorBadRequest(w, "endpoint id required", nil)
	}
	return id
}
