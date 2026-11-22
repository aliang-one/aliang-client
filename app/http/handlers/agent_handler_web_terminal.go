package handlers

// 网页终端 HTTP 面(spec docs/superpowers/specs/2026-11-21-web-terminal-design.md §6.1):
// GET state + POST command。真身在 user agent runtime(保险箱状态与令牌),
// core 侧闸门②+proxyIfNeeded 透传;reset 额外 loopback-only(spec §2/§6.4,
// 只能在 core 拦——runtime 侧全是 loopback)。core 侧 vault 是构造期陈旧镜像,
// 状态/命令一律 proxy 之后由 runtime 角色处理,core 腿绝不直读。

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
)

// WebTerminalTokenHeader 解锁令牌随 header 走(plain HTTP proxy 全量转发,
// 见 copyAgentProxyHeaders);WS 腿走首帧 auth,不经 header。
const WebTerminalTokenHeader = "X-Web-Terminal-Token"

// WebTerminalLoopbackHeader 审计真源标记头(P3.6,阶段 1 终审 Finding B 残余):
// core 腿在 proxy 前把「请求真实来源是否 loopback」编码进此头,proxy 全量转发
// ——runtime 侧透传腿的 RemoteAddr 恒为 127.0.0.1(core 拨号),IsLoopbackRequest
// 恒真,只能靠此头区分 LAN cookie 来源与 loopback 来源,审计 origin 才真实。
const WebTerminalLoopbackHeader = "X-Aliang-WT-Loopback"

// markWebTerminalLoopbackSource 真源标记(仅 core 腿执行):先无条件 Del 再按
// IsLoopbackRequest 决定 Set——Del 不可省:LAN 客户端伪造同名头若不被剥净,会经
// proxy 原样透传,在 runtime 侧冒充 loopback(lock 坏令牌豁免/审计标记都会被
// 冒充)。runtime 腿(IsUserAgentRuntime)不做标记:头是 core 透传来的真源凭证,
// 按本跳 RemoteAddr(proxy 拨号恒 loopback)重写会把 LAN 又误标回 loopback。
// 直呼 runtime HTTP 口的本机进程无此头 → 按无头=cookie 保守误标(审计价值在
// 区分 LAN;直呼路径归属 loopback 的场景经 core 走即可正确标记)。
func markWebTerminalLoopbackSource(r *http.Request) {
	if services.IsUserAgentRuntime() {
		return
	}
	r.Header.Del(WebTerminalLoopbackHeader)
	if middleware.IsLoopbackRequest(r) {
		r.Header.Set(WebTerminalLoopbackHeader, "1")
	}
}

// webTerminalLoopbackOrigin 审计 origin 判定(runtime 腿语义):header==1 ⟺
// core 腿见证的真实来源是 loopback(core 对 loopback 来源也会透传并带标记);
// 无头=cookie(LAN 透传腿 core 剥净了标记,或直呼 runtime 的本机调用——后者
// 保守误标为 cookie,见 markWebTerminalLoopbackSource 注记)。
func webTerminalLoopbackOrigin(r *http.Request) string {
	if r.Header.Get(WebTerminalLoopbackHeader) == "1" {
		return "loopback"
	}
	return "cookie"
}

func guardWebTerminalGate(w http.ResponseWriter, r *http.Request) bool {
	// 与 HandleAIStream 同款双闸:有效 dashboard session ∥ loopback。
	if !middleware.ValidateDashboardSession(r) && !middleware.IsLoopbackRequest(r) {
		middleware.RequireDashboardSession(w, r)
		return false
	}
	return true
}

// HandleWebTerminalState GET /api/agent/web-terminal → vault 状态快照。
func (h *AgentHandler) HandleWebTerminalState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	common.Success(w, h.service.WebTerminalStatus())
}

// parseWebTerminalCommandBody 读取并解析命令体(≤4KiB),随后**回填 r.Body**
// 供 core 腿 proxyIfNeeded 原样转发——io.ReadAll 会抽干 body,而
// proxyToUserAgentClient 直接转发 r.Body,不回填=runtime 收到空体→400,
// LAN setup/verify/enable 全链路死(评审 Critical)。走到回填时 raw 必为
// 完整合法 JSON,回填无损;任何解析失败返回 ok=false(400 由调用方落)。
func parseWebTerminalCommandBody(r *http.Request) (action, pin, oldPin, newPin string, enabled *bool, ok bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil || len(raw) == 0 {
		return "", "", "", "", nil, false
	}
	var body struct {
		Action  string `json:"action"`
		Pin     string `json:"pin,omitempty"`
		OldPin  string `json:"old_pin,omitempty"`
		NewPin  string `json:"new_pin,omitempty"`
		Enabled *bool  `json:"enabled,omitempty"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", "", "", "", nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw)) // 回填供 proxy 腿原样转发
	return body.Action, body.Pin, body.OldPin, body.NewPin, body.Enabled, true
}

// HandleWebTerminalCommand POST /api/agent/web-terminal {action,...}。
// 业务失败走 Success 信封 {ok:false, reason,...}(前端要结构化读锁定秒数/
// 剩余次数,Error 信封的 details 前端 request() 不透);真正的门(闸门②/reset
// loopback)才用 HTTP 错误码。
func (h *AgentHandler) HandleWebTerminalCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	action, pin, oldPin, newPin, enabled, ok := parseWebTerminalCommandBody(r)
	if !ok {
		common.ErrorBadRequest(w, "Invalid request body", nil)
		return
	}
	// reset 仅 loopback(core 腿拦截;runtime 侧天然 loopback)
	if action == "reset" && !middleware.IsLoopbackRequest(r) {
		common.Error(w, common.CodeForbidden, "PIN reset is loopback only", nil)
		return
	}
	token := r.Header.Get(WebTerminalTokenHeader)
	// killall 的空令牌放行只该属于 loopback 直呼(终审 Finding A 时 runtime 侧
	// IsLoopbackRequest 恒真、服务级闸不可达,故在此 core 腿预 proxy 执法):
	// HTTP 面非本机来源必须带令牌。P3.6 真源头落地后服务级闸亦真实可达,本处
	// 降级为纵深防御的前置一层(双闸同拒)。令牌有效性仍在 runtime 校验。
	if action == "killall" && !middleware.IsLoopbackRequest(r) && token == "" {
		common.Error(w, common.CodeForbidden, "killall requires the unlock token", nil)
		return
	}
	// 真源标记先于 proxy:core 见证的真实来源随头透传,runtime 侧据实标 origin
	markWebTerminalLoopbackSource(r)
	if h.proxyIfNeeded(w, r) {
		return
	}
	// loopback 语义改读真源头(Finding B 修正):runtime 腿 RemoteAddr 恒
	// 127.0.0.1,IsLoopbackRequest 恒真——LAN 透传腿此前被误标 loopback 且享
	// loopback 门豁免(killall 空令牌/lock 坏令牌);读头后 LAN=false、本机=1,
	// 服务层「非 loopback 拒」的纵深防御经 HTTP 真正可达。
	resp := h.service.WebTerminalRunCommand(action, pin, oldPin, newPin, enabled, token, webTerminalLoopbackOrigin(r) == "loopback")
	common.Success(w, resp)
}

// HandleWebTerminalStream GET /api/agent/web-terminal/stream。
// core 角色:闸门②+同源闸+proxyAIStream TCP 透传(与聊天 WS 同管线,路径无关)。
// runtime 角色:双闸→upgrade→首帧强制 terminal.auth(15s 内未过=close 1008)
// →读循环每帧复验令牌(闲置重锁后死连接不得继续驱动 shell;复验即滑动续期)
// →DispatchTerminalFrame(msg, write, WebTerminalEnabled)。每会话一条 WS,
// 前端重连带 attach:true 续屏(manager 语义)。
func (h *AgentHandler) HandleWebTerminalStream(w http.ResponseWriter, r *http.Request) {
	if !guardWebTerminalGate(w, r) {
		return
	}
	if !services.IsUserAgentRuntime() {
		proxyAIStream(w, r)
		return
	}
	// nil-service 防御(单测形态 NewAgentHandler(nil)):auth 校验前必须拦住,
	// 否则 nil receiver 解引用 panic(net/http 会 recover 但语义丢失)
	if h.service == nil {
		rejectBrowserWS(w, r, "web terminal unavailable")
		return
	}
	conn, err := aiStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	// 入站只有小帧(auth/input/resize/close),1MiB 足够;勿照抄远端 4MiB
	// (那是为 replay 批量出站留的,与本入站限制无关)
	conn.SetReadLimit(1 << 20)

	var writeMu sync.Mutex
	writeJSON := func(payload interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(payload)
	}

	// 首帧强制鉴权
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var authMsg map[string]interface{}
	if err := conn.ReadJSON(&authMsg); err != nil {
		return
	}
	authToken := ""
	if webTerminalFrameString(authMsg, "type") == "terminal.auth" {
		authToken = webTerminalFrameString(authMsg, "token")
	}
	if !h.service.WebTerminalValidateToken(authToken) {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "web terminal locked or token invalid"),
			time.Now().Add(2*time.Second))
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		if msg == nil {
			continue
		}
		// 每帧复验令牌:闲置重锁(15m)后,已建立的连接不得继续驱动 shell;
		// 复验同时即滑动续期(ValidateToken 语义)
		if !h.service.WebTerminalValidateToken(authToken) {
			_ = writeJSON(services.AgentTerminalErrorPayloadPublic("", errors.New("web terminal re-locked")))
			// 与首帧鉴权失败路径对称:补发 1008 关闭帧,免前端白发一轮重连
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "web terminal re-locked"),
				time.Now().Add(2*time.Second))
			return
		}
		frameType := webTerminalFrameString(msg, "type")
		switch frameType {
		case "terminal.create":
			h.service.WebTerminalAuditTerminal("terminal_create", webTerminalFrameString(msg, "session_id"))
		case "terminal.close":
			h.service.WebTerminalAuditTerminal("terminal_close", webTerminalFrameString(msg, "session_id"))
		}
		h.service.DispatchTerminalFrame(msg, writeJSON, h.service.WebTerminalEnabled)
	}
}

// webTerminalFrameString 帧字段安全取串(services 的 remoteString 不导出)。
func webTerminalFrameString(msg map[string]interface{}, key string) string {
	if msg == nil {
		return ""
	}
	v, _ := msg[key].(string)
	return v
}

// ——— 阶段 3:FS 四端点(spec §6.3)———
// 门链与终端面同门同治:①双闸(guardWebTerminalGate)→②core 腿 proxyIfNeeded
// 透传→③runtime 腿主开关+令牌头(403 风格对齐 reset/killall 的 HTTP 门)→
// ④服务层路径守卫(agentFSResolve)。core 腿对四端点零 body 解析(list/read/
// download 是 GET+query;upload 的 multipart 原样流穿——阶段1 勘误#10 的
// body-drain 陷阱在 FS 面从设计上规避,不靠自觉)。真执行全在 runtime;core
// 侧 vault 是构造期陈旧镜像,令牌/开关判断绝不在 core 做。

// webTerminalFSRuntimeGate 运行时腿统一门(所有 FS 端点过 proxy 之后先过它):
// 主开关关=FS 一并关(403)、令牌头无效=403、nil 服务防御。失败已写响应。
func (h *AgentHandler) webTerminalFSRuntimeGate(w http.ResponseWriter, r *http.Request) bool {
	if h.service == nil {
		common.Error(w, common.CodeForbidden, "Web terminal unavailable", nil)
		return false
	}
	if !h.service.WebTerminalEnabled() {
		common.Error(w, common.CodeForbidden, "Web terminal is disabled", nil)
		return false
	}
	if !h.service.WebTerminalValidateToken(r.Header.Get(WebTerminalTokenHeader)) {
		common.Error(w, common.CodeForbidden, "Invalid web terminal token", nil)
		return false
	}
	return true
}

// writeAgentFSQueryError FS 查询/IO 错误的统一业务映射:穿越=102 禁止、不存
// 在=103,其余(权限/是目录等)以禁止兜底(不向 LAN 泄内部路径细节)。
func writeAgentFSQueryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, services.ErrAgentFSEscapeRoot):
		common.Error(w, common.CodeForbidden, "Path escapes the file browser root", nil)
	case errors.Is(err, fs.ErrNotExist):
		common.Error(w, common.CodeNotFound, "Path not found", nil)
	default:
		common.Error(w, common.CodeForbidden, "Path is not accessible", nil)
	}
}

// HandleWebTerminalFSList GET /api/agent/fs/list?path=(缺省=守卫根 home)。
func (h *AgentHandler) HandleWebTerminalFSList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	markWebTerminalLoopbackSource(r) // 真源标记先于 proxy(list/read 不审计,标记供同链路统一语义)
	if h.proxyIfNeeded(w, r) {
		return
	}
	if !h.webTerminalFSRuntimeGate(w, r) {
		return
	}
	dir := strings.TrimSpace(r.URL.Query().Get("path"))
	if dir == "" {
		dir = "."
	}
	resolved, entries, err := h.service.AgentFSList(dir)
	if err != nil {
		writeAgentFSQueryError(w, err)
		return
	}
	common.Success(w, map[string]interface{}{"path": resolved, "entries": entries})
}

// HandleWebTerminalFSRead GET /api/agent/fs/read?path=——2MiB 上限截断+前 8KiB
// NUL 二进制探测,信封 {path,content,truncated,binary}(spec §6.3)。
func (h *AgentHandler) HandleWebTerminalFSRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	markWebTerminalLoopbackSource(r) // 同 list:标记先于 proxy
	if h.proxyIfNeeded(w, r) {
		return
	}
	if !h.webTerminalFSRuntimeGate(w, r) {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		common.ErrorBadRequest(w, "Missing required query: path", nil)
		return
	}
	resolved, content, truncated, binary, err := h.service.AgentFSRead(path, services.AgentFSReadMaxBytes)
	if err != nil {
		writeAgentFSQueryError(w, err)
		return
	}
	common.Success(w, map[string]interface{}{
		"path":      resolved,
		"content":   content,
		"truncated": truncated,
		"binary":    binary,
	})
}

// HandleWebTerminalFSDownload GET /api/agent/fs/download?path=——ServeContent
// 流式(range 支持),头模板=HandleAttachmentGet 的 nosniff+Content-Disposition
// (此处 attachment)。fs_download 审计在发完后落(spec §6.4;ServeContent
// 阻塞到发完才返回)。
func (h *AgentHandler) HandleWebTerminalFSDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	markWebTerminalLoopbackSource(r) // fs_download 审计真源:标记先于 proxy 透传
	// download 是大文件流:走零硬超时的 fsClient 数据面腿(proxyFSIfNeeded),
	// 12s 控制面 client 会把慢链路大文件中途掐断成误报 503。
	if h.proxyFSIfNeeded(w, r) {
		return
	}
	if !h.webTerminalFSRuntimeGate(w, r) {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		common.ErrorBadRequest(w, "Missing required query: path", nil)
		return
	}
	f, info, resolved, err := h.service.AgentFSOpen(path)
	if err != nil {
		writeAgentFSQueryError(w, err)
		return
	}
	defer func() { _ = f.Close() }()
	contentType := mime.TypeByExtension(filepath.Ext(info.Name()))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="`+webTerminalDownloadName(info.Name())+`"`)
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	h.service.WebTerminalAuditFS("fs_download", resolved, info.Size(), webTerminalLoopbackOrigin(r))
}

// webTerminalDownloadName Content-Disposition 文件名防注入:引号/CR/LF 替 _
// (盘上既有文件名不经上传清洗,header 值不能被它们撑破)。
func webTerminalDownloadName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\r' || r == '\n' {
			return '_'
		}
		return r
	}, filepath.Base(name))
}

// HandleWebTerminalFSUpload POST /api/agent/fs/upload——multipart(field `file`
// +可选 `dir`,缺省守卫根)。**PROXY-FIRST:core 腿零 body 解析**(multipart
// 原样流穿;阶段1 勘误#10),全部校验在 runtime 腿:主开关+令牌(同门)→
// MaxBytesReader 钳总包(100MiB 文件+1MiB multipart 开销,防超大包把临时盘
// 打爆——ParseMultipartForm 的入参只是内存阈值不是总量上限)→LimitReader
// 精确量文件部分→服务层清洗+守卫落盘+fs_upload 审计。
func (h *AgentHandler) HandleWebTerminalFSUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	markWebTerminalLoopbackSource(r) // fs_upload 审计真源:标记先于 proxy 透传
	// upload 同为数据面:multipart 流穿零硬超时 fsClient(12s 控制面会把
	// 100MiB 慢上传中途掐断);core 腿依旧零 body 解析(proxy-first)。
	if h.proxyFSIfNeeded(w, r) {
		return
	}
	if !h.webTerminalFSRuntimeGate(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, services.AgentFSUploadMaxBytes+(1<<20))
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		common.ErrorBadRequest(w, "Invalid multipart form or file exceeds 100 MiB", nil)
		return
	}
	// Go server 不自动清理 ParseMultipartForm 溢出到 os.TempDir 的临时文件
	// (长驻 runtime 进程会积累,单文件可达 ~100MiB)——解析成功后显式清理
	// (字节已由 FormFile/ReadAll 读走,RemoveAll 只删临时落盘件)。
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	dir := strings.TrimSpace(r.FormValue("dir"))
	if dir == "" {
		dir = "."
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		common.ErrorBadRequest(w, "Missing required multipart file: file", nil)
		return
	}
	defer func() { _ = file.Close() }()
	src, err := io.ReadAll(io.LimitReader(file, services.AgentFSUploadMaxBytes+1))
	if err != nil {
		common.ErrorBadRequest(w, "Read upload failed", nil)
		return
	}
	if int64(len(src)) > services.AgentFSUploadMaxBytes {
		common.ErrorBadRequest(w, "File exceeds 100 MiB", nil)
		return
	}
	finalPath, err := h.service.AgentFSUpload(dir, header.Filename, src)
	if err != nil {
		if errors.Is(err, services.ErrAgentFSBadFilename) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		writeAgentFSQueryError(w, err)
		return
	}
	// fs_upload 审计(P3.6 起在 handler 落):真源 origin 只有 HTTP 层可见
	// (服务层无请求上下文),与 fs_download 同走 WebTerminalAuditFS 同款摘要。
	h.service.WebTerminalAuditFS("fs_upload", finalPath, int64(len(src)), webTerminalLoopbackOrigin(r))
	common.Success(w, map[string]interface{}{"path": finalPath, "bytes": len(src)})
}
