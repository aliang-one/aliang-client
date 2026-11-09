# agentcore 记忆层实施计划(写侧提取+读侧注入,项目级,宿主侧)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** agentcore 每个 run 收尾后宿主异步提取项目级长期事实落盘,后续 run 的 system prompt 全量注入既有记忆,容量 100 条 LRU,文件+API+仪表盘卡片三管理面,内核只读。

**Architecture:** 方案 A 宿主侧独立模块——新 `agent_ai_agentcore_memory.go`(store+提取管线+注入)与 journal 同族(尽力而为,绝不炸 run);两处宿主接线(收尾钩子+prompt 注入);内核唯一触点=导出 `IsEffortRejection` 一行包装。spec=docs/superpowers/specs/2026-11-09-agentcore-memory-design.md(两轮评审通过,决策全在案)。

**Tech Stack:** Go(agentcore SDK MessagesRequest/Stream/StreamEvent)、原生 ServeMux 方法前缀路由、Vue3 settings 卡片、vitest。

---

## 全局纪律(每个任务都适用)

1. **worktree 操作**,目录 `/Users/mac/MyProgram/GoProgram/nursor/alianggate-memory`(Task 0 建),不在主仓改代码。
2. gopls「not in workspace」是误报,一律无视;以 `go build`/`go test` 为准。**禁止 `-race`**(仓内预存 flake)。
3. **安全红线**:提取输入与 prompt 不得携带 server env/headers 值;记忆文件只落 projectPath 相关事实,不含密钥(prompt 层禁 + 宿主不二次过滤,v1 口径)。
4. 注释中文,风格对齐文件内既有注释(说「为什么」)。
5. 提交标题「新增:/修复:/重构:/测试:」全中文;HEREDOC 格式;footer:
   `🤖 Generated with [Claude Code](https://claude.com/claude-code)` + `Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>`
6. **测试编写两条纪律**:①goroutine/回调闭包内禁止 `t.Fatalf`(会崩测试二进制),错误用通道/标志位带回主 goroutine 断言;②新测试先 `go test -list` 或 `-v -run` 确认真实执行(Go 隐式 GOOS 后缀静默跳过的老坑)。

---

### Task 0: worktree 准备

**Files:** 无代码改动。

- [ ] **Step 1: 建 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add ../alianggate-memory -b feat/agentcore-memory
```

- [ ] **Step 2: 补前端构建产物**(新 worktree 首次 build 必踩的坑:dist 被 gitignore)

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
cp -R app/website/dist /Users/mac/MyProgram/GoProgram/nursor/alianggate-memory/app/website/
```

- [ ] **Step 3: 拷入 spec/plan 文档**(不入库,供子代理就近读)

```bash
W=/Users/mac/MyProgram/GoProgram/nursor/alianggate-memory
mkdir -p $W/docs/superpowers/specs $W/docs/superpowers/plans
cp docs/superpowers/specs/2026-11-09-agentcore-memory-design.md $W/docs/superpowers/specs/
cp docs/superpowers/plans/2026-11-09-agentcore-memory.md $W/docs/superpowers/plans/
```

- [ ] **Step 4: 确认基线绿**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-memory && go build ./... && go test ./app/http/services -run '^TestAgentCoreMCP' -count=1
```
Expected: ok(基线不绿先停下查环境,不许带病开工)。

---

### Task 1: customer.agentcore.memory_enabled 开关字段

**Files:**
- Modify: `processor/config/types.go`(AgentCoreTuningConfig :685 附近 + Effective 方法 :720 附近)

- [ ] **Step 1: 加字段**(紧邻 SkillsEnabled,同款三态指针先例)

```go
	// MemoryEnabled 是记忆层总开关(提取+注入)。三态指针沿 MCPEnabled/SkillsEnabled
	// 先例:nil=未配置(默认开启)、true/false 显式。只作用于 agentcore 内核。
	// 关闭时不提取、不注入,但 API 仍可读可管(手动管理既有记忆不被开关锁死)。
	MemoryEnabled *bool `json:"memory_enabled,omitempty"`
```

- [ ] **Step 2: 加 Effective 方法**(紧邻 EffectiveAgentCoreSkillsEnabled)

```go
// EffectiveAgentCoreMemoryEnabled 返回记忆层是否开启。默认开启(同 MCP/技能):
// 记忆是加法能力,开关不能把没配过的人的功能关掉。nil(未配置)与 true 等价。
func (c *Config) EffectiveAgentCoreMemoryEnabled() bool {
	if c == nil || c.Customer == nil || c.Customer.AgentCore == nil {
		return true
	}
	if c.Customer.AgentCore.MemoryEnabled == nil {
		return true
	}
	return *c.Customer.AgentCore.MemoryEnabled
}
```

- [ ] **Step 3: 副本同步排查**(allowlist 族老坑)

```bash
grep -rn "skills_enabled" --include="*.go" --include="*.vue" --include="*.js" . | grep -v node_modules | grep -v worktrees
```
逐个命中点核对:结构体副本(agentcore 三态族)、前端表单/文案是否需要带 memory_enabled。**逐一同步,不留单侧**。customer 配置的 Validate 对 `*bool` 无需新校验(指针三态天然合法)。

- [ ] **Step 4: 测试**

在 `processor/config` 既有测试文件加(沿 MCPEnabled 测试形态):

```go
func TestEffectiveAgentCoreMemoryEnabled(t *testing.T) {
	var c Config
	if !c.EffectiveAgentCoreMemoryEnabled() {
		t.Fatal("nil 全链应默认开启")
	}
	off := false
	c.Customer = &CustomerConfig{AgentCore: &AgentCoreTuningConfig{MemoryEnabled: &off}}
	if c.EffectiveAgentCoreMemoryEnabled() {
		t.Fatal("显式 false 必须关闭")
	}
	on := true
	c.Customer.AgentCore.MemoryEnabled = &on
	if !c.EffectiveAgentCoreMemoryEnabled() {
		t.Fatal("显式 true 必须开启")
	}
}
```
(CustomerConfig/AgentCoreTuningConfig 的实际字段路径以文件为准,对齐 MCPEnabled 同款测试的构造方式。)

Run: `go test ./processor/config -run TestEffectiveAgentCoreMemory -count=1 -v` → PASS。

- [ ] **Step 5: Commit**

```bash
git add processor/config/ && git commit -m "新增:customer.agentcore.memory_enabled 记忆层三态开关(默认开,nil=未配置)"
```

---

### Task 2: 存储层核心——store/slug/往返/手改兜底/损坏 .bak/no-op

**Files:**
- Create: `app/http/services/agent_ai_agentcore_memory.go`
- Test: `app/http/services/agent_ai_agentcore_memory_test.go`

- [ ] **Step 1: 写失败测试**(文件新建;测试注入 seam 用 dirFn 可换)

```go
package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swapAgentCoreMemoryDirForTest 把包级 store 的目录指向 t.TempDir;测试结束还原。
func swapAgentCoreMemoryDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := agentCoreMemory.dirFn
	agentCoreMemory.dirFn = func() string { return dir }
	t.Cleanup(func() { agentCoreMemory.dirFn = prev })
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
	_ = agentCoreMemory.addManual("/p/c", "after-crash")
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
```

- [ ] **Step 2: 跑测试确认失败**(函数不存在编译错)

Run: `go test ./app/http/services -run 'TestAgentCoreMemory(Slug|RoundTrip|HandEdited|Broken|NoHome)' -count=1`
Expected: FAIL(未定义)。

- [ ] **Step 3: 实现 store 核心**

```go
package services

// agentcore(AliangCode)记忆层(spec: docs/superpowers/specs/2026-11-09-agentcore-memory-design.md)。
// 项目级长期事实:写侧=run 收尾后宿主异步提取;读侧=system prompt 全量注入。
// 与 journal 同族纪律:尽力而为,任何失败只 Warn,绝不影响 run。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// agentCoreMemoryCap 单项目记忆条数上限,满则 LRU 淘汰(spec §4)。
const agentCoreMemoryCap = 100
// agentCoreMemoryContentCap 单条 content 上限(字符数,trim 后)。写通道
// fail-closed:API 三通道拒绝,提取 ops 通道丢弃该条(spec §8 拍板)。
const agentCoreMemoryContentCap = 500
// agentCoreMemoryDedupListCap 提取 prompt 既有条目对账预算(last_hit_at 降序
// 取前 N 条)——满库全量对账每回合上万 token 不可接受(spec §5)。
const agentCoreMemoryDedupListCap = 20
// agentCoreMemoryInjectBudgetChars 注入字符预算,超限截最旧(spec §6)。
const agentCoreMemoryInjectBudgetChars = 6000

type agentCoreMemoryEntry struct {
	ID        int    `json:"id"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	LastHitAt string `json:"last_hit_at,omitempty"`
	Source    string `json:"source"` // extract | manual
}

type agentCoreMemoryFile struct {
	Version   int                    `json:"version"`
	Project   string                 `json:"project"`
	NextID    int                    `json:"next_id"`
	UpdatedAt string                 `json:"updated_at"`
	Memories  []agentCoreMemoryEntry `json:"memories"`
}

// agentCoreMemoryStore 进程内单例。dirFn 可注入(测试 t.TempDir);跨项目
// 单把全局锁——写频=每回合一次,分文件锁是过度设计(spec §4 定死)。
type agentCoreMemoryStore struct {
	mu    sync.Mutex
	dirFn func() string
}

var agentCoreMemory = newAgentCoreMemoryStore(agentCoreMemoryDir)

func newAgentCoreMemoryStore(dirFn func() string) *agentCoreMemoryStore {
	return &agentCoreMemoryStore{dirFn: dirFn}
}

// agentCoreMemoryDir 存储根;<home>/.aliang/memory。home 不可得=空串,
// 全路径 no-op(沿 journal 同款退化)。
func agentCoreMemoryDir() string {
	home := agentHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".aliang", "memory")
}

// agentCoreMemoryProjectSlug projectPath → 安全文件名:非白名单字符一律 `_`,
// 超长截断(沿 agentCoreJournalFileName 先例)。空 path 返回空串=该 run 不做记忆。
func agentCoreMemoryProjectSlug(projectPath string) string {
	var b strings.Builder
	for _, r := range projectPath {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

// loadLocked 读入并归一。缺文件=空库;损坏=改名 .bak 留现场+空库重建(spec §4
// 新决策,不静默覆盖);手改兜底=trim/丢空/丢坏 id/重复 id 丢弃(spec §4)。
func (s *agentCoreMemoryStore) loadLocked(dir, slug string) *agentCoreMemoryFile {
	fresh := func() *agentCoreMemoryFile {
		return &agentCoreMemoryFile{Version: 1, NextID: 1}
	}
	path := filepath.Join(dir, slug+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn(fmt.Sprintf("agentcore-memory: 读失败按空库处理 path=%s err=%v", slug, err))
		}
		return fresh()
	}
	var f agentCoreMemoryFile
	if err := json.Unmarshal(raw, &f); err != nil {
		_ = os.Rename(path, path+".bak") // 同名 .bak 覆盖:现场以最近一次为准
		logger.Warn(fmt.Sprintf("agentcore-memory: 文件损坏已留 .bak 并重建 slug=%s 摘要=%.200q", slug, string(raw)))
		return fresh()
	}
	seen := map[int]bool{}
	kept := make([]agentCoreMemoryEntry, 0, len(f.Memories))
	dropped := 0
	for _, e := range f.Memories {
		e.Content = strings.TrimSpace(e.Content)
		if e.Content == "" || e.ID <= 0 || seen[e.ID] {
			dropped++ // 手改兜底:丢弃并计数,聚合一条 Warn(spec §4「丢弃并 Warn」)
			continue
		}
		seen[e.ID] = true
		kept = append(kept, e)
	}
	if dropped > 0 {
		logger.Warn(fmt.Sprintf("agentcore-memory: 手改文件兜底丢弃 %d 条非法条目 slug=%s", dropped, slug))
	}
	f.Memories = kept
	if f.NextID < 1 {
		f.NextID = 1
	}
	return &f
}

// saveLocked 落盘。目录懒建。
func (s *agentCoreMemoryStore) saveLocked(dir, slug string, f *agentCoreMemoryFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, slug+".json"), raw, 0o644)
}

// list 按项目列全量(按 last_hit_at 降序,空回退 created_at;spec §8)。
func (s *agentCoreMemoryStore) list(projectPath string) []agentCoreMemoryEntry {
	dir := s.dirFn()
	slug := agentCoreMemoryProjectSlug(projectPath)
	if dir == "" || slug == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.loadLocked(dir, slug)
	out := append([]agentCoreMemoryEntry(nil), f.Memories...)
	// 稳定排序+平局 ID 降序:同秒 RFC3339(recency 相等)时后写的 id 大者靠前
	// ——平局确定性,测试与生产 LRU 均不再 flake。
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := memoryRecency(out[i]), memoryRecency(out[j])
		if ri != rj {
			return ri > rj
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// memoryRecency LRU 序键:last_hit_at 优先,空回退 created_at(RFC3339 字典序=时间序)。
func memoryRecency(e agentCoreMemoryEntry) string {
	if e.LastHitAt != "" {
		return e.LastHitAt
	}
	return e.CreatedAt
}

// addManual 手动增(source=manual);content trim/空拒/超限拒(spec §8)。
func (s *agentCoreMemoryStore) addManual(projectPath, content string) (agentCoreMemoryEntry, error) {
	return s.addEntry(projectPath, content, "manual")
}

func (s *agentCoreMemoryStore) addEntry(projectPath, content, source string) (agentCoreMemoryEntry, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return agentCoreMemoryEntry{}, fmt.Errorf("content 必填")
	}
	if len([]rune(content)) > agentCoreMemoryContentCap {
		return agentCoreMemoryEntry{}, fmt.Errorf("单条记忆超上限 %d 字符", agentCoreMemoryContentCap)
	}
	dir := s.dirFn()
	slug := agentCoreMemoryProjectSlug(projectPath)
	if dir == "" || slug == "" {
		return agentCoreMemoryEntry{}, fmt.Errorf("记忆存储不可用")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.loadLocked(dir, slug)
	e := agentCoreMemoryEntry{
		ID: f.NextID, Content: content, Source: source,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	f.NextID++
	f.Memories = append(f.Memories, e)
	f.Project = projectPath
	s.evictLocked(dir, slug, f)
	if err := s.saveLocked(dir, slug, f); err != nil {
		return agentCoreMemoryEntry{}, err
	}
	return e, nil
}

// evictLocked 满员 LRU 淘汰:按 memoryRecency 最旧出列,逐条 Warn(spec §4)。
// 调用方持锁、追加后调用。
func (s *agentCoreMemoryStore) evictLocked(dir, slug string, f *agentCoreMemoryFile) {
	for len(f.Memories) > agentCoreMemoryCap {
		oldest := 0
		for i := 1; i < len(f.Memories); i++ {
			if memoryRecency(f.Memories[i]) < memoryRecency(f.Memories[oldest]) {
				oldest = i
			}
		}
		v := f.Memories[oldest]
		head := v.Content
		if r := []rune(head); len(r) > 40 {
			head = string(r[:40])
		}
		logger.Warn(fmt.Sprintf("agentcore-memory: 满员淘汰 id=%d recency=%s 内容=%s…", v.ID, memoryRecency(v), head))
		f.Memories = append(f.Memories[:oldest], f.Memories[oldest+1:]...)
	}
}

// update 改 content:保留原 source、不刷 last_hit_at(spec §8 两通道口径)。
func (s *agentCoreMemoryStore) update(projectPath string, id int, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("content 必填")
	}
	if len([]rune(content)) > agentCoreMemoryContentCap {
		return fmt.Errorf("单条记忆超上限 %d 字符", agentCoreMemoryContentCap)
	}
	dir := s.dirFn()
	slug := agentCoreMemoryProjectSlug(projectPath)
	if dir == "" || slug == "" {
		return fmt.Errorf("记忆存储不可用")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.loadLocked(dir, slug)
	for i := range f.Memories {
		if f.Memories[i].ID == id {
			f.Memories[i].Content = content
			if err := s.saveLocked(dir, slug, f); err != nil {
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("记忆条目不存在: %d", id)
}

// remove 删单条;clear 清空。
func (s *agentCoreMemoryStore) remove(projectPath string, id int) error {
	dir := s.dirFn()
	slug := agentCoreMemoryProjectSlug(projectPath)
	if dir == "" || slug == "" {
		return fmt.Errorf("记忆存储不可用")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.loadLocked(dir, slug)
	for i := range f.Memories {
		if f.Memories[i].ID == id {
			f.Memories = append(f.Memories[:i], f.Memories[i+1:]...)
			return s.saveLocked(dir, slug, f)
		}
	}
	return fmt.Errorf("记忆条目不存在: %d", id)
}

func (s *agentCoreMemoryStore) clear(projectPath string) error {
	dir := s.dirFn()
	slug := agentCoreMemoryProjectSlug(projectPath)
	if dir == "" || slug == "" {
		return fmt.Errorf("记忆存储不可用")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.loadLocked(dir, slug)
	f.Memories = nil
	return s.saveLocked(dir, slug, f)
}

// projects 扫存储根,返回有记忆文件的项目路径与条目数(仪表盘卡片项目下拉)。
// 损坏/无名项目文件跳过。
func (s *agentCoreMemoryStore) projects() []agentCoreMemoryProjectInfo {
	dir := s.dirFn()
	if dir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []agentCoreMemoryProjectInfo
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}
		var f agentCoreMemoryFile
		if err := json.Unmarshal(raw, &f); err != nil || f.Project == "" {
			continue
		}
		out = append(out, agentCoreMemoryProjectInfo{Project: f.Project, Count: len(f.Memories)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out
}

type agentCoreMemoryProjectInfo struct {
	Project string `json:"project"`
	Count   int    `json:"count"`
}

// touch 注入命中:逐 id 校验条目仍存在才刷 last_hit_at(防与 DELETE 竞态复活,
// spec §6)。失败只 Warn。
func (s *agentCoreMemoryStore) touch(projectPath string, ids []int) {
	if len(ids) == 0 {
		return
	}
	dir := s.dirFn()
	slug := agentCoreMemoryProjectSlug(projectPath)
	if dir == "" || slug == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.loadLocked(dir, slug)
	now := time.Now().UTC().Format(time.RFC3339)
	hit := false
	idset := map[int]bool{}
	for _, id := range ids {
		idset[id] = true
	}
	for i := range f.Memories {
		if idset[f.Memories[i].ID] {
			f.Memories[i].LastHitAt = now
			hit = true
		}
	}
	if !hit {
		return
	}
	if err := s.saveLocked(dir, slug, f); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-memory: touch 落盘失败 slug=%s err=%v", slug, err))
	}
}
```

注意 `agentCoreMemory` 单例的字段名是 `dirFn`(测试 seam 直接换字段)——**包级单例的 dirFn 换取必须在测试内做,不导出 setter**。

### 导出门面(handlers 包消费,Task 6 用;同文件紧随其后追加)

```go
// AgentCoreMemoryFacade 导出门面:handlers 包经此操作记忆 store——services
// 内部单例不导出,仓内边界先例=handlers 只用导出 services API(无 import 环:
// handlers 现已 import services)。包内测试走未导出 seam,跨包走门面方法,
// 两者并存自洽。
type AgentCoreMemoryFacade struct{}

// AgentCoreMemory 记忆操作入口(handlers 与其测试共用)。
var AgentCoreMemory = AgentCoreMemoryFacade{}

func (AgentCoreMemoryFacade) List(project string) []agentCoreMemoryEntry {
	return agentCoreMemory.list(project)
}

func (AgentCoreMemoryFacade) Add(project, content string) (agentCoreMemoryEntry, error) {
	return agentCoreMemory.addManual(project, content)
}

func (AgentCoreMemoryFacade) Update(project string, id int, content string) error {
	return agentCoreMemory.update(project, id, content)
}

func (AgentCoreMemoryFacade) Remove(project string, id int) error {
	return agentCoreMemory.remove(project, id)
}

func (AgentCoreMemoryFacade) Clear(project string) error {
	return agentCoreMemory.clear(project)
}

func (AgentCoreMemoryFacade) Projects() []agentCoreMemoryProjectInfo {
	return agentCoreMemory.projects()
}

// SwapDirForTest 导出测试目录注入:handlers 包测试拿不到 services 未导出
// seam,经此换 TempDir;t.Cleanup 还原。
func (AgentCoreMemoryFacade) SwapDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := agentCoreMemory.dirFn
	agentCoreMemory.dirFn = func() string { return dir }
	t.Cleanup(func() { agentCoreMemory.dirFn = prev })
	return dir
}
```
(文件需补 import `testing`。)

- [ ] **Step 4: 跑测试确认通过**

再补两条(spec §11 全覆盖):

```go
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
```
(并发测试走真 goroutine,无 t.Fatalf 进闭包——错误经返回值/最终 list 断言,纪律①合规。)

Run: `go build ./... && go test ./app/http/services -run 'TestAgentCoreMemory' -count=1 -v`
Expected: 全 PASS(含本任务全部新旧用例)。

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_ai_agentcore_memory.go app/http/services/agent_ai_agentcore_memory_test.go
git commit -m "新增:agentcore 记忆存储层——项目级 slug 落盘/手改兜底/损坏 bak 现场满员 LRU/touch 防复活"
```

---

### Task 3: IsEffortRejection 导出 + 提取调用(回退链+锁存+seam+截断守卫)

**Files:**
- Modify: `app/agentcore/loop.go`(导出包装,唯一内核触点)
- Modify: `app/http/services/agent_ai.go`(agentAIManager 加 seam 字段,**紧邻 coreMCPDialForTest :64 同处**)
- Modify: `app/http/services/agent_ai_agentcore_memory.go`(提取调用)
- Test: `app/http/services/agent_ai_agentcore_memory_test.go`

- [ ] **Step 0: seam 字段**(agent_ai.go agentAIManager struct 内,coreMCPDialForTest 旁)

```go
	// coreMemoryCallForTest 记忆提取 LLM 调用 seam(nil=走真身);测试注入假身。
	coreMemoryCallForTest agentCoreMemoryStreamFn
```

- [ ] **Step 1: 内核导出包装**(loop.go,isEffortRejection :940 定义之后)

```go
// IsEffortRejection 导出包装:宿主侧提取调用复用同一判定——跨包安全决策
// 单点化,services 禁止复制错误形态识别(防漂移)。
func IsEffortRejection(err error) bool { return isEffortRejection(err) }
```

- [ ] **Step 2: 写失败测试**(追加到 memory 测试文件;fake seam 不进 goroutine 断言)

```go
// fake 计数器经通道带回,闭包内禁 t.Fatalf(纪律②)
func TestAgentCoreMemoryExtractEffortFallback(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	m := &agentAIManager{}
	calls := make(chan string, 8) // 记录每次尝试的 effort 档
	resetLatch := func() { agentCoreMemoryEffortLatched, agentCoreMemoryEffortLatchedSet = "", false }
	prevLatched, prevSet := agentCoreMemoryEffortLatched, agentCoreMemoryEffortLatchedSet
	resetLatch()
	t.Cleanup(func() { agentCoreMemoryEffortLatched, agentCoreMemoryEffortLatchedSet = prevLatched, prevSet })

	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		eff := ""
		if req.OutputConfig != nil {
			eff = req.OutputConfig.Effort
		}
		calls <- eff
		if eff == "low" {
			return "", "", fmt.Errorf("4xx effort unsupported")
		}
		return `{"ops":[]}`, "end_turn", nil
	}
	eff := agentcore.NormalizeEffortLevel("ultracode") // = "max"
	text, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", eff, "sys", "usr")
	if err != nil || text != `{"ops":[]}` {
		t.Fatalf("low 被拒不降档则失败: %q %v", text, err)
	}
	got1 := <-calls
	got2 := <-calls
	if got1 != "low" || got2 != "max" {
		t.Fatalf("回退序应为 low→max(归一后 run.effort): %q %q", got1, got2)
	}
	if !agentCoreMemoryEffortLatchedSet || agentCoreMemoryEffortLatched != "max" {
		t.Fatalf("成功档必须锁存: set=%v %q", agentCoreMemoryEffortLatchedSet, agentCoreMemoryEffortLatched)
	}
	// 第二次调用直接用锁存档,不再试 low
	_, _ = m.agentCoreMemoryExtractCall(context.Background(), "m1", eff, "sys", "usr")
	got3 := <-calls
	if got3 != "max" {
		t.Fatalf("锁存后不得再试 low: %q", got3)
	}
}

func TestAgentCoreMemoryExtractMaxTokensGuard(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	m := &agentAIManager{}
	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		if req.MaxTokens != 4096 {
			t.Errorf("MaxTokens 必须 4096: %d", req.MaxTokens) // 同步闭包允许 t.Errorf
			return "", "", fmt.Errorf("bad")
		}
		return `{"ops":[{"op":"add","content":"半截"}]}`, "max_tokens", nil
	}
	_, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", "", "sys", "usr")
	if err == nil || !strings.Contains(err.Error(), "截断") {
		t.Fatalf("max_tokens 截断必须定性报错: %v", err)
	}
}
```

(测试里的 `agentCoreMemoryEffortLatched` 是包级变量;若实现为 atomic.Value,测试直接读写同名字段,保持简单——锁存只要求进程内串行生效,原子性非目标。)

- [ ] **Step 3: 实现提取调用**

```go
// agentcore 进程级 effort 锁存(spec §5):首次成功即置 Set 并记档——档位可能
// 是空串(无 effort 终档),故需 Set 配对位区分「未锁存」,否则每回合重吃 400。
var (
	agentCoreMemoryEffortLatched    string
	agentCoreMemoryEffortLatchedSet bool
)

// agentCoreMemoryExtractMaxTokens 提取输出预算(spec §5):1024 装不下 ops 包络
// (单条 500 字符≈250-350 token),4096 给足修正 ops 余量。
const agentCoreMemoryExtractMaxTokens = 4096

// agentCoreMemoryStreamFn 提取 LLM 调用 seam:真身=NewClient+Stream 聚合;
// 测试注入假身。返回(纯文本, stopReason, err)。
type agentCoreMemoryStreamFn func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error)

func (m *agentAIManager) agentCoreMemoryRealStream(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
	client := agentcore.NewClient(agentCoreBaseURL()).WithAPIKey(agentCoreAuthToken())
	var b strings.Builder
	stopReason := ""
	err := client.Stream(ctx, req, func(ev agentcore.StreamEvent) error {
		switch ev.Kind {
		case agentcore.EventText:
			b.WriteString(ev.Text)
		case agentcore.EventStop:
			stopReason = ev.StopReason
		case agentcore.EventError:
			return ev.Err
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return b.String(), stopReason, nil
}

// agentCoreMemoryExtractCall 同端点同模型提取(spec §5):effort 首选 low,被拒
// 降归一后 run.effort,再拒无 effort;锁存后直用。非 effort 拒绝的错误不重试。
// max_tokens 截断定性报错(拒绝半截 JSON,沿 compact.go:324 先例)。
func (m *agentAIManager) agentCoreMemoryExtractCall(ctx context.Context, model, runEffort, system, user string) (string, error) {
	stream := m.agentCoreMemoryRealStream
	if m.coreMemoryCallForTest != nil {
		stream = m.coreMemoryCallForTest
	}
	// 候选档:锁存优先;否则 low → 归一(run.effort) → 无,归一后去重保序。
	var candidates []string
	if agentCoreMemoryEffortLatchedSet {
		candidates = []string{agentCoreMemoryEffortLatched}
	} else {
		candidates = []string{"low"}
		if norm := agentcore.NormalizeEffortLevel(runEffort); norm != "" && norm != "low" {
			candidates = append(candidates, norm)
		}
		candidates = append(candidates, "")
	}
	var lastErr error
	for _, eff := range candidates {
		req := &agentcore.MessagesRequest{
			Model: model, MaxTokens: agentCoreMemoryExtractMaxTokens,
			System: system,
			Messages: []agentcore.Message{agentcore.TextMessage("user", user)},
		}
		if eff != "" {
			req.OutputConfig = &agentcore.OutputConfig{Effort: eff}
		}
		text, stop, err := stream(ctx, req)
		if err != nil {
			if agentcore.IsEffortRejection(err) && eff != candidates[len(candidates)-1] {
				logger.Warn(fmt.Sprintf("agentcore-memory: 上游拒绝 effort=%q,降档重试: %v", eff, err))
				lastErr = err
				continue
			}
			return "", err // 非 effort 拒绝(或终档)不重试
		}
		if stop == agentcore.StopReasonMaxTokens {
			return "", fmt.Errorf("agentcore-memory: 提取输出被 max_tokens 截断,丢弃本次")
		}
		agentCoreMemoryEffortLatched, agentCoreMemoryEffortLatchedSet = eff, true
		return text, nil
	}
	return "", lastErr
}
```

(实现时把上面草稿里的空 if 占位删掉;测试断言的回退序以这段实现为准。)

- [ ] **Step 4: 跑测试**

再补 effort 链四分支测试(spec §11 全覆盖,形态同上,seam 每用例重设+锁存 reset):

```go
func TestAgentCoreMemoryExtractEffortBranches(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	resetLatch := func() { agentCoreMemoryEffortLatched, agentCoreMemoryEffortLatchedSet = "", false }
	// ⓪ 终档无 effort 成功也必须锁存(set=true 且档=""),否则每回合重吃 low 400
	// ① run.effort 归一后为空 → low 被拒直接跳无 effort(calls 只两档 low、"")
	// ② run.effort 归一后恰为 low → 同①(去重跳过,不重复试 low)
	// ③ low 与 run.effort 双拒 → 无 effort 终试成功
	// ④ 三档全拒 → 返回错误(Warn 放弃路径),calls 记满 3 次
}
```
四分支各写成独立子测试(逐个 resetLatch + 独立 seam);④ 断言 `err != nil` 且 seam 恰被调 3 次。

Run: `go build ./... && go test ./app/http/services -run 'TestAgentCoreMemoryExtract' -count=1 -v` + `go test ./app/agentcore -run TestEffort -count=1`(确保内核侧无扰)。
Expected: 全 PASS。

- [ ] **Step 5: Commit**

```bash
git add app/agentcore/loop.go app/http/services/agent_ai.go app/http/services/agent_ai_agentcore_memory.go app/http/services/agent_ai_agentcore_memory_test.go
git commit -m "新增:记忆提取 LLM 调用——同端点同模型 effort=low 被拒降当前对话档再拒裸奔,进程锁存;内核导出 IsEffortRejection 复用判定"
```

---

### Task 4: ops 解析应用 + 提取管线 + run 收尾钩子

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_memory.go`(ops/管线/钩子)
- Modify: `app/http/services/agent_ai_agentcore.go`(成功分支钩子 + prompt 注入点占位)
- Test: `app/http/services/agent_ai_agentcore_memory_test.go`

- [ ] **Step 1: 写失败测试**

```go
// agentCoreMemoryOp 类型定义在主文件(Task 4 Step 2);测试直接引用同包类型。
func TestAgentCoreMemoryApplyOps(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	_, _ = agentCoreMemory.addManual("/p/d", "旧事实")
	ops := []agentCoreMemoryOp{
		{Op: "update", ID: 1, Content: "旧事实已被纠正"},
		{Op: "add", Content: "新事实"},
		{Op: "add", Content: strings.Repeat("长", 501)}, // 超限丢弃该条
		{Op: "bogus"},                                  // 非法 op 丢弃
	}
	agentCoreMemory.applyOps("/p/d", ops)
	got := agentCoreMemory.list("/p/d")
	if len(got) != 2 {
		t.Fatalf("应剩 2 条(超限丢弃+非法丢弃): %+v", got)
	}
	// 断言按内容查,不按序(平局 ID 降序的排序规则下新 add 可能排前)
	byContent := map[string]agentCoreMemoryEntry{}
	for _, e := range got {
		byContent[e.Content] = e
	}
	upd, ok := byContent["旧事实已被纠正"]
	if !ok || upd.Source != "manual" || upd.ID != 1 {
		t.Fatalf("update 保 source 与 id: %+v", upd)
	}
	if _, ok := byContent["新事实"]; !ok {
		t.Fatal("add 丢失")
	}
}

func TestAgentCoreMemoryExtractPipelineSkips(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	m := &agentAIManager{}
	called := make(chan struct{}, 1)
	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		called <- struct{}{}
		return `{"ops":[]}`, "end_turn", nil
	}
	m.agentCoreMemoryExtractAsync("/p/e", "m-x", "", "   ", "   ") // 全空白回合:跳过
	select {
	case <-called:
		t.Fatal("空白回合不得发起提取调用")
	default:
	}
	m.agentCoreMemoryExtractAsync("/p/e", "m-x", "", "用户消息", "助手结论")
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("正常回合应发起提取")
	}
	// 等异步落盘(管线在 goroutine 里);超时显式 Fatal,禁止静默通过
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(agentCoreMemory.dirFn(), "_p_e.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("正常回合提取未落盘(2s 超时)")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentCoreMemoryExtractFailuresWarnOnly(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	m := &agentAIManager{}
	// ① LLM 报错:不 panic、不落盘(Warn 放弃)
	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		return "", "", fmt.Errorf("boom")
	}
	m.agentCoreMemoryExtractAsync("/p/f1", "m-x", "", "u", "a")
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(agentCoreMemory.dirFn(), "_p_f1.json")); !os.IsNotExist(err) {
		t.Fatal("LLM 失败不得落盘")
	}
	// ② 非法 JSON:同上
	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		return "不是json", "end_turn", nil
	}
	m.agentCoreMemoryExtractAsync("/p/f2", "m-x", "", "u", "a")
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(agentCoreMemory.dirFn(), "_p_f2.json")); !os.IsNotExist(err) {
		t.Fatal("非法 JSON 不得落盘")
	}
}
```

- [ ] **Step 2: 实现 ops/管线/钩子**

```go
// agentCoreMemoryOp 提取 ops 契约(提取 prompt 输出):
// {"ops":[{"op":"add","content":"…"},{"op":"update","id":7,"content":"…"}]}
type agentCoreMemoryOp struct {
	Op      string `json:"op"`
	ID      int    `json:"id,omitempty"`
	Content string `json:"content"`
}
```
(类型定义放主文件,测试引用同包类型即可。)

```go
// applyOps 提取产物落地(spec §5):update 改 content+刷新 last_hit_at;
// add 追加;单条超 500 字符丢弃该条+Warn(最小损失面,不截断防残句误导);
// 非法 op 忽略。锁内一次往返。
func (s *agentCoreMemoryStore) applyOps(projectPath string, ops []agentCoreMemoryOp) {
	dir := s.dirFn()
	slug := agentCoreMemoryProjectSlug(projectPath)
	if dir == "" || slug == "" || len(ops) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.loadLocked(dir, slug)
	changed := false
	for _, op := range ops {
		content := strings.TrimSpace(op.Content)
		switch op.Op {
		case "update":
			if content == "" || op.ID <= 0 {
				continue
			}
			if len([]rune(content)) > agentCoreMemoryContentCap {
				logger.Warn(fmt.Sprintf("agentcore-memory: 提取 update 超限丢弃 id=%d len=%d", op.ID, len([]rune(content))))
				continue
			}
			for i := range f.Memories {
				if f.Memories[i].ID == op.ID {
					f.Memories[i].Content = content
					f.Memories[i].LastHitAt = time.Now().UTC().Format(time.RFC3339)
					changed = true
					break
				}
			}
		case "add":
			if content == "" {
				continue
			}
			if len([]rune(content)) > agentCoreMemoryContentCap {
				logger.Warn(fmt.Sprintf("agentcore-memory: 提取 add 超限丢弃 len=%d", len([]rune(content))))
				continue
			}
			f.Memories = append(f.Memories, agentCoreMemoryEntry{
				ID: f.NextID, Content: content, Source: "extract",
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
			})
			f.NextID++
			changed = true
		}
	}
	if !changed {
		return
	}
	s.evictLocked(dir, slug, f)
	f.Project = projectPath
	if err := s.saveLocked(dir, slug, f); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-memory: ops 落盘失败 slug=%s err=%v", slug, err))
	}
}

// agentCoreMemoryExtractSystemPrompt 提取规则(system 角色)。禁密钥入记忆是
// 硬规则(宁漏勿泄);既有条目清单按 last_hit_at 降序前 20 条供 LLM 对账。
const agentCoreMemoryExtractSystem = `你是记忆提取器。从这轮对话提取值得跨会话长期记住的项目事实,输出严格 JSON,不要输出任何其他文本。
只提取四类:项目约定、用户偏好、环境事实、踩坑教训。
禁止:密钥/token/密码/内网地址等敏感值宁漏勿泄;一次性任务细节、代码内容、闲聊一律不记。
既有记忆清单(可对账去重:已存在的事实若被纠正/补充,输出 update 带其 id;新增输出 add;无值得记的输出空 ops):
%s
输出格式:{"ops":[{"op":"add","content":"..."},{"op":"update","id":7,"content":"..."}]}`

// agentCoreMemoryEnabledFn 开关 seam(提取钩子与注入共用,Task 5 消费;禁双轨)。
var agentCoreMemoryEnabledFn = func() bool {
	return config.GetGlobalConfig().EffectiveAgentCoreMemoryEnabled()
}

// agentCoreMemoryExtractAsync 收尾钩子(spec §5):开关关/输入空/模型不可用
// 全跳过;ctx 60s 超时;任何失败 Warn 放弃。detached goroutine,run 早已结束。
func (m *agentAIManager) agentCoreMemoryExtractAsync(projectPath, model, runEffort, userText, assistantText string) {
	go func() {
		defer func() { _ = recover() }()
		if !agentCoreMemoryEnabledFn() {
			return
		}
		if agentCoreModelUnusable(model) {
			return // 模型不可用(synthetic/空):提取无从发起,直接放弃
		}
		if strings.TrimSpace(projectPath) == "" || strings.TrimSpace(agentCoreMemory.dirFn()) == "" {
			return
		}
		u, a := truncateRunes(userText, 4000), truncateRunes(assistantText, 4000)
		if strings.TrimSpace(u) == "" && strings.TrimSpace(a) == "" {
			return
		}
		entries := agentCoreMemory.list(projectPath)
		var dedup strings.Builder
		for i, e := range entries {
			if i >= agentCoreMemoryDedupListCap {
				break
			}
			fmt.Fprintf(&dedup, "- [%d] %s\n", e.ID, e.Content)
		}
		if dedup.Len() == 0 {
			dedup.WriteString("(空)")
		}
		system := fmt.Sprintf(agentCoreMemoryExtractSystem, dedup.String())
		user := "本轮用户消息:\n" + u + "\n\n本轮助手结论:\n" + a
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		text, err := m.agentCoreMemoryExtractCall(ctx, model, runEffort, system, user)
		if err != nil {
			logger.Warn(fmt.Sprintf("agentcore-memory: 提取调用失败(放弃本次): %v", err))
			return
		}
		var out struct {
			Ops []agentCoreMemoryOp `json:"ops"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &out); err != nil {
			logger.Warn(fmt.Sprintf("agentcore-memory: 提取输出非合法 JSON(放弃本次) 摘要=%.200q", text))
			return
		}
		agentCoreMemory.applyOps(projectPath, out.Ops)
	}()
}
```

说明:`truncateRunes` 若 services 无现成 helper,就地写一个(`[]rune` 截断)。钩子五参定稿 `(projectPath, model, runEffort, userText, assistantText)`,调用点(Step 3)与之一致;model 不可用早退已在函数体首部。开关判定统一走 `agentCoreMemoryEnabledFn` seam——提取钩子与注入同源,禁双轨。

- [ ] **Step 3: 接 run 收尾钩子**(agent_ai_agentcore.go 成功分支,`endStatus = "success"` 之后、同 `if m.agentAIRunSideEffectsAllowed` 块内)

```go
		// 记忆提取(spec §5):正常成功回合收尾后异步小提取;中断/stopped/
		// 失败/僵尸迟返回不提取(被丢弃的输出没有记忆价值)。detached,
		// 任何失败只 Warn 绝不影响 run(与 journal 同族纪律)。
		m.agentCoreMemoryExtractAsync(run.projectPath, agentCoreModel(run), run.effort,
			agentCoreLastUserText(messages), assistantOutput)
```

`agentCoreLastUserText` 新 helper(放 memory 文件;`messages` 是 :1499 传给 Run 的快照,在钩子作用域内):

```go
// agentCoreLastUserText 取消息快照最后一条 user 的纯文本(提取输入)。
// content 是 string 直取;块数组收集 Text 字段拼接;找不到=空串(钩子跳过)。
func agentCoreLastUserText(messages []agentcore.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		switch c := msg.Content.(type) {
		case string:
			return c
		case []agentcore.ContentBlock:
			var b strings.Builder
			for _, blk := range c {
				b.WriteString(blk.Text)
			}
			return b.String()
		}
	}
	return ""
}
```
(`ContentBlock` 的 Text 字段名以 types.go 实际为准,实施者核对后对齐。)

- [ ] **Step 4: 跑测试**

**结构性豁免(spec §11「stopped-不提取」)**:钩子位于 `agentAIRunSideEffectsAllowed` 成功分支内,stopped/中断/僵尸迟返回路径在更早的 return 处已离开(代码结构保证),不建全链 run 测试——以代码走读+既有 stopped 路径测试间接覆盖。

Run: `go build ./... && go test ./app/http/services -run 'TestAgentCoreMemory' -count=1 -v`
Expected: 全 PASS(含既有全部)。

- [ ] **Step 5: Commit**

```bash
git add app/http/services/agent_ai_agentcore_memory.go app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_memory_test.go
git commit -m "新增:记忆提取管线与收尾钩子——ops 解析应用(超限丢弃)+对账预算20条+成功分支 detached 异步提取"
```

---

### Task 5: 读侧注入 <memory> 块 + 异步 touch

**Files:**
- Modify: `app/http/services/agent_ai_agentcore_memory.go`(prompt block)
- Modify: `app/http/services/agent_ai_agentcore.go`(`agentCoreSystemPromptWithContext` :422,env 块后一行)
- Test: `app/http/services/agent_ai_agentcore_memory_test.go` + 既有 prompt 测试回归

- [ ] **Step 1: 写失败测试**

```go
func TestAgentCoreMemoryPromptBlock(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	_ = agentCoreMemory.addManual("/p/f", "提交信息全中文")
	blk := agentCoreMemoryPromptBlock("/p/f")
	if !strings.Contains(blk, "<memory>") || !strings.Contains(blk, "[1] 提交信息全中文") {
		t.Fatalf("注入块缺失: %q", blk)
	}
	if agentCoreMemoryPromptBlock("/p/none") != "" {
		t.Fatal("空记忆必须零块(prompt 与无记忆逐字节一致)")
	}
	_ = dir
}

func TestAgentCoreMemoryPromptBudget(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	for i := 0; i < 30; i++ {
		_, _ = agentCoreMemory.addManual("/p/g", fmt.Sprintf("条目%03d %s", i, strings.Repeat("内", 300)))
	}
	blk := agentCoreMemoryPromptBlock("/p/g")
	if len([]rune(blk)) > agentCoreMemoryInjectBudgetChars+200 { // 头尾开销余量
		t.Fatalf("超预算: %d", len([]rune(blk)))
	}
	// 新条目(id 大=后写)应保留:块内含最后写入的 id 30,最早的 id 1 被截
	if !strings.Contains(blk, "[30]") {
		t.Fatalf("预算应保最新: %q", blk[:200])
	}
}

// 注入后 touch 生效:last_hit_at 刷新(等异步落盘)
func TestAgentCoreMemoryPromptTouchBumps(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	_, _ = agentCoreMemory.addManual("/p/h", "会命中")
	before := agentCoreMemory.list("/p/h")[0].LastHitAt
	_ = agentCoreMemoryPromptBlock("/p/h")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := agentCoreMemory.list("/p/h")
		if len(got) == 1 && got[0].LastHitAt != before && got[0].LastHitAt != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("注入后 last_hit_at 必须被异步刷新")
}

// 多条排序:recency 降序 + 平局 ID 降序(后写靠前)
func TestAgentCoreMemoryPromptOrder(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	for i := 1; i <= 3; i++ {
		_, _ = agentCoreMemory.addManual("/p/ord", fmt.Sprintf("条目%d", i))
	}
	blk := agentCoreMemoryPromptBlock("/p/ord")
	if strings.Index(blk, "[3]") > strings.Index(blk, "[1]") {
		t.Fatalf("平局应后写靠前(3 在 1 前): %q", blk)
	}
}

// touch 与 DELETE 竞态:已删条目不得被复活
func TestAgentCoreMemoryTouchAfterDelete(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	e, _ := agentCoreMemory.addManual("/p/td", "将删")
	agentCoreMemory.remove("/p/td", e.ID)
	agentCoreMemory.touch("/p/td", []int{e.ID}) // 在途 touch 命中已删 id
	got := agentCoreMemory.list("/p/td")
	if len(got) != 0 {
		t.Fatalf("已删条目被 touch 复活: %+v", got)
	}
}

// 子 agent prompt 同样带块(同一函数链)
func TestAgentCoreSystemPromptCarriesMemory(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	_ = agentCoreMemory.addManual("/p/i", "项目约定X")
	p := agentCoreSystemPrompt("/p/i")
	if !strings.Contains(p, "项目约定X") {
		t.Fatal("子 agent 路径(agentCoreSystemPrompt)也必须注入记忆")
	}
}

// 开关关 = 零提取零注入(seam 替换,双覆盖)
func TestAgentCoreMemoryDisabledKillsBoth(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	prev := agentCoreMemoryEnabledFn
	agentCoreMemoryEnabledFn = func() bool { return false }
	t.Cleanup(func() { agentCoreMemoryEnabledFn = prev })
	if agentCoreMemoryPromptBlock("/p/off") != "" {
		t.Fatal("开关关=注入零块")
	}
	m := &agentAIManager{}
	called := make(chan struct{}, 1)
	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		called <- struct{}{}
		return `{"ops":[]}`, "end_turn", nil
	}
	m.agentCoreMemoryExtractAsync("/p/off", "m-x", "", "u", "a")
	select {
	case <-called:
		t.Fatal("开关关=不得发起提取调用")
	case <-time.After(300 * time.Millisecond):
	}
}
```

(开关测试:seam 变量替换——`prev := agentCoreMemoryEnabledFn; agentCoreMemoryEnabledFn = func() bool { return false }; t.Cleanup(还原)`。断言两件事:①`agentCoreMemoryPromptBlock` 返回空串;②fake seam 不被调用(channel 空跑 2s)。提取钩子与注入共用同一 seam,一处替换双覆盖。)

- [ ] **Step 2: 实现 prompt block**

```go
// agentCoreMemoryPromptBlock 读侧注入(spec §6):空记忆/开关关/无 home=空串
// (prompt 与无记忆逐字节一致);全量 last_hit_at 降序,6000 字符预算截最旧;
// 注入即异步 touch 命中(锁内逐 id 校验防 DELETE 竞态复活)。
func agentCoreMemoryPromptBlock(projectPath string) string {
	if !agentCoreMemoryEnabledFn() {
		return ""
	}
	entries := agentCoreMemory.list(projectPath)
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<memory>\n项目长期记忆(此前会话沉淀的事实,供参考;与当前对话冲突时以当前对话为准):\n")
	budget := agentCoreMemoryInjectBudgetChars
	var hit []int
	for _, e := range entries {
		line := fmt.Sprintf("- [%d] %s\n", e.ID, e.Content)
		if budget-len([]rune(line)) < 0 {
			break
		}
		budget -= len([]rune(line))
		b.WriteString(line)
		hit = append(hit, e.ID)
	}
	b.WriteString("</memory>\n\n")
	if len(hit) > 0 {
		go agentCoreMemory.touch(projectPath, hit)
	}
	return b.String()
}

// agentCoreMemoryEnabledFn 已在 Task 4 定义(提取钩子与注入共用同一 seam)。

```

接线(agent_ai_agentcore.go `agentCoreSystemPromptWithContext`,env 块 `</env>\n\n` 之后):

```go
	// 项目记忆(spec §6):全量注入既有长期事实;空记忆=空串,prompt 逐字节
	// 与从前一致。主 run 与子 agent 同享(同一函数链)。
	b.WriteString(agentCoreMemoryPromptBlock(projectPath))
```

- [ ] **Step 3: 跑测试 + 既有 prompt 测试回归**

**既有 prompt 回归的环境隔离(N9)**:在 `agent_ai_agentcore_memory_test.go` 加包级 TestMain(services 包唯一),全程把 `agentCoreMemory.dirFn` 指向一个 TempDir——防止开发者本机 `~/.aliang/memory` 恰有同名项目记忆时,prompt 回归测试被 `<memory>` 块打破「逐字节一致」断言:

```go
func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "agentcore-memory-test")
	agentCoreMemory.dirFn = func() string { return dir }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
```
(既有各测试文件里的 `swapAgentCoreMemoryDirForTest` 仍可局部覆盖,不冲突。)

Run: `go build ./... && go test ./app/http/services -run 'TestAgentCoreMemory|TestAgentCoreSystemPrompt' -count=1 -v`
Expected: 新测试全 PASS + 既有 prompt 测试不破。

- [ ] **Step 4: Commit**

```bash
git add app/http/services/agent_ai_agentcore_memory.go app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_memory_test.go
git commit -m "新增:记忆读侧注入——system prompt <memory> 块全量注入 6000 预算截最旧,注入即异步 touch 防复活"
```

---

### Task 6: API CRUD + projects + 双注册

**Files:**
- Create: `app/http/handlers/agent_memory_handler.go`
- Modify: `app/http/routes/routes.go`(:320 附近 MCP 注册表之后)
- Modify: `app/agentruntime/server.go`(:177 附近同款)
- Test: `app/http/handlers/agent_memory_handler_test.go` + services 层 store 已测

- [ ] **Step 1: handler**(无状态构造,沿 `NewSSHKnownHostsHandlerFromCache` 先例:两进程各持实例等价)

```go
// agent_memory_handler.go — agentcore 记忆 CRUD API。
// 双注册纪律:core routes.go + agentruntime server.go 缺任一侧=404(老坑)。
// 无状态:store 是 services 包级单例,两进程各持 handler 等价。
type AgentMemoryHandler struct{}

// handler 文件 import services 包(门面入口;无 import 环,仓内先例同)。

var _ = services.AgentCoreMemory // import 保证(实施时按实际引用自然满足)

func NewAgentMemoryHandler() *AgentMemoryHandler { return &AgentMemoryHandler{} }

func memoryProjectFromQuery(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("project"))
}

// HandleMemoryList GET /api/agent/memory?project= 列表;无 project=400。
func (h *AgentMemoryHandler) HandleMemoryList(w http.ResponseWriter, r *http.Request) {
	project := memoryProjectFromQuery(r)
	if project == "" {
		writeAgentMemoryError(w, http.StatusBadRequest, "project 必填")
		return
	}
	writeAgentMemoryJSON(w, map[string]interface{}{"memories": services.AgentCoreMemory.List(project)})
}

// HandleMemoryProjects GET /api/agent/memory/projects 项目清单(卡片下拉)。
func (h *AgentMemoryHandler) HandleMemoryProjects(w http.ResponseWriter, r *http.Request) {
	writeAgentMemoryJSON(w, map[string]interface{}{"projects": services.AgentCoreMemory.Projects()})
}

// HandleMemoryPost POST /api/agent/memory {project, content} 手动增。
func (h *AgentMemoryHandler) HandleMemoryPost(w http.ResponseWriter, r *http.Request) {
	var body struct{ Project, Content string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAgentMemoryError(w, http.StatusBadRequest, "请求体非法")
		return
	}
	if strings.TrimSpace(body.Project) == "" {
		writeAgentMemoryError(w, http.StatusBadRequest, "project 必填")
		return
	}
	e, err := services.AgentCoreMemory.Add(body.Project, body.Content)
	if err != nil {
		writeAgentMemoryError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAgentMemoryJSON(w, map[string]interface{}{"entry": e})
}

// HandleMemoryPut PUT /api/agent/memory/{id} {project, content}。
func (h *AgentMemoryHandler) HandleMemoryPut(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeAgentMemoryError(w, http.StatusBadRequest, "id 非法")
		return
	}
	var body struct{ Project, Content string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAgentMemoryError(w, http.StatusBadRequest, "请求体非法")
		return
	}
	if strings.TrimSpace(body.Project) == "" {
		writeAgentMemoryError(w, http.StatusBadRequest, "project 必填")
		return
	}
	if err := services.AgentCoreMemory.Update(body.Project, id, body.Content); err != nil {
		writeAgentMemoryError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeAgentMemoryJSON(w, map[string]interface{}{"ok": true})
}

// HandleMemoryDelete DELETE:带 id 路径=删单条,无=清空项目。
func (h *AgentMemoryHandler) HandleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	project := memoryProjectFromQuery(r)
	if project == "" {
		writeAgentMemoryError(w, http.StatusBadRequest, "project 必填")
		return
	}
	if raw := r.PathValue("id"); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			writeAgentMemoryError(w, http.StatusBadRequest, "id 非法")
			return
		}
		if err := services.AgentCoreMemory.Remove(project, id); err != nil {
			writeAgentMemoryError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else if err := services.AgentCoreMemory.Clear(project); err != nil {
		writeAgentMemoryError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAgentMemoryJSON(w, map[string]interface{}{"ok": true})
}

func writeAgentMemoryJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeAgentMemoryError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
```

(JSON 解析严格性:decoder 默认容错未知字段,这里可接受;`json.NewDecoder(...).Decode` 后可加 `DisallowUnknownFields` 与否沿仓内 handler 现状,实施者对齐 `agent_mcp_registry_handler.go` 的既有口径。)

- [ ] **Step 2: 双注册**

routes.go(:330 MCP 注册表之后)。**routes.go 的 `register` helper(:110-113)是裸 pattern 透传(methods 只进 docs catalog)——同一裸 pattern 注册两次=ServeMux panic,六行全部用方法前缀形态**(先例 :320-322 mcp servers)。聚合体是 routes.go:17 的 `Handlers` struct(构造在 `newHandlers`:75),`h.Memory = handlers.NewAgentMemoryHandler()` 挂新字段:

```go
	// agentcore 记忆 CRUD(spec §8)。双注册另一处 agentruntime server.go
	// (缺任一侧=404 老坑)。register 是裸 pattern 透传,必须方法前缀防 panic。
	register("GET /api/agent/memory", h.Memory.HandleMemoryList, http.MethodGet)
	register("DELETE /api/agent/memory", h.Memory.HandleMemoryDelete, http.MethodDelete)
	register("POST /api/agent/memory", h.Memory.HandleMemoryPost, http.MethodPost)
	register("PUT /api/agent/memory/{id}", h.Memory.HandleMemoryPut, http.MethodPut)
	register("DELETE /api/agent/memory/{id}", h.Memory.HandleMemoryDelete, http.MethodDelete)
	register("GET /api/agent/memory/projects", h.Memory.HandleMemoryProjects, http.MethodGet)
```

agentruntime/server.go(:177 同段之后;局部构造沿 :129 先例):

```go
	// agentcore 记忆 CRUD;双注册纪律——core routes.go 同名注册透传,缺任一
	// 侧=404(老坑)。全部方法前缀形态,projects 精确段比 /memory 前缀段更长,
	// Go 1.22 mux 自动最长匹配。
	agentMemoryHandler := handlers.NewAgentMemoryHandler()
	mux.HandleFunc("GET /api/agent/memory", agentMemoryHandler.HandleMemoryList)
	mux.HandleFunc("DELETE /api/agent/memory", agentMemoryHandler.HandleMemoryDelete)
	mux.HandleFunc("POST /api/agent/memory", agentMemoryHandler.HandleMemoryPost)
	mux.HandleFunc("PUT /api/agent/memory/{id}", agentMemoryHandler.HandleMemoryPut)
	mux.HandleFunc("DELETE /api/agent/memory/{id}", agentMemoryHandler.HandleMemoryDelete)
	mux.HandleFunc("GET /api/agent/memory/projects", agentMemoryHandler.HandleMemoryProjects)
```

- [ ] **Step 3: handler 测试**(httptest 直打 handler;store 目录经**导出** `services.AgentCoreMemory.SwapDirForTest(t)` 换 TempDir——handlers 包拿不到 services 未导出 seam,直打真 `agentHome()` 会污染 `~/.aliang/memory`)

覆盖:GET 无 project=400 / POST+GET 往返 / PUT 改 content / DELETE 单条 / DELETE 清空 / content>500=400 / GET projects。实施者写 6-7 个子测试,形态沿 `agent_mcp_registry_handler_test.go`。

- [ ] **Step 4: 双注册冒烟测试**(spec §11;形态沿仓内双注册先例测试)

新增 `TestAgentMemoryRoutesDualRegistration`(handlers 测试文件):分别用 `http.NewServeMux()` 按两处注册代码逐字复刻注册(handler 同实例),httptest 打 `GET /api/agent/memory?project=x` 与 `GET /api/agent/memory/projects` 各得 200——证明两条路由形态(pattern 优先级)都不会被彼此吞掉。若 core 侧 `register` 有 catalog 副作用,该测试只管 mux 行为。

```bash
go build ./... && go test ./app/http/handlers -run 'TestAgentMemory' -count=1 -v && go test ./app/http/services -run 'TestAgentCoreMemory' -count=1
```
Expected: 全 PASS。

- [ ] **Step 5: Commit**

```bash
git add app/http/handlers/agent_memory_handler.go app/http/handlers/agent_memory_handler_test.go app/http/routes/routes.go app/agentruntime/server.go
git commit -m "新增:记忆 CRUD API 六端点——GET list/projects POST PUT DELETE 单条/清空,core+runtime 双注册"
```

---

### Task 7: 前端记忆卡片 + i18n

**Files:**
- Create: `app/website/src/components/settings/AgentMemorySettings.vue`
- Create: `app/website/src/components/settings/AgentMemorySettings.test.js`
- Modify: `app/website/src/components/settings/agent/AgentPage.vue`(aliangcode tab 模板 + import)
- Modify: `app/website/src/utils/agentCoreSettings.js`(**normalizeCustomerConfig allowlist**:`normalizeAgentCoreTuning` 必须透传 `memory_enabled` 三态布尔——漏挂则保存 HTTP 200 但永不落盘、刷新恒显默认开,2026-10-31 mcp_enabled 验收缺陷同族;同文件对应 test.js 期望值更新)
- Modify: `SettingsPage` 的 `normalizeCustomerConfig` 挂键第二处(agentCoreSettings.js 内 peer_mcp 注释点名的位置,grep `peer_mcp` 定位)
- Modify: i18n 文案文件(grep `mcp_registry` 或 AgentMCPRegistrySettings 用到的 key 定位)

- [ ] **Step 1: 组件**(script setup 完整逻辑;template 沿 AgentMCPRegistrySettings 的卡片骨架,实施者打开该文件对齐 class 与结构)

要点:
1. props:`config / loading / saving / error / successMessage`(与 PeerAccessSettings 同款契约)+ `emit('save', payload)` 两段式保存开关;
2. 开关段:三态 select(nil=跟随默认开/true/false),初值从 `config?.customer?.agentcore?.memory_enabled` 取,变更时 emit save(payload 只含变更字段,沿 PeerMCPSettings 的两段式写法);文案「记忆总开关(默认开启)」;
3. 项目下拉:`GET /api/agent/memory/projects` 拉清单(count 展示);
4. 条目列表:`GET /api/agent/memory?project=` 渲染(content/时间/来源徽标「提取」/「手动」);
5. 增:textarea(500 字符上限,前端同步计数);改:行内编辑;删:单条+清空(清空需 confirm)——交互先例沿 AgentMCPRegistrySettings;
6. **normalize allowlist 透传**(BLOCKER 级,漏挂=开关永不落盘):`app/website/src/utils/agentCoreSettings.js` 的 `normalizeAgentCoreTuning` 按 `mcp_enabled/skills_enabled` 同款写法透传 `memory_enabled`(三态:null/true/false),其 test.js 期望值加该键;SettingsPage 的 `normalizeCustomerConfig` 第二挂键点同补。保存链两处都过,开关才算接通;
7. 所有文案走 `t('...')` i18n key,新增 key 加进与 MCP 注册表卡片相同的世界区,**文案口径:全角标点/「」/「你」/token 小写**。

- [ ] **Step 2: vitest**(沿 AgentMCPRegistrySettings.test.js 形态:mock fetch)

覆盖:卡片渲染条目 / 开关三态初值与 emit save payload / 删除确认弹层出现 / 500 字符计数禁写。4-5 个用例。

- [ ] **Step 3: AgentPage 接线**

aliangcode tab(登录占位块之后的卡片序列)加:

```vue
<AgentMemorySettings
  :config="config"
  :loading="loading"
  :saving="saving"
  :error="error"
  :success-message="successMessage"
  @save="emitSave"
/>
```
(import + components 注册同步;与 AgentCoreSettings/AgentMCPGlobalSettings 同段。)

- [ ] **Step 4: 跑前端测试 + 构建**

```bash
cd app/website && npx vitest run src/components/settings/AgentMemorySettings.test.js && npm run build
```
Expected: 测试 PASS、build 成功。

- [ ] **Step 5: Commit**

```bash
git add app/website/src/
git commit -m "新增:仪表盘记忆卡片——项目下拉+条目增改删+三态总开关;normalizeAgentCoreTuning 透传 memory_enabled(漏挂=永不落盘)"
```

(整目录 add:本任务触及 components/settings/、SettingsPage.vue、utils/agentCoreSettings.js(+test)、i18n;若 src/ 下有无关脏文件,先 `git status` 核对再收窄。)

---

### Task 8: 全量验证

**Files:** 无新改动。

- [ ] **Step 1: 全包后端**

```bash
go build ./... && go vet ./app/... && go test ./app/http/services ./app/http/handlers ./app/agentcore ./processor/config -count=1
```
Expected: build/vet 零输出,四包全 PASS。预存失败(auth/agent_ai 子进程超时族)单包复跑确认非回归后记录,不顺手修。

- [ ] **Step 2: 关键回归三连**

```bash
go test ./app/http/services -run '^TestAgentCoreMemory|^TestAgentCoreSystemPrompt|^TestAgentCoreMCP' -count=1 -v
```
Expected: 全 PASS(空记忆零块=既有 prompt 测试逐字节不破)。

- [ ] **Step 3: 前端**

```bash
cd app/website && npx vitest run src/components/settings/ && npm run build
```

- [ ] **Step 4: 冒烟口径(部署前,手工)**

配一个 echo 型项目跑 agentcore 会话 → 提一句「记住:本项目用 pnpm」→ 下个新会话问「这个项目用什么包管理器」应答对;`~/.aliang/memory/<slug>.json` 出现条目;仪表盘卡片可见可删;`memory_enabled=false` 后新会话 prompt 无 `<memory>` 块(抓内核请求体或日志验证)。

---

## 计划外备忘

- spec §5 说「提取模型独立可配留 v2 口子」——本计划不实现 model 字段,仅 effort 回退链。
- `agentCoreLastUserText` 的块数组分支:实施者核对 `agentcore.ContentBlock` 实际形状(types.go),Text 字段名可能不同。
- Task 6 的 `register` helper 与 handlers 聚合结构字段名,以 routes.go / handlers 实际为准,对齐 MCP 注册表的挂接写法。
- spec §11「双注册冒烟」落在 Task 6 测试:core 侧 routes 注册形态 + agentruntime mux 注册形态分别 httptest 或起 server 打一条。
