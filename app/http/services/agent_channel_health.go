package services

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

const (
	// agentChannelAlertThreshold 是触发「平台凭据过期」设备级告警的最小连续 401 次数。
	agentChannelAlertThreshold = 3
	// agentChannelAlertWindow 是「持续 401」的告警时间阈值。
	agentChannelAlertWindow = 5 * time.Minute
	// agentChannelAlertInterval 是同类告警的最小间隔（限频）。
	agentChannelAlertInterval = 30 * time.Minute
	// agentChannelBackoffBase / Max 是 endpoint 级 401 退避的指数参数。
	agentChannelBackoffBase = 30 * time.Second
	agentChannelBackoffMax  = 10 * time.Minute
)

// agentChannelRefreshHeader 刷新本地 token 并返回新的 Authorization 头；
// 声明为 var 便于测试替换，生产路径调用的就是这个名字。
var agentChannelRefreshHeader = func() (string, error) {
	return ensureFreshRegisterAuthHeaderForPush()
}

// 边界说明（重要，别把这条链路当成「401 自愈」）：
// ensureFreshRegisterAuthHeaderForPush → auth.EnsureFreshAccessTokenForPush 只在
// token **临近过期** 时才真刷新；token 被服务端提前吊销但仍未过期时，它会把同一个
// Authorization 头原样返回。因此这里的「刷新后重试一次」在这种场景下会再吃一个
// 401，然后由退避 + 告警收敛——**用户可感知的结果是「收到告警」，不是「自动恢复」**。
// 本轮不做强制刷新：processor/auth 没有导出 force-refresh（pushRefreshNow 未导出），
// 强行加会牵扯 owner 判定与 token store 写入，超出止血范围；告警已足以让用户重新登录。

type agentChannelEndpointState struct {
	consecutive int
	nextAt      time.Time
	// authFingerprint 是触发退避时所用 Authorization 头的指纹（不是原文）。
	// 只用于识别「凭据已轮转，旧退避作废」，不足以还原凭据。
	authFingerprint string
}

// agentChannelHealth 汇总平台 REST 通道的健康状态：连续 401 计数、端点退避、
// 告警限频。所有方法都零值安全。
type agentChannelHealth struct {
	mu          sync.Mutex
	consecutive int
	firstAt     time.Time
	lastAlertAt time.Time
	endpoints   map[string]*agentChannelEndpointState
}

var agentChannelHealthInstance = &agentChannelHealth{}

// allow 判断这个 endpoint 当前是否允许发请求（处于退避窗口内则拒绝）。
//
// 若当前凭据与触发退避时的凭据不同，说明用户已重新登录/续期，退避立即
// 作废：否则告警引导用户重新登录后，注册仍会被残余退避挡住最多一个窗口。
func (h *agentChannelHealth) allow(endpoint string, authHeader string) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.endpoints[endpoint]
	if state == nil || state.nextAt.IsZero() {
		return true
	}
	if agentChannelAuthFingerprint(authHeader) != state.authFingerprint {
		delete(h.endpoints, endpoint)
		return true
	}
	return !time.Now().Before(state.nextAt)
}

// agentChannelAuthFingerprint 取 Authorization 头的短指纹（8 字节），只做相等比较。
func agentChannelAuthFingerprint(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(header))
	return hex.EncodeToString(sum[:8])
}

// record401 记一次 401：推进端点指数退避，并在「连续 ≥3 次」或「持续 >5min」
// 时发一条限频告警。
func (h *agentChannelHealth) record401(endpoint string, authHeader string) {
	if h == nil {
		return
	}
	now := time.Now()
	h.mu.Lock()
	if h.firstAt.IsZero() {
		h.firstAt = now
	}
	h.consecutive++
	if h.endpoints == nil {
		h.endpoints = make(map[string]*agentChannelEndpointState)
	}
	state := h.endpoints[endpoint]
	if state == nil {
		state = &agentChannelEndpointState{}
		h.endpoints[endpoint] = state
	}
	state.consecutive++
	state.authFingerprint = agentChannelAuthFingerprint(authHeader)
	backoff := agentChannelBackoffBase << (state.consecutive - 1)
	if backoff > agentChannelBackoffMax || backoff <= 0 {
		backoff = agentChannelBackoffMax
	}
	state.nextAt = now.Add(backoff)

	shouldAlert := (h.consecutive >= agentChannelAlertThreshold || now.Sub(h.firstAt) > agentChannelAlertWindow) &&
		(h.lastAlertAt.IsZero() || now.Sub(h.lastAlertAt) >= agentChannelAlertInterval)
	if shouldAlert {
		h.lastAlertAt = now
	}
	// 所有要在锁外使用的值都必须在锁内拷成局部变量：state 是指向 map 内
	// 结构体的指针，解锁后再读 state.consecutive 会与并发的 record401 构成
	// 数据竞争（-race 下必报）。
	total := h.consecutive
	firstAt := h.firstAt
	stateConsecutive := state.consecutive
	h.mu.Unlock()

	logger.Warn(fmt.Sprintf("[AGENT-CHANNEL] 401 endpoint=%s consecutive=%d total=%d backoff=%s",
		sanitizeAgentEndpoint(endpoint), stateConsecutive, total, backoff))
	if shouldAlert {
		agentChannelAlert(firstAt, total)
	}
}

// recordSuccess 清掉连续计数与端点退避。
func (h *agentChannelHealth) recordSuccess(endpoint string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.consecutive = 0
	h.firstAt = time.Time{}
	if state := h.endpoints[endpoint]; state != nil {
		delete(h.endpoints, endpoint)
	}
	h.mu.Unlock()
}

func (h *agentChannelHealth) reset() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.consecutive = 0
	h.firstAt = time.Time{}
	h.lastAlertAt = time.Time{}
	h.endpoints = map[string]*agentChannelEndpointState{}
	h.mu.Unlock()
}

// agentChannelAlertWriter 是告警的实际发送通道；声明为 var 便于测试替换。
//
// 这里必须用 init() 赋值，不能在声明处写初始化表达式：Go 的包初始化依赖分析
// 会顺着初始化表达式引用的函数体继续追踪，而发送逻辑引用 GetSharedAgentService
// → NewAgentService → … → agentTransferFetchInbox（包级 func var）→
// callAgentServer → 本文件 record401，形成 initialization cycle 编译错误。
// init() 内的赋值不参与依赖分析，这条边因此在编译期被截断。
var agentChannelAlertWriter func(map[string]interface{}) error

func init() {
	agentChannelAlertWriter = func(payload map[string]interface{}) error {
		svc := GetSharedAgentService()
		if svc == nil {
			return errors.New("no shared agent service")
		}
		writer := svc.currentRemoteWriter()
		if writer == nil {
			return errors.New("agent is not connected")
		}
		return writer(payload)
	}
}

// agentChannelAlert 把「设备在线但平台凭据过期」发成设备级告警帧。
//
// 走当前 WS 连接（writeJSON 的同一个 live writer）：没有连接时只记日志，
// 不重试——设备离线时本来也送不到，等它上线自然会重试到新的 401 计数。
func agentChannelAlert(since time.Time, total int) {
	payload := map[string]interface{}{
		"type":            "agent.channel.alert",
		"code":            "platform_credential_expired",
		"message":         "设备在线但平台凭据过期，文件传输等功能暂不可用",
		"consecutive_401": total,
		"since":           since.UTC().Format(time.RFC3339),
	}
	if agentChannelAlertWriter == nil {
		logger.Warn("[AGENT-CHANNEL] alert skipped: writer not installed")
		return
	}
	if err := agentChannelAlertWriter(payload); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-CHANNEL] alert send failed: %v", err))
		return
	}
	logger.Warn(fmt.Sprintf("[AGENT-CHANNEL] alert sent consecutive_401=%d since=%s", total, payload["since"]))
}

// agentChannelBackoffError 表示该 endpoint 正处于 401 退避窗口内。
type agentChannelBackoffError struct {
	endpoint string
}

func (e agentChannelBackoffError) Error() string {
	return fmt.Sprintf("agent server endpoint %s is backing off after repeated 401", e.endpoint)
}
