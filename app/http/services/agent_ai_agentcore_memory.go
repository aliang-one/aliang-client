package services

// agentcore(AliangCode)记忆层(spec: docs/superpowers/specs/2026-11-09-agentcore-memory-design.md)。
// 项目级长期事实:写侧=run 收尾后宿主异步提取;读侧=system prompt 全量注入。
// 与 journal 同族纪律:尽力而为,任何失败只 Warn,绝不影响 run。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
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
		// 同名 .bak 覆盖:现场以最近一次为准;改名失败也留 Warn(尽力而为)
		bakErr := os.Rename(path, path+".bak")
		logger.Warn(fmt.Sprintf("agentcore-memory: 文件损坏已留 .bak 并重建 slug=%s 摘要=%.200q bak=%v", slug, string(raw), bakErr))
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
	// 手改文件 next_id 可能 ≤ 既有最大 id:抬到 maxSeenID+1,否则下一条
	// add 拿到重复 ID,下次 load 去重会把新写入的合法条目当重复静默丢弃。
	var maxSeenID int
	for _, e := range kept {
		if e.ID > maxSeenID {
			maxSeenID = e.ID
		}
	}
	if f.NextID <= maxSeenID {
		f.NextID = maxSeenID + 1
	}
	return &f
}

// saveLocked 落盘。目录懒建;tmp+rename 原子写(沿 agent_ai_run_journal.go
// 先例)——崩写只可能留下完好的旧文件或新文件,不留半截 JSON;.bak 防手改/
// 磁盘损坏,本处防崩写,两道防线互补。
func (s *agentCoreMemoryStore) saveLocked(dir, slug string, f *agentCoreMemoryFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, slug+".json")
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// list 按项目列全量(按 last_hit_at 降序,空回退 created_at;spec §8)。
func (s *agentCoreMemoryStore) list(projectPath string) []agentCoreMemoryEntry {
	return s.listAt(s.dirFn(), projectPath)
}

// listAt 同 list,但存储目录由调用方显式传入——detached 提取 goroutine 用它,
// 不在 goroutine 内触碰 dirFn seam(与测试 t.Cleanup 还原写构成数据竞态,
// 2026-11-10 -race 实测)。
func (s *agentCoreMemoryStore) listAt(dir, projectPath string) []agentCoreMemoryEntry {
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
// spec §6)。失败只 Warn。同步调用方入口(目录经 dirFn 现取);注入 goroutine
// 走 touchAt,不在 goroutine 内触碰 dirFn seam。
func (s *agentCoreMemoryStore) touch(projectPath string, ids []int) {
	s.touchAt(s.dirFn(), projectPath, ids)
}

// touchAt 同 touch,但存储目录由调用方显式传入(与 listAt/applyOpsAt 同款
// 两层纪律:goroutine 内零 dirFn 接触,与测试 t.Cleanup 还原写无数据竞态,
// 2026-11-10 -race 实测)。
func (s *agentCoreMemoryStore) touchAt(dir, projectPath string, ids []int) {
	if len(ids) == 0 {
		return
	}
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
		// 同步段拿 dir 局部(两层纪律),goroutine 内零 dirFn 接触;WG 计数
		// 供测试清理段等 goroutine 退出(与 Task 4 done channel 同款
		// happens-before),防其写盘与 t.TempDir RemoveAll 相撞。
		dir := agentCoreMemory.dirFn()
		agentCoreMemoryTouchWG.Add(1)
		go func() {
			defer agentCoreMemoryTouchWG.Done()
			agentCoreMemory.touchAt(dir, projectPath, hit)
		}()
	}
	return b.String()
}

// agentCoreMemoryTouchWG 在途注入 touch goroutine 计数;生产无消费者,测试
// 清理段用它等待 goroutine 退出(无界生产计数、有界消费,泄漏面=毫秒级文件写)。
var agentCoreMemoryTouchWG sync.WaitGroup

// agentCoreMemoryEnabledFn 已在 Task 4 定义(提取钩子与注入共用同一 seam)。

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

// SetDirForTest 导出测试目录注入:handlers 包测试拿不到 services 未导出
// seam,经此换目录;返回 restore 函数由调用方还原(测试侧配 defer/t.Cleanup
// 包装)。非 T 签名——生产文件不链 testing。
func (AgentCoreMemoryFacade) SetDirForTest(dir string) (restore func()) {
	prev := agentCoreMemory.dirFn
	agentCoreMemory.dirFn = func() string { return dir }
	return func() { agentCoreMemory.dirFn = prev }
}

// —— 提取调用(spec §5):同端点同模型,effort 回退链+进程锁存 ——

// agentcore 进程级 effort 锁存(spec §5):首次成功即置 Set 并记档——档位可能
// 是空串(无 effort 终档),故需 Set 配对位区分「未锁存」,否则每回合重吃 400。
// 边界:进程锁存不随 customer 配置切换重探;换 provider/模型后仍用锁存档,
// 重启复位。
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
			Model:     model,
			MaxTokens: agentCoreMemoryExtractMaxTokens,
			System:    system,
			Messages:  []agentcore.Message{agentcore.TextMessage("user", user)},
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

// —— 提取产物落地与收尾钩子(spec §5) ——

// agentCoreMemoryOp 提取 ops 契约(提取 prompt 输出):
// {"ops":[{"op":"add","content":"…"},{"op":"update","id":7,"content":"…"}]}
type agentCoreMemoryOp struct {
	Op      string `json:"op"`
	ID      int    `json:"id,omitempty"`
	Content string `json:"content"`
}

// applyOps 提取产物落地(spec §5)。同步调用方入口(目录经 dirFn 现取);
// detached goroutine 走 applyOpsAt,不在 goroutine 内触碰 dirFn seam。
func (s *agentCoreMemoryStore) applyOps(projectPath string, ops []agentCoreMemoryOp) {
	s.applyOpsAt(s.dirFn(), projectPath, ops)
}

// applyOpsAt 提取产物落地核心:update 改 content+刷新 last_hit_at(未命中
// Warn,疑被 LRU 淘汰的对账失败可诊断);add 追加(批内同文只入第一条);
// 单条超 500 字符丢弃该条+Warn(最小损失面,不截断防残句误导);非法 op
// 忽略。锁内一次往返。
func (s *agentCoreMemoryStore) applyOpsAt(dir, projectPath string, ops []agentCoreMemoryOp) {
	slug := agentCoreMemoryProjectSlug(projectPath)
	if dir == "" || slug == "" || len(ops) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.loadLocked(dir, slug)
	changed := false
	seenAdd := map[string]bool{} // 批内 add 去重:同一产物里同文只入第一条
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
			hit := false
			for i := range f.Memories {
				if f.Memories[i].ID == op.ID {
					f.Memories[i].Content = content
					f.Memories[i].LastHitAt = time.Now().UTC().Format(time.RFC3339)
					hit = true
					changed = true
					break
				}
			}
			if !hit {
				logger.Warn(fmt.Sprintf("agentcore-memory: 提取 update 未命中条目(疑被 LRU 淘汰) id=%d", op.ID))
			}
		case "add":
			if content == "" {
				continue
			}
			if len([]rune(content)) > agentCoreMemoryContentCap {
				logger.Warn(fmt.Sprintf("agentcore-memory: 提取 add 超限丢弃 len=%d", len([]rune(content))))
				continue
			}
			if seenAdd[content] {
				logger.Warn(fmt.Sprintf("agentcore-memory: 提取 add 批内重复跳过 摘要=%.40q", content))
				continue
			}
			seenAdd[content] = true
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

// truncateRunes 按字符(rune)截断,超长丢弃尾段;短于上限原样返回。
func truncateRunes(s string, max int) string {
	if len(s) <= max { // 字节快速路径:字节不超则字符必不超,免 []rune 分配
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// agentCoreMemoryExtractAsync 收尾钩子(spec §5):开关关/输入空/模型不可用
// 全跳过;ctx 60s 超时;任何失败 Warn 放弃。detached goroutine,run 早已结束。
// 返回完成信号(goroutine 末尾 close;早退路径同步 close),测试用它替代
// sleep/轮询建立 happens-before。守卫全部在同步段做完,不满足不起 goroutine
// ——seam/dirFn 只在调用方 goroutine 读一次存局部变量,与测试 t.Cleanup
// 还原写无竞态(2026-11-10 -race 实测);goroutine 内一律用捕获的局部值。
func (m *agentAIManager) agentCoreMemoryExtractAsync(projectPath, model, runEffort, userText, assistantText string) chan struct{} {
	done := make(chan struct{})
	skip := func() chan struct{} {
		close(done)
		return done
	}
	if !agentCoreMemoryEnabledFn() {
		return skip()
	}
	if agentCoreModelUnusable(model) {
		return skip() // 模型不可用(synthetic/空):提取无从发起,直接放弃
	}
	dir := strings.TrimSpace(agentCoreMemory.dirFn())
	if strings.TrimSpace(projectPath) == "" || dir == "" {
		return skip()
	}
	u, a := truncateRunes(userText, 4000), truncateRunes(assistantText, 4000)
	if strings.TrimSpace(u) == "" && strings.TrimSpace(a) == "" {
		return skip()
	}
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				logger.Warn(fmt.Sprintf("agentcore-memory: 提取 goroutine panic(已吞): %v", r))
			}
		}()
		entries := agentCoreMemory.listAt(dir, projectPath)
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
		agentCoreMemory.applyOpsAt(dir, projectPath, out.Ops)
	}()
	return done
}

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
