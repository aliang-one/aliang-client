package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aliang.one/nursorgate/app/agentcore"
)

// swapAgentCoreMemoryDirForTest 把包级 store 的目录指向 t.TempDir;测试结束还原。
func swapAgentCoreMemoryDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore := AgentCoreMemory.SetDirForTest(dir)
	t.Cleanup(restore)
	return dir
}

func TestAgentCoreMemorySlug(t *testing.T) {
	got := agentCoreMemoryProjectSlug("/Users/mac/My Project/x")
	if strings.ContainsAny(got, "/") {
		t.Fatalf("slug 不得含路径分隔符: %q", got)
	}
	if got != agentCoreMemoryProjectSlug("/Users/mac/My Project/x") {
		t.Fatal("slug 必须确定")
	}
}

func TestAgentCoreMemoryRoundTrip(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	e, err := agentCoreMemory.addManual("/p/a", "提交信息全中文")
	if err != nil || e.ID != 1 || e.Source != "manual" {
		t.Fatalf("首条写入: %+v err=%v", e, err)
	}
	got := agentCoreMemory.list("/p/a")
	if len(got) != 1 || got[0].Content != "提交信息全中文" || got[0].ID != 1 {
		t.Fatalf("读回不一致: %+v", got)
	}
	// 落盘文件形态:JSON 可解析、version/project 字段在
	raw, err := os.ReadFile(filepath.Join(agentCoreMemory.dirFn(), "_p_a.json"))
	if err != nil || !strings.Contains(string(raw), `"version"`) {
		t.Fatalf("落盘形态: %v %s", err, raw)
	}
}

func TestAgentCoreMemoryHandEditedBail(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	// 手改文件:空白项/坏 id/重复 id 全丢弃,合法项保留,trim 生效
	p := filepath.Join(dir, "_p_b.json")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(p, []byte(`{"version":1,"project":"/p/b","next_id":9,"memories":[
		{"id":3,"content":" ok ","source":"extract"},
		{"id":0,"content":"bad-id","source":"extract"},
		{"id":3,"content":"dup","source":"extract"},
		{"id":5,"content":"  ","source":"extract"}]}`), 0o644)
	got := agentCoreMemory.list("/p/b")
	if len(got) != 1 || got[0].ID != 3 || got[0].Content != "ok" {
		t.Fatalf("手改兜底: %+v", got)
	}
}

func TestAgentCoreMemoryBrokenFileBak(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "_p_c.json"), []byte("{not-json"), 0o644)
	_, _ = agentCoreMemory.addManual("/p/c", "after-crash")
	if _, err := os.Stat(filepath.Join(dir, "_p_c.json.bak")); err != nil {
		t.Fatalf("损坏文件必须留 .bak 现场且重建成功: %v", err)
	}
	if len(agentCoreMemory.list("/p/c")) != 1 {
		t.Fatal("重建后新写入必须可读")
	}
}

func TestAgentCoreMemoryNoHomeNoop(t *testing.T) {
	prev := agentCoreMemory.dirFn
	agentCoreMemory.dirFn = func() string { return "" }
	t.Cleanup(func() { agentCoreMemory.dirFn = prev })
	if got := agentCoreMemory.list("/p/x"); len(got) != 0 {
		t.Fatalf("dir 空=list no-op: %+v", got)
	}
	if _, err := agentCoreMemory.addManual("/p/x", "y"); err == nil {
		t.Fatal("dir 空=写必须显式报错而非静默成功")
	}
}

func TestAgentCoreMemoryCapLRU(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	for i := 1; i <= agentCoreMemoryCap; i++ {
		if _, err := agentCoreMemory.addManual("/p/cap", fmt.Sprintf("条目%03d", i)); err != nil {
			t.Fatalf("第 %d 条必须放行: %v", i, err)
		}
	}
	// 注入首条制造「最旧」,再写一条触发 LRU
	agentCoreMemory.touch("/p/cap", []int{agentCoreMemoryCap}) // id=100 变最新
	_, _ = agentCoreMemory.addManual("/p/cap", "新来的")
	got := agentCoreMemory.list("/p/cap")
	if len(got) != agentCoreMemoryCap {
		t.Fatalf("满员仍应 %d 条: %d", agentCoreMemoryCap, len(got))
	}
	for _, e := range got {
		if e.ID == 1 { // touch 把 100 顶新,id=1(未命中最旧)必被淘汰
			t.Fatal("id=1 应已被 LRU 淘汰")
		}
	}
}

func TestAgentCoreMemoryConcurrentWrite(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = agentCoreMemory.addManual("/p/cc", fmt.Sprintf("并发%d", n))
		}(i)
	}
	wg.Wait()
	if got := agentCoreMemory.list("/p/cc"); len(got) != 8 {
		t.Fatalf("并发 8 写应全落: %d", len(got))
	}
}

func TestAgentCoreMemoryProjects(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	if _, err := agentCoreMemory.addManual("/p/alpha", "甲"); err != nil {
		t.Fatalf("写甲: %v", err)
	}
	if _, err := agentCoreMemory.addManual("/p/beta", "乙"); err != nil {
		t.Fatalf("写乙: %v", err)
	}
	got := agentCoreMemory.projects()
	if len(got) != 2 || got[0].Project != "/p/alpha" || got[0].Count != 1 ||
		got[1].Project != "/p/beta" || got[1].Count != 1 {
		t.Fatalf("projects 应两项目按路径有序: %+v", got)
	}
	// 坏 JSON 文件必须被跳过而非拖垮整表
	_ = os.WriteFile(filepath.Join(dir, "zz-broken.json"), []byte("{not-json"), 0o644)
	if got := agentCoreMemory.projects(); len(got) != 2 {
		t.Fatalf("坏文件跳过后仍应 2 项目: %+v", got)
	}
}

func TestAgentCoreMemoryNextIDLift(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	// 手改文件:memories 有 id=1、3,但 next_id=1(≤ 最大 id)——归一必须抬
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "_p_n.json"), []byte(
		`{"version":1,"project":"/p/n","next_id":1,"memories":[
		{"id":1,"content":"a","source":"extract"},
		{"id":3,"content":"b","source":"extract"}]}`), 0o644)
	_ = agentCoreMemory.list("/p/n") // 触发一次 load 归一
	e, err := agentCoreMemory.addManual("/p/n", "新条目")
	if err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	if e.ID <= 3 {
		t.Fatalf("next_id 必须抬到 maxSeen+1=4,拿到 %d", e.ID)
	}
	// 再 load 一轮:新条目绝不能被去重当重复静默丢弃
	got := agentCoreMemory.list("/p/n")
	if len(got) != 3 {
		t.Fatalf("新条目不得被去重丢弃,应 3 条: %+v", got)
	}
}

// kernelEffortRejectionErr 经真内核 client 打一个回 400+effort 字样的本地上游,
// 取回内核自产的 httpStatusError——isEffortRejection 识别的两类错误面字段均未
// 导出,services 测试无法自行拼装;由内核自产保证判定形态零复制(纪律:services
// 禁止复制错误形态识别)。自证:取回的错误必须被内核判为 effort 拒绝。
func kernelEffortRejectionErr(t *testing.T) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"output_config.effort is not supported by this model"}}`))
	}))
	t.Cleanup(srv.Close)
	err := agentcore.NewClient(srv.URL).Stream(context.Background(), &agentcore.MessagesRequest{
		Model:    "m1",
		MaxTokens: 16,
		Messages: []agentcore.Message{agentcore.TextMessage("user", "hi")},
	}, func(agentcore.StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("预期内核自产 400 错误")
	}
	if !agentcore.IsEffortRejection(err) {
		t.Fatalf("预置错误必须被内核判为 effort 拒绝: %v", err)
	}
	return err
}

// kernelPlainRejectionErr 同法取一个非 effort 拒绝错误(403 且错误体不提 effort
// ——内核 isEffortRejection 对其判 false;403 在内核 isRetryable 也判 false,
// fixture 自身只发一次请求)。
func kernelPlainRejectionErr(t *testing.T) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"forbidden"}}`))
	}))
	t.Cleanup(srv.Close)
	err := agentcore.NewClient(srv.URL).Stream(context.Background(), &agentcore.MessagesRequest{
		Model:    "m1",
		MaxTokens: 16,
		Messages: []agentcore.Message{agentcore.TextMessage("user", "hi")},
	}, func(agentcore.StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("预期内核自产 403 错误")
	}
	if agentcore.IsEffortRejection(err) {
		t.Fatalf("预置错误必须被内核判为非 effort 拒绝: %v", err)
	}
	return err
}

// memorySeamForTest 构造提取调用 seam:按 reject(effort) 决定每档成败,拒绝档
// 返回内核自产错误,成功档返回定值文本+指定 stop_reason;顺带断言 MaxTokens=4096。
// 返回 recorder 取历次尝试的 effort 档(累积,按序)。
func memorySeamForTest(t *testing.T, reject func(eff string) bool, succeedStop string) (agentCoreMemoryStreamFn, func() []string) {
	t.Helper()
	rejErr := kernelEffortRejectionErr(t)
	var mu sync.Mutex
	var calls []string
	fn := func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		eff := ""
		if req.OutputConfig != nil {
			eff = req.OutputConfig.Effort
		}
		mu.Lock()
		calls = append(calls, eff)
		mu.Unlock()
		if reject(eff) {
			return "", "", rejErr
		}
		if req.MaxTokens != agentCoreMemoryExtractMaxTokens {
			t.Errorf("MaxTokens 必须 %d: %d", agentCoreMemoryExtractMaxTokens, req.MaxTokens)
			return "", "", fmt.Errorf("bad max tokens")
		}
		return `{"ops":[]}`, succeedStop, nil
	}
	return fn, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// withResetLatch 测试前后重置进程级 effort 锁存(Set 配对位一并清,空串哨兵
// 不可表达「未锁存」)。
func withResetLatch(t *testing.T) {
	t.Helper()
	prevLatched, prevSet := agentCoreMemoryEffortLatched, agentCoreMemoryEffortLatchedSet
	agentCoreMemoryEffortLatched, agentCoreMemoryEffortLatchedSet = "", false
	t.Cleanup(func() { agentCoreMemoryEffortLatched, agentCoreMemoryEffortLatchedSet = prevLatched, prevSet })
}

// fake 计数器经闭包带回,seam 内禁 t.Fatalf(纪律②);提取调用同步执行,
// 闭包内 t.Errorf 合法。
func TestAgentCoreMemoryExtractEffortFallback(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	withResetLatch(t)
	m := &agentAIManager{}
	stream, calls := memorySeamForTest(t, func(eff string) bool { return eff == "low" }, agentcore.StopReasonEndTurn)
	m.coreMemoryCallForTest = stream
	eff := agentcore.NormalizeEffortLevel("ultracode") // = "max"
	text, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", eff, "sys", "usr")
	if err != nil || text != `{"ops":[]}` {
		t.Fatalf("low 被拒不降档则失败: %q %v", text, err)
	}
	got := calls()
	if len(got) != 2 || got[0] != "low" || got[1] != "max" {
		t.Fatalf("回退序应为 low→max(归一后 run.effort): %q", got)
	}
	if !agentCoreMemoryEffortLatchedSet || agentCoreMemoryEffortLatched != "max" {
		t.Fatalf("成功档必须锁存: set=%v %q", agentCoreMemoryEffortLatchedSet, agentCoreMemoryEffortLatched)
	}
	// 第二次调用直接用锁存档,不再试 low
	_, _ = m.agentCoreMemoryExtractCall(context.Background(), "m1", eff, "sys", "usr")
	if got := calls(); len(got) != 3 || got[2] != "max" {
		t.Fatalf("锁存后不得再试 low: %q", got)
	}
}

func TestAgentCoreMemoryExtractMaxTokensGuard(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	withResetLatch(t)
	m := &agentAIManager{}
	stream, calls := memorySeamForTest(t, func(eff string) bool { return false }, agentcore.StopReasonMaxTokens)
	m.coreMemoryCallForTest = stream
	_, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", "", "sys", "usr")
	if err == nil || !strings.Contains(err.Error(), "截断") {
		t.Fatalf("max_tokens 截断必须定性报错: %v", err)
	}
	if len(calls()) != 1 {
		t.Fatalf("截断不得重试: %q", calls())
	}
	if agentCoreMemoryEffortLatchedSet {
		t.Fatal("截断丢弃不得锁存档位")
	}
}

func TestAgentCoreMemoryExtractEffortBranches(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	withResetLatch(t)

	t.Run("终档无effort成功也锁存", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		stream, calls := memorySeamForTest(t, func(eff string) bool { return eff == "low" }, agentcore.StopReasonEndTurn)
		m.coreMemoryCallForTest = stream
		if _, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", "", "sys", "usr"); err != nil {
			t.Fatalf("无 effort 终试应成功: %v", err)
		}
		if got := calls(); len(got) != 2 || got[0] != "low" || got[1] != "" {
			t.Fatalf("候选序应 low→无 effort: %q", got)
		}
		if !agentCoreMemoryEffortLatchedSet || agentCoreMemoryEffortLatched != "" {
			// 空串哨兵不可表达「未锁存」——Set 位才是锁存标志
			t.Fatalf("空串终档也必须置 Set 位锁存: set=%v %q", agentCoreMemoryEffortLatchedSet, agentCoreMemoryEffortLatched)
		}
		_, _ = m.agentCoreMemoryExtractCall(context.Background(), "m1", "", "sys", "usr")
		if got := calls(); len(got) != 3 || got[2] != "" {
			t.Fatalf("锁存后不得再试 low: %q", got)
		}
	})

	t.Run("run.effort归一为空跳中间档", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		stream, calls := memorySeamForTest(t, func(eff string) bool { return eff == "low" }, agentcore.StopReasonEndTurn)
		m.coreMemoryCallForTest = stream
		if _, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", "bogus-effort", "sys", "usr"); err != nil {
			t.Fatalf("终档成功: %v", err)
		}
		if got := calls(); len(got) != 2 || got[0] != "low" || got[1] != "" {
			t.Fatalf("归一为空应只两档 low→无 effort: %q", got)
		}
	})

	t.Run("run.effort恰为low去重", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		stream, calls := memorySeamForTest(t, func(eff string) bool { return eff == "low" }, agentcore.StopReasonEndTurn)
		m.coreMemoryCallForTest = stream
		if _, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", "low", "sys", "usr"); err != nil {
			t.Fatalf("终档成功: %v", err)
		}
		if got := calls(); len(got) != 2 || got[0] != "low" || got[1] != "" {
			t.Fatalf("恰为 low 应去重不重复试: %q", got)
		}
	})

	t.Run("双拒降无effort终试成功", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		stream, calls := memorySeamForTest(t, func(eff string) bool { return eff == "low" || eff == "max" }, agentcore.StopReasonEndTurn)
		m.coreMemoryCallForTest = stream
		text, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", agentcore.NormalizeEffortLevel("ultracode"), "sys", "usr")
		if err != nil || text != `{"ops":[]}` {
			t.Fatalf("无 effort 终试应成功: %q %v", text, err)
		}
		if got := calls(); len(got) != 3 || got[0] != "low" || got[1] != "max" || got[2] != "" {
			t.Fatalf("三档序应 low→max→无 effort: %q", got)
		}
		if !agentCoreMemoryEffortLatchedSet || agentCoreMemoryEffortLatched != "" {
			t.Fatalf("终档锁存: set=%v %q", agentCoreMemoryEffortLatchedSet, agentCoreMemoryEffortLatched)
		}
	})

	t.Run("三档全拒返回错误", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		stream, calls := memorySeamForTest(t, func(eff string) bool { return true }, agentcore.StopReasonEndTurn)
		m.coreMemoryCallForTest = stream
		if _, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", agentcore.NormalizeEffortLevel("ultracode"), "sys", "usr"); err == nil {
			t.Fatal("三档全拒必须报错(Warn 放弃路径)")
		}
		if got := calls(); len(got) != 3 {
			t.Fatalf("全拒应恰试三档: %q", got)
		}
		if agentCoreMemoryEffortLatchedSet {
			t.Fatal("全拒不得锁存")
		}
	})

	t.Run("非effort拒绝不重试", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		plainErr := kernelPlainRejectionErr(t)
		var mu sync.Mutex
		var got []string
		m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
			mu.Lock()
			got = append(got, "x")
			mu.Unlock()
			return "", "", plainErr
		}
		_, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", agentcore.NormalizeEffortLevel("ultracode"), "sys", "usr")
		if err == nil || !errors.Is(err, plainErr) {
			t.Fatalf("非 effort 拒绝必须原样返回: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("非 effort 拒绝不得重试,应恰 1 次: %d", len(got))
		}
	})
}
