package services

// 设备互访阶段 1（spec 2026-10-29-device-peer-link-and-profile-design §5.3/§7/§10）：
// LLM 意图工具门面（peer_exec / peer_fetch_file / device_info / device_note，
// 另加 device_list 设备发现目录——2026-10-30 审计补缺：阶段1 四件全部只收
// device_id,LLM 无从自查账号设备清单）。两个接入点共享同一套实现：
//   - 注册表工具（服务端编排的 AI 会话）：agent_tool_registry.go 追加描述符，
//     事件经 default 臂 RunWithGuards 护栏分发（事件名不与显式臂冲突）；
//   - agentcore 自定义工具（本机 AI 会话）：agentCorePeerTools 宿主注入。
//
// 审批取舍（v1 拍板，记录在案——写工具免逐次审批，但各工具真实门禁不同，
// 注释必须如实区分，不得笼统写成「ACL+开关三层」）：
//   - peer_exec：服务端 peer ACL（同账号隐式允许+全量审计）+ 设备级
//     peer_exec_enabled 开关（默认关）。三层缓解成立。
//   - peer_fetch_file：同账号 + 目标设备 file_transfer_v1 能力位（服务端编排
//     校验链），无 peer ACL、无设备开关；走既有 transfer 审计。
//   - device_note：同账号（JWT 归属校验）即可写，无设备开关无 ACL；审计靠
//     metadata updated_by 记录来源。
//   - device_info：读工具，同账号读。
//   ShellPolicy 是「空白名单=全放行」，方向与审批相反，不可依赖，故不在
//   agentCoreApprove 挂点。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/app/http/models"
)

// peer 工具事件名（注册表 Event + agentcore 工具名）。与 peer.exec.request/
// peer.exec.result 帧族是两套名空间；严禁与 agent_remote_ws.go 显式 switch
// 臂冲突（冲突=被广告但永不走护栏分发）。
const (
	AgentPeerToolEventExec       = "peer.exec"
	AgentPeerToolEventFetchFile  = "peer.fetch_file"
	AgentPeerToolEventDeviceInfo = "peer.device_info"
	AgentPeerToolEventDeviceNote = "peer.device_note"
	AgentPeerToolEventDeviceList = "peer.device_list"
)

const (
	// agentPeerToolTimeoutMs 是工具层等待服务端编排回包的 ctx 上限。分层刻意
	// 留裕量：服务端 fence（peer exec 上界 600s）→ 先行结构化裁决；客户端
	// ctx 630s（= fence+30s）→ 兜底；注册表护栏 TimeoutMs 640s → 最后防线。
	// ctx 与 fence 平局会让 transport 超时吃掉服务端的裁决回包。
	agentPeerToolTimeoutMs = 630_000
	// agentPeerFetchFileTimeoutMs 是 fetch 编排（异步受理，非文件字节面）的
	// ctx 上限。
	agentPeerFetchFileTimeoutMs = 60_000
	// agentPeerNoteTimeoutMs 是 metadata 写的 ctx 上限。
	agentPeerNoteTimeoutMs = 30_000
	// agentPeerDeviceInfoTimeoutMs 是 device_info 的 ctx 超时（两跳 GET）。
	agentPeerDeviceInfoTimeoutMs = 14_000
	// agentPeerDeviceListTimeoutMs 是 device_list 的 ctx 超时（单跳 GET 清单）。
	agentPeerDeviceListTimeoutMs = 14_000
	// agentPeerHTTPBodyLimit 限制编排响应体的读取上限。超出即截断——解析侧
	// 对缺关键字段的结构化错误兜底（见 peerExecCall 的 exit_code 必存在断言），
	// 绝不把截断的响应解析成假成功。
	agentPeerHTTPBodyLimit = 1 << 20
	// device_note 服务端 Zod 校验的本地预检上界（字节）。
	agentPeerNoteKeyMaxBytes   = 64
	agentPeerNoteValueMaxBytes = 4096
)

// agentPeerHTTPClient 是 peer 编排调用的专用 HTTP 客户端：刻意无全局 Timeout，
// 超时完全由 ctx 驱动。共享 s.client（Timeout 8s）会让任何 >8s 的长命令在 A 侧
// transport 超时——LLM 看到报错可能重试，目标机就会被重复执行同一命令。
var agentPeerHTTPClient = &http.Client{}

// agentPeerServiceProvider 是工具处理器取 AgentService 的可注入点：生产=
// GetSharedAgentService()；测试换成指向 httptest 的桩服务。生产接线放 init()
// 而非 var 初始化式——agentToolsRegistry 的包级初始化引用这些 handler，handler
// 若在 var 初始化式里静态引用 GetSharedAgentService（其 body 链最终回到
// agentToolsRegistry），编译器的初始化依赖分析会报 initialization cycle。
var agentPeerServiceProvider func() *AgentService

func init() {
	agentPeerServiceProvider = func() *AgentService { return GetSharedAgentService() }
}

// ---------------------------------------------------------------------------
// callAgentServer 的 POST/PATCH 变体（spec §10：照 doAgentServerGET 模式）
// ---------------------------------------------------------------------------

// agentPeerHTTPStatusError 携带非 2xx 状态码与原始响应体，供工具层把服务端
// reason 转可读错误。
type agentPeerHTTPStatusError struct {
	status int
	body   string
}

func (e agentPeerHTTPStatusError) Error() string {
	msg := sanitizePeerErrorMsg(e.body)
	if msg == "" {
		return fmt.Sprintf("peer api status %d", e.status)
	}
	return fmt.Sprintf("peer api status %d: %s", e.status, msg)
}

// doAgentServerJSON 是 doAgentServerGET 的带体变体：user-JWT 鉴权 + 本设备
// X-Aliang-Device-ID + ctx 超时 + 响应体封顶 + 专用无 Timeout 客户端（ctx
// 驱动，见 agentPeerHTTPClient）。非 2xx 返回 agentPeerHTTPStatusError（不
// 触发 callAgentServer 的 401 自愈链——工具调用的凭据过期自愈交给下一次
// 心跳/同步，避免把长命令拖进重试风暴）。2xx 含 200/201 均受理（fetch 编排
// 回 201）。
func (s *AgentService) doAgentServerJSON(ctx context.Context, method, endpoint string, payload interface{}) ([]byte, error) {
	s.mu.Lock()
	authHeader := strings.TrimSpace(s.effectiveUserAuthorizationLocked(""))
	deviceID := strings.TrimSpace(s.state.DeviceID)
	registered := s.state.Registered
	s.mu.Unlock()
	if authHeader == "" {
		return nil, errors.New("user authorization is empty")
	}
	if deviceID == "" || !registered {
		return nil, errors.New("device is not registered")
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("X-Aliang-Device-ID", deviceID)
	resp, err := agentPeerHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, agentPeerHTTPBodyLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, agentPeerHTTPStatusError{status: resp.StatusCode, body: string(raw)}
	}
	return raw, nil
}

// doAgentServerPOST 是 spec §10 点名的 POST 变体入口。
func (s *AgentService) doAgentServerPOST(ctx context.Context, endpoint string, payload interface{}) ([]byte, error) {
	return s.doAgentServerJSON(ctx, http.MethodPost, endpoint, payload)
}

// ---------------------------------------------------------------------------
// 错误脱敏（spec §7.3：照 sanitizeTransferError 模式扩展 sanitizePeerError）
// ---------------------------------------------------------------------------

// agentPeerPresignedHostPattern 整体匹配 COS 预签名 URL（对象存储签名 URL
// 是纯 bearer 凭据，参数级打码不足以使其失效，整体替换——对齐
// sanitizeTransferError 的判断）。
var agentPeerPresignedHostPattern = regexp.MustCompile(`(?i)https?://[^\s]*myqcloud\.com[^\s]*`)

// agentPeerSecretQueryValuePattern 匹配 URL 查询参数里 token/signature 族键的
// 值部分（保留键名与 URL 其余部分，值打码），覆盖 ?token=、?access_token=、
// ?q-signature= 等形态。
var agentPeerSecretQueryValuePattern = regexp.MustCompile(
	`(?i)([?&])([A-Za-z0-9_-]*(?:token|secret|signature|password|passphrase|apikey|api_key|authorization|ticket|sig)[A-Za-z0-9_-]*)=[^&\s]*`)

// sanitizePeerError 产出可回传给 LLM 的错误文本（spec §7.3，照
// sanitizeTransferError 模式）：剥 *url.Error 的完整 URL（同
// sanitizeUploadError）、COS 预签名 URL 整体替换、其余 URL 的查询参数里
// token/signature 族键值打码、最后限长 500（按字节截断保 UTF-8 边界）。
// 刻意不做「任意 URL 整体替换」——那会把无害的诊断信息也抹掉，参数级脱敏
// 已覆盖敏感面。
func sanitizePeerError(err error) string {
	if err == nil {
		return ""
	}
	msg := sanitizeUploadError(err).Error()
	msg = agentPeerPresignedHostPattern.ReplaceAllString(msg, "[redacted-presigned-url]")
	msg = agentPeerSecretQueryValuePattern.ReplaceAllString(msg, "$1$2=[redacted]")
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "peer operation failed"
	}
	if len(msg) > 500 {
		cut := 500
		for cut > 0 && !isPeerUTF8Start(msg[cut]) {
			cut--
		}
		msg = msg[:cut]
	}
	return msg
}

func isPeerUTF8Start(b byte) bool {
	return b&0xC0 != 0x80
}

// agentPeerReadableError 把编排 4xx 的契约 reason 转可读错误（spec §7：
// 「4xx → 把 reason 转可读错误」，ZodError details 一并摘出），其余错误走
// sanitizePeerError。
func agentPeerReadableError(err error) string {
	var statusErr agentPeerHTTPStatusError
	if errors.As(err, &statusErr) && statusErr.status >= 400 && statusErr.status < 500 {
		if reason := peerStatusErrorReason(statusErr); reason != "" {
			return peerReasonReadable(reason)
		}
	}
	return sanitizePeerError(err)
}

// sanitizePeerErrorMsg 是 sanitizePeerError 的文本入口（响应体等已非 error
// 的文本走这里），脱敏规则一致。
func sanitizePeerErrorMsg(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return sanitizePeerError(errors.New(text))
}

// ---------------------------------------------------------------------------
// 服务端编排调用（工具实现共享）
// ---------------------------------------------------------------------------

// peerAPIResultObject 容错解析编排响应：服务端可能回顶层对象、{data:...}/
// {result:...} 壳（callAgentServer 的 unwrapAgentServerData 语义）、或 fetch
// 编排的 {transfer:{...}} 嵌套壳。认不出任何已知结构时返回原 map（调用方的
// 必存键断言会把它变成结构化错误，绝不假装成功）。
func peerAPIResultObject(raw []byte) map[string]interface{} {
	var top map[string]interface{}
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return map[string]interface{}{}
	}
	if looksLikePeerResult(top) {
		return top
	}
	for _, wrapper := range []string{"data", "result", "transfer"} {
		if inner, ok := top[wrapper].(map[string]interface{}); ok && looksLikePeerResult(inner) {
			return inner
		}
	}
	return top
}

func looksLikePeerResult(m map[string]interface{}) bool {
	for _, key := range []string{"exit_code", "transfer_id", "transferId", "device_id", "deviceId", "entries", "name", "ok"} {
		if _, present := m[key]; present {
			return true
		}
	}
	return false
}

// peerAPIString 按序取第一个非空字符串字段（camelCase/snake_case 双兼容）。
func peerAPIString(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func peerAPIInt(m map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			return int(v), true
		case int:
			return v, true
		}
	}
	return 0, false
}

func peerAPIBool(m map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		if v, ok := m[key].(bool); ok && v {
			return true
		}
	}
	return false
}

// peerReasonReadable 把服务端 4xx 的 reason 转可读错误（契约 reason 开集，
// 枚举与服务端 rejectPeer 语义对齐）。
func peerReasonReadable(reason string) string {
	switch strings.TrimSpace(reason) {
	case "busy":
		return "target device is busy running another peer command, retry later"
	case "device_offline":
		return "target device is offline"
	case "not_allowed":
		return "target device does not allow this peer operation (peer ACL or device switch off)"
	case "rate_limited":
		return "peer operation rate limited, retry later"
	case "timeout":
		return "peer operation timed out before the target device could complete it"
	case "target_missing_capability":
		return "target device does not support this peer operation (capability missing)"
	case "workdir_missing":
		return "requested workdir does not exist on target device"
	case "start_failed":
		return "command failed to start on target device"
	case "":
		return "peer operation rejected by server"
	default:
		return "peer operation rejected: " + strings.TrimSpace(reason)
	}
}

// peerStatusErrorReason 从 4xx 响应体里取 reason（契约字段，兼容 error/
// message），并把 ZodError 的 details 数组摘成可读摘要——否则 LLM 只看到裸
// "validation_error"，不知道是哪个字段怎么错的。
func peerStatusErrorReason(statusErr agentPeerHTTPStatusError) string {
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(statusErr.body), &body); err != nil || body == nil {
		return ""
	}
	reason := peerAPIString(body, "reason", "error", "message")
	details := peerErrorDetailsSummary(body["details"])
	switch {
	case reason != "" && details != "":
		return reason + ": " + details
	case reason != "":
		return reason
	default:
		return details
	}
}

// peerErrorDetailsSummary 把 ZodError details（[{path,message}...] 或字符串
// 数组）摘成 "; " 连接的一行摘要（上限 200 字符）。
func peerErrorDetailsSummary(raw interface{}) string {
	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				parts = append(parts, v)
			}
		case map[string]interface{}:
			msg := peerAPIString(v, "message", "error")
			if msg == "" {
				continue
			}
			if path := peerErrorDetailPath(v); path != "" {
				parts = append(parts, path+": "+msg)
			} else {
				parts = append(parts, msg)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return truncateAgentText(strings.Join(parts, "; "), 200)
}

func peerErrorDetailPath(detail map[string]interface{}) string {
	if path, ok := detail["path"].([]interface{}); ok {
		segments := make([]string, 0, len(path))
		for _, seg := range path {
			switch v := seg.(type) {
			case string:
				segments = append(segments, v)
			case float64:
				segments = append(segments, fmt.Sprintf("%d", int(v)))
			}
		}
		return strings.Join(segments, ".")
	}
	return peerAPIString(detail, "path")
}

// agentPeerServerURL 是编排 API 的基址钩子：生产=配置的云服务端；测试换成
// httptest 本地服务器（工具层调用形状的可观测点）。
var agentPeerServerURL = func() string {
	return strings.TrimRight(currentAgentServerURL(), "/")
}

// peerExecCall 编排一次对端命令执行：POST /api/peer/exec
// {target_device_id, command, timeout_ms, workdir}。返回可直接回给 LLM 的
// 结果文本（exit_code/stdout/stderr/truncated/duration_ms）。timeout_ms 在
// 客户端预钳 [1000,600000]（服务端 Zod 同界）——越界值本地归界，不让 LLM 吃
// validation_error。响应缺 exit_code 或解析失败一律结构化报错，绝不假装成功。
func (s *AgentService) peerExecCall(ctx context.Context, deviceID, command string, timeoutMs int, workdir string) (string, error) {
	body := map[string]interface{}{
		"target_device_id": deviceID,
		"command":          command,
	}
	if timeoutMs > 0 {
		body["timeout_ms"] = clampPeerExecTimeoutMs(timeoutMs)
	}
	if strings.TrimSpace(workdir) != "" {
		body["workdir"] = workdir
	}
	raw, err := s.doAgentServerPOST(ctx, agentPeerServerURL()+"/api/peer/exec", body)
	if err != nil {
		return "", err
	}
	res := peerAPIResultObject(raw)
	exitCode, ok := peerAPIInt(res, "exit_code")
	if !ok {
		return "", fmt.Errorf("peer exec response missing exit_code (status ok but body not a result): %.200s", sanitizePeerErrorMsg(string(raw)))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "peer exec on device %s finished: exit_code=%d", deviceID, exitCode)
	if failReason := peerAPIString(res, "fail_reason", "failReason"); failReason != "" {
		fmt.Fprintf(&b, " fail_reason=%s", failReason)
	}
	if durationMs, ok := peerAPIInt(res, "duration_ms", "durationMs"); ok {
		fmt.Fprintf(&b, " duration_ms=%d", durationMs)
	}
	if stdout := peerAPIString(res, "stdout"); stdout != "" {
		b.WriteString("\nstdout:\n")
		b.WriteString(stdout)
	}
	if stderr := peerAPIString(res, "stderr"); stderr != "" {
		b.WriteString("\nstderr:\n")
		b.WriteString(stderr)
	}
	if peerAPIBool(res, "truncated") {
		b.WriteString("\n(output truncated at 256KiB per stream)")
	}
	if reason := peerAPIString(res, "error", "message"); reason != "" && exitCode != 0 {
		b.WriteString("\nerror: ")
		b.WriteString(reason)
	}
	return b.String(), nil
}

// peerFetchFileCall 发起「从对端取文件」：POST /api/transfers
// {source_device_id, source_path}（调用设备=目标语义，服务端回 201
// {transfer:{transfer_id,...}}）。异步完成，返回 transfer_id 与进度提示；
// 文件落到本机收件箱，进度走 transfer.progress。
func (s *AgentService) peerFetchFileCall(ctx context.Context, deviceID, remotePath string) (string, error) {
	raw, err := s.doAgentServerPOST(ctx, agentPeerServerURL()+"/api/transfers", map[string]interface{}{
		"source_device_id": deviceID,
		"source_path":      remotePath,
	})
	if err != nil {
		return "", err
	}
	res := peerAPIResultObject(raw)
	transferID := peerAPIString(res, "transfer_id", "transferId")
	if transferID == "" {
		return "", fmt.Errorf("transfer accepted but server returned no transfer_id (body: %.200s)", sanitizePeerErrorMsg(string(raw)))
	}
	return fmt.Sprintf("transfer %s queued: fetching %s from device %s. The file will arrive in this machine's transfer inbox; watch transfer.progress events for completion.", transferID, remotePath, deviceID), nil
}

// deviceNoteCall upsert 一条设备 metadata：PATCH /api/devices/:deviceId/metadata
// {entries:[{key,value}]}。deviceID 为空=本机。key/value 在客户端预检长度
// （服务端 Zod：key≤64B、value≤4096B），越界本地报可读错误，不烧一次网络
// 往返换 validation_error。
func (s *AgentService) deviceNoteCall(ctx context.Context, deviceID, key, value string) (string, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		s.mu.Lock()
		deviceID = strings.TrimSpace(s.state.DeviceID)
		s.mu.Unlock()
	}
	if deviceID == "" {
		return "", errors.New("device_id unavailable (device not registered)")
	}
	if len(key) > agentPeerNoteKeyMaxBytes {
		return "", fmt.Errorf("note key too long: %d bytes (max %d)", len(key), agentPeerNoteKeyMaxBytes)
	}
	if len(value) > agentPeerNoteValueMaxBytes {
		return "", fmt.Errorf("note value too long: %d bytes (max %d)", len(value), agentPeerNoteValueMaxBytes)
	}
	raw, err := s.doAgentServerJSON(ctx, http.MethodPatch,
		fmt.Sprintf("%s/api/devices/%s/metadata", agentPeerServerURL(), url.PathEscape(deviceID)),
		map[string]interface{}{
			"entries": []map[string]string{{"key": key, "value": value}},
		})
	if err != nil {
		return "", err
	}
	summary := fmt.Sprintf("device note saved: %s = %s (device %s)", key, value, deviceID)
	if updatedBy := peerMetadataUpdatedBy(peerAPIResultObject(raw)); updatedBy != "" {
		summary += ", recorded updated_by=" + updatedBy
	}
	return summary, nil
}

// peerMetadataUpdatedBy 从 metadata 响应的 entries[].updated_by 里取来源
// （服务端把 updated_by 记在条目级，顶层没有该字段）。
func peerMetadataUpdatedBy(meta map[string]interface{}) string {
	entries, _ := meta["entries"].([]interface{})
	for _, item := range entries {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if by := peerAPIString(entry, "updated_by", "updatedBy"); by != "" {
			return by
		}
	}
	return ""
}

// deviceListReport 拉取账号设备清单并瘦身为 LLM 可读目录。安全字段白名单：
// id/name/platform/status/agent_version/peer_exec——host/location/load/IP 一律
// 不进上下文（延续「LLM 路径零泄漏」纪律，spec §7.3）。这是设备发现闭环的
// 唯一目录口：其余 peer 工具全部要 device_id，LLM 从这里选目标。
func (s *AgentService) deviceListReport(ctx context.Context) (string, error) {
	raw, err := s.doAgentServerGET(ctx, agentPeerServerURL()+"/api/devices")
	if err != nil {
		return "", err
	}
	var devices []map[string]interface{}
	if err := json.Unmarshal(raw, &devices); err != nil {
		return "", fmt.Errorf("device list response is not an array: %w", err)
	}
	selfID := strings.TrimSpace(s.currentDeviceID())
	type peerDeviceEntry struct {
		id, name, platform, status, version string
		peerExec                            bool
		self                                bool
	}
	entries := make([]peerDeviceEntry, 0, len(devices))
	for _, d := range devices {
		id := peerAPIString(d, "id", "device_id")
		if id == "" {
			continue
		}
		entries = append(entries, peerDeviceEntry{
			id:       id,
			name:     peerAPIString(d, "name", "device_name"),
			platform: peerAPIString(d, "platform"),
			status:   peerAPIString(d, "status"),
			version:  peerAPIString(d, "agent_version", "agentVersion"),
			peerExec: peerAPIBool(d, "peer_exec_enabled", "peerExecEnabled"),
			self:     id == selfID,
		})
	}
	// 本机置顶（LLM 最常用的锚点），其余按名称稳定排序。
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].self != entries[j].self {
			return entries[i].self
		}
		return entries[i].name < entries[j].name
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%d device(s) in this account:\n", len(entries))
	for _, e := range entries {
		name := e.name
		if name == "" {
			name = "-"
		}
		if e.self {
			name += " (this machine)"
		}
		peerExec := "disabled"
		if e.peerExec {
			peerExec = "enabled"
		}
		field := func(v string) string {
			if v == "" {
				return "-"
			}
			return v
		}
		fmt.Fprintf(&b, "- id=%s | %s | platform=%s | status=%s | agent=%s | peer_exec=%s\n",
			e.id, name, field(e.platform), field(e.status), field(e.version), peerExec)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// deviceInfoReport 组装设备画像快照：deviceID 为空=本机（冷层+load 摘要）；
// 非空=远端 GET /api/devices/:id + /api/devices/:id/metadata。这是唯一允许
// 返回链路基础信息的口（spec §7.3），但仍只给一行式摘要，不给拓扑细节。
// 已知取舍（评审接受）：本地分支触发的全量扫描在冷缓存时偏重；device_info
// 是低频 LLM 内省调用，缓存暖态（hello 心跳已暖）可接受。
func (s *AgentService) deviceInfoReport(ctx context.Context, deviceID string) (string, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" || deviceID == strings.TrimSpace(s.currentDeviceID()) {
		return s.deviceInfoLocalReport()
	}
	return s.deviceInfoRemoteReport(ctx, deviceID)
}

func (s *AgentService) deviceInfoLocalReport() (string, error) {
	s.mu.Lock()
	scanDirs := activeScanDirs(s.state.ScanDirectories, s.state.ScanDirectoriesEnabled)
	s.mu.Unlock()
	snapshot := collectAgentSyncSnapshot(scanDirs)
	load := collectAgentLoadSnapshot()

	hostname, _ := os.Hostname()
	projectCount := len(snapshot.Projects)
	topPaths := agentProjectTopPaths(snapshot.Projects, 5)

	var b strings.Builder
	fmt.Fprintf(&b, "device %s (this machine)\n", strings.TrimSpace(s.currentDeviceID()))
	fmt.Fprintf(&b, "hostname: %s\n", hostname)
	fmt.Fprintf(&b, "platform: %s, agent_version: %s\n", snapshot.Platform, snapshot.AgentVersion)
	fmt.Fprintf(&b, "load: cpu=%.0f%% mem=%.0f%% (%d/%d MiB used) load1=%.2f uptime=%ds\n",
		load.CPUUsagePct, load.MemUsagePct, load.MemUsedBytes>>20, load.MemTotalBytes>>20,
		load.LoadAvg1, load.AgentUptimeSec)
	fmt.Fprintf(&b, "projects: %d\n", projectCount)
	if len(topPaths) > 0 {
		fmt.Fprintf(&b, "recent projects (top %d by last_active):\n", len(topPaths))
		for _, p := range topPaths {
			fmt.Fprintf(&b, "  - %s (last_active=%s)\n", p.path, p.lastActive)
		}
	}
	fmt.Fprintf(&b, "authorized_directories: %d\n", len(snapshot.AuthorizedDirectories))
	return strings.TrimRight(b.String(), "\n"), nil
}

func (s *AgentService) deviceInfoRemoteReport(ctx context.Context, deviceID string) (string, error) {
	// 两跳 GET 都允许失败降级：devices 详情失败仍有 metadata 摘要价值。
	deviceText := ""
	raw, err := s.doAgentServerGET(ctx, fmt.Sprintf("%s/api/devices/%s", agentPeerServerURL(), url.PathEscape(deviceID)))
	if err != nil {
		deviceText = fmt.Sprintf("device detail unavailable: %s", sanitizePeerError(err))
	} else {
		device := peerAPIResultObject(raw)
		var b strings.Builder
		fmt.Fprintf(&b, "device %s\n", deviceID)
		fmt.Fprintf(&b, "name: %s\n", peerAPIString(device, "name", "device_name"))
		fmt.Fprintf(&b, "platform: %s, agent_version: %s, status: %s\n",
			peerAPIString(device, "platform"), peerAPIString(device, "agent_version", "agentVersion"),
			peerAPIString(device, "status"))
		if loadText := peerAPILoadSummary(device); loadText != "" {
			fmt.Fprintf(&b, "load: %s\n", loadText)
		}
		deviceText = strings.TrimRight(b.String(), "\n")
	}
	metaText := "metadata unavailable"
	if rawMeta, err := s.doAgentServerGET(ctx, fmt.Sprintf("%s/api/devices/%s/metadata", agentPeerServerURL(), url.PathEscape(deviceID))); err == nil {
		metaText = peerAPIMetadataSummary(peerAPIResultObject(rawMeta))
	}
	return deviceText + "\n" + metaText, nil
}

// peerAPILoadSummary 从设备详情响应里抽一行式 load 摘要（键缺失=空串）。键名
// 对齐 heartbeat net/load 线字段：cpu_usage_pct/mem_usage_pct/load_avg_1。
func peerAPILoadSummary(device map[string]interface{}) string {
	load, ok := device["load"].(map[string]interface{})
	if !ok {
		return ""
	}
	parts := make([]string, 0, 4)
	if cpu, ok := load["cpu_usage_pct"].(float64); ok {
		parts = append(parts, fmt.Sprintf("cpu=%.0f%%", cpu))
	}
	if mem, ok := load["mem_usage_pct"].(float64); ok {
		parts = append(parts, fmt.Sprintf("mem=%.0f%%", mem))
	}
	if avg, ok := load["load_avg_1"].(float64); ok {
		parts = append(parts, fmt.Sprintf("load1=%.2f", avg))
	}
	return strings.Join(parts, " ")
}

// peerAPIMetadataSummary 概括 metadata 响应：条数 + 最近 updated_by（条目级）。
func peerAPIMetadataSummary(meta map[string]interface{}) string {
	entries, _ := meta["entries"].([]interface{})
	keys := make([]string, 0, len(entries))
	updatedBy := map[string]bool{}
	for _, item := range entries {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if key := peerAPIString(entry, "key"); key != "" {
			keys = append(keys, key)
		}
		if by := peerAPIString(entry, "updated_by", "updatedBy"); by != "" {
			updatedBy[by] = true
		}
	}
	if len(keys) == 0 {
		return "metadata: 0 entries"
	}
	sort.Strings(keys)
	byList := make([]string, 0, len(updatedBy))
	for by := range updatedBy {
		byList = append(byList, by)
	}
	sort.Strings(byList)
	return fmt.Sprintf("metadata: %d entries (%s), updated_by: %s",
		len(keys), strings.Join(keys, ", "), strings.Join(byList, ", "))
}

type agentProjectPathLastActive struct {
	path       string
	lastActive string
}

// agentProjectTopPaths 按 last_active 取前 N 个项目路径（服务端展示口径一致：
// 无 last_active 的排后）。
func agentProjectTopPaths(projects []models.AgentProject, n int) []agentProjectPathLastActive {
	out := make([]agentProjectPathLastActive, 0, len(projects))
	for _, p := range projects {
		if strings.TrimSpace(p.Path) == "" {
			continue
		}
		out = append(out, agentProjectPathLastActive{path: p.Path, lastActive: p.LastActiveAt})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].lastActive > out[j].lastActive
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// ---------------------------------------------------------------------------
// 注册表工具处理器（tools.HandlerFunc 形状）
// ---------------------------------------------------------------------------

func agentPeerToolRequestID(msg map[string]interface{}) string {
	return remoteString(msg, "request_id")
}

func agentPeerToolError(eventType, requestID string, err error) map[string]interface{} {
	return map[string]interface{}{
		"type":       eventType,
		"request_id": requestID,
		"error":      agentPeerReadableError(err),
	}
}

func agentPeerExecPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := agentPeerToolRequestID(msg)
	deviceID := remoteString(msg, "device_id")
	command := remoteString(msg, "command")
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(command) == "" {
		return agentPeerToolError(AgentPeerToolEventExec+".result", requestID, errors.New("device_id and command are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentPeerToolTimeoutMs*time.Millisecond)
	defer cancel()
	text, err := agentPeerServiceProvider().peerExecCall(ctx, deviceID, command,
		remoteInt(msg, "timeout_ms", 0), remoteString(msg, "workdir"))
	if err != nil {
		return agentPeerToolError(AgentPeerToolEventExec+".result", requestID, err)
	}
	return map[string]interface{}{
		"type":       AgentPeerToolEventExec + ".result",
		"request_id": requestID,
		"result":     text,
	}
}

func agentPeerFetchFilePayload(msg map[string]interface{}) map[string]interface{} {
	requestID := agentPeerToolRequestID(msg)
	deviceID := remoteString(msg, "device_id")
	remotePath := remoteString(msg, "remote_path")
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(remotePath) == "" {
		return agentPeerToolError(AgentPeerToolEventFetchFile+".result", requestID, errors.New("device_id and remote_path are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentPeerFetchFileTimeoutMs*time.Millisecond)
	defer cancel()
	text, err := agentPeerServiceProvider().peerFetchFileCall(ctx, deviceID, remotePath)
	if err != nil {
		return agentPeerToolError(AgentPeerToolEventFetchFile+".result", requestID, err)
	}
	return map[string]interface{}{
		"type":       AgentPeerToolEventFetchFile + ".result",
		"request_id": requestID,
		"result":     text,
	}
}

func agentDeviceInfoPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := agentPeerToolRequestID(msg)
	ctx, cancel := context.WithTimeout(context.Background(), agentPeerDeviceInfoTimeoutMs*time.Millisecond)
	defer cancel()
	text, err := agentPeerServiceProvider().deviceInfoReport(ctx, remoteString(msg, "device_id"))
	if err != nil {
		return agentPeerToolError(AgentPeerToolEventDeviceInfo+".result", requestID, err)
	}
	return map[string]interface{}{
		"type":       AgentPeerToolEventDeviceInfo + ".result",
		"request_id": requestID,
		"result":     text,
	}
}

func agentPeerDeviceListPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := agentPeerToolRequestID(msg)
	ctx, cancel := context.WithTimeout(context.Background(), agentPeerDeviceListTimeoutMs*time.Millisecond)
	defer cancel()
	text, err := agentPeerServiceProvider().deviceListReport(ctx)
	if err != nil {
		return agentPeerToolError(AgentPeerToolEventDeviceList+".result", requestID, err)
	}
	return map[string]interface{}{
		"type":       AgentPeerToolEventDeviceList + ".result",
		"request_id": requestID,
		"result":     text,
	}
}

func agentDeviceNotePayload(msg map[string]interface{}) map[string]interface{} {
	requestID := agentPeerToolRequestID(msg)
	key := remoteString(msg, "key")
	value := remoteString(msg, "value")
	if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return agentPeerToolError(AgentPeerToolEventDeviceNote+".result", requestID, errors.New("key and value are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentPeerNoteTimeoutMs*time.Millisecond)
	defer cancel()
	text, err := agentPeerServiceProvider().deviceNoteCall(ctx, remoteString(msg, "device_id"), key, value)
	if err != nil {
		return agentPeerToolError(AgentPeerToolEventDeviceNote+".result", requestID, err)
	}
	return map[string]interface{}{
		"type":       AgentPeerToolEventDeviceNote + ".result",
		"request_id": requestID,
		"result":     text,
	}
}

// ---------------------------------------------------------------------------
// agentcore 自定义工具桥（宿主注入，子 agent 桥/MCP 桥同款）
// ---------------------------------------------------------------------------

// agentCorePeerToolBridge 把一个 peer 工具包成内核 agentcore.Tool。Execute 共享
// 注册表处理器实现；自带 journal 落行（内核 OnToolCall 只记内置名集，桥接
// 工具按既有约定自带 onCall，不重不漏）。
type agentCorePeerToolBridge struct {
	name        string
	description string
	schema      map[string]interface{}
	execute     func(ctx context.Context, input map[string]interface{}) (string, bool)
	journal     func(tool, summary string, isErr bool)
}

func (b *agentCorePeerToolBridge) Name() string        { return b.name }
func (b *agentCorePeerToolBridge) Description() string { return b.description }
func (b *agentCorePeerToolBridge) InputSchema() map[string]interface{} {
	return b.schema
}

func (b *agentCorePeerToolBridge) Execute(ctx context.Context, input map[string]interface{}, _ *agentcore.ToolEnv) agentcore.ToolResult {
	out, isErr := b.execute(ctx, input)
	if b.journal != nil {
		// rune 安全截断（共享 inventory 侧 truncateAgentText），防多字节字符
		// 被字节切开。
		summary := truncateAgentText(strings.ReplaceAll(out, "\n", " "), 200)
		b.journal(b.name, summary, isErr)
	}
	return agentcore.ToolResult{Content: out, IsError: isErr}
}

// peerToolSchema 组 JSON schema：type=object + properties + required。
func peerToolSchema(required []string, props map[string]interface{}) map[string]interface{} {
	req := make([]interface{}, 0, len(required))
	for _, r := range required {
		req = append(req, r)
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": props,
		"required":   req,
	}
}

// agentCorePeerTools 构建设备互访工具集（阶段 1 四件）。run.readOnly 由调用方
// 门控（与 MCP/子 agent 注入同判据）；审批取舍见文件头注释（各工具真实门禁
// 不同，peer_exec 有 ACL+开关，fetch/note 仅同账号）。
func (m *agentAIManager) agentCorePeerTools(run agentAIRun) []agentcore.Tool {
	journal := func(tool, summary string, isErr bool) {
		if m == nil || m.coreJournal == nil || strings.TrimSpace(run.sessionID) == "" {
			return
		}
		m.coreJournal.appendTool(run.sessionID, tool, summary, isErr)
	}
	return []agentcore.Tool{
		&agentCorePeerToolBridge{
			name: AgentPeerToolEventExec,
			description: "Run a one-shot command on another device owned by the same account and " +
				"return its exit code plus stdout/stderr. The remote device must have peer exec enabled. " +
				"Serial per target device: a busy target returns fail_reason=busy immediately.",
			schema: peerToolSchema([]string{"device_id", "command"}, map[string]interface{}{
				"device_id":  map[string]interface{}{"type": "string", "description": "Target device id"},
				"command":    map[string]interface{}{"type": "string", "description": "Shell command to run on the target device"},
				"timeout_ms": map[string]interface{}{"type": "integer", "description": "Command timeout in ms (1000-600000, default 30000; out-of-range values are clamped)"},
				"workdir":    map[string]interface{}{"type": "string", "description": "Working directory on the target device (must exist, default home)"},
			}),
			execute: func(ctx context.Context, input map[string]interface{}) (string, bool) {
				text, err := agentPeerServiceProvider().peerExecCall(ctx,
					inputStringField(input, "device_id"),
					inputStringField(input, "command"),
					inputIntField(input, "timeout_ms"),
					inputStringField(input, "workdir"))
				if err != nil {
					return agentPeerReadableError(err), true
				}
				return text, false
			},
			journal: journal,
		},
		&agentCorePeerToolBridge{
			name: AgentPeerToolEventFetchFile,
			description: "Queue an asynchronous file fetch from another device into this machine's " +
				"transfer inbox. Returns a transfer_id; completion is delivered via transfer.progress.",
			schema: peerToolSchema([]string{"device_id", "remote_path"}, map[string]interface{}{
				"device_id":   map[string]interface{}{"type": "string", "description": "Source device id"},
				"remote_path": map[string]interface{}{"type": "string", "description": "File path on the source device"},
			}),
			execute: func(ctx context.Context, input map[string]interface{}) (string, bool) {
				text, err := agentPeerServiceProvider().peerFetchFileCall(ctx,
					inputStringField(input, "device_id"),
					inputStringField(input, "remote_path"))
				if err != nil {
					return agentPeerReadableError(err), true
				}
				return text, false
			},
			journal: journal,
		},
		&agentCorePeerToolBridge{
			name: AgentPeerToolEventDeviceInfo,
			description: "Device profile snapshot: hostname, platform, agent version, load summary, " +
				"project count with recent project paths, authorized directory count. Omit device_id " +
				"for this machine; pass one for a remote device in the same account.",
			schema: peerToolSchema(nil, map[string]interface{}{
				"device_id": map[string]interface{}{"type": "string", "description": "Target device id; omit for this machine"},
			}),
			execute: func(ctx context.Context, input map[string]interface{}) (string, bool) {
				text, err := agentPeerServiceProvider().deviceInfoReport(ctx, inputStringField(input, "device_id"))
				if err != nil {
					return agentPeerReadableError(err), true
				}
				return text, false
			},
			journal: journal,
		},
		&agentCorePeerToolBridge{
			name: AgentPeerToolEventDeviceList,
			description: "List every device in this account with id, name, platform, online status " +
				"and whether peer exec is enabled. This is the directory to pick device_id from for " +
				"the other peer tools (peer.exec / peer.fetch_file / peer.device_info / peer.device_note).",
			schema: peerToolSchema(nil, map[string]interface{}{}),
			execute: func(ctx context.Context, _ map[string]interface{}) (string, bool) {
				text, err := agentPeerServiceProvider().deviceListReport(ctx)
				if err != nil {
					return agentPeerReadableError(err), true
				}
				return text, false
			},
			journal: journal,
		},
		&agentCorePeerToolBridge{
			name: AgentPeerToolEventDeviceNote,
			description: "Save a named note (key/value metadata) on a device. Omit device_id to note " +
				"this machine. Notes survive reconnects and are visible to every device in the account. " +
				"Key max 64 bytes, value max 4096 bytes.",
			schema: peerToolSchema([]string{"key", "value"}, map[string]interface{}{
				"key":       map[string]interface{}{"type": "string", "description": "Note key (max 64 bytes)"},
				"value":     map[string]interface{}{"type": "string", "description": "Note value (max 4096 bytes)"},
				"device_id": map[string]interface{}{"type": "string", "description": "Target device id; omit for this machine"},
			}),
			execute: func(ctx context.Context, input map[string]interface{}) (string, bool) {
				text, err := agentPeerServiceProvider().deviceNoteCall(ctx,
					inputStringField(input, "device_id"),
					inputStringField(input, "key"),
					inputStringField(input, "value"))
				if err != nil {
					return agentPeerReadableError(err), true
				}
				return text, false
			},
			journal: journal,
		},
	}
}

func inputStringField(input map[string]interface{}, key string) string {
	if input == nil {
		return ""
	}
	return remoteString(input, key)
}

func inputIntField(input map[string]interface{}, key string) int {
	if input == nil {
		return 0
	}
	return remoteInt(input, key, 0)
}

// ---------------------------------------------------------------------------
// 本地 MCP/CLI 面（导出门面）
// ---------------------------------------------------------------------------

// Local 门面：`aliang mcp`（stdio MCP）与本机 CLI 经 56433 loopback 路由调用，
// 复用与 LLM 工具完全相同的实现与脱敏，不另开代码路径。认证三层（零新增凭据）：
// ① stdio 子进程由 CLI 自己拉起——同用户同进程身份即信任；② 本地路由只在
// 56433 loopbackOnly 红线内可达；③ 出网凭据（用户 JWT + 设备身份）由本进程
// 持有，CLI/LLM 全程接触不到。目标机门禁（peer_exec_enabled 默认关 + 云端
// peer ACL + 全量审计）对 MCP 路径同样生效。

func (s *AgentService) PeerExecLocal(ctx context.Context, deviceID, command string, timeoutMs int, workdir string) (string, error) {
	return s.peerExecCall(ctx, deviceID, command, timeoutMs, workdir)
}

func (s *AgentService) PeerFetchFileLocal(ctx context.Context, deviceID, remotePath string) (string, error) {
	return s.peerFetchFileCall(ctx, deviceID, remotePath)
}

func (s *AgentService) DeviceListLocal(ctx context.Context) (string, error) {
	return s.deviceListReport(ctx)
}

func (s *AgentService) DeviceInfoLocal(ctx context.Context, deviceID string) (string, error) {
	return s.deviceInfoReport(ctx, deviceID)
}

func (s *AgentService) DeviceNoteLocal(ctx context.Context, deviceID, key, value string) (string, error) {
	return s.deviceNoteCall(ctx, deviceID, key, value)
}
