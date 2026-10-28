package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 这一组锁死压缩链路的失败侧契约：
//  1. 摘要请求中断流后最多安全重试一次（Stream 的 delivered 守卫不重试，
//     这层重试在 compactSummarize，状态可弃 + 零宿主帧所以安全）；
//  2. ctx 取消/超时不重试；
//  3. 自动压缩失败后有回合级冷却，连续失败达上限则本 run 禁用——不许出现
//     「每回合重烧一次全窗口摘要请求」的压缩失败熔炉。
//
// 变异守卫：摘掉冷却逻辑，TestMaybeAutoCompactCooldownSkipsAndDisables 即红
// （12 个回合会烧出 24 次摘要请求而非 6 次）。

// compactionBigHistory 造一段远超小窗口阈值的历史（每条约 210 tokens）。
func compactionBigHistory() []Message {
	var history []Message
	for i := 0; i < 3; i++ {
		history = append(history,
			TextMessage("user", strings.Repeat("甲", 120)),
			TextMessage("assistant", strings.Repeat("乙", 120)))
	}
	return history
}

// newCompactIntegrityRunner 构造一个可直接驱动 maybeAutoCompact 的 runner
// （每调用一次 = 一个回合的压缩判定）。writer 必须给非 nil：失败日志取
// r.writer.sessionID，nil 会在参数求值时炸。
func newCompactIntegrityRunner(t *testing.T, srvURL string, logs *[]string) *runner {
	t.Helper()
	cfg := &Config{
		Client:        NewClient(srvURL),
		Model:         "claude-x",
		ContextWindow: 800, // 阈值 600，compactionBigHistory 约 750 必触发
		Logf: func(format string, args ...interface{}) {
			*logs = append(*logs, fmt.Sprintf(format, args...))
		},
	}
	return &runner{
		cfg:       cfg,
		history:   compactionBigHistory(),
		writer:    newStreamWriter(nil, "sess-cd"),
		startedAt: time.Now(),
	}
}

// isCompactSummaryRequest 判定请求体是不是压缩摘要调用（system 是压缩器提示）。
func isCompactSummaryRequest(body map[string]interface{}) bool {
	sys, _ := body["system"].(string)
	return strings.Contains(sys, "上下文压缩器")
}

// TestCompactSummarizeRetriesInterruptedStreamOnce 锁安全重试：第一次摘要请求
// 在事件中途被上游掐断（Stream 的 delivered 守卫不会重试），第二次成功 →
// Compact 整体成功、上游命中 2。
func TestCompactSummarizeRetriesInterruptedStreamOnce(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		if !isCompactSummaryRequest(body) {
			writeSimpleText(w, "ok")
			return
		}
		if n == 1 {
			// 先吐 message_start + 一段文本，再把连接掐断：客户端侧是
			// 「已下发事件后的 EOF」，Stream 层绝不重试，只能靠本层兜。
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude-x\"}}\n\n" +
					"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
					"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"半截\"}}\n\n"))
			// Hijack 后必须显式 Flush + Close：不 Flush 半截帧还挂在服务端
			// 缓冲里（客户端见不到任何字节，只会干等块间空闲闸），不 Close
			// 连接半开着（同样等 idle 超时）。要的是「发完半截立刻断」。
			if conn, buf, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = buf.Flush()
				_ = conn.Close()
			}
			return
		}
		writeSimpleText(w, "摘要：第二次尝试成功")
	}))
	defer srv.Close()

	messages := compactionFixture()
	tailBudget := estimateMessageTokens(messages[6]) + estimateMessageTokens(messages[5]) + estimateMessageTokens(messages[4])
	cfg := &Config{Client: NewClient(srv.URL), Model: "claude-x", ContextWindow: tailBudget * compactTailRatioDen}
	compacted, info, err := Compact(t.Context(), cfg, messages, "auto")
	if err != nil {
		t.Fatalf("中断流后安全重试应成功: %v", err)
	}
	if !strings.Contains(headMessageText(t, compacted), "第二次尝试成功") {
		t.Fatalf("摘要应来自第二次尝试, got %q", headMessageText(t, compacted))
	}
	if info.Removed <= 0 {
		t.Fatalf("压缩应实际发生, info=%+v", info)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 2 {
		t.Fatalf("上游命中 %d 次, want 2（首次断流 + 安全重试）", hits)
	}
}

// TestCompactSummarizeNoRetryOnCtxDeadline 锁取消/超时不重试：摘要请求超时
// 后不得再烧第二次。
func TestCompactSummarizeNoRetryOnCtxDeadline(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(300 * time.Millisecond) // 远大于客户端超时
		writeSimpleText(w, "迟到")
	}))
	defer srv.Close()

	messages := compactionFixture()
	tailBudget := estimateMessageTokens(messages[6]) + estimateMessageTokens(messages[5]) + estimateMessageTokens(messages[4])
	cfg := &Config{Client: NewClient(srv.URL), Model: "claude-x", ContextWindow: tailBudget * compactTailRatioDen}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := Compact(ctx, cfg, messages, "auto")
	if err == nil {
		t.Fatal("超时的压缩应报错")
	}
	if !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		t.Fatalf("错误应是超时类: %v", err)
	}
	time.Sleep(400 * time.Millisecond) // 确认没有后台重试
	if got := atomic.LoadInt32(&hits); got > 2 {
		t.Fatalf("上游命中 %d 次, 超时不得重试出第 3 次请求", got)
	}
}

// TestMaybeAutoCompactCooldownSkipsAndDisables 锁熔炉修复的时间线：持续失败
// 时尝试只发生在 T1 / T5 / T9（每次失败后冷却 3 个回合），T9 连续第 3 次
// 失败后本 run 彻底禁用，T10 起一个摘要请求都不发。
//
// 逐回合记时间线是变异守卫的关键：只去掉冷却 → 变成 T1/T2/T3（红）；
// 只去掉禁用 → 第 13 回合还会再试（红）。总请求数单独钉不住这两条。
func TestMaybeAutoCompactCooldownSkipsAndDisables(t *testing.T) {
	var summaryHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if isCompactSummaryRequest(body) {
			// 400 非 max_tokens 措辞：Stream 层不重试不降档，立刻失败——
			// 测试里每次尝试 = compactSummarize 一次 + 其安全重试一次 = 2 请求。
			atomic.AddInt32(&summaryHits, 1)
			http.Error(w, `{"error":{"message":"boom"}}`, http.StatusBadRequest)
			return
		}
		writeSimpleText(w, "ok")
	}))
	defer srv.Close()

	logs := []string{}
	r := newCompactIntegrityRunner(t, srv.URL, &logs)
	var hitTurns []int
	for turn := 1; turn <= 14; turn++ {
		before := atomic.LoadInt32(&summaryHits)
		r.maybeAutoCompact(t.Context())
		if after := atomic.LoadInt32(&summaryHits); after > before {
			hitTurns = append(hitTurns, turn)
		}
	}

	// 时间线：T1 失败 → T2-T4 冷却 → T5 失败 → T6-T8 冷却 → T9 失败（达
	// 上限禁用）→ T10-T14 零请求。3 次尝试 × 每次安全重试 2 请求 = 6。
	if want := []int{1, 5, 9}; !reflect.DeepEqual(hitTurns, want) {
		t.Fatalf("压缩尝试发生的回合 = %v, want %v（失败后冷却 3 回合、第 3 次失败后禁用）", hitTurns, want)
	}
	if got := atomic.LoadInt32(&summaryHits); got != 6 {
		t.Fatalf("摘要上游命中 %d 次, want 6（三次尝试，每次含一次安全重试）", got)
	}
	if r.compactFailures != compactFailureLimit {
		t.Fatalf("compactFailures = %d, want %d", r.compactFailures, compactFailureLimit)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "不再自动压缩") {
		t.Fatalf("达上限时应打一条禁用日志, logs=%s", joined)
	}
	// 前两次失败是「跳过冷却」日志；第 3 次是「不再自动压缩」——失败日志
	// 按各自措辞各数各的（禁用日志的措辞是「连续失败」，不含前者子串）。
	if n := strings.Count(joined, "跳过接下来"); n != compactFailureLimit-1 {
		t.Fatalf("冷却日志应恰好 %d 条, got %d, logs=%s", compactFailureLimit-1, n, joined)
	}
	if n := strings.Count(joined, "不再自动压缩"); n != 1 {
		t.Fatalf("禁用日志应恰好 1 条, got %d", n)
	}
}

// TestMaybeAutoCompactCooldownClearsOnSuccess 锁成功清零：失败一次、冷却耗尽
// 后的下次尝试成功 → 失败计数与冷却清零、历史被替换。
func TestMaybeAutoCompactCooldownClearsOnSuccess(t *testing.T) {
	var summaryHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if isCompactSummaryRequest(body) {
			if atomic.AddInt32(&summaryHits, 1) <= 2 {
				// 第 1 次尝试及其安全重试都失败。
				http.Error(w, `{"error":{"message":"boom"}}`, http.StatusBadRequest)
				return
			}
			writeSimpleText(w, "摘要正文")
			return
		}
		writeSimpleText(w, "ok")
	}))
	defer srv.Close()

	logs := []string{}
	r := newCompactIntegrityRunner(t, srv.URL, &logs)
	// T1 失败 → T2-T4 冷却跳过 → T5 成功。
	for turn := 0; turn < 5; turn++ {
		r.maybeAutoCompact(t.Context())
	}
	if atomic.LoadInt32(&summaryHits) != 3 {
		t.Fatalf("摘要上游命中 %d 次, want 3（失败尝试 2 请求 + 成功尝试 1 请求）", atomic.LoadInt32(&summaryHits))
	}
	if r.compactFailures != 0 || r.compactCooldown != 0 {
		t.Fatalf("成功后失败/冷却应清零, failures=%d cooldown=%d", r.compactFailures, r.compactCooldown)
	}
	if r.autoCompacts != 1 {
		t.Fatalf("autoCompacts = %d, want 1", r.autoCompacts)
	}
	if len(r.history) == 0 {
		t.Fatal("压缩后历史为空")
	}
	if text, ok := r.history[0].Content.(string); !ok || !strings.Contains(text, compactSummaryHeader) {
		t.Fatalf("压缩后历史首条应是摘要, got %#v", r.history[0].Content)
	}
}

// headMessageText 取压缩后首条消息的文本。
func headMessageText(t *testing.T, messages []Message) string {
	t.Helper()
	if len(messages) == 0 {
		t.Fatal("压缩结果为空")
	}
	text, _ := messages[0].Content.(string)
	return text
}

// writeSummarySSEWithStopReason 输出一段完整 SSE 摘要流，stop_reason 可控——
// 用于锁「stop_reason=max_tokens 的半截摘要不得替换历史」。
func writeSummarySSEWithStopReason(w http.ResponseWriter, text, stopReason string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude-x\"}}\n\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + stopReason + "\"}}\n\n" +
			"data: [DONE]\n\n"))
}

// TestCompactRejectsMaxTokensTruncatedSummary 锁截断契约：上游 200 + 完整
// SSE 但 stop_reason=max_tokens 时，Compact 必须报错且不发第二次请求（截断
// 是确定性的，重试只是双倍烧钱）；stop_reason=end_turn 的同形流正常成功。
//
// 变异守卫：摘掉 EventStop 的 stop_reason 校验，第一臂即红（半截摘要被
// 非空校验放行、替换历史，丢的恰是最接近当前的后半段）。
func TestCompactRejectsMaxTokensTruncatedSummary(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !isCompactSummaryRequest(body) {
			writeSimpleText(w, "ok")
			return
		}
		atomic.AddInt32(&hits, 1)
		// 非空文本 + 截断停止：只看文本内容的旧校验会把它当成功放行。
		writeSummarySSEWithStopReason(w, "半截摘要：用户在修 comp", "max_tokens")
	}))
	defer srv.Close()

	messages := compactionFixture()
	tailBudget := estimateMessageTokens(messages[6]) + estimateMessageTokens(messages[5]) + estimateMessageTokens(messages[4])
	cfg := &Config{Client: NewClient(srv.URL), Model: "claude-x", ContextWindow: tailBudget * compactTailRatioDen}
	_, _, err := Compact(t.Context(), cfg, messages, "auto")
	var trunc *compactSummaryTruncatedError
	if !errors.As(err, &trunc) {
		t.Fatalf("max_tokens 截断的摘要应报错, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("上游命中 %d 次, want 1（截断确定性，不得重试）", got)
	}

	// 对照臂：同样的流形，stop_reason=end_turn → 正常成功。
	var okHits int32
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !isCompactSummaryRequest(body) {
			writeSimpleText(w, "ok")
			return
		}
		atomic.AddInt32(&okHits, 1)
		writeSummarySSEWithStopReason(w, "完整摘要正文", "end_turn")
	}))
	defer okSrv.Close()
	cfgOK := &Config{Client: NewClient(okSrv.URL), Model: "claude-x", ContextWindow: tailBudget * compactTailRatioDen}
	compacted, _, err := Compact(t.Context(), cfgOK, messages, "auto")
	if err != nil {
		t.Fatalf("end_turn 摘要应成功: %v", err)
	}
	if !strings.Contains(headMessageText(t, compacted), "完整摘要正文") {
		t.Fatalf("end_turn 摘要应替换进历史, got %q", headMessageText(t, compacted))
	}
}

// TestAutoCompactTruncatedSummaryKeepsHistory 锁 run 侧：截断错误走修复-2 的
// 失败路径（计一次失败、进冷却），r.history 不得被半截摘要替换。
func TestAutoCompactTruncatedSummaryKeepsHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if isCompactSummaryRequest(body) {
			writeSummarySSEWithStopReason(w, "半截摘要", "max_tokens")
			return
		}
		writeSimpleText(w, "ok")
	}))
	defer srv.Close()

	logs := []string{}
	r := newCompactIntegrityRunner(t, srv.URL, &logs)
	before := append([]Message(nil), r.history...)
	r.maybeAutoCompact(t.Context())

	if r.autoCompacts != 0 {
		t.Fatalf("autoCompacts = %d, want 0（截断不得算成功压缩）", r.autoCompacts)
	}
	if len(r.history) != len(before) {
		t.Fatalf("历史被替换：before=%d 条 after=%d 条", len(before), len(r.history))
	}
	for i := range before {
		if before[i].Role != r.history[i].Role {
			t.Fatalf("历史第 %d 条被改动: %q → %q", i, before[i].Role, r.history[i].Role)
		}
	}
	if r.compactFailures != 1 || r.compactCooldown != compactFailureCooldownTurns {
		t.Fatalf("截断应走失败路径进冷却, failures=%d cooldown=%d", r.compactFailures, r.compactCooldown)
	}
}
