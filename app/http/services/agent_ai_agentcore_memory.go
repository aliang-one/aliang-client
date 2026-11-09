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
