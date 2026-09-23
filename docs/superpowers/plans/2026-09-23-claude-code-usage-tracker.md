# Claude Code 用量感知（usage tracker）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** agent 常驻增量解析 Claude Code 本地会话 JSONL，按（本地小时 × 模型）聚合 token/请求数，经现有 WS 通道 `usage.report` 上报服务端入库，作为后台行为材料（coding 力度 / 宠物活跃度）。

**Architecture:** 新包 `processor/usage`（解析器纯函数 / GORM sqlite 存储 / tracker 增量扫描 / reporter 推送），由 `app/http/services` 接线到 user-agent 进程生命周期与 WS registered 钩子。服务端（nursor/aliang-phone-agent-server）只做一张表 + upsert，本计划交付交接文档。

**Tech Stack:** Go 1.x（仓库现状）、GORM + sqlite（`gorm.io/driver/sqlite`，cgo 强制启用）、gorilla/websocket（现有连接）、内部 `common/cache.GetUnifiedDataDBPath`（统一本地库 `aliang.data`）。

**Spec:** `docs/superpowers/specs/2026-09-23-claude-code-usage-awareness-design.md`（已批准）

---

## 前置约束（执行者必读）

1. **worktree 工作流**：所有开发在独立 worktree 分支进行，不直接改 master 工作区（见 Task 0）。
2. **提交规范**（用户 CLAUDE.md）：标题全中文「新增：/修复：/…」；多行用 HEREDOC；尾行固定为：
   ```
   🤖 Generated with [Claude Code](https://claude.com/claude-code)

   Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
   ```
   后文各任务提交步骤只写标题，尾行一律按此模板附加。
3. **测试**：逐包运行 `go test ./processor/usage/... -count=1`；全仓测试有预存 flake（agent_ai 子进程超时，见仓库记忆），不要跑全仓 `go test ./...` 当回归门槛，用 `go build ./...` + 相关包测试。
4. **PhoneServer 不在本机**：服务端工作项以交接文档交付（Task 8），不在本仓库实现。
5. **既有事件 `ai.usage`**（`app/http/models/agent_protocol.go:69`）是平台远程 AI run 的逐轮用量上报，与本功能的 `usage.report`（本地 JSONL 小时聚合）用途不同、互不影响。不要改动 `ai.usage`。
6. WS 发送是 fire-and-forget；服务端按 `(device_id, hour_start, model)` upsert 整行覆盖，因此重复推送天然幂等——客户端不需要 ACK 等待逻辑。
7. **任务顺序有依赖**：Task 5（配置开关）必须先于 Task 6（接线），接线代码引用 `cfg.Core.UsageTracker`。

## 文件结构

| 文件 | 新建/修改 | 职责 |
|---|---|---|
| `processor/usage/parser.go` | 新建 | JSONL 行 → `UsageSample`（白名单字段，无 content） |
| `processor/usage/parser_test.go` | 新建 | 解析器单测（fixture 驱动） |
| `processor/usage/store.go` | 新建 | GORM 模型 + 打开统一库 + 水位/桶读写 |
| `processor/usage/store_test.go` | 新建 | 存储单测（临时目录建库） |
| `processor/usage/tracker.go` | 新建 | 文件发现、水位增量读、分桶聚合、时区桶 |
| `processor/usage/tracker_test.go` | 新建 | 采集/聚合/健壮性单测 |
| `processor/usage/reporter.go` | 新建 | 桶 → `usage.report` 批次推送、dirty 管理 |
| `processor/usage/reporter_test.go` | 新建 | 推送/分批/失败保 dirty 单测 |
| `processor/config/types.go` | 修改 | `CoreConfig.UsageTracker` 开关（默认开） |
| `app/http/models/agent_protocol.go` | 修改 | 新增 `AgentEventUsageReport` 常量 + ClientSends 注册表条目 |
| `app/http/services/usage_tracker_wiring.go` | 新建 | 组装 tracker/reporter，user-agent 生命周期接线 |
| `app/agentruntime/server.go` | 修改 | StartLocalServer/StopLocalServer 启停接线 |
| `app/http/services/agent_remote_ws.go` | 修改 | registered 钩子处触发连接后全量补推 |
| `docs/superpowers/specs/2026-09-23-usage-report-server-handoff.md` | 新建 | PhoneServer 侧契约交接文档 |

---

### Task 0: 创建 worktree 分支

- [ ] **Step 1: 创建 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add ../alianggate-usage-tracker -b feat/usage-tracker
cd ../alianggate-usage-tracker
```

预期：新目录 `alianggate-usage-tracker` 检出到新分支 `feat/usage-tracker`。后续所有 Task 在该目录进行。

---

### Task 1: 解析器（processor/usage/parser.go）

**Files:**
- Create: `processor/usage/parser.go`
- Create: `processor/usage/parser_test.go`

- [ ] **Step 1: 写失败测试**

创建 `processor/usage/parser_test.go`：

```go
package usage

import (
	"testing"
	"time"
)

// 真实 Claude Code assistant 行（字段裁剪自真实会话文件，content 已移除——
// 解析器本就不应触碰它）
const assistantLine = `{"parentUuid":null,"isSidechain":false,"userType":"external","cwd":"/tmp/proj","sessionId":"8f0e2c1a-1111-2222-3333-444455556666","version":"2.0.1","type":"assistant","message":{"id":"msg_01ABC","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"(内容不进入解析)"}],"usage":{"input_tokens":4,"cache_creation_input_tokens":5036,"cache_read_input_tokens":12000,"output_tokens":168}},"requestId":"req_01","uuid":"0b9e8f2a-aaaa-bbbb-cccc-dddddddddddd","timestamp":"2026-09-23T10:05:29.545Z"}`

func TestParseLineAssistantWithUsage(t *testing.T) {
	sample, err := ParseLine([]byte(assistantLine))
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if sample == nil {
		t.Fatal("expected sample, got nil")
	}
	if sample.SessionID != "8f0e2c1a-1111-2222-3333-444455556666" {
		t.Errorf("SessionID = %q", sample.SessionID)
	}
	if sample.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("Model = %q", sample.Model)
	}
	if sample.InputTokens != 4 || sample.OutputTokens != 168 {
		t.Errorf("tokens = %d/%d", sample.InputTokens, sample.OutputTokens)
	}
	if sample.CacheReadTokens != 12000 || sample.CacheCreationTokens != 5036 {
		t.Errorf("cache = read %d / creation %d", sample.CacheReadTokens, sample.CacheCreationTokens)
	}
	want := time.Date(2026, 9, 23, 10, 5, 29, 545000000, time.UTC)
	if !sample.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", sample.Timestamp, want)
	}
}

func TestParseLineSkips(t *testing.T) {
	cases := map[string]string{
		"user line":          `{"type":"user","sessionId":"s","uuid":"u","message":{"role":"user","content":"hi"}}`,
		"assistant no usage": `{"type":"assistant","sessionId":"s","uuid":"u","timestamp":"2026-09-23T10:05:29.545Z","message":{"model":"m","role":"assistant"}}`,
		"summary line":       `{"type":"summary","summary":"标题","leafUuid":"x"}`,
		"all zero usage":     `{"type":"assistant","sessionId":"s","uuid":"u","timestamp":"2026-09-23T10:05:29.545Z","message":{"model":"m","usage":{"input_tokens":0,"output_tokens":0}}}`,
	}
	for name, line := range cases {
		sample, err := ParseLine([]byte(line))
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
		if sample != nil {
			t.Errorf("%s: expected nil sample, got %+v", name, sample)
		}
	}
}

func TestParseLineSidechainCounted(t *testing.T) {
	line := `{"type":"assistant","isSidechain":true,"sessionId":"s","uuid":"u","timestamp":"2026-09-23T10:05:29.545Z","message":{"model":"m","usage":{"input_tokens":10,"output_tokens":5}}}`
	sample, err := ParseLine([]byte(line))
	if err != nil || sample == nil {
		t.Fatalf("sidechain line must count, got sample=%v err=%v", sample, err)
	}
}

func TestParseLineMalformed(t *testing.T) {
	if _, err := ParseLine([]byte(`{"type":"assistant",`)); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./processor/usage/ -count=1 -v`
Expected: 编译失败（`ParseLine` 未定义）。

- [ ] **Step 3: 实现解析器**

创建 `processor/usage/parser.go`：

```go
// Package usage 感知用户机器上 Claude Code 的会话用量（token/请求次数），
// 聚合为小时粒度桶上报后台，作为 coding 力度 / 宠物活跃度材料。
// 隐私边界：解析器只反序列化白名单元数据字段，对话内容从类型层面不可达。
package usage

import (
	"encoding/json"
	"fmt"
	"time"
)

// UsageSample 是一条携带用量的 assistant 消息（≈ 一次上游 API 响应）。
type UsageSample struct {
	SessionID   string
	MessageUUID string
	Timestamp   time.Time
	Model       string
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheCreationTokens int64
}

// jsonlLine 是 Claude Code 会话 JSONL 行的白名单视图。结构体刻意不声明
// message.content 等内容字段——未知字段被 encoding/json 忽略，内容永远
// 不会进入本包。
type jsonlLine struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	UUID        string `json:"uuid"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	Message     *struct {
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseLine 解析一行 JSONL。返回 (nil, nil) 表示该行不携带用量（user 行、
// 无 usage 的 assistant 行、summary 行、全零 usage），调用方直接跳过；
// (nil, err) 表示行损坏，调用方同样跳过但可计数。
func ParseLine(line []byte) (*UsageSample, error) {
	var raw jsonlLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, fmt.Errorf("usage: malformed jsonl line: %w", err)
	}
	if raw.Type != "assistant" || raw.Message == nil || raw.Message.Usage == nil {
		return nil, nil
	}
	u := raw.Message.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
		return nil, nil
	}
	ts := time.Now().UTC()
	if raw.Timestamp != "" {
		parsed, err := time.Parse(time.RFC3339, raw.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("usage: bad timestamp %q: %w", raw.Timestamp, err)
		}
		ts = parsed
	}
	return &UsageSample{
		SessionID:   raw.SessionID,
		MessageUUID: raw.UUID,
		Timestamp:   ts,
		Model:       raw.Message.Model,
		InputTokens:        u.InputTokens,
		OutputTokens:       u.OutputTokens,
		CacheReadTokens:    u.CacheReadInputTokens,
		CacheCreationTokens: u.CacheCreationInputTokens,
	}, nil
}
```

说明：`time.Parse(time.RFC3339, ...)` 可解析带毫秒的 RFC3339（Go 标准库支持可选小数秒）。`isSidechain` 无需特判——sidechain assistant 行同样计入。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./processor/usage/ -count=1 -v`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add processor/usage/parser.go processor/usage/parser_test.go
git commit -m "新增:用量感知解析器-白名单解析Claude-Code会话JSONL"
```

---

### Task 2: 存储（processor/usage/store.go）

**Files:**
- Create: `processor/usage/store.go`
- Create: `processor/usage/store_test.go`

- [ ] **Step 1: 写失败测试**

创建 `processor/usage/store_test.go`：

```go
package usage

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStoreAt(filepath.Join(t.TempDir(), "usage.data"))
	if err != nil {
		t.Fatalf("OpenStoreAt: %v", err)
	}
	return store
}

func TestWatermarkCRUD(t *testing.T) {
	store := openTestStore(t)
	if got, _ := store.GetWatermark("/a/b.jsonl"); got != 0 {
		t.Fatalf("fresh watermark = %d, want 0", got)
	}
	if err := store.SetWatermark("/a/b.jsonl", 1234); err != nil {
		t.Fatalf("SetWatermark: %v", err)
	}
	if got, _ := store.GetWatermark("/a/b.jsonl"); got != 1234 {
		t.Fatalf("watermark = %d, want 1234", got)
	}
	if err := store.SetWatermark("/a/b.jsonl", 0); err != nil {
		t.Fatalf("SetWatermark reset: %v", err)
	}
	if got, _ := store.GetWatermark("/a/b.jsonl"); got != 0 {
		t.Fatalf("after reset watermark = %d, want 0", got)
	}
}

func TestBucketAccumulateAndDirty(t *testing.T) {
	store := openTestStore(t)

	b, err := store.Bucket(1727071200, "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("Bucket: %v", err)
	}
	b.InputTokens += 100
	b.Requests++
	if err := store.SaveBucket(b); err != nil {
		t.Fatalf("SaveBucket: %v", err)
	}

	// 同 key 再取应是同一行（累计语义，不是新行）
	b2, _ := store.Bucket(1727071200, "claude-sonnet-4-5")
	if b2.ID != b.ID || b2.InputTokens != 100 || b2.Requests != 1 {
		t.Fatalf("bucket not accumulated: %+v", b2)
	}

	dirty, err := store.DirtyBuckets()
	if err != nil || len(dirty) != 1 {
		t.Fatalf("DirtyBuckets = %v, %v; want 1 row", dirty, err)
	}
	if err := store.ClearDirty([]int64{dirty[0].ID}); err != nil {
		t.Fatalf("ClearDirty: %v", err)
	}
	if dirty, _ = store.DirtyBuckets(); len(dirty) != 0 {
		t.Fatalf("after clear, dirty = %d rows", len(dirty))
	}
}

func TestAllBucketsAndDeleteWatermarks(t *testing.T) {
	store := openTestStore(t)
	for _, key := range [][2]interface{}{{int64(1), "m1"}, {int64(2), "m2"}} {
		b, _ := store.Bucket(key[0].(int64), key[1].(string))
		_ = store.SaveBucket(b)
	}
	all, err := store.AllBuckets()
	if err != nil || len(all) != 2 {
		t.Fatalf("AllBuckets = %d rows, %v; want 2", len(all), err)
	}

	_ = store.SetWatermark("/gone.jsonl", 10)
	if err := store.DeleteWatermarks([]string{"/gone.jsonl"}); err != nil {
		t.Fatalf("DeleteWatermarks: %v", err)
	}
	if got, _ := store.GetWatermark("/gone.jsonl"); got != 0 {
		t.Fatal("watermark not deleted")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./processor/usage/ -count=1`
Expected: 编译失败（`Store`/`OpenStoreAt` 未定义）。

- [ ] **Step 3: 实现存储**

创建 `processor/usage/store.go`：

```go
package usage

import (
	"fmt"
	"time"

	"gorm.io/driver/sqlite" // cgo/mattn 驱动，go.mod 已有（processor/auth 同款）
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"aliang.one/nursorgate/common/cache"
)

// UsageWatermark 记录每个会话 JSONL 已解析到的字节偏移。重启后从断点续读，
// 防止全量重扫造成的数量级重复计数。
type UsageWatermark struct {
	FilePath  string    `gorm:"column:file_path;primaryKey"`
	Offset    int64     `gorm:"column:offset"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (UsageWatermark) TableName() string { return "usage_watermarks" }

// UsageBucket 是（本地小时 × 模型）聚合桶，累计快照语义：整行覆盖式
// 上报，服务端按 (device_id, hour_start, model) upsert，重复推送无副作用。
type UsageBucket struct {
	ID                  int64  `gorm:"primaryKey;autoIncrement"`
	HourStart           int64  `gorm:"column:hour_start;uniqueIndex:idx_usage_hour_model"`
	Model               string `gorm:"column:model;size:128;uniqueIndex:idx_usage_hour_model"`
	Requests            int64  `gorm:"column:requests"`
	InputTokens         int64  `gorm:"column:input_tokens"`
	OutputTokens        int64  `gorm:"column:output_tokens"`
	CacheReadTokens     int64  `gorm:"column:cache_read_tokens"`
	CacheCreationTokens int64  `gorm:"column:cache_creation_tokens"`
	ActiveSessions      int    `gorm:"column:active_sessions"`
	// SessionSet 是该小时内出现过的 sessionId 去重集（JSON 数组），随桶
	// 持久化，保证重启后同会话不重复计数。大小受"该小时活跃会话数"约束。
	SessionSet string `gorm:"column:session_set;type:text"`
	FirstSeen  int64  `gorm:"column:first_seen"`
	LastSeen   int64  `gorm:"column:last_seen"`
	Dirty      bool   `gorm:"column:dirty"`
}

func (UsageBucket) TableName() string { return "usage_buckets" }

// Store 是用量模块的本地 sqlite 存储（统一库 aliang.data，独立连接，
// 与 processor/auth 的连接并存——写频极低，秒级以下）。
type Store struct {
	db *gorm.DB
}

// OpenStoreAt 在指定路径建/开库（测试注入用）。
func OpenStoreAt(dbPath string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("usage: open store: %w", err)
	}
	if err := db.AutoMigrate(&UsageWatermark{}, &UsageBucket{}); err != nil {
		return nil, fmt.Errorf("usage: migrate store: %w", err)
	}
	return &Store{db: db}, nil
}

// OpenDefaultStore 打开生产路径的统一数据库。
func OpenDefaultStore() (*Store, error) {
	path, err := cache.GetUnifiedDataDBPath()
	if err != nil {
		return nil, fmt.Errorf("usage: resolve unified db path: %w", err)
	}
	return OpenStoreAt(path)
}
```

> sqlite 驱动已核实为 `gorm.io/driver/sqlite`（cgo/mattn，本仓库 CGO 强制启用，`processor/auth/user_info.go` 同款用法），上面的 import 直接可用。

在同文件继续追加（或按执行者习惯拆 `store_ops.go`，保持包内小文件即可）：

```go
func (s *Store) GetWatermark(path string) (int64, error) {
	var wm UsageWatermark
	err := s.db.Where("file_path = ?", path).First(&wm).Error
	if err == gorm.ErrRecordNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("usage: get watermark: %w", err)
	}
	return wm.Offset, nil
}

func (s *Store) SetWatermark(path string, offset int64) error {
	return s.db.Save(&UsageWatermark{FilePath: path, Offset: offset, UpdatedAt: time.Now()}).Error
}

func (s *Store) DeleteWatermarks(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return s.db.Where("file_path IN ?", paths).Delete(&UsageWatermark{}).Error
}

// Bucket 取（或创建）聚合桶。返回的桶可修改后 SaveBucket 持久化。
func (s *Store) Bucket(hourStart int64, model string) (*UsageBucket, error) {
	var b UsageBucket
	err := s.db.Where("hour_start = ? AND model = ?", hourStart, model).First(&b).Error
	if err == nil {
		return &b, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("usage: load bucket: %w", err)
	}
	b = UsageBucket{HourStart: hourStart, Model: model}
	if err := s.db.Create(&b).Error; err != nil {
		return nil, fmt.Errorf("usage: create bucket: %w", err)
	}
	return &b, nil
}

func (s *Store) SaveBucket(b *UsageBucket) error {
	return s.db.Save(b).Error
}

func (s *Store) DirtyBuckets() ([]UsageBucket, error) {
	var out []UsageBucket
	err := s.db.Where("dirty = ?", true).Order("hour_start ASC").Find(&out).Error
	return out, err
}

func (s *Store) AllBuckets() ([]UsageBucket, error) {
	var out []UsageBucket
	err := s.db.Order("hour_start ASC").Find(&out).Error
	return out, err
}

func (s *Store) ClearDirty(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.Model(&UsageBucket{}).Where("id IN ?", ids).Update("dirty", false).Error
}

func (s *Store) allWatermarkPaths() ([]string, error) {
	var rows []UsageWatermark
	err := s.db.Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.FilePath)
	}
	return out, nil
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./processor/usage/ -count=1`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add processor/usage/store.go processor/usage/store_test.go
git commit -m "新增:用量感知本地存储-水位与小时桶GORM模型"
```

---

### Task 3: 采集与聚合（processor/usage/tracker.go）

**Files:**
- Create: `processor/usage/tracker.go`
- Create: `processor/usage/tracker_test.go`
- Modify: `processor/usage/store.go`（追加 `allWatermarkPaths`，若 Task 2 未加）

- [ ] **Step 1: 写失败测试**

创建 `processor/usage/tracker_test.go`：

```go
package usage

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newTestTracker(t *testing.T, root string) *Tracker {
	t.Helper()
	store := openTestStore(t)
	return NewTracker(store, []string{root}, func() bool { return true })
}

func jsonlLineAt(ts string, model string, in, out int64, session string) string {
	return `{"type":"assistant","sessionId":"` + session + `","uuid":"u-` + session + `-` + ts + `","timestamp":"` + ts + `","message":{"model":"` + model + `","usage":{"input_tokens":` +
		strconv.FormatInt(in, 10) + `,"output_tokens":` + strconv.FormatInt(out, 10) + `}}}` + "\n"
}

func TestTrackerIncrementalScan(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "proj", "s1.jsonl")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	mustWrite(t, path,
		jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "sess-a")+
			`{"type":"user","sessionId":"sess-a","uuid":"x"}`+"\n"+ // 应跳过
			`{"type":"assistant",`+"\n"+ // 损坏整行：跳过
			jsonlLineAt("2026-09-23T10:30:00.000Z", "m1", 20, 8, "sess-a"))

	tr := newTestTracker(t, root)
	if err := tr.ScanOnce(); err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}

	all, _ := tr.store.AllBuckets()
	if len(all) != 1 {
		t.Fatalf("buckets = %d, want 1", len(all))
	}
	b := all[0]
	if b.InputTokens != 30 || b.OutputTokens != 13 || b.Requests != 2 {
		t.Fatalf("aggregation wrong: %+v", b)
	}
	if b.ActiveSessions != 1 {
		t.Fatalf("active sessions = %d, want 1", b.ActiveSessions)
	}

	// 追加新行：增量读取不重复计数
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(jsonlLineAt("2026-09-23T10:45:00.000Z", "m1", 7, 3, "sess-a"))
	_ = f.Close()
	if err := tr.ScanOnce(); err != nil {
		t.Fatalf("ScanOnce 2: %v", err)
	}
	all, _ = tr.store.AllBuckets()
	if all[0].Requests != 3 || all[0].InputTokens != 37 {
		t.Fatalf("incremental recount wrong: %+v", all[0])
	}
}

func TestTrackerPartialTrailingLineNotConsumed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "s2.jsonl")
	mustWrite(t, path, jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "s")+
		`{"type":"assistant","sessionId":"s","timestamp":"2026-09-23T10:0`) // 半行

	tr := newTestTracker(t, root)
	if err := tr.ScanOnce(); err != nil {
		t.Fatalf("ScanOnce: %v", err)
	}
	all, _ := tr.store.AllBuckets()
	if len(all) != 1 || all[0].Requests != 1 {
		t.Fatalf("want exactly 1 request counted, got %+v", all)
	}

	// 半行补全后下轮可读
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(`:05.000Z","message":{"model":"m1","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")
	_ = f.Close()
	_ = tr.ScanOnce()
	all, _ = tr.store.AllBuckets()
	if all[0].Requests != 2 {
		t.Fatalf("completed line not consumed: %+v", all[0])
	}
}

func TestTrackerTruncatedFileRescans(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "s3.jsonl")
	mustWrite(t, path, jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "s")+
		jsonlLineAt("2026-09-23T10:06:00.000Z", "m1", 10, 5, "s"))
	tr := newTestTracker(t, root)
	_ = tr.ScanOnce()

	// 文件被截断重建：水位归零重扫（可能重复计入，spec 容忍）。
	// 关键断言：重扫后水位重新推进，新数据不漏。
	mustWrite(t, path, jsonlLineAt("2026-09-23T11:00:00.000Z", "m1", 10, 5, "s2"))
	_ = tr.ScanOnce()
	wm, _ := tr.store.GetWatermark(path)
	if wm == 0 {
		t.Fatal("watermark should be re-advanced after truncate rescan")
	}
	all, _ := tr.store.AllBuckets()
	var newHourRequests int64
	for _, b := range all {
		if time.Unix(b.HourStart, 0).Local().Hour() == 11 {
			newHourRequests += b.Requests
		}
	}
	if newHourRequests != 1 {
		t.Fatalf("post-truncate rescan should count new line once, got %d", newHourRequests)
	}
}

func TestTrackerMissingFileWatermarkCleaned(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "gone.jsonl")
	mustWrite(t, path, jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "s"))
	tr := newTestTracker(t, root)
	_ = tr.ScanOnce()
	_ = os.Remove(path)
	_ = tr.ScanOnce() // 完整走完一轮后应清理水位
	if wm, _ := tr.store.GetWatermark(path); wm != 0 {
		t.Fatalf("watermark for deleted file should be removed, got %d", wm)
	}
}

func TestLocalHourBucketAndTZ(t *testing.T) {
	ts, _ := time.Parse(time.RFC3339, "2026-09-23T10:59:59.000Z")
	hour, tz := LocalHourStart(ts)
	if tz == "" {
		t.Fatal("tz name should not be empty")
	}
	local := ts.Local()
	want := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, local.Location()).Unix()
	if hour != want {
		t.Fatalf("hour bucket = %d, want %d", hour, want)
	}
}

func TestTrackerDisabledPredicateSkips(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "s4.jsonl")
	mustWrite(t, path, jsonlLineAt("2026-09-23T10:05:29.545Z", "m1", 10, 5, "s"))
	allowed := false
	store := openTestStore(t)
	tr := NewTracker(store, []string{root}, func() bool { return allowed })
	_ = tr.ScanOnce()
	all, _ := store.AllBuckets()
	if len(all) != 0 {
		t.Fatalf("disabled tracker must not collect, got %+v", all)
	}
	_ = fmt.Sprint() // 保持 fmt 引用（执行者可按需移除）
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./processor/usage/ -count=1`
Expected: 编译失败（`Tracker`/`NewTracker`/`ScanOnce`/`LocalHourStart` 未定义）。

- [ ] **Step 3: 实现 tracker**

创建 `processor/usage/tracker.go`：

```go
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

const (
	// scanBudgetMs 限制单次 ScanOnce 的处理时长：首启回溯大目录时分多轮
	// 完成（水位断点续扫），不阻塞调用方。
	scanBudgetMs = 2000
)

// Tracker 增量扫描 Claude Code 会话 JSONL 并聚合进小时桶。
type Tracker struct {
	store   *Store
	roots   []string
	allowed func() bool
}

func NewTracker(store *Store, roots []string, allowed func() bool) *Tracker {
	return &Tracker{store: store, roots: roots, allowed: allowed}
}

// ScanOnce 执行一轮扫描：发现文件 → 增量读 → 聚合 → 推进水位。
// allowed() 返回 false 时直接跳过（agent 被 disable 时暂停采集）。
func (t *Tracker) ScanOnce() error {
	if t.allowed != nil && !t.allowed() {
		return nil
	}
	deadline := time.Now().Add(scanBudgetMs * time.Millisecond)

	var seen []string
	completePass := true
	for _, root := range t.roots {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // 目录可能随时变动，跳过不可达子树
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			seen = append(seen, path)
			if time.Now().After(deadline) {
				completePass = false
				return fs.SkipAll
			}
			if err := t.processFile(path); err != nil {
				logger.Debug("usage: scan " + path + ": " + err.Error())
			}
			return nil
		})
		if err != nil {
			logger.Debug("usage: walk " + root + ": " + err.Error())
		}
	}
	// 只有完整走完（未超预算中断）才清理消失文件的水位，防止误删尚未
	// 扫到的文件。
	if completePass {
		if missing := t.missingWatermarks(seen); len(missing) > 0 {
			if err := t.store.DeleteWatermarks(missing); err != nil {
				logger.Debug("usage: cleanup watermarks: " + err.Error())
			}
		}
	}
	return nil
}

func (t *Tracker) processFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	offset, err := t.store.GetWatermark(path)
	if err != nil {
		return err
	}
	// 文件被截断/重建（备份恢复等）：水位归零重扫。可能轻微重复计入，
	// 感性统计容忍（spec §7）。
	if info.Size() < offset {
		offset = 0
	}
	if info.Size() == offset {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	consumed := offset
	var samples []*UsageSample
	for {
		line, rerr := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				if sample, perr := ParseLine(trimmed); perr == nil && sample != nil {
					samples = append(samples, sample)
				}
			}
			consumed += int64(len(line))
			if rerr != nil {
				break // ReadBytes 返回 err 时也已读出末个完整行
			}
			continue
		}
		// 半行（文件正在被写）：不消费，水位停在最后完整行
		break
	}

	for _, s := range samples {
		if err := t.append(s); err != nil {
			// sqlite 写失败按 spec §7 记 Warn，下轮水位未推进自动重试
			logger.Warn("usage: bucket append " + path + ": " + err.Error())
			return err
		}
	}
	if consumed > offset {
		if err := t.store.SetWatermark(path, consumed); err != nil {
			logger.Warn("usage: watermark " + path + ": " + err.Error())
			return err
		}
	}
	return nil
}

func (t *Tracker) append(s *UsageSample) error {
	hour, _ := LocalHourStart(s.Timestamp)
	b, err := t.store.Bucket(hour, s.Model)
	if err != nil {
		return err
	}
	set := parseSessionSet(b.SessionSet)
	if _, ok := set[s.SessionID]; !ok {
		set[s.SessionID] = struct{}{}
		b.ActiveSessions++
	}
	b.InputTokens += s.InputTokens
	b.OutputTokens += s.OutputTokens
	b.CacheReadTokens += s.CacheReadTokens
	b.CacheCreationTokens += s.CacheCreationTokens
	b.Requests++
	epoch := s.Timestamp.Unix()
	if b.FirstSeen == 0 || epoch < b.FirstSeen {
		b.FirstSeen = epoch
	}
	if epoch > b.LastSeen {
		b.LastSeen = epoch
	}
	b.Dirty = true
	enc, err := json.Marshal(setKeys(set))
	if err == nil {
		b.SessionSet = string(enc)
	}
	return t.store.SaveBucket(b)
}

// missingWatermarks 返回水位表中存在但本轮扫描未见的文件。
func (t *Tracker) missingWatermarks(seen []string) []string {
	seenSet := make(map[string]struct{}, len(seen))
	for _, p := range seen {
		seenSet[p] = struct{}{}
	}
	var missing []string
	rows, err := t.store.allWatermarkPaths()
	if err != nil {
		return nil
	}
	for _, p := range rows {
		if _, ok := seenSet[p]; !ok {
			missing = append(missing, p)
		}
	}
	return missing
}

func parseSessionSet(raw string) map[string]struct{} {
	set := map[string]struct{}{}
	if raw != "" {
		var keys []string
		if err := json.Unmarshal([]byte(raw), &keys); err == nil {
			for _, k := range keys {
				set[k] = struct{}{}
			}
		}
	}
	return set
}

func setKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	return keys
}

// LocalHourStart 把消息时间落到 agent 本地时区的小时桶起点，并返回时区名。
var (
	tzNameOnce sync.Once
	tzName     string
)

func LocalHourStart(t time.Time) (int64, string) {
	loc := t.Local()
	bucket := time.Date(loc.Year(), loc.Month(), loc.Day(), loc.Hour(), 0, 0, 0, loc.Location())
	return bucket.Unix(), TZName()
}

// TZName 返回本机时区的 IANA 名（尽力而为）：TZ env → /etc/localtime 符号
// 链接中的 zoneinfo 路径 → Location 名兜底。
func TZName() string {
	tzNameOnce.Do(func() {
		tzName = detectTZName()
	})
	return tzName
}

func detectTZName() string {
	if tz := os.Getenv("TZ"); strings.TrimSpace(tz) != "" {
		return strings.TrimSpace(tz)
	}
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if idx := strings.Index(target, "zoneinfo/"); idx >= 0 {
			return target[idx+len("zoneinfo/"):]
		}
	}
	return time.Local.String()
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./processor/usage/ -count=1`
Expected: PASS。若有失败逐个修正（常见坑：ReadBytes EOF 时半行处理）。

- [ ] **Step 5: 提交**

```bash
git add processor/usage/tracker.go processor/usage/tracker_test.go
git commit -m "新增:用量采集tracker-增量水位扫描与本地小时桶聚合"
```

---

### Task 4: 上报器（processor/usage/reporter.go）

**Files:**
- Create: `processor/usage/reporter.go`
- Create: `processor/usage/reporter_test.go`

- [ ] **Step 1: 写失败测试**

创建 `processor/usage/reporter_test.go`：

```go
package usage

import (
	"encoding/json"
	"errors"
	"testing"
)

type fakeWriter struct {
	messages []map[string]interface{}
	failNext bool
}

func (w *fakeWriter) write(payload interface{}) error {
	if w.failNext {
		w.failNext = false
		return errors.New("fake write failure")
	}
	m, _ := payload.(map[string]interface{})
	w.messages = append(w.messages, m)
	return nil
}

func seedBuckets(t *testing.T, store *Store, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		b, _ := store.Bucket(int64(1727071200+i*3600), "claude-sonnet-4-5")
		b.Requests = int64(i + 1)
		b.InputTokens = 100
		b.OutputTokens = 50
		b.CacheReadTokens = 7
		b.CacheCreationTokens = 3
		b.ActiveSessions = 1
		b.SessionSet = `["s1"]`
		b.FirstSeen = int64(1727071200 + i*3600)
		b.LastSeen = int64(1727071500 + i*3600)
		b.Dirty = true
		if err := store.SaveBucket(b); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func TestFlushAllSendsBatchesAndClearsDirty(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 3)
	w := &fakeWriter{}
	r := NewReporter(store, func() string { return "dev-1" }, "Asia/Shanghai")

	if err := r.FlushAll(w.write); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	if len(w.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(w.messages))
	}
	if w.messages[0]["type"] != "usage.report" {
		t.Fatalf("type = %v", w.messages[0]["type"])
	}
	records := w.messages[0]["records"].([]map[string]interface{})
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	rec := records[0]
	if rec["device_id"] != "dev-1" || rec["tz"] != "Asia/Shanghai" || rec["model"] != "claude-sonnet-4-5" {
		t.Fatalf("record fields wrong: %+v", rec)
	}
	if dirty, _ := store.DirtyBuckets(); len(dirty) != 0 {
		t.Fatal("dirty should be cleared after successful flush")
	}
}

func TestFlushBatchesOver500(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 501)
	w := &fakeWriter{}
	r := NewReporter(store, func() string { return "dev-1" }, "UTC")
	if err := r.FlushAll(w.write); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	if len(w.messages) != 2 {
		t.Fatalf("messages = %d, want 2 (500+1)", len(w.messages))
	}
}

func TestFlushFailureKeepsDirty(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 1)
	w := &fakeWriter{failNext: true}
	r := NewReporter(store, func() string { return "dev-1" }, "UTC")
	if err := r.FlushAll(w.write); err == nil {
		t.Fatal("expected error")
	}
	if dirty, _ := store.DirtyBuckets(); len(dirty) != 1 {
		t.Fatal("dirty must survive failed flush")
	}
}

func TestRecordJSONShape(t *testing.T) {
	store := openTestStore(t)
	seedBuckets(t, store, 1)
	all, _ := store.AllBuckets()
	rec := usageRecord("dev-1", all[0], "UTC")
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe struct {
		DeviceID  string  `json:"device_id"`
		HourStart float64 `json:"hour_start"`
		TZ        string  `json:"tz"`
		Model     string  `json:"model"`
		Requests  float64 `json:"requests"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if probe.DeviceID != "dev-1" || probe.Model != "claude-sonnet-4-5" || probe.Requests != 1 {
		t.Fatalf("shape wrong: %+v", probe)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./processor/usage/ -count=1`
Expected: 编译失败（`NewReporter`/`FlushAll`/`usageRecord` 未定义）。

- [ ] **Step 3: 实现上报器**

创建 `processor/usage/reporter.go`：

```go
package usage

import (
	"fmt"

	"aliang.one/nursorgate/common/logger"
)

// reportBatchMax 单批 records 上限（spec §5.1）。
const reportBatchMax = 500

// Reporter 把本地小时桶推送到服务端。推送经现有 WS 通道 fire-and-forget；
// 服务端按 (device_id, hour_start, model) upsert 整行覆盖，重复推送幂等。
type Reporter struct {
	store    *Store
	deviceID func() string
	tz       string
}

func NewReporter(store *Store, deviceID func() string, tz string) *Reporter {
	return &Reporter{store: store, deviceID: deviceID, tz: tz}
}

// FlushDirty 周期推送：只发 dirty 桶。write 为 WS 发送函数（可为 nil = 未连接）。
func (r *Reporter) FlushDirty(write func(interface{}) error) error {
	return r.flush(write, false)
}

// FlushAll 连接建立后全量补推（含历史未确认桶），成功后清空 dirty。
func (r *Reporter) FlushAll(write func(interface{}) error) error {
	return r.flush(write, true)
}

func (r *Reporter) flush(write func(interface{}) error, all bool) error {
	if write == nil {
		return nil
	}
	var (
		buckets []UsageBucket
		err     error
	)
	if all {
		buckets, err = r.store.AllBuckets()
	} else {
		buckets, err = r.store.DirtyBuckets()
	}
	if err != nil {
		return fmt.Errorf("usage: load buckets: %w", err)
	}
	if len(buckets) == 0 {
		return nil
	}

	var pushedIDs []int64
	for start := 0; start < len(buckets); start += reportBatchMax {
		end := start + reportBatchMax
		if end > len(buckets) {
			end = len(buckets)
		}
		batch := buckets[start:end]
		records := make([]map[string]interface{}, 0, len(batch))
		for _, b := range batch {
			records = append(records, usageRecord(r.deviceID(), b, r.tz))
		}
		payload := map[string]interface{}{
			"type":    "usage.report",
			"records": records,
		}
		if err := write(payload); err != nil {
			// 已成功推出的批次清 dirty；失败的批保持 dirty 下轮重推（幂等）
			logger.Debug(fmt.Sprintf("usage: report push failed (%d pushed this round): %v", len(pushedIDs), err))
			if len(pushedIDs) > 0 {
				_ = r.store.ClearDirty(pushedIDs)
			}
			return err
		}
		for _, b := range batch {
			pushedIDs = append(pushedIDs, b.ID)
		}
	}
	return r.store.ClearDirty(pushedIDs)
}

func usageRecord(deviceID string, b UsageBucket, tz string) map[string]interface{} {
	return map[string]interface{}{
		"device_id":             deviceID,
		"hour_start":            b.HourStart,
		"tz":                    tz,
		"model":                 b.Model,
		"requests":              b.Requests,
		"input_tokens":          b.InputTokens,
		"output_tokens":         b.OutputTokens,
		"cache_read_tokens":     b.CacheReadTokens,
		"cache_creation_tokens": b.CacheCreationTokens,
		"active_sessions":       b.ActiveSessions,
		"first_seen":            b.FirstSeen,
		"last_seen":             b.LastSeen,
	}
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./processor/usage/ -count=1`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add processor/usage/reporter.go processor/usage/reporter_test.go
git commit -m "新增:用量上报器-usage.report批次推送与dirty管理"
```

---

### Task 5: 配置开关（core.usage_tracker）

> 本任务必须先于 Task 6：接线代码引用 `cfg.Core.UsageTracker`。

**Files:**
- Modify: `processor/config/types.go`（`CoreConfig` 结构（约 :267）+ 新类型，参照 `HTTP1DropConfig`（:313-320）的 `Enabled *bool` + `IsEnabled()` 惯例）

- [ ] **Step 1: 加类型与字段**

在 `processor/config/types.go` 的 `HTTP1DropConfig` 附近加：

```go
// UsageTrackerConfig 控制本地 Claude Code 用量采集上报（core.usage_tracker）。
// 默认开启；关闭后 tracker 不扫描、reporter 不推送（spec §5.3）。
type UsageTrackerConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

func (c *UsageTrackerConfig) IsEnabled() bool {
	return c == nil || c.Enabled == nil || *c.Enabled
}
```

在 `CoreConfig` 结构加字段：

```go
	UsageTracker *UsageTrackerConfig `json:"usage_tracker,omitempty"`
```

- [ ] **Step 2: 编译 + 现有配置测试回归**

Run: `go build ./... && go test ./processor/config/... -count=1`
Expected: 通过（新字段 omitempty 可选，不破坏既有配置解析）。

- [ ] **Step 3: 提交**

```bash
git add processor/config/types.go
git commit -m "新增:core.usage_tracker配置开关-默认开启可关闭"
```

---

### Task 6: 协议常量 + 接线（services/agentruntime/registered 钩子）

**Files:**
- Modify: `app/http/models/agent_protocol.go`（事件常量区，`AgentEventAIUsage = "ai.usage"` 同段附近 + ClientSends 注册表）
- Create: `app/http/services/usage_tracker_wiring.go`
- Modify: `app/agentruntime/server.go`（`StartLocalServer`（:45）/`StopLocalServer`（:90））
- Modify: `app/http/services/agent_remote_ws.go`（`handleRemoteAgentMessage`（:515）的 `case models.AgentEventRegistered:`（:526）段，`s.ai.emitApprovalSync(writeJSON)`（:547）之后）
- Modify: `app/http/services/agent_service.go`（新增线程安全 `agentEnabled()`——已核实仓库无现成等价方法，仅有需持有 `s.mu` 的 `isEnabledLocked()`（:1092））

- [ ] **Step 1: 新增事件常量 + 注册表条目**

在 `app/http/models/agent_protocol.go` 常量块（`AgentEventAIUsage` 附近）加一行：

```go
	AgentEventUsageReport            = "usage.report"           // agent→cloud: Claude Code 本地会话小时级用量聚合（后台材料，勿与逐轮 ai.usage 混淆）
```

并在同文件 `ClientSends` 注册表（文档契约）按现有格式补一行 `"usage.report"`。

- [ ] **Step 2: 创建接线文件**

创建 `app/http/services/usage_tracker_wiring.go`：

```go
package services

import (
	"path/filepath"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/usage"
)

// usageRuntime 持有 user-agent 进程内的用量采集/上报循环。只在
// IsUserAgentRuntime() 进程启动，保证单进程采集（避免多进程重复计数）。
var usageRuntime struct {
	mu       sync.Mutex
	started  bool
	stop     chan struct{}
	reporter *usage.Reporter
}

// StartUsageTrackerRuntime 启动采集与周期上报（幂等；非 user-agent 进程 no-op）。
func StartUsageTrackerRuntime() {
	if !IsUserAgentRuntime() {
		return
	}
	usageRuntime.mu.Lock()
	if usageRuntime.started {
		usageRuntime.mu.Unlock()
		return
	}
	// agentHome() 返回 string（agent_home.go:17），无法解析时为空串
	home := agentHome()
	if home == "" {
		usageRuntime.mu.Unlock()
		logger.Warn("[USAGE] start skipped: agent home unavailable")
		return
	}
	store, err := usage.OpenDefaultStore()
	if err != nil {
		usageRuntime.mu.Unlock()
		logger.Warn("[USAGE] start skipped: open store: " + err.Error())
		return
	}
	tz := usage.TZName()
	tracker := usage.NewTracker(store, []string{filepath.Join(home, ".claude", "projects")}, usageCollectionAllowed)
	reporter := usage.NewReporter(store, func() string { return GetSharedAgentService().currentDeviceID() }, tz)

	stop := make(chan struct{})
	usageRuntime.started = true
	usageRuntime.stop = stop
	usageRuntime.reporter = reporter
	usageRuntime.mu.Unlock()

	go func() {
		scanTicker := time.NewTicker(30 * time.Second)
		defer scanTicker.Stop()
		flushTicker := time.NewTicker(5 * time.Minute)
		defer flushTicker.Stop()
		lastHour := time.Now().Local().Hour()
		for {
			select {
			case <-stop:
				return
			case <-scanTicker.C:
				if err := tracker.ScanOnce(); err != nil {
					logger.Debug("[USAGE] scan: " + err.Error())
				}
				// 跨本地小时边界立即 flush（spec §5.2）
				if h := time.Now().Local().Hour(); h != lastHour {
					lastHour = h
					if usageCollectionAllowed() {
						_ = reporter.FlushDirty(currentRemoteWriterFunc())
					}
				}
			case <-flushTicker.C:
				if !usageCollectionAllowed() {
					continue
				}
				if err := reporter.FlushDirty(currentRemoteWriterFunc()); err != nil {
					logger.Debug("[USAGE] flush dirty: " + err.Error())
				}
			}
		}
	}()
	logger.Info("[USAGE] tracker started tz=" + tz)
}

// StopUsageTrackerRuntime 停止采集循环（进程退出时调用）。
func StopUsageTrackerRuntime() {
	usageRuntime.mu.Lock()
	defer usageRuntime.mu.Unlock()
	if !usageRuntime.started {
		return
	}
	close(usageRuntime.stop)
	usageRuntime.started = false
}

// usageFlushAllNow 在 WS 注册成功后全量补推（registered 钩子调用）。
func usageFlushAllNow(write func(interface{}) error) {
	usageRuntime.mu.Lock()
	reporter := usageRuntime.reporter
	usageRuntime.mu.Unlock()
	if reporter == nil || !usageCollectionAllowed() {
		return
	}
	if err := reporter.FlushAll(write); err != nil {
		logger.Debug("[USAGE] flush all after register: " + err.Error())
	}
}

// usageCollectionAllowed 是采集总开关：本地配置开启 且 agent 处于 enable 态
// （spec §5.3：disable 暂停采集与上报；离线不影响采集）。
func usageCollectionAllowed() bool {
	cfg := config.GetGlobalConfig()
	if cfg != nil && cfg.Core != nil && cfg.Core.UsageTracker != nil && !cfg.Core.UsageTracker.IsEnabled() {
		return false
	}
	return GetSharedAgentService().agentEnabled()
}

// currentRemoteWriterFunc 返回当前 WS writer（未连接返回 nil）。
// currentRemoteWriter() 返回 agentTerminalWriter（= func(interface{}) error），
// 可直接赋给本函数签名。
func currentRemoteWriterFunc() func(interface{}) error {
	if w := GetSharedAgentService().currentRemoteWriter(); w != nil {
		return w
	}
	return nil
}
```

同时在 `app/http/services/agent_service.go` 追加（已核实无现成线程安全读法可复用）：

```go
// agentEnabled 线程安全读取 enable 态（供用量采集判定）。
func (s *AgentService) agentEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Enabled
}
```

- [ ] **Step 3: 接入生命周期**

修改 `app/agentruntime/server.go`（已 import services，无新增依赖）：

- `StartLocalServer()` 末尾（`logger.Info("[AGENT-BOOT] local_server awaiting session owner sync")`（:86）之前）加：
  ```go
  services.StartUsageTrackerRuntime()
  ```
- `StopLocalServer()` 在 `srv.Shutdown(ctx)`（:101）之后加：
  ```go
  services.StopUsageTrackerRuntime()
  ```

- [ ] **Step 4: registered 钩子触发全量补推**

修改 `app/http/services/agent_remote_ws.go` 的 `handleRemoteAgentMessage`，在 `case models.AgentEventRegistered:` 分支中 `s.ai.emitApprovalSync(writeJSON)`（:547）之后加：

```go
		// 用量桶全量补推：重连后把历史未确认桶一次补齐（服务端 upsert 幂等）。
		usageFlushAllNow(writeJSON)
```

- [ ] **Step 5: 编译验证**

Run: `go build ./... && go test ./processor/usage/... ./processor/config/... -count=1`
Expected: 编译通过、测试通过。

- [ ] **Step 6: 提交**

```bash
git add app/http/models/agent_protocol.go app/http/services/usage_tracker_wiring.go app/agentruntime/server.go app/http/services/agent_remote_ws.go app/http/services/agent_service.go
git commit -m "新增:用量感知接线-user-agent生命周期与registered补推钩子"
```

---

### Task 7: 全量验证

- [ ] **Step 1: 全量构建 + 相关包测试**

```bash
go build ./...
go vet ./processor/usage/... ./app/http/services/... ./app/agentruntime/... ./processor/config/...
go test ./processor/usage/... ./processor/config/... -count=1
```

Expected: 全部通过。

- [ ] **Step 2: 收尾核查**

- `git status` 确认无意外未跟踪文件；本任务不留 smoke 脚本（tracker 真实文件解析已由 Task 3 的临时目录测试覆盖；与 PhoneServer 的完整链路冒烟在 Task 8 交接后两端联调时进行）。
- 人工过一遍 wiring diff：确认 StartLocalServer 仅在 user-agent 进程路径上调用、registered 钩子只加了一行。

- [ ] **Step 3: 提交（如有遗留修正）**

```bash
git status
# 如有未提交修正：git add -A && git commit -m "修复:用量感知收尾修正"
```

---

### Task 8: PhoneServer 交接文档

**Files:**
- Create: `docs/superpowers/specs/2026-09-23-usage-report-server-handoff.md`

- [ ] **Step 1: 写交接文档**

内容直接引用 spec §5.1（消息契约）、§6（`ai_usage_hourly` DDL + upsert 语义 + 服务端工作项清单），并补充：

- WS 消息到达侧：PhoneServer 在现有 agent WS 消息分发器里加 `type == "usage.report"` 分支；忽略未知字段；records 非数组返回错误应答（`AgentEventError` 惯例），数组逐条 upsert（单条失败跳过，不影响其他）。
- 明确该表为内部材料表：第一期无任何对外查询接口；`ai.usage`（逐轮）事件链路不受影响。
- 部署顺序：**先服务端（能接收 usage.report 并忽略/入库）后客户端**——旧服务端收到未知 type 的行为需在联调时确认（现有分发器对未知 type 的默认处理通常是忽略，若不是则必须先服务端上线）。

- [ ] **Step 2: 提交**

```bash
git add docs/superpowers/specs/2026-09-23-usage-report-server-handoff.md
git commit -m "新增:usage.report服务端契约交接文档"
```

---

### Task 9: 收尾

- [ ] **Step 1: 汇总验证结果**

向用户汇报：构建/测试结果、提交列表（`git log --oneline master..HEAD`）、遗留项（PhoneServer 侧实施 + 两端联调）。

- [ ] **Step 2: 合并策略交由用户决定**

按仓库规矩：不自动 push、不自动合 master（master 受保护禁强推）。用户确认后由用户或经批准执行 merge/push。

---

## 明确不做（继承 spec §9）

美元估算、App 用户端展示、会话/项目级明细上报、exactly-once、Cursor 等其他工具、fsnotify、网关流量统计改造、`ai.usage` 事件链路改动。
