package handlers

// 远程协助 P2 Task 2:客户端 core 同构代理路由(spec §7.3/§8.3「两端同构,
// rrweb 资产 URL 通用的关键」)。GET /assist/service/* 剥离 /assist/service
// 前缀后反代到本机 dashboard 真身(默认 127.0.0.1:56431),使 rrweb 快照里
// 的根相对资产 URL(/assets/…)与相对资产 URL(./assets/…)在观看端两个
// origin 上均可解析:admin 操作页 iframe 落在 /assist/:id/service/* 前缀下,
// 相对资产解析进该前缀;客户端观看页的 Replayer iframe base 即本机 dashboard
// origin,本路由与之路径形态同构,观看页日后若迁去前缀下也直接可用。
//
// 段序(照 assist 家族四段制):
//   ①方法闸(白名单 GET/HEAD/POST/PUT/PATCH/DELETE,其余 405——评审修订,
//     初版 GET-only 会把镜像操作面饿死:镜像仪表盘的操作流量是 POST 携带的,
//     连仪表盘启动的 POST /api/dashboard/session 都会被 405,页面镜像的
//     整个目的 spec §8.3「admin 真实操作客户端仪表盘」不成立。实测口径:
//     agentApi.js GET×25/POST×25/PUT×3/DELETE×3/PATCH×1,authApi/
//     quickSetupApi 另有 GET/POST/DELETE——写方法必须放行;写请求照常先过
//     双闸+同源闸+会话闸,审计经下游 handler 的 WT/assist 审计链)
//   ②双闸(guardWebTerminalGate)+ 同源闸(assistSameOriginGuard)+ 真源标记
//   ③前缀剥离 + 回环守卫(剥离后仍带 /assist/service 前缀的嵌套路径 400:
//     core 腿上游就是本进程,该形态每层前缀会多一跳 loopback 自拨、N 层=
//     N 跳的放大面;前缀剥离本身已消除正常回环面,此守卫兜人造嵌套形态)
//   ④会话闸(requested/active 放行,spec §11「非 active 会话 403」;GET 与
//     写方法同闸——写操作只在会话期放行):会话状态机真源住 user agent
//     runtime(P1 定案),runtime 腿读进程内快照,core 腿探针拨 runtime
//     session 端点取真值(admin 隧道腿落在本路由的正是 core,spec §8.3;
//     探针拨号失败=信封 503,同 proxy-不可达惯例)
//   ⑤剥前缀反代上游(零硬超时 client,照 proxyFSIfNeeded 惯例——镜像页的
//     大资产流不吃 12s 控制面期限;body 原样流穿,PROXY-FIRST 零解析)+
//     透传腿真源标记背书 + 剥响应帧头(X-Frame-Options 全剥、CSP 仅摘
//     frame-ancestors 指令,其余透传)后流穿。
//
// core 腿上游=本进程(127.0.0.1:56431 自拨)是 spec §7.3 明文 bless 的
// self-call 形态(「self-call 或直挂静态+API 同源面」):dashboard 静态面
// (registerStaticFiles 的 mux)在 app/http 包内不作为离散 http.Handler
// 暴露,handlers 包反向引用会构成 import 环,自拨一跳 loopback 是最廉价的
// 等价实现——且正常形态经前缀剥离后不再匹配本路由,零回环。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
)

// assistServicePrefix 同构代理路径前缀(注册形态 /assist/service/ 子树;
// spec §7.3 客户端侧无 :id 段——会话闸按「当前会话」语义收口,与观看 WS
// 的单活跃会话模型一致)。
const assistServicePrefix = "/assist/service/"

// assistServiceAllowedMethods 镜像操作面方法白名单(评审修订,见文件头①):
// agentApi.js 实测 GET/POST/PUT/DELETE/PATCH 全在用,authApi/quickSetupApi
// 另有 GET/POST/DELETE;HEAD 随 GET 放行(资产探测)。CONNECT/TRACE/OPTIONS
// 等其余一律 405(裸路径注册下 mux 不分方法,闸在此处唯一生效)。
var assistServiceAllowedMethods = map[string]bool{
	http.MethodGet:    true,
	http.MethodHead:   true,
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// errAssistServiceSessionInactive core 腿探针「会话不活跃」哨兵(idle/ended
// 或 runtime 侧开关 403)——调用方映射 403;拨号/解析失败走 503 信封。
var errAssistServiceSessionInactive = errors.New("assist session is not active")

// HandleAssistServiceProxy GET /assist/service/* 同构代理(双注册:core
// routes.go + agentruntime/server.go,缺任一侧=404,双注册纪律)。
func (h *AgentHandler) HandleAssistServiceProxy(w http.ResponseWriter, r *http.Request) {
	if !assistServiceAllowedMethods[r.Method] {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	if !assistSameOriginGuard(w, r) {
		return
	}
	markWebTerminalLoopbackSource(r) // 真源标记先于任何拨号(伪造头剥净,家族纪律)

	// 前缀剥离 + 回环守卫(段③)。判别式:剥离必须发生在反代之前——
	// 不剥则每个资产/API 请求 404,而 SPA 回退根 handler 对缺失资产返回
	// 200+index.html=静默坏页,其余闸门测试都抓不住它的缺失。
	stripped, ok := assistServiceStrippedPath(r.URL.Path)
	if !ok || strings.HasPrefix(stripped, "/assist/service") {
		common.ErrorBadRequest(w, "Invalid assist service path", nil)
		return
	}

	// 会话闸(段④,requested/active 放行;写方法同闸——镜像操作只在会话期
	// 放行,评审修订①的闸序前提)。
	if services.IsUserAgentRuntime() {
		if !h.assistRuntimeGate(w, r) {
			return
		}
		snap := h.service.AssistSnapshot()
		if snap.State != "requested" && snap.State != "active" {
			common.ErrorForbidden(w, "Assist session is not active")
			return
		}
	} else if err := h.assistRuntimeSessionActive(); err != nil {
		if errors.Is(err, errAssistServiceSessionInactive) {
			common.ErrorForbidden(w, "Assist session is not active")
			return
		}
		common.Error(w, common.CodeServiceUnavailable, "User agent process is not running", map[string]interface{}{
			"error": err.Error(),
			"url":   services.UserAgentBaseURL(),
		})
		return
	}

	if err := h.proxyAssistService(w, r, stripped); err != nil {
		common.Error(w, common.CodeServiceUnavailable, "Dashboard upstream is not reachable", map[string]interface{}{
			"error": err.Error(),
			"url":   assistServiceUpstreamBase(),
		})
	}
}

// assistServiceStrippedPath 剥离同构前缀,返回上游路径(恒以 / 开头;前缀
// 根 /assist/service 与 /assist/service/ 都归一到上游 /——镜像 iframe 以
// 本路由子树根为应用根,landing 即上游 index.html)。前缀不匹配返回 false
// (mux 子树注册下不可达,直呼防御)。
func assistServiceStrippedPath(path string) (string, bool) {
	switch {
	case path == assistServicePrefix || path == assistServicePrefix[:len(assistServicePrefix)-1]:
		return "/", true
	case strings.HasPrefix(path, assistServicePrefix):
		rest := strings.TrimPrefix(path, assistServicePrefix)
		if !strings.HasPrefix(rest, "/") {
			rest = "/" + rest
		}
		return rest, true
	default:
		return "", false
	}
}

// assistServiceEntryLoopbackMarker 入腿真源决策(透传腿背书的唯一真值源):
//   - core 入腿:按本跳 RemoteAddr 现算(入口 mark 已剥净伪造头,本跳
//     RemoteAddr 即真源);
//   - runtime 透传腿:按 core 入腿背书来的标记头读——本跳 RemoteAddr 恒是
//     拨号源(127.0.0.1=core),无真源信息,"1" 只能是 core 背书来的。
//
// 返回值恒非空:loopback→"1",cookie→显式哨兵(缺省也归哨兵,与
// webTerminalLoopbackOrigin 的无头=cookie 保守口径一致)。
func assistServiceEntryLoopbackMarker(r *http.Request) string {
	if services.IsUserAgentRuntime() {
		if r.Header.Get(WebTerminalLoopbackHeader) == "1" {
			return "1"
		}
		return webTerminalLoopbackCookieMarker
	}
	if middleware.IsLoopbackRequest(r) {
		return "1"
	}
	return webTerminalLoopbackCookieMarker
}

// assistServiceUpstreamBase 反代上游基地址:dashboard 静态/API 真身恒在
// core 管理口。解析链与 agentruntime.ownerBaseURL 同族(override 端口回退
// 回灌 > ALIANG_MANAGEMENT_ADDR 部署级 env > 默认 --host 管理口,默认值下
// 即 http://127.0.0.1:56431)——handlers 包反向 import agentruntime 会构环,
// 链路在此按同语义镜像,改链路时两处同改。
func assistServiceUpstreamBase() string {
	if override := services.SessionOwnerAddrOverride(); override != "" {
		return override
	}
	if addr := strings.TrimSpace(os.Getenv("ALIANG_MANAGEMENT_ADDR")); addr != "" {
		addr = strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
		return "http://" + addr
	}
	return services.DefaultSessionOwnerAddr()
}

// assistRuntimeSessionActive core 腿会话闸:拨 user agent runtime 的
// /api/agent/assist/session 取会话真值(requested/active 放行)。runtime
// 侧 403(总开关关)与不活跃快照同归不活跃哨兵;拨号/非零业务码/解析失败
// 原样上抛,由调用方落信封 503。控制面 h.client 即可:小 JSON、12s 期限
// 足够;数据面反代走 fsClient(proxyAssistService)。
func (h *AgentHandler) assistRuntimeSessionActive() error {
	req, err := http.NewRequest(http.MethodGet, services.UserAgentBaseURL()+"/api/agent/assist/session", nil)
	if err != nil {
		return err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return errAssistServiceSessionInactive
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			State string `json:"state"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&payload); err != nil {
		return err
	}
	if payload.Code != 0 {
		return fmt.Errorf("assist session probe code=%d", payload.Code)
	}
	if payload.Data.State != "requested" && payload.Data.State != "active" {
		return errAssistServiceSessionInactive
	}
	return nil
}

// proxyAssistService 剥前缀后的反代(白名单方法,body 原样流穿——PROXY-FIRST
// 零解析,同 FS 上传 multipart 流穿惯例)。响应帧头剥除见 stripAssistFrameHeaders;
// 其余响应头透传(照 proxyToUserAgentClient)。
func (h *AgentHandler) proxyAssistService(w http.ResponseWriter, r *http.Request, strippedPath string) error {
	target := assistServiceUpstreamBase() + strippedPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		return err
	}
	if r.ContentLength >= 0 {
		req.ContentLength = r.ContentLength // 精确透传长度,避免写方法退化 chunked
	}
	copyAgentProxyHeaders(req.Header, r.Header)
	// 透传腿真源标记背书(评审 Fix 2):上游(同进程自拨或跨进程拨 core)的
	// 入口 mark 只见本跳 RemoteAddr=拨号源(恒 loopback),无背书时按本跳
	// 重标会把 LAN cookie 真源洗成 loopback——fs_*/lock 的审计 origin 与
	// bad-token 豁免判定失真。按入腿决策显式背书(见 assistServiceEntryLoopbackMarker,
	// 契约恒非空:loopback 入腿→"1",cookie 入腿→cookie 哨兵;上游 mark 见
	// 哨兵采信跳过重标,伪造面分析见 webTerminalLoopbackCookieMarker)。
	req.Header.Set(WebTerminalLoopbackHeader, assistServiceEntryLoopbackMarker(r))
	// 防御注记(P3 评审收口):marker 恒非空(上函数两腿各返回 "1" 或 cookie
	// 哨兵,无第三形态),本 Set 不可能写入空值——上游入口 mark 永远见到
	// 「明确背书」,不存在「空头=按本跳重标」的歧义路径;恒非空契约改链路时
	// 由 assistServiceEntryLoopbackMarker 头注与 handler 测试共同把守。
	// 透传腿 Origin 剥除:入腿同源闸已验过(assistSameOriginGuard),上游
	// r.Host 是拨号目标(127.0.0.1:56431),浏览器 Origin 与之必然失配,
	// 照搬会在上游同源闸(aiStreamHostAllowed 口径家族)误杀——同
	// assistSameOriginGuard 透传腿注记的根因,同源闸只在入腿执行一次。
	req.Header.Del("Origin")
	req.Header.Set("X-Aliang-Agent-Proxy", "assist-service")
	// 凭据透传头剥净:上游是 dashboard 静态/API 面(鉴权走 Cookie,已随
	// copyAgentProxyHeaders 透传),LAN 伪造的 runtime 凭据头不得借道转发
	// (proxyToUserAgentClient 同款 Del 纪律;runtime 凭据链归 core→runtime
	// 透传腿,与本路由无关)。
	req.Header.Del(services.AgentForwardedAuthorizationHeader)
	req.Header.Del(services.AgentForwardedUserKeyHeader)

	resp, err := h.fsClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	copyAgentProxyHeaders(w.Header(), resp.Header)
	stripAssistFrameHeaders(w.Header())
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("copy assist service response: %w", err)
	}
	return nil
}

// stripAssistFrameHeaders 剥 iframe 阻断头(spec §8.3):X-Frame-Options 全剥;
// CSP 只摘 frame-ancestors 指令、其余指令逐字透传(改写整条 CSP 会连脚本
// 白名单/防盗链一起丢)。CSP 可重复头逐条处理;CSP-Report-Only 不拦嵌入,
// 原样透传不碰。摘空整条 CSP 则删头(纯 frame-ancestors 的 CSP 留空值是
// 畸形头)。
func stripAssistFrameHeaders(header http.Header) {
	header.Del("X-Frame-Options")
	values := header.Values("Content-Security-Policy")
	if len(values) == 0 {
		return
	}
	var kept []string
	for _, value := range values {
		if filtered := filterCSPFrameAncestors(value); filtered != "" {
			kept = append(kept, filtered)
		}
	}
	header.Del("Content-Security-Policy")
	for _, value := range kept {
		header.Add("Content-Security-Policy", value)
	}
}

// filterCSPFrameAncestors 从单条 CSP 值里摘 frame-ancestors 指令(指令名
// 大小写不敏感,分号分隔),返回其余指令的分号重组;全摘/原本为空返回空串。
func filterCSPFrameAncestors(value string) string {
	var kept []string
	for _, directive := range strings.Split(value, ";") {
		fields := strings.Fields(directive)
		if len(fields) == 0 {
			continue
		}
		if strings.EqualFold(fields[0], "frame-ancestors") {
			continue
		}
		kept = append(kept, strings.TrimSpace(directive))
	}
	return strings.Join(kept, "; ")
}
