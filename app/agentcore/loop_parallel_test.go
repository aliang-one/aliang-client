package agentcore

// 并行工具执行 + 单工具超时覆盖(TimeoutHint)的契约测试。
//
// 背景(2026-11-05 子 agent 审计盲点 1/2):
//  1. 子 agent 桥 Execute 内是完整子 run,曾一律被内核默认 10 分钟单工具
//     上限罩住——子 run 里弹手机审批卡>10min 会连坐 cancel 整个子 run,
//     与宿主 24h 审批 SLA 严重不一致。修法=工具可声明 TimeoutHint 覆盖
//     默认上限(子 agent 桥给 6h,与 claude 侧看门狗同旋钮)。
//  2. executeTools 曾严格串行——模型一回合派多个子 agent 只能排队跑,
//     而 claude Task 原生可并行。修法=相邻的 Parallelizable 工具组成批
//     并发执行(上限 maxParallelToolCalls),结果/journal 钩子/toolCalls
//     一律按原调用顺序在批汇合后回填,内核状态保持单线程。
//
// 本文件锁死:并行语义、顺序保持、批边界串行、并发上限、TimeoutHint
// 管道、以及取消行为不因重构而漂移。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// parProbeTool 可编程工具桩:可声明并行性、超时覆盖、执行耗时;可挂
// 会合栅栏(串行执行会死锁,并行执行双方互放)。
type parProbeTool struct {
	name     string
	parallel bool
	hint     time.Duration
	hold     time.Duration
	wg       *sync.WaitGroup // 非 nil=执行时 Done+Wait(会合)
	onExec   func()
	onDone   func() // Execute 返回前调用(与 onExec 配对,供计数型断言)
}

func (t *parProbeTool) Name() string        { return t.name }
func (t *parProbeTool) Description() string { return "probe" }
func (t *parProbeTool) InputSchema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *parProbeTool) TimeoutHint() time.Duration { return t.hint }
func (t *parProbeTool) Parallelizable() bool       { return t.parallel }
func (t *parProbeTool) Execute(ctx context.Context, _ map[string]interface{}, _ *ToolEnv) ToolResult {
	if t.onExec != nil {
		t.onExec()
	}
	if t.hold > 0 {
		select {
		case <-time.After(t.hold):
		case <-ctx.Done():
			return ErrorResult("cancelled: %v", ctx.Err())
		}
	}
	if t.wg != nil {
		t.wg.Done()
		t.wg.Wait()
	}
	if t.onDone != nil {
		t.onDone()
	}
	return ToolResult{Content: "done:" + t.name}
}

func parRegistry(tools ...Tool) *Registry {
	r := NewRegistry()
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

func parToolUse(id, name string) ContentBlock {
	return ContentBlock{Type: BlockTypeToolUse, ID: id, Name: name, Input: map[string]interface{}{}}
}

func parRunner(reg *Registry, hooks *LoopHooks, logf func(string, ...interface{})) *runner {
	cfg := &Config{Tools: reg, Logf: logf}
	if hooks != nil {
		cfg.Hooks = *hooks
	}
	return &runner{cfg: cfg}
}

// runExecuteToolsWithTimeout 带死锁保险丝地驱动 executeTools:串行实现遇
// 会合栅栏会永远卡死,这里 5s 判负,避免测试本身挂死。
func runExecuteToolsWithTimeout(t *testing.T, r *runner, uses []ContentBlock) []ContentBlock {
	t.Helper()
	type outcome struct {
		blocks []ContentBlock
	}
	done := make(chan outcome, 1)
	go func() { done <- outcome{blocks: r.executeTools(context.Background(), uses, nil)} }()
	select {
	case o := <-done:
		return o.blocks
	case <-time.After(5 * time.Second):
		t.Fatal("executeTools 5s 未返回——并行会合死锁(实现仍是串行?)")
		return nil
	}
}

// TestExecuteToolsParallelToolsRunConcurrently:两个 Parallelizable 工具互相
// 会合——串行实现必然死锁,并行实现双方互放。
func TestExecuteToolsParallelToolsRunConcurrently(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)
	a := &parProbeTool{name: "sub-a", parallel: true, wg: &wg}
	b := &parProbeTool{name: "sub-b", parallel: true, wg: &wg}
	r := parRunner(parRegistry(a, b), nil, nil)

	blocks := runExecuteToolsWithTimeout(t, r, []ContentBlock{parToolUse("u1", "sub-a"), parToolUse("u2", "sub-b")})
	if len(blocks) != 2 {
		t.Fatalf("结果数 = %d, want 2", len(blocks))
	}
	for i, want := range []string{"done:sub-a", "done:sub-b"} {
		if blocks[i].Content != want {
			t.Fatalf("结果[%d] = %q, want %q", i, blocks[i].Content, want)
		}
		if blocks[i].ToolUseID != []string{"u1", "u2"}[i] {
			t.Fatalf("结果[%d] ToolUseID = %q, 顺序必须保持原调用序", i, blocks[i].ToolUseID)
		}
	}
}

// TestExecuteToolsPreservesCallOrder:并行批内各工具耗时交错,但结果块、
// toolCalls 记录、OnToolCall 钩子三者都必须按原调用顺序回填(内核状态
// 单线程化的硬约束——journal 上游按顺序消费)。
func TestExecuteToolsPreservesCallOrder(t *testing.T) {
	var mu sync.Mutex
	var hookNames []string
	hooks := &LoopHooks{OnToolCall: func(name string, _ map[string]interface{}, _ ToolResult) {
		mu.Lock()
		defer mu.Unlock()
		hookNames = append(hookNames, name)
	}}
	a := &parProbeTool{name: "p-slow", parallel: true, hold: 40 * time.Millisecond}
	b := &parProbeTool{name: "p-fast", parallel: true, hold: 5 * time.Millisecond}
	c := &parProbeTool{name: "p-mid", parallel: true, hold: 20 * time.Millisecond}
	r := parRunner(parRegistry(a, b, c), hooks, nil)

	blocks := runExecuteToolsWithTimeout(t, r, []ContentBlock{
		parToolUse("1", "p-slow"), parToolUse("2", "p-fast"), parToolUse("3", "p-mid"),
	})
	wantOrder := []string{"p-slow", "p-fast", "p-mid"}
	for i, name := range wantOrder {
		if !strings.Contains(fmt.Sprintf("%v", blocks[i].Content), name) || blocks[i].ToolUseID != wantID(i) {
			t.Fatalf("结果[%d] 顺序漂移: content=%q id=%q", i, blocks[i].Content, blocks[i].ToolUseID)
		}
	}
	if len(r.toolCalls) != 3 {
		t.Fatalf("toolCalls = %d 条, want 3", len(r.toolCalls))
	}
	for i, name := range wantOrder {
		if r.toolCalls[i].Name != name {
			t.Fatalf("toolCalls[%d] = %q, want %q(顺序必须保持)", i, r.toolCalls[i].Name, name)
		}
	}
	if strings.Join(hookNames, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("OnToolCall 顺序 = %v, want %v(钩子必须按调用序串行触发)", hookNames, wantOrder)
	}
}

func wantID(i int) string { return []string{"1", "2", "3"}[i] }

// TestExecuteToolsMixedBatchesSerializeAcrossBoundaries:可并行工具与普通
// 工具交错时,只在「相邻可并行」的批内并发;批与批之间(以及与普通工具
// 之间)保持严格串行。
func TestExecuteToolsMixedBatchesSerializeAcrossBoundaries(t *testing.T) {
	var mu sync.Mutex
	var ledger []string
	record := func(name string) func() {
		return func() {
			mu.Lock()
			defer mu.Unlock()
			ledger = append(ledger, name)
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	x := &parProbeTool{name: "par-x", parallel: true, wg: &wg, onExec: record("par-x")}
	y := &parProbeTool{name: "par-y", parallel: true, wg: &wg, onExec: record("par-y")}
	s := &parProbeTool{name: "serial-s", onExec: record("serial-s")}
	z := &parProbeTool{name: "par-z", parallel: true, onExec: record("par-z")}
	r := parRunner(parRegistry(x, y, s, z), nil, nil)

	blocks := runExecuteToolsWithTimeout(t, r, []ContentBlock{
		parToolUse("1", "par-x"), parToolUse("2", "par-y"), parToolUse("3", "serial-s"), parToolUse("4", "par-z"),
	})
	if len(blocks) != 4 || !strings.Contains(fmt.Sprintf("%v", blocks[2].Content), "serial-s") {
		t.Fatalf("结果异常: %+v", blocks)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ledger) != 4 {
		t.Fatalf("ledger = %v", ledger)
	}
	idx := func(name string) int {
		for i, v := range ledger {
			if v == name {
				return i
			}
		}
		return -1
	}
	// par-x/par-y(并发)都先于 serial-s;serial-s 先于 par-z。
	if idx("par-x") >= idx("serial-s") || idx("par-y") >= idx("serial-s") {
		t.Fatalf("批边界未串行: ledger=%v", ledger)
	}
	if idx("serial-s") >= idx("par-z") {
		t.Fatalf("serial-s 应先于 par-z: ledger=%v", ledger)
	}
}

// TestExecuteToolsCapsParallelism:同批 6 个可并行工具,同时在飞的不得超过
// maxParallelToolCalls。
func TestExecuteToolsCapsParallelism(t *testing.T) {
	var active, maxActive atomic.Int32
	mk := func(name string) *parProbeTool {
		return &parProbeTool{name: name, parallel: true, hold: 60 * time.Millisecond,
			onExec: func() {
				cur := active.Add(1)
				for {
					old := maxActive.Load()
					if cur <= old || maxActive.CompareAndSwap(old, cur) {
						break
					}
				}
			},
			onDone: func() { active.Add(-1) },
		}
	}
	tools := []Tool{mk("c1"), mk("c2"), mk("c3"), mk("c4"), mk("c5"), mk("c6")}
	r := parRunner(parRegistry(tools...), nil, nil)
	uses := make([]ContentBlock, 0, 6)
	for i := 1; i <= 6; i++ {
		uses = append(uses, parToolUse(string(rune('0'+i)), tools[i-1].Name()))
	}
	blocks := runExecuteToolsWithTimeout(t, r, uses)
	if len(blocks) != 6 {
		t.Fatalf("结果数 = %d, want 6", len(blocks))
	}
	if got := maxActive.Load(); got > int32(maxParallelToolCalls) {
		t.Fatalf("并发峰值 = %d, 超过上限 %d", got, maxParallelToolCalls)
	}
}

// TestToolExecutionCeiling:TimeoutHint 的取值规则——>0 覆盖默认;0/负值
// 回落内核默认(defaultToolTimeout,原 10 分钟)。
func TestToolExecutionCeiling(t *testing.T) {
	withHint := &parProbeTool{name: "hinted", hint: 6 * time.Hour}
	zeroHint := &parProbeTool{name: "zero", hint: 0}
	negHint := &parProbeTool{name: "neg", hint: -time.Minute}
	if got := toolExecutionCeiling(withHint); got != 6*time.Hour {
		t.Fatalf("hint 覆盖 = %v, want 6h", got)
	}
	if got := toolExecutionCeiling(zeroHint); got != defaultToolTimeout {
		t.Fatalf("0 应回落默认 = %v, want %v", got, defaultToolTimeout)
	}
	if got := toolExecutionCeiling(negHint); got != defaultToolTimeout {
		t.Fatalf("负值应回落默认 = %v, want %v", got, defaultToolTimeout)
	}
}

// TestExecuteOneAppliesTimeoutHint:端到端——hint 40ms 的工具,执行体等
// ctx.Done,应在窗口到期后被取消,拿到错误结果。
func TestExecuteOneAppliesTimeoutHint(t *testing.T) {
	slow := &parProbeTool{name: "slow", hint: 40 * time.Millisecond, hold: 5 * time.Second}
	r := parRunner(parRegistry(slow), nil, nil)

	done := make(chan ToolResult, 1)
	go func() { done <- r.executeOne(context.Background(), parToolUse("u1", "slow")) }()
	select {
	case res := <-done:
		if !res.IsError {
			t.Fatalf("应超时失败, got %+v", res)
		}
		if !strings.Contains(res.Content, "context deadline") && !strings.Contains(res.Content, "cancelled") {
			t.Fatalf("错误文案应体现取消: %q", res.Content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TimeoutHint 未生效——工具窗口仍按默认 10min(或工具没收到 ctx)")
	}
}

// TestExecuteToolsCancelledCallsSkipRecords:已取消的调用回填取消结果块,
// 但不进 toolCalls、不触发钩子(锁死既有串行行为,防重构漂移)。
func TestExecuteToolsCancelledCallsSkipRecords(t *testing.T) {
	var mu sync.Mutex
	var hookNames []string
	hooks := &LoopHooks{OnToolCall: func(name string, _ map[string]interface{}, _ ToolResult) {
		mu.Lock()
		defer mu.Unlock()
		hookNames = append(hookNames, name)
	}}
	ctx, cancel := context.WithCancel(context.Background())
	canceler := &parProbeTool{name: "canceler", onExec: cancel}
	follower := &parProbeTool{name: "follower"}
	r := parRunner(parRegistry(canceler, follower), hooks, nil)

	blocks := r.executeTools(ctx, []ContentBlock{parToolUse("1", "canceler"), parToolUse("2", "follower")}, nil)
	if len(blocks) != 2 {
		t.Fatalf("结果数 = %d, want 2", len(blocks))
	}
	if !blocks[1].IsError || !strings.Contains(fmt.Sprintf("%v", blocks[1].Content), "取消") {
		t.Fatalf("后续调用应回填取消错误: %+v", blocks[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(hookNames, ",") != "canceler" {
		t.Fatalf("取消调用不得触发钩子: %v", hookNames)
	}
	if len(r.toolCalls) != 1 || r.toolCalls[0].Name != "canceler" {
		t.Fatalf("取消调用不得进 toolCalls: %+v", r.toolCalls)
	}
}
