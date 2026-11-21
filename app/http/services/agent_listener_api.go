package services

// agent_listener_api.go 监听器本地 REST 代理 + cloud HTTP 客户端缝
// (specs/005-event-listener §2/FR-4/FR-11,plan Task 11)。
//
// 两个消费面:
// 1. 本地代理路由(app/http/routes/routes.go 挂 /api/listeners*):仪表盘
//   侧栏 CRUD 直达 PhoneServer 用户面(/api/listeners*,server listeners.ts
//   用户 JWT 路由),auth 材料与远端 WS 握手同源(remoteConnectionSnapshot →
//   Authorization + X-Aliang-Device-ID);本地鉴权闸 = RequireDashboardSession
//   (同 quick-setup CRUD 家族约定——浏览器面持会话 cookie,代理替用户签发
//   云端写请求,门禁不得弱于被代理面)。POST 的 source.device_id 在代理层
//   强制覆盖为本机(用户面无服务端覆盖闸,归属锚在此钉死,永不信任 body)。
// 2. LLM 工具面客户端(Task 12/13 消费):ListenerCreateViaCloud 走设备面
//   POST /api/agent/listeners(server 侧另有 deviceIdOverride 双保险,同
//   created_by=user:<deviceId> 惯例),list 走设备面 GET,update/delete 走
//   用户面 PATCH/DELETE(server 只在用户面开这两法)。
//
// 断联语义(FR-11 前端离线徽标):云端不可达(未注册/无凭据/base 未配置/
// 拨号失败/超时)→ 哨兵 errListenerCloudOffline → 路由层 502
// {code:"listener_cloud_offline"},绝不静默假成功;云端业务错误(422/409
// 族)≠离线——云端状态保留,应答经 writeListenerPassthrough 信封化
// ({code:<status>,msg:可读错误,data:null}),前端 request() 抛携带
// server 错误文案的 Error(I1:复活撞 LWW 的 listener_conflict 等可读)。
//
// 缝纪律(文件头注同 agent_listener_boot.go):复用 listenerCloudBaseURL /
// listenerCloudFetchTimeout / listenerCloudFetchBodyLimit 三缝,勿另起炉灶。
// 响应 wire 形态按 PhoneServer 真实契约(裸 JSON 键名对象,如
// {listeners:[...]}/{listener:{...}}),信封 {code,msg,data} 经
// unwrapAgentServerData 兼容容忍。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/common/logger"
)

// errListenerCloudOffline 云端不可达哨兵(连接拒绝/超时/无身份/base 未配置):
// 路由层据此 502 listener_cloud_offline;客户端调用方(Task 12/13)以
// errors.Is 判离线。errors.Is 分支要求包装方一律 fmt.Errorf("%w: ...")。
var errListenerCloudOffline = errors.New("listener cloud offline")

// listenerCloudStatusError 云端应答非 2xx:携带原始 status+body 供路由层
// 原样透传(业务错误直达前端,不在代理层吞成 5xx)。
type listenerCloudStatusError struct {
	status int
	body   []byte
}

func (e *listenerCloudStatusError) Error() string {
	if excerpt := listenerCloudBodyExcerpt(e.body); excerpt != "" {
		return fmt.Sprintf("agent server listeners: status %d: %s", e.status, excerpt)
	}
	return fmt.Sprintf("agent server listeners: status %d", e.status)
}

// listenerCloudBodyExcerpt 云端应答 body 摘录(≤200 rune,空白压成单空格):
// 错误文本直达两个 LLM 面(agentcore 桥 / stdio MCP),LWW 409 与 422 schema
// 的真实原因都在 body 里——不摘录模型只看得到裸状态码,重试只会复读同错。
// 超长截断按 rune 走,防把多字节字切成乱码。
func listenerCloudBodyExcerpt(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if r := []rune(s); len(r) > 200 {
		return string(r[:200])
	}
	return s
}

// listenerProxyBodyLimit 本地代理入站 body 上限(创建/补丁载荷 ~1KB 量级,
// 64KB 与 webhook 回调面同宽;触限 400 本地拒绝,不打云端)。
const listenerProxyBodyLimit = 64 << 10

// listenerCloudRequest 云端调用统一入口:注入与远端 WS 握手同源的
// Authorization + X-Aliang-Device-ID,8s 超时(listenerCloudFetchTimeout,
// 与调用方 ctx 取 min——M4:HTTP 面传入 r.Context(),客户端断连即取消云端
// 拨号,不再熬满 8s),读侧 4MiB 上限;非 2xx 回 *listenerCloudStatusError
// (携带原 body),云端不可达族一律回 errListenerCloudOffline 包装错误。
// path 为 base 后的绝对路径(如 /api/listeners、/api/agent/listeners)。
func listenerCloudRequest(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	svc := GetSharedAgentService()
	if svc == nil {
		return nil, 0, errListenerCloudOffline
	}
	identity, shouldRun := svc.remoteConnectionSnapshot()
	if !shouldRun || identity.authorization == "" || identity.deviceID == "" {
		return nil, 0, fmt.Errorf("%w: agent remote identity unavailable (not registered/enabled or no authorization)", errListenerCloudOffline)
	}
	base, err := listenerCloudBaseURL()
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", errListenerCloudOffline, err)
	}
	endpoint := base + path

	ctx, cancel := context.WithTimeout(ctx, listenerCloudFetchTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", errListenerCloudOffline, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", identity.authorization)
	req.Header.Set("X-Aliang-Device-ID", identity.deviceID)
	resp, err := svc.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", errListenerCloudOffline, err)
	}
	defer resp.Body.Close()
	// limit+1 读法探测截断(agent_goal.go 同款仓内 idiom):超限 body 必是被
	// 截断的腐败 JSON,透传=转发残缺载荷——归离线哨兵族而非假成功。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, listenerCloudFetchBodyLimit+1))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: read response: %v", errListenerCloudOffline, err)
	}
	if len(raw) > listenerCloudFetchBodyLimit {
		return nil, 0, fmt.Errorf("%w: response exceeds body limit (%d bytes)", errListenerCloudOffline, listenerCloudFetchBodyLimit)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, &listenerCloudStatusError{status: resp.StatusCode, body: raw}
	}
	return raw, resp.StatusCode, nil
}

// --- 本地代理路由 handler(routes.go 挂载;方法前缀注册下的纵深防御检查) ---

// HandleListenerList GET /api/listeners → 云端 GET /api/listeners 透传;
// listeners 键 null 归一为 [](前端可迭代契约,FR-11)。查询串刻意丢弃
// (固定形态契约;未来分页须扩此处与云端面)。
func HandleListenerList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}
	body, status, err := listenerCloudRequest(r.Context(), http.MethodGet, "/api/listeners", nil)
	if err != nil {
		writeListenerCloudError(w, err)
		return
	}
	writeListenerPassthrough(w, status, normalizeListenerListBody(body, "listeners"))
}

// HandleListenerCreate POST /api/listeners → source.device_id 强制覆盖为本机
// (永不信任 body)→ 云端 POST;应答信封化(2xx 拍平 200,data={listener:…})。
func HandleListenerCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}
	defer r.Body.Close()
	raw, err := readListenerProxyBody(w, r)
	if err != nil {
		return
	}
	forwarded, err := overrideListenerSourceDeviceID(raw)
	if err != nil {
		writeListenerCloudError(w, err)
		return
	}
	body, status, err := listenerCloudRequest(r.Context(), http.MethodPost, "/api/listeners", forwarded)
	if err != nil {
		writeListenerCloudError(w, err)
		return
	}
	writeListenerPassthrough(w, status, body)
}

// HandleListenerPatch PATCH /api/listeners/{id} → 云端 PATCH 透传(body 含
// LWW 期望 updated_at,由前端携带;代理不改写)。
func HandleListenerPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}
	id, ok := listenerPathID(w, r)
	if !ok {
		return
	}
	defer r.Body.Close()
	raw, err := readListenerProxyBody(w, r)
	if err != nil {
		return
	}
	body, status, err := listenerCloudRequest(r.Context(), http.MethodPatch, "/api/listeners/"+id, raw)
	if err != nil {
		writeListenerCloudError(w, err)
		return
	}
	writeListenerPassthrough(w, status, body)
}

// HandleListenerDelete DELETE /api/listeners/{id} → 云端 DELETE 透传。
func HandleListenerDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}
	id, ok := listenerPathID(w, r)
	if !ok {
		return
	}
	body, status, err := listenerCloudRequest(r.Context(), http.MethodDelete, "/api/listeners/"+id, nil)
	if err != nil {
		writeListenerCloudError(w, err)
		return
	}
	writeListenerPassthrough(w, status, body)
}

// HandleListenerEvents GET /api/listeners/{id}/events → 云端 GET 透传;
// events 键 null 归一为 []。查询串刻意丢弃(固定形态契约;未来分页须扩
// 此处与云端面)。
func HandleListenerEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}
	id, ok := listenerPathID(w, r)
	if !ok {
		return
	}
	body, status, err := listenerCloudRequest(r.Context(), http.MethodGet, "/api/listeners/"+id+"/events", nil)
	if err != nil {
		writeListenerCloudError(w, err)
		return
	}
	writeListenerPassthrough(w, status, normalizeListenerListBody(body, "events"))
}

// listenerValidID listener id 合法性闸(模型可控 id 拼进云端 URL 前的注入
// 防线):TrimSpace 后非空、非 ".."、不含路径/查询定界符与空白。合法
// listener id 形如 L-<uuid>。handler 面与客户端面(Update/Delete)共用。
func listenerValidID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && id != ".." && !strings.ContainsAny(id, "/?# \t")
}

// listenerPathID 取 {id} 路径段并过 listenerValidID 闸,拒绝即 400。
func listenerPathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !listenerValidID(id) {
		http.Error(w, "invalid listener id", http.StatusBadRequest)
		return "", false
	}
	return id, true
}

// readListenerProxyBody 读入站 body,limit+1 探测截断(仓内 idiom,同
// agent_goal.go):超限本地 400 拒绝、不打云端——被截断的 body 转发只会
// 变成云端 422 噪声,注释即行为。
func readListenerProxyBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, listenerProxyBodyLimit+1))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return nil, err
	}
	if len(raw) > listenerProxyBodyLimit {
		http.Error(w, "request body too large", http.StatusBadRequest)
		return nil, fmt.Errorf("request body exceeds %d bytes", listenerProxyBodyLimit)
	}
	return raw, nil
}

// writeListenerPassthrough 云端应答信封化转发(C1 评审定案:浏览器 request()
// 只懂 {code,msg,data} 信封,裸云端对象过不去 extractEnvelope 的 code!==0 闸):
//
//	2xx   → 解析 body,经 common.Success 重裹 {code:0,msg,data},data=云端对象
//	        ({listeners:[…]}/{listener:{…}}/{events:[…]}/{} for DELETE;
//	        本地状态拍平为 200——浏览器只认 ok+code 0,201/204 无消费者);
//	        2xx 但 body 非 JSON = 云端腐败响应,归离线哨兵族 502,绝不当
//	        成功信封把残缺体下发。
//	非2xx → 云端状态码原样保留,msg 取 body.error / body.detail / 裸体截断
//	        (rune 安全 ≤200),code=<status>:云端业务错误(如复活撞 LWW 的
//	        listener_conflict)在浏览器变成可读的 thrown Error,而非
//	        "Request failed with HTTP 409"(I1)。
//
// 已知边缘(M4 记录,1a 接受):云端 401 经本地 401 透传会触发网页端
// handleAuthenticationFailure 登出(设备 JWT 陈旧而云端仍可达时)。
func writeListenerPassthrough(w http.ResponseWriter, status int, body []byte) {
	if status < 200 || status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": status,
			"msg":  listenerCloudErrMsg(status, body),
			"data": nil,
		})
		return
	}
	parsed, ok := parseListenerCloudBody(body)
	if !ok {
		writeListenerCloudError(w, fmt.Errorf("%w: cloud 2xx body is not valid JSON", errListenerCloudOffline))
		return
	}
	common.Success(w, parsed)
}

// parseListenerCloudBody 2xx 云端 body → 通用对象;空 body(204 族)视为空
// 对象(DELETE 面),非 JSON 返回 ok=false(腐败响应归离线哨兵族)。
func parseListenerCloudBody(body []byte) (interface{}, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return map[string]interface{}{}, true
	}
	var parsed interface{}
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

// listenerCloudErrMsg 非2xx 云端应答的可读错误文案:优先语义字段
// body.error / body.detail(PhoneServer 惯例),缺失兜裸体截断(rune 安全
// ≤200,防多字节字符劈半+巨型 body 灌爆浏览器错误框),空体给状态占位。
func listenerCloudErrMsg(status int, body []byte) string {
	var probe struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &probe); err == nil {
		if strings.TrimSpace(probe.Error) != "" {
			return probe.Error
		}
		if strings.TrimSpace(probe.Detail) != "" {
			return probe.Detail
		}
	}
	runes := []rune(string(body))
	if len(runes) == 0 {
		return fmt.Sprintf("cloud status %d", status)
	}
	if len(runes) > 200 {
		return string(runes[:200]) + "…"
	}
	return string(runes)
}

// writeListenerCloudError 把 listenerCloudRequest 的错误写到本地响应:
// 云端业务错误(非 2xx)状态保留、应答信封化(writeListenerPassthrough
// 非2xx 分支);离线哨兵(及一切未完成调用的错误)→ 502
// {code:"listener_cloud_offline"}(前端离线徽标,绝不静默 200)。
func writeListenerCloudError(w http.ResponseWriter, err error) {
	var statusErr *listenerCloudStatusError
	if errors.As(err, &statusErr) {
		writeListenerPassthrough(w, statusErr.status, statusErr.body)
		return
	}
	logger.Warn(fmt.Sprintf("[AGENT-LISTENER] cloud request offline: %v", err))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	// msg 前缀带哨兵(Task 16 pre-step 0):request() 抛的 Error 只携带
	// envelope.msg(502 body 的 code 字段被前端 extractEnvelope 丢弃),哨兵
	// 落进 msg 让 utils/listenerFormat.isListenerCloudOffline 的主匹配臂
	// (LISTENER_CLOUD_OFFLINE 子串)成为权威信号;prose 尾巴保留兼容旧包。
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code": "listener_cloud_offline",
		"msg":  "listener_cloud_offline: listener cloud unreachable (offline, not registered or timed out)",
	})
}

// normalizeListenerListBody 空列表归一:透传 body 里值为 null 的列表键改写为
// [](FR-11 前端可迭代契约);body 整体为 null 时合成 {"<key>":[]};其余
// (含裸数组/信封/非 JSON)原样返回不掺假。
func normalizeListenerListBody(body []byte, key string) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		out, err := json.Marshal(map[string]interface{}{key: []interface{}{}})
		if err != nil {
			return body
		}
		return out
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return body
	}
	raw, ok := obj[key]
	if !ok || !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return body
	}
	obj[key] = json.RawMessage("[]")
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// overrideListenerSourceDeviceID 强制 source.device_id=本机 deviceID(用户面
// POST 无服务端覆盖闸,归属锚在代理层钉死)。source 缺失/非对象/整体非 JSON
// 对象时原样转发(server 422 兜底校验,代理不抢答 schema 错误)。
func overrideListenerSourceDeviceID(body []byte) ([]byte, error) {
	svc := GetSharedAgentService()
	if svc == nil {
		return nil, errListenerCloudOffline
	}
	identity, shouldRun := svc.remoteConnectionSnapshot()
	if !shouldRun || identity.deviceID == "" {
		return nil, fmt.Errorf("%w: agent remote identity unavailable", errListenerCloudOffline)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, nil
	}
	source, ok := payload["source"].(map[string]interface{})
	if !ok {
		return body, nil
	}
	source["device_id"] = identity.deviceID
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- LLM 工具面客户端(Task 12/13 消费;导出面) -----------------------------

// ListenerCloudSource 工具面 create 的源参数(device_id 不在此列——由本层
// 强制本机,spec §3 file/process 源必填=本机,调用方无权指定)。
type ListenerCloudSource struct {
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
}

// ListenerCreateViaCloud 注册监听器(设备面 POST /api/agent/listeners,server
// 侧另有 deviceIdOverride 双保险):body 为 server listenerCreateSchema 字段
// 名(snake_case:title/source{type,device_id,params}/prompt_template/mode/
// target_session_id),201 应答 {listener:{...}} 中取 id+status 回传。云端
// 业务 4xx → 非 sentinel 错误;不可达 → errListenerCloudOffline 包装。
func ListenerCreateViaCloud(ctx context.Context, title string, source ListenerCloudSource, promptTemplate, mode, targetSessionID string) (string, string, error) {
	svc := GetSharedAgentService()
	if svc == nil {
		return "", "", errListenerCloudOffline
	}
	identity, shouldRun := svc.remoteConnectionSnapshot()
	if !shouldRun || identity.deviceID == "" {
		return "", "", fmt.Errorf("%w: agent remote identity unavailable", errListenerCloudOffline)
	}
	if source.Params == nil {
		source.Params = json.RawMessage("{}")
	}
	body, err := json.Marshal(map[string]interface{}{
		"title":             title,
		"source":            map[string]interface{}{"type": source.Type, "device_id": identity.deviceID, "params": source.Params},
		"prompt_template":   promptTemplate,
		"mode":              mode,
		"target_session_id": targetSessionID,
	})
	if err != nil {
		return "", "", err
	}
	raw, _, err := listenerCloudRequest(ctx, http.MethodPost, "/api/agent/listeners", body)
	if err != nil {
		return "", "", err
	}
	data, err := unwrapAgentServerData(raw)
	if err != nil {
		return "", "", err
	}
	var created struct {
		Listener struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"listener"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		return "", "", fmt.Errorf("decode listener create response: %w", err)
	}
	if created.Listener.ID == "" {
		return "", "", fmt.Errorf("listener create response missing id: %s", raw)
	}
	return created.Listener.ID, created.Listener.Status, nil
}

// ListenerListViaCloud 全量列表(设备面 GET /api/agent/listeners,与 boot
// 镜像拉取同源):解析 server {listeners:[...]} 真实 wire 形态(信封/裸数组
// 容忍),空表归一非 nil。未知键容忍(与 listener.upsert 帧同口径)。
func ListenerListViaCloud(ctx context.Context) ([]ListenerMirror, error) {
	raw, _, err := listenerCloudRequest(ctx, http.MethodGet, "/api/agent/listeners", nil)
	if err != nil {
		return nil, err
	}
	data, err := unwrapAgentServerData(raw)
	if err != nil {
		return nil, err
	}
	var rows []ListenerMirror
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, fmt.Errorf("decode listeners payload: %w", err)
		}
	} else {
		var payload struct {
			Listeners []ListenerMirror `json:"listeners"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, fmt.Errorf("decode listeners payload: %w", err)
		}
		rows = payload.Listeners
	}
	if rows == nil {
		rows = []ListenerMirror{} // data:"null"/{listeners:null} 同空表口径
	}
	return rows, nil
}

// ListenerUpdateViaCloud 部分更新(用户面 PATCH /api/listeners/{id}):patchFields
// 原样作 body(须含 LWW 期望 updated_at——server listenerPatchSchema 必填,
// 由调用方 Task 12 从镜像行携带)。id 过 listenerValidID 闸:畸形 id 是普通
// 错误,不是离线哨兵(调用方可区分「入参烂」与「云端不可达」)。
func ListenerUpdateViaCloud(ctx context.Context, id string, patchFields map[string]interface{}) error {
	id = strings.TrimSpace(id)
	if !listenerValidID(id) {
		return fmt.Errorf("invalid listener id")
	}
	body, err := json.Marshal(patchFields)
	if err != nil {
		return err
	}
	_, _, err = listenerCloudRequest(ctx, http.MethodPatch, "/api/listeners/"+id, body)
	return err
}

// ListenerDeleteViaCloud 删除(用户面 DELETE /api/listeners/{id});id 闸
// 同 ListenerUpdateViaCloud(畸形 id → 普通错误,零云端调用)。
func ListenerDeleteViaCloud(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if !listenerValidID(id) {
		return fmt.Errorf("invalid listener id")
	}
	_, _, err := listenerCloudRequest(ctx, http.MethodDelete, "/api/listeners/"+id, nil)
	return err
}
