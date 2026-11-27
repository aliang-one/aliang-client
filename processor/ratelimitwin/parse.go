// Package ratelimitwin 解析上游限流窗口响应头，把「什么时候可以再来」折算成
// unix 秒的冷却截止（rate_limited_until）。
//
// 刻意做成纯 stdlib 叶子包（只依赖 net/http/strconv/strings/time，不 import
// 仓库内任何包）：56432 路由层（inbound/http）与 services 层
// （app/http/services）都要在「拿得到响应头的那条缝」里用它，而两包之间的
// import 方向是单向的（services → inbound/http）。两边都需要的这个能力只能落
// 在谁都不依赖的叶子里——否则要么成环，要么各写一份随时间漂移。
package ratelimitwin

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// minWindow / maxWindow 是冷却窗口相对 now 的钳制区间。
	//
	// 下限 60s：上游给 0/过去时间（或只有秒级精度导致略早）时不要立刻再打，
	// 否则 429 会被打成忙等；上限 6h：防止异常/恶意头把某个 endpoint 钉死一整天。
	minWindow = 60 * time.Second
	maxWindow = 6 * time.Hour
)

// Parse 从响应头解析限流窗口，返回冷却截止的 unix 秒。
//
// 识别两种来源（可同时出现，取**更远**者）：
//   - Retry-After：秒数，或 HTTP-date（RFC1123/RFC850/ANSI C，交给 http.ParseTime）；
//   - anthropic-ratelimit-unified-reset：unix 时间戳秒。
//
// 解析出的绝对时刻相对 now 的时长钳到 [60s, 6h] 后换算回 unix 秒。没有任何可
// 解析头（缺失/空/非法）返回 0，由调用方回落组策略默认冷却。
//
// status 仅用于调用点语义自文档化（典型为 429），解析本身不依赖状态码——
// Retry-After 在 503 上同样合法。无可解析头时 status 不影响返回值。
func Parse(status int, h http.Header) int64 {
	// 保留参数：调用点按状态组织语义（429 才把 until 交给 ReportFailure），
	// 解析保持对状态码中立的纯函数。
	_ = status
	if h == nil {
		return 0
	}

	now := time.Now()
	var reset time.Time

	if raw := strings.TrimSpace(h.Get("Retry-After")); raw != "" {
		if t, ok := parseRetryAfter(raw, now); ok {
			reset = t
		}
	}
	if raw := strings.TrimSpace(h.Get("anthropic-ratelimit-unified-reset")); raw != "" {
		if secs, err := strconv.ParseInt(raw, 10, 64); err == nil && secs > 0 {
			if t := time.Unix(secs, 0); reset.IsZero() || t.After(reset) {
				reset = t
			}
		}
	}
	if reset.IsZero() {
		return 0
	}

	window := reset.Sub(now)
	if window < minWindow {
		window = minWindow
	} else if window > maxWindow {
		window = maxWindow
	}
	return now.Add(window).Unix()
}

// parseRetryAfter 解析单个 Retry-After 值：先试秒数，再试 HTTP-date。
// 负数与无法识别的文本返回 ok=false（交给 unified-reset 或回落组策略）。
func parseRetryAfter(raw string, now time.Time) (time.Time, bool) {
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if secs < 0 {
			return time.Time{}, false
		}
		return now.Add(time.Duration(secs) * time.Second), true
	}
	if t, err := http.ParseTime(raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}
