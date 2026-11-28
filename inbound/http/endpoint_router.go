// endpoint_router.go — 56432 明文 AI 路径的路由层(spec 2026-11-25 §6/§7/§17)。
//
// 职责:明文 /v1/messages、/v1/chat/completions、/v1/models 三个 AI path 被
// 本层「全权处理」——命中即必 return,永不回落透明代理;其余 path 与 CONNECT
// 隧道零变化(不变量 1/2)。
//
// 依赖纪律:app/http/services 已 import 本包(run.go),本包反向 import 它会成
// 环。所以 endpoint registry 的能力通过窄接口 AIEndpointSelector 注入(包级
// var + SetAIEndpointSelector);adapter 在 services 侧实现。aiSelector 为 nil
// (未接线/registry 构造失败降级)时 AI path 走原透明代理行为——降级安全。
//
// 不变量(spec §17):
//   - 内置 endpoint 转发不注入 Authorization-Inner(watcher relay 无条件注入,
//     不变量 3);路由层对 BYO 上游注入 Bearer <api_key>,对内置链原样透传。
//   - 流式响应一旦开始写出,绝不跨 endpoint 切换(不变量 4):BYO 侧以
//     「响应头是否已写客户端」为闸;内置侧委托 tcp handler 后不可探知,一律
//     不重试。
package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/ratelimitwin"
	"aliang.one/nursorgate/processor/tcp"
)

// AIEndpointSelector 是路由层对 endpoint registry 的窄依赖面。nil = 路由层不
// 激活,AI path 走原透明代理(测试/降级安全)。
type AIEndpointSelector interface {
	// Select 选一个 endpoint:explicitID 非空为显式点名(管理面/内核指定);
	// protocol 为 "anthropic"/"openai";model 为请求体里的 model 字段。
	// excludeID 是重试 attempt 传来的上一失败 endpoint id:自动选择路径排除
	// 它,保证「失败切下一 endpoint 重试一次」真的切换(final-review 必改 2);
	// 显式点名路径不受 exclude 影响。首次 attempt 传空串。
	// 错误契约:errors.Is 可判 ErrAINoEndpoint(→503)、
	// ErrAIEndpointNotAvailable(→404)、ErrAIQueueTimeout(→503);
	// 其它 wrapped raw error 由路由层映射 500(registry 读挂是服务端故障)。
	Select(protocol, model, explicitID, excludeID string) (id string, baseURL string, apiKey string, builtin bool, err error)
	// Acquire 拿并发令牌;maxConcurrency 传 0 = 以 endpoint 行内配置为准。
	// 返回的 release 必须恰好调用一次;返回 err(队满/排队超时)时无槽位,
	// 无 release 可调。ErrAIQueueTimeout 的路由语义(二期 spec §6):自动路径
	// =排除忙行切下一候选(不计失败不动健康列),显式点名路径=立即 503。
	Acquire(id string, maxConcurrency int) (release func(), err error)
	ReportSuccess(id string)
	// ReportFailure 上报一次失败。until 是调用方在能拿到响应头处（BYO 完整响应
	// 头/内核 OnRequestError 缝）用 ratelimitwin.Parse 预解析的 429 冷却截止
	// unix 秒；0/负 = 无预解析，由 service 按组策略推导（只有 429 消费它）。
	ReportFailure(id string, status int, errMsg string, until int64)
	// ModelsUnion 返回 /v1/models 聚合数据:{id, owner} 列表(已去重)。
	ModelsUnion() []map[string]any
}

// aiSelector 是包级选择器槽。SetAIEndpointSelector(nil) 即降级。
var aiSelector AIEndpointSelector

// SetAIEndpointSelector 注入 endpoint 选择器;传 nil 显式降级(AI path 回落
// 原透明代理)。
func SetAIEndpointSelector(s AIEndpointSelector) { aiSelector = s }

// 路由层自己的哨兵:registry 在 services 侧,哨兵经 adapter 翻译成这三个
// (本包不 import services,防环)。
var (
	ErrAINoEndpoint           = errors.New("ai route: no endpoint available")
	ErrAIEndpointNotAvailable = errors.New("ai route: endpoint not available")
	ErrAIQueueTimeout         = errors.New("ai route: endpoint queue timeout")
)

const (
	// maxAIRequestBodyBytes 是 AI path 请求体的内存上限:重试需按 attempt 重建
	// 请求体,必须整读进内存;超限 413(SSE 大上下文是现实负载,8MB 足够宽)。
	maxAIRequestBodyBytes = 8 << 20
	// aiMaxAttempts 是总尝试次数:首次 + 失败重试一次(spec §6.2)。
	aiMaxAttempts = 2
	// aiProtocolAnthropic / aiProtocolOpenAI 与 registry 的协议 toggle 对齐。
	aiProtocolAnthropic = "anthropic"
	aiProtocolOpenAI    = "openai"
)

// aiForwardClient 是 BYO 上游的直连客户端:Timeout 0 = 不设总期限(SSE 流式
// 响应可持续远超任何固定期限);连接级超时由 DefaultTransport 承担。
var aiForwardClient = &http.Client{Timeout: 0}

// aiRouteFor 判定一条明文请求是否 AI path(spec §7.1 三条目)。返回 protocol
// ("anthropic"/"openai")或 aggregate(/v1/models 聚合);ok=false 走原透明代理。
func aiRouteFor(method, path string) (protocol string, aggregate bool, ok bool) {
	switch path {
	case "/v1/models":
		if method == http.MethodGet {
			return "", true, true
		}
	case "/v1/messages":
		if method == http.MethodPost {
			return aiProtocolAnthropic, false, true
		}
	case "/v1/chat/completions":
		if method == http.MethodPost {
			return aiProtocolOpenAI, false, true
		}
	}
	return "", false, false
}

// aiForwardResult 是一次转发尝试的结局。
type aiForwardResult struct {
	// started:已向客户端写出响应(状态行/头)——BYO 侧由本层写回时置位;
	// 或 delegated-to-unobservable(builtin 链):tcp handler 全权接管连接,
	// 本层无法探知响应头是否已写出,一律按「已开始」从严处理(绝不重试,
	// 不变量 4)。未来维护者切勿因「started 恒 true」而给 builtin 开重试。
	started   bool
	completed bool           // 响应完整写完(started 且无中断)
	status    int            // 上游 HTTP 状态;0 = 未收到响应(传输失败)
	held      *http.Response // 收到但未透传的响应(429/5xx,留给重试耗尽时透传)
	err       error          // 传输/流中断原因
	configErr bool           // base_url 非法等 registry 数据问题(不计数不重试)
}

// errMsg 是 err 的安全字符串化:nil 时返回空串(防维护中的 nil-deref)。
func (r aiForwardResult) errMsg() string {
	if r.err == nil {
		return ""
	}
	return r.err.Error()
}

// handleAIRequest 全权处理一条 AI path 请求:命中本函数即不回落透明代理。
// metadata 是 extractMetadataFromHTTP 的产物(内置转发原样交给 tcp handler,
// 让 shouldForceAliangRoute 的强制路由生效)。
func handleAIRequest(conn net.Conn, reader *bufio.Reader, req *http.Request, metadata *M.Metadata, protocol string, aggregate bool) {
	if aggregate {
		handleAIModelsRequest(conn)
		return
	}

	body, tooLarge, err := readAIRequestBody(req)
	if err != nil {
		logger.Info(fmt.Sprintf("ai route: 读请求体失败 %s: %v", req.URL.Path, err))
		writeAIErrorBody(conn, http.StatusBadRequest, "invalid_request_body", "failed to read request body")
		return
	}
	if tooLarge {
		logger.Info(fmt.Sprintf("ai route: 请求体超过 %d 字节上限 %s", maxAIRequestBodyBytes, req.URL.Path))
		writeAIErrorBody(conn, http.StatusRequestEntityTooLarge, "request_body_too_large",
			fmt.Sprintf("request body exceeds %d bytes", maxAIRequestBodyBytes))
		return
	}

	model := extractAIModel(body)
	explicitID := req.Header.Get("X-Endpoint-Id")
	// excludeID 是重试真切换的缝(final-review 必改 2):上一失败 attempt 的
	// endpoint id 传给 Select,自动路径排除后天然命中下一候选——不传则单次
	// 失败不达摘除阈值时 Select 仍返回同一 endpoint,重试=同 endpoint 两枪。
	excludeID := ""

	for attempt := 1; attempt <= aiMaxAttempts; attempt++ {
		id, baseURL, apiKey, builtin, err := aiSelector.Select(protocol, model, explicitID, excludeID)
		if err != nil {
			status, code := mapAISelectorError(err)
			logger.Warn(fmt.Sprintf("ai route: select 失败 protocol=%s model=%q explicit=%q exclude=%q attempt=%d status=%d code=%s err=%v",
				protocol, model, explicitID, excludeID, attempt, status, code, err))
			writeAIErrorBody(conn, status, code, "endpoint selection failed")
			return
		}

		release, err := aiSelector.Acquire(id, 0)
		if err != nil {
			if errors.Is(err, ErrAIQueueTimeout) {
				// 队满≠病,是忙(二期 spec §6):不计失败、不动健康列。service 的
				// Acquire 队满/超时返回 (nil, ErrQueueTimeout) 无槽位,release
				// 语义天然满足——没有任何可释放的东西,绝不能在此调 release。
				if explicitID != "" {
					// 显式点名 + 该行忙 → 立即 503:点名不重选(重选违背点名
					// 语义,且会再烧一个 30s 排队等待)。
					logger.Warn(fmt.Sprintf("ai route: 显式点名 endpoint 忙,立即 503 endpoint=%s explicit=%s attempt=%d", id, explicitID, attempt))
					writeAIErrorBody(conn, http.StatusServiceUnavailable, "endpoint_queue_timeout",
						fmt.Sprintf("explicit endpoint %s is busy", id))
					return
				}
				if attempt < aiMaxAttempts {
					// 自动路径:排除忙行切下一候选重试(attempt 上限仍 2,不为
					// 队满扩——≥3 行且前两行全忙时第三行本轮不试,下一请求
					// 自然轮到)。
					logger.Info(fmt.Sprintf("ai route: endpoint 队满,排除后切下一候选 endpoint=%s attempt=%d", id, attempt))
					excludeID = id
					continue
				}
				logger.Warn(fmt.Sprintf("ai route: 全部候选队满,503 endpoint=%s attempt=%d", id, attempt))
				writeAIErrorBody(conn, http.StatusServiceUnavailable, "endpoint_queue_timeout",
					"all endpoint candidates are busy")
				return
			}
			status, code := mapAISelectorError(err)
			logger.Warn(fmt.Sprintf("ai route: acquire 失败 endpoint=%s attempt=%d status=%d code=%s err=%v", id, attempt, status, code, err))
			writeAIErrorBody(conn, status, code, "endpoint acquire failed")
			return
		}

		res := forwardAIAttempt(conn, reader, req, metadata, body, id, baseURL, apiKey, builtin)
		release()

		switch {
		case res.configErr:
			// registry 数据问题(base_url 非法):确定性失败,不计数不重试。
			logger.Error(fmt.Sprintf("ai route: endpoint=%s base_url 非法 status=%d err=%v", id, res.status, res.err))
			writeAIErrorBody(conn, http.StatusInternalServerError, "endpoint_config_invalid", "endpoint base_url is invalid")
			return
		case res.completed && res.status >= 400 && res.status != http.StatusTooManyRequests:
			// 其它 4xx(400/404/413/422 等,不含已走 held 通道的 401/402/429):
			// 已透传,不重试(spec §6.2 不变量 5)。403 例外(二期 §9.1 不变量 5
			// 修订):归计数路径——语义歧义(可能是凭据/权限问题)按普通失败
			// 记 total/failed 与 last_error,与内核链对齐;但不切候选(已透传的
			// 确定性客户端错误,切换=重打同族两枪)、不消费冷却(until=0)。
			if res.status == http.StatusForbidden {
				aiSelector.ReportFailure(id, res.status,
					fmt.Sprintf("%d %s", res.status, http.StatusText(res.status)), 0)
			}
			logger.Info(fmt.Sprintf("ai route: 客户端错误透传 endpoint=%s status=%d protocol=%s", id, res.status, protocol))
			return
		case res.completed:
			aiSelector.ReportSuccess(id)
			logger.Info(fmt.Sprintf("ai route: 转发完成 endpoint=%s protocol=%s status=%d attempt=%d", id, protocol, res.status, attempt))
			return
		case res.started:
			// 不变量 4:流已开始但中断——绝不切换 endpoint,断开收场。
			aiSelector.ReportFailure(id, 0, res.errMsg(), 0)
			logger.Warn(fmt.Sprintf("ai route: 流式响应中断,不重试 endpoint=%s status=%d err=%v", id, res.status, res.err))
			return
		}

		if res.held != nil {
			// held 通道(429/5xx/401/402):429 的冷却截止在能拿到响应头处预解析
			// （spec §5.5）：Retry-After（秒/HTTP-date）与
			// anthropic-ratelimit-unified-reset 取更远者，clamp 60..21600。
			// 401/402 走同一条 ReportFailure 缝,until 传 0——service 按 status
			// 进锁存分支(spec §5.3),不消费冷却。
			var until int64
			if res.status == http.StatusTooManyRequests {
				until = ratelimitwin.Parse(res.status, res.held.Header)
			}
			aiSelector.ReportFailure(id, res.status, res.held.Status, until)
			if attempt < aiMaxAttempts {
				drainAndCloseAIResponse(res.held)
				// 重试切下一候选:把本失败 endpoint 记为 exclude,下次 Select
				// 不再选它(429 与 5xx 同口径)。
				excludeID = id
				logger.Warn(fmt.Sprintf("ai route: 上游失败,重试并排除 endpoint=%s status=%d attempt=%d", id, res.status, attempt))
				continue
			}
			// 重试耗尽:透传最后一次上游错误响应(比合成 502 信息量大)。
			logger.Warn(fmt.Sprintf("ai route: 重试耗尽,透传上游错误 endpoint=%s status=%d", id, res.status))
			_, _ = writeAIUpstreamResponse(conn, res.held)
			_ = res.held.Body.Close()
			return
		}

		// 传输失败(无 HTTP 响应)。
		aiSelector.ReportFailure(id, 0, res.errMsg(), 0)
		if attempt < aiMaxAttempts {
			// 传输失败同样切下一候选(重试不是同 endpoint 重打)。
			excludeID = id
			logger.Warn(fmt.Sprintf("ai route: 上游不可达,重试并排除 endpoint=%s err=%v attempt=%d", id, res.err, attempt))
			continue
		}
		logger.Warn(fmt.Sprintf("ai route: 重试耗尽,上游不可达 endpoint=%s err=%v", id, res.err))
		writeAIErrorBody(conn, http.StatusBadGateway, "upstream_unreachable",
			fmt.Sprintf("upstream endpoint unreachable: %v", res.err))
		return
	}
}

// forwardAIAttempt 做一次转发:builtin 走 tcp handler 强制路由链;BYO 用
// http.Client 直连 base_url(不经 tcp handler——会被再次强制路由回平台)。
func forwardAIAttempt(conn net.Conn, reader *bufio.Reader, req *http.Request, metadata *M.Metadata, body []byte, id, baseURL, apiKey string, builtin bool) aiForwardResult {
	if builtin {
		return forwardAIBuiltinAttempt(conn, reader, req, metadata, body)
	}
	return forwardAIByoAttempt(conn, req, body, baseURL, apiKey)
}

// forwardAIBuiltinAttempt 内置 endpoint 转发:重放 conn = 请求头(以内存中的
// body 重建 Content-Length;Authorization-Inner 由 aiSkipRequestHeaders 统一
// 剥——客户端自带的同名头是伪造,watcher relay 会无条件覆写,路由层作为最后
// 一跳必须先剥)+ body 字节 + reader 残余。原 metadata 原样交给 tcp handler:
// 目标 127.0.0.1:56432 命中 shouldForceAliangRoute → RouteToALiang,
// watcher relay 注入 Authorization-Inner(spec §7.2,不变量 3)。
//
// tcp handler 全程自管流式中继,路由层无法探知响应头是否已写出——因此内置
// 链一律单次尝试,失败只 ReportFailure + 断开(不变量 4 的保守实现)。
//
// 状态可见性(spec 二期 §5.3a):builtin 的上游 HTTP 状态被 tcp handler 吞掉,
// 本层**尽力而为**——成功返回 completed=true/status=0;handleErr 返回
// status=0 的失败(由 handleAIRequest 的 res.started 分支 ReportFailure 0)。
// 上游 4xx/5xx(尤其 429 窗口)在 builtin 路径不可观测,不承诺精确锁存/限流,
// 这是 spec 明示的尽力语义;需要精确窗口时只能走 BYO(完整响应头)。
//
// 已知风险(quality-fix 评审 Important 2,文档化不改行为):本委托用
// context.Background() 无 deadline——accept 循环的读期限已在 handleRawConnection
// 清除,builtin 链若卡死会持信号量永久阻塞、饿死整个 builtin endpoint。超时
// 语义由 tcp handler/relay 层治理,本层不设 deadline;Task 5 内核接入时复核
// (届时 registry 健康态会把卡死链计失败摘除,但摘除前已饿死窗口仍在)。
func forwardAIBuiltinAttempt(conn net.Conn, reader *bufio.Reader, req *http.Request, metadata *M.Metadata, body []byte) aiForwardResult {
	head, err := serializeAIReplayHead(req, body)
	if err != nil {
		return aiForwardResult{err: fmt.Errorf("重建请求头失败: %w", err)}
	}
	replay := &replayConn{
		Conn:   conn,
		reader: io.MultiReader(bytes.NewReader(head), bytes.NewReader(body), reader),
	}
	logger.Info(fmt.Sprintf("ai route: 内置链转发 %s %s host=%s:%d", req.Method, req.URL.Path, metadata.HostName, metadata.DstPort))
	handleErr := tcp.GetHandler().Handle(context.Background(), replay, metadata)
	if handleErr != nil {
		return aiForwardResult{started: true, status: 0, err: handleErr}
	}
	return aiForwardResult{started: true, completed: true}
}

// forwardAIByoAttempt BYO endpoint 转发:直连 base_url,Authorization 换成
// Bearer <api_key>,Host 覆盖为上游 host,其余头与 body 透传。
func forwardAIByoAttempt(conn net.Conn, req *http.Request, body []byte, baseURL, apiKey string) aiForwardResult {
	target, err := buildAIUpstreamURL(baseURL, req)
	if err != nil {
		return aiForwardResult{err: err, configErr: true}
	}
	attemptReq, err := http.NewRequestWithContext(context.Background(), req.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return aiForwardResult{err: fmt.Errorf("构造上游请求失败: %w", err), configErr: true}
	}
	copyAIUpstreamHeaders(attemptReq.Header, req.Header)
	if apiKey != "" {
		attemptReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	// Authorization-Inner 已由 copyAIUpstreamHeaders 统一剥(aiSkipRequestHeaders),
	// 此处不再重复 Del。
	attemptReq.Host = target.Host

	logger.Info(fmt.Sprintf("ai route: BYO 转发 %s %s -> %s", req.Method, req.URL.Path, target.Host))

	resp, err := aiForwardClient.Do(attemptReq)
	if err != nil {
		return aiForwardResult{status: 0, err: err}
	}

	status := resp.StatusCode
	// 401/402 与 429/5xx 同走 held 通道(二期 spec §5.3+§6):确定性凭据错误
	// 既要经 ReportFailure(status 原样透传)进 service 锁存,又值得切下一候选
	// 重试——held 意味着响应头未写客户端,切换不违反不变量 4;重试耗尽时
	// 透传最后一次上游错误响应。其余 4xx 仍照旧直通(不重试不计数)。
	if status == http.StatusTooManyRequests || status >= 500 ||
		status == http.StatusUnauthorized || status == http.StatusPaymentRequired {
		// 不透传(留着重试/收尾),body 保持打开交还调用方处置。
		return aiForwardResult{status: status, held: resp}
	}

	// 2xx/3xx/4xx(非 429):透传。
	completed, werr := writeAIUpstreamResponse(conn, resp)
	_ = resp.Body.Close()
	if werr != nil {
		return aiForwardResult{status: status, started: true, err: werr}
	}
	return aiForwardResult{status: status, started: true, completed: completed}
}

// handleAIModelsRequest GET /v1/models 聚合:直接序列化 ModelsUnion,不转发。
func handleAIModelsRequest(conn net.Conn) {
	items := aiSelector.ModelsUnion()
	if items == nil {
		items = []map[string]any{}
	}
	data := make([]map[string]any, 0, len(items))
	for _, item := range items {
		id, _ := item["id"].(string)
		owner, _ := item["owner"].(string)
		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"owned_by": owner,
		})
	}
	payload, err := json.Marshal(map[string]any{"object": "list", "data": data})
	if err != nil {
		writeAIErrorBody(conn, http.StatusInternalServerError, "internal_error", "failed to encode models list")
		return
	}
	logger.Info(fmt.Sprintf("ai route: /v1/models 聚合 %d 条", len(data)))
	writeAIJSONResponse(conn, http.StatusOK, payload)
}

// ---- 请求体/头处理 ------------------------------------------------------------

// readAIRequestBody 整读请求体,超 8MB 返回 tooLarge。
func readAIRequestBody(req *http.Request) (body []byte, tooLarge bool, err error) {
	if req.Body == nil {
		return nil, false, nil
	}
	defer func() { _ = req.Body.Close() }()
	body, err = io.ReadAll(io.LimitReader(req.Body, maxAIRequestBodyBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > maxAIRequestBodyBytes {
		return nil, true, nil
	}
	return body, false, nil
}

// extractAIModel 从请求体提取 model 字段;解析失败按空 model 放行
// (registry 的模型过滤把空请求视为不限,不因脏 body 503)。
func extractAIModel(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Model)
}

// aiHopByHopHeaders 逐跳头:代理不透传(RFC 7230 §6.1)。
var aiHopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "TE", "Trailer",
	"Transfer-Encoding", "Upgrade", "Proxy-Authenticate", "Proxy-Authorization",
}

// aiSkipRequestHeaders 是转发/重放请求头时的统一跳过集(两处调用点共享:
// copyAIUpstreamHeaders 的 BYO 直连、serializeAIReplayHead 的 builtin 重放)。
// 逐跳头 + 定长框架头(由 body bytes/transport 重建)+ 路由指令头
// (X-Endpoint-Id 是 56432 私有指令,绝不外泄)+ Authorization-Inner
// (watcher relay 的注入凭据,客户端自带的同名头是伪造,路由层是最后一跳,
// 必须剥——BYO/builtin 两侧口径一致)。
var aiSkipRequestHeaders = func() map[string]bool {
	m := map[string]bool{"Host": true, "Content-Length": true, "Authorization-Inner": true, "X-Endpoint-Id": true}
	for _, h := range aiHopByHopHeaders {
		m[h] = true
	}
	return m
}()

// copyAIUpstreamHeaders 把客户端请求头透传到上游,剥掉 aiSkipRequestHeaders。
func copyAIUpstreamHeaders(dst, src http.Header) {
	for key, values := range src {
		if aiSkipRequestHeaders[key] {
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}

// buildAIUpstreamURL 把 base_url 与请求 path/query 拼成上游目标:
// base_url 可带路径前缀(如 https://gw.example.com/anthropic)。
func buildAIUpstreamURL(baseURL string, req *http.Request) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("base_url 非法: %q", baseURL)
	}
	target := *base
	target.Path = strings.TrimRight(base.Path, "/") + req.URL.Path
	target.RawPath = ""
	target.RawQuery = req.URL.RawQuery
	target.Fragment = ""
	return &target, nil
}

// serializeAIReplayHead 为内置链重放构造请求头:与 serializeHTTPRequestHead
// 的差异是定长框架以内存 body 为准(客户端 chunked 时 req.Body 已被解码,
// 原 Transfer-Encoding 框架不可重放,一律改写显式 Content-Length)。
func serializeAIReplayHead(req *http.Request, body []byte) ([]byte, error) {
	if req == nil {
		return nil, errors.New("request is nil")
	}
	target := strings.TrimSpace(req.RequestURI)
	if target == "" && req.URL != nil {
		target = req.URL.String()
	}
	if target == "" {
		return nil, errors.New("request target is empty")
	}
	proto := strings.TrimSpace(req.Proto)
	if proto == "" {
		proto = "HTTP/1.1"
	}

	var buf bytes.Buffer
	if _, err := fmt.Fprintf(&buf, "%s %s %s\r\n", req.Method, target, proto); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(&buf, "Host: %s\r\n", req.Host); err != nil {
		return nil, err
	}
	for key, values := range req.Header {
		if aiSkipRequestHeaders[key] {
			continue
		}
		for _, value := range values {
			if _, err := fmt.Fprintf(&buf, "%s: %s\r\n", key, value); err != nil {
				return nil, err
			}
		}
	}
	if _, err := fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- 响应写回 ------------------------------------------------------------------

// writeAIUpstreamResponse 把上游响应写回客户端:
//   - Content-Length >= 0:原样转发定长框架,io.Copy 精确搬运;
//   - Content-Length < 0(上游 chunked/未知):不写框架头,靠 Connection: close
//     定界(调用方收尾即断开)。手动 chunk 再编码不如让 EOF 定界,且 io.Copy
//     直写 socket(无缓冲层),SSE 流式写回天然成立。
//
// 返回 completed=false 表示写回中途失败(流中断,不变量 4 的触发点)。
func writeAIUpstreamResponse(conn net.Conn, resp *http.Response) (completed bool, err error) {
	var head bytes.Buffer
	statusLine := resp.Status
	if statusLine == "" {
		statusLine = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if _, err := fmt.Fprintf(&head, "HTTP/1.1 %s\r\n", statusLine); err != nil {
		return false, err
	}
	writeAIResponseHeaders(&head, resp.Header)
	if resp.ContentLength >= 0 {
		fmt.Fprintf(&head, "Content-Length: %d\r\n", resp.ContentLength)
	} else {
		head.WriteString("Connection: close\r\n")
	}
	head.WriteString("\r\n")
	if _, err := conn.Write(head.Bytes()); err != nil {
		return false, err
	}
	if _, err := io.Copy(conn, resp.Body); err != nil {
		return false, err
	}
	return true, nil
}

// writeAIResponseHeaders 写回上游响应头,剥逐跳头与框架头(重新定框)。
func writeAIResponseHeaders(buf *bytes.Buffer, header http.Header) {
	skip := map[string]bool{}
	for _, h := range aiHopByHopHeaders {
		skip[h] = true
	}
	skip["Content-Length"] = true
	for key, values := range header {
		if skip[key] {
			continue
		}
		for _, v := range values {
			fmt.Fprintf(buf, "%s: %s\r\n", key, v)
		}
	}
}

// drainAndCloseAIResponse 重试前丢弃 held 响应体(复用连接池语义)。
func drainAndCloseAIResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// ---- 错误应答 ------------------------------------------------------------------

// mapAISelectorError 把 selector 错误映射到 (HTTP 状态, 错误码):
// 哨兵判定用 errors.Is(adapter 负责把 services 侧哨兵翻译成本包哨兵);
// 其它 wrapped raw error → 500(registry 读挂是服务端故障,不是「无可用」)。
func mapAISelectorError(err error) (status int, code string) {
	switch {
	case errors.Is(err, ErrAIEndpointNotAvailable):
		return http.StatusNotFound, "endpoint_not_available"
	case errors.Is(err, ErrAIQueueTimeout):
		return http.StatusServiceUnavailable, "endpoint_queue_timeout"
	case errors.Is(err, ErrAINoEndpoint):
		return http.StatusServiceUnavailable, "no_endpoint_available"
	default:
		return http.StatusInternalServerError, "endpoint_select_failed"
	}
}

// writeAIJSONResponse 定长 JSON 应答(状态行+头+体一次写成,Content-Length
// 与实际体一致;Connection: close——本连接为单请求语义)。
func writeAIJSONResponse(conn net.Conn, status int, body []byte) {
	head := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		status, http.StatusText(status), len(body))
	if _, err := conn.Write([]byte(head)); err != nil {
		return
	}
	_, _ = conn.Write(body)
}

// writeAIErrorBody 错误 JSON 体,形状对齐 OpenAI 风格:
// {"error":{"type":"<code>","message":"..."}}。
func writeAIErrorBody(conn net.Conn, status int, code, message string) {
	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    code,
			"message": message,
		},
	})
	if err != nil {
		payload = []byte(fmt.Sprintf(`{"error":{"type":%q,"message":%q}}`, code, message))
	}
	writeAIJSONResponse(conn, status, payload)
}
