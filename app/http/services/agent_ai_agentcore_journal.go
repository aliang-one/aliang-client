package services

// agentcore（AliangCode）调用记录 journal：会话回合期间把对话逐行追加到
// <agentHome()>/.aliang/ai-sessions/<sessionID>.jsonl，供仪表盘 AI 会话
// 历史列表与详情回放扫描。设计文档：
// docs/superpowers/specs/2026-10-26-agentcore-journal-design.md
//
// 尽力而为留痕：任何写失败只 Warn，绝不影响 run。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// agentCoreJournalMaxFiles 限制磁盘上保留的会话文件数；在新建会话文件时
// prune（按 mtime 保留最新）。扫描侧另有 recency cap，二者独立。
const agentCoreJournalMaxFiles = 100

// agentCoreUnfinishedScanCap 装配期增量游标 map 的会话数封顶：超限整体清零
// （丢游标=下次整份重扫，正确性无损）。与 written 的满额重置同哲学。
const agentCoreUnfinishedScanCap = 1024

// agentCoreJournalWrittenCap 去重集合上限：超过即整体清空。去重只防同进程内
// 同一消息写两行，进程重启集合清零没有正确性问题（重发即新 messageID）。
const agentCoreJournalWrittenCap = 4096

type agentCoreJournal struct {
	mu             sync.Mutex
	dir            string
	dirReady       bool
	written        map[string]struct{}
	unfinishedScan map[string]*unfinishedScanState // 会话→装配期增量扫描游标（unfinishedSubagents 专用）
}

func newAgentCoreJournal(dir string) *agentCoreJournal {
	return &agentCoreJournal{dir: strings.TrimSpace(dir), written: make(map[string]struct{})}
}

// unfinishedScanEntry 单个子 agent 的扫描状态（含 finish 标记）。
type unfinishedScanEntry struct {
	agentCoreSubagentInterrupted
	finished bool
}

// unfinishedScanState 会话级增量扫描游标（审计 2026-11-10 ④）：装配期不再
// 锁内全文件重扫——只读上次游标之后的新行。offset=已消费完整行的文件偏移
// （未遇换行的残尾留在磁盘上，下轮从同一偏移重读——磁盘本身就是缓冲，
// 内存侧不再复制残尾）；firstLine=首行内容（外部改写文件的粗校验之一）；
// entries/order 与全量扫描同构。
type unfinishedScanState struct {
	offset    int64
	firstLine string
	order     []string
	entries   map[string]*unfinishedScanEntry
}

func (s *unfinishedScanState) scanLine(line []byte) {
	var row struct {
		Kind           string `json:"kind"`
		Event          string `json:"event"`
		TaskID         string `json:"task_id"`
		Name           string `json:"name"`
		Task           string `json:"task"`
		Text           string `json:"text"`
		Ts             string `json:"ts"`
		SubagentTaskID string `json:"subagent_task_id"`
		Tool           string `json:"tool"`
	}
	if json.Unmarshal(line, &row) != nil {
		return
	}
	switch {
	case row.Kind == "subagent" && row.Event == "start" && strings.TrimSpace(row.TaskID) != "":
		if _, ok := s.entries[row.TaskID]; ok {
			return
		}
		s.entries[row.TaskID] = &unfinishedScanEntry{agentCoreSubagentInterrupted: agentCoreSubagentInterrupted{
			TaskID: row.TaskID, Name: row.Name, Task: row.Task, StartedAt: row.Ts,
		}}
		s.order = append(s.order, row.TaskID)
	case row.Kind == "subagent" && row.Event == "finish":
		if e, ok := s.entries[row.TaskID]; ok {
			e.finished = true
			// 终态条目只留计数与标识，文本字段即清（复核 2026-11-10 ④ P2）：
			// 输出口径只消费未终态条目，清文本不改变任何返回值，只压内存驻留。
			e.LastText = ""
			e.Task = ""
		}
	case row.Kind == "tool" && strings.TrimSpace(row.SubagentTaskID) != "":
		if e, ok := s.entries[row.SubagentTaskID]; ok && !e.finished {
			e.ToolCalls++
			e.LastTool = row.Tool
		}
	case row.Kind == "subagent_turn":
		if e, ok := s.entries[row.TaskID]; ok && !e.finished {
			if t := strings.TrimSpace(row.Text); t != "" {
				e.LastText = t
			}
		}
	}
}

// agentCoreJournalDir 返回 journal 存储根；agentHome() 不可得时返回空串
// （= journal 禁用，所有方法退化为 no-op）。
func agentCoreJournalDir() string {
	home := agentHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".aliang", "ai-sessions")
}

// agentCoreJournalFileName 把 sessionID 变成安全文件名：只保留
// [A-Za-z0-9._-]，其余转 `_`，超长截断（防路径注入与文件名超限）。
func agentCoreJournalFileName(sessionID string) string {
	var b strings.Builder
	for _, r := range sessionID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if len(name) > 120 {
		name = name[:120]
	}
	return name + ".jsonl"
}

// ensureSessionHeader 保证会话文件第一行是头行（kind=session）。文件不存在
// 时创建；存在但无头行（steer 先于 run 的历史竞态，spec §4.3）时整体重写
// 补头——读全量、写 头行+原内容、原子 rename。文件体量有界（消息+截断
// 工具行），重写代价可接受。幂等：已有头行直接返回。顺带触发 prune（锁内）。
// approvalMode 会话级审批模式(2026-11-05,空=默认):头行持久化 + 读侧回填,
// 与 model/effort 同族;显式改键走 syncSessionApprovalMode(幂等头行不重写)。
func (j *agentCoreJournal) ensureSessionHeader(sessionID, projectPath, model, effort, createdAt, origin string, disabledMCP, disabledSkills []string, approvalMode string) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	path := filepath.Join(j.dir, agentCoreJournalFileName(sessionID))
	if _, err := os.Stat(path); err == nil {
		if j.hasSessionHeaderLocked(path) {
			return
		}
		j.prependHeaderLocked(path, sessionID, projectPath, model, effort, createdAt, origin, disabledMCP, disabledSkills, approvalMode)
		return
	}
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: mkdir failed session=%s err=%v", sessionID, err))
		return
	}
	j.dirReady = true
	line := j.sessionHeaderLine(sessionID, projectPath, model, effort, createdAt, origin, disabledMCP, disabledSkills, approvalMode)
	if line == nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header marshal failed session=%s", sessionID))
		return
	}
	if err := os.WriteFile(path, line, 0o600); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header write failed session=%s err=%v", sessionID, err))
		return
	}
	// 首次纳管一个会话打 Info（spec §12 观测点），写失败才有 Warn 对照。
	logger.Info(fmt.Sprintf("agentcore-journal: session tracked session=%s file=%s", sessionID, agentCoreJournalFileName(sessionID)))
	j.pruneLocked(sessionID)
}

// sessionHeaderLine 生成头行字节（含结尾 \n）；marshal 失败返回 nil。
// 开关名单只在非 nil 时写键（nil=未设置，读侧容忍缺键）——首建头行就带上
// 当前名单（2026-10-31 评审 F1：create 先于首个 run 设的键，不能等首个 run
// 之后的显式同步才落盘，否则重启即丢）。
func (j *agentCoreJournal) sessionHeaderLine(sessionID, projectPath, model, effort, createdAt, origin string, disabledMCP, disabledSkills []string, approvalMode string) []byte {
	header := map[string]interface{}{
		"kind":          "session",
		"session_id":    sessionID,
		"project_path":  projectPath,
		"model":         model,
		"effort":        effort,
		"approval_mode": approvalMode,
		"created_at":    createdAt,
		"origin":        origin,
	}
	if disabledMCP != nil {
		header["disabled_mcp_servers"] = append([]string{}, disabledMCP...)
	}
	if disabledSkills != nil {
		header["disabled_skills"] = append([]string{}, disabledSkills...)
	}
	line, err := jsonMarshalLine(header)
	if err != nil {
		return nil
	}
	return line
}

// hasSessionHeaderLocked 判断文件首行是否已是合法 session 头行；文件打不开、
// 空文件、首行非法 JSON 或 kind 不是 session 都算无头。调用方必须持有 j.mu。
func (j *agentCoreJournal) hasSessionHeaderLocked(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return false
	}
	var head map[string]interface{}
	if json.Unmarshal(sc.Bytes(), &head) != nil {
		return false
	}
	return head["kind"] == "session"
}

// healMergeJournal 把拆分 journal(源)的内容行并回 canonical journal(目标),
// 成功后删除源文件。存量错位治愈(2026-11-07):手机续聊曾把回合写进
// ai_import_<hash>.jsonl,桌面清单因此多出「新对话」。
//
// 幂等语义:源文件在才动手;目标文件缺失时以源头行字段(换 session_id)补建
// 头行;追加成功即删源,进程在「追加完/未删源」窗口崩溃则重试会产生少量重复
// 行(装饰性,可接受)。返回移动的内容行数;源缺失/无头行返回 (0,false) 且
// 不动任何文件。
func (j *agentCoreJournal) healMergeJournal(sourceSessionID, targetSessionID string) (int, bool) {
	if j == nil || j.dir == "" {
		return 0, false
	}
	sourceID := strings.TrimSpace(sourceSessionID)
	targetID := strings.TrimSpace(targetSessionID)
	if sourceID == "" || targetID == "" || sourceID == targetID {
		return 0, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	sourcePath := filepath.Join(j.dir, agentCoreJournalFileName(sourceID))
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return 0, false
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return 0, false
	}
	var head map[string]interface{}
	if json.Unmarshal([]byte(lines[0]), &head) != nil || head["kind"] != "session" {
		return 0, false
	}
	targetPath := filepath.Join(j.dir, agentCoreJournalFileName(targetID))
	_, statErr := os.Stat(targetPath)
	if statErr != nil || !j.hasSessionHeaderLocked(targetPath) {
		// 目标缺失/无头:以源头行字段补建(会话 id 换成目标)。调用方已持
		// j.mu——这里必须用无锁原语(sessionHeaderLine 纯函数/prependHeaderLocked
		// 为 Locked 语义),绝不调 ensureSessionHeader(内部加锁=自锁死)。
		stringField := func(key string) string {
			value, _ := head[key].(string)
			return value
		}
		var disabledMCP, disabledSkills []string
		if list, ok := head["disabled_mcp_servers"].([]interface{}); ok {
			for _, item := range list {
				if value, ok := item.(string); ok {
					disabledMCP = append(disabledMCP, value)
				}
			}
		}
		if list, ok := head["disabled_skills"].([]interface{}); ok {
			for _, item := range list {
				if value, ok := item.(string); ok {
					disabledSkills = append(disabledSkills, value)
				}
			}
		}
		line := j.sessionHeaderLine(targetID, stringField("project_path"), stringField("model"), stringField("effort"),
			stringField("created_at"), stringField("origin"), disabledMCP, disabledSkills, stringField("approval_mode"))
		if line == nil {
			return 0, false
		}
		if statErr != nil {
			if err := os.MkdirAll(j.dir, 0o700); err != nil {
				return 0, false
			}
			if err := os.WriteFile(targetPath, line, 0o600); err != nil {
				return 0, false
			}
		} else {
			j.prependHeaderLocked(targetPath, targetID, stringField("project_path"), stringField("model"), stringField("effort"),
				stringField("created_at"), stringField("origin"), disabledMCP, disabledSkills, stringField("approval_mode"))
		}
		// 头行补建/prepend 使目标文件前缀整体位移 → 装配期增量游标失效；
		// 纯 O_APPEND 合流路径不动前缀，游标继续有效。
		delete(j.unfinishedScan, targetID)
		if !j.hasSessionHeaderLocked(targetPath) {
			return 0, false
		}
	}
	var body strings.Builder
	moved := 0
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
		moved++
	}
	file, err := os.OpenFile(targetPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, false
	}
	if _, err := file.WriteString(body.String()); err != nil {
		file.Close()
		return 0, false
	}
	if err := file.Close(); err != nil {
		return 0, false
	}
	if err := os.Remove(sourcePath); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: heal remove source failed source=%s target=%s err=%v", sourceID, targetID, err))
		return 0, false
	}
	return moved, true
}

// syncSessionSwitches 把会话级 MCP/技能开关同步进 journal 头行（会话级开关
// 持久化，2026-10-31）。
//
// ⚠️ 调用纪律（2026-10-31 两轮评审定案）：**只在显式改键点调用**——create()
// 与 message() 里「键存在才整体替换」的两处。绝不许做「每 run 进门同步」：
// run 携带的内存名单可能尚未从 journal 头行水合（手机 v3 的 ai.run.start 恒
// 带 transcript、懒建恢复可能被跳过、恢复读可能瞬时失败），拿未水合的 nil 当
// 权威会把头行里持久化的名单**删掉**——持久化机制自己变成删除机制。
//
// set 标记区分「本载荷没带这个键」与「带键」：没带的**一侧完全不动头行**
// （preserve 语义，2026-10-31 评审 F2——单键写入绝不许拿未水合的兄弟内存值
// 覆盖兄弟持久值）。带键一侧：非空写键、nil/空删键（与缺键行为等价，读侧
// 容忍）。仅在头行实际变化时重写第一行（tmp+rename 原子替换，文件体量有界，
// 与 prependHeaderLocked 同款代价）；无变化零写盘。
func (j *agentCoreJournal) syncSessionSwitches(sessionID string, disabledMCP []string, mcpSet bool, disabledSkills []string, skillsSet bool) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	path := filepath.Join(j.dir, agentCoreJournalFileName(sessionID))
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		return
	}
	var head map[string]interface{}
	if json.Unmarshal(data[:nl], &head) != nil || head["kind"] != "session" {
		return
	}
	changed := false
	if mcpSet && !headerSwitchesEqual(head["disabled_mcp_servers"], disabledMCP) {
		applyHeaderSwitches(head, "disabled_mcp_servers", disabledMCP)
		changed = true
	}
	if skillsSet && !headerSwitchesEqual(head["disabled_skills"], disabledSkills) {
		applyHeaderSwitches(head, "disabled_skills", disabledSkills)
		changed = true
	}
	if !changed {
		return
	}
	line, err := jsonMarshalLine(head)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	buf := append([]byte{}, line...)
	buf = append(buf, data[nl+1:]...)
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header switches write failed session=%s err=%v", sessionID, err))
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header switches rename failed session=%s err=%v", sessionID, err))
		_ = os.Remove(tmp)
		return
	}
}

// syncSessionApprovalMode 把会话级审批模式同步进 journal 头行(显式改键点
// 调用:ai.session.create/apply 里键存在时;缺键绝不调用——preserve 纪律,
// 防老客户端把持久化模式冲成默认)。空串=默认推荐,合法值照写。
func (j *agentCoreJournal) syncSessionApprovalMode(sessionID, approvalMode string, set bool) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" || !set {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	path := filepath.Join(j.dir, agentCoreJournalFileName(sessionID))
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		return
	}
	var head map[string]interface{}
	if json.Unmarshal(data[:nl], &head) != nil || head["kind"] != "session" {
		return
	}
	if stored, _ := head["approval_mode"].(string); stored == approvalMode {
		return
	}
	head["approval_mode"] = approvalMode
	line, err := jsonMarshalLine(head)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	buf := append([]byte{}, line...)
	buf = append(buf, data[nl+1:]...)
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header approval_mode write failed session=%s err=%v", sessionID, err))
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header approval_mode rename failed session=%s err=%v", sessionID, err))
		_ = os.Remove(tmp)
		return
	}
}

// headerSwitchesEqual 判断头行已存的开关名单与会话当前值是否一致。缺键与
// 空数组等价（都=未关任何项）；非字符串元素按缺失计（旧文件/畸形容忍）。
func headerSwitchesEqual(stored interface{}, list []string) bool {
	storedRaw, _ := stored.([]interface{})
	normalized := make([]string, 0, len(storedRaw))
	for _, v := range storedRaw {
		if s, ok := v.(string); ok {
			normalized = append(normalized, s)
		}
	}
	if len(normalized) != len(list) {
		return false
	}
	for i := range list {
		if normalized[i] != list[i] {
			return false
		}
	}
	return true
}

// applyHeaderSwitches 把一侧开关名单写进头行 map：nil/空数组=删键（不写键）。
func applyHeaderSwitches(head map[string]interface{}, key string, list []string) {
	if len(list) == 0 {
		delete(head, key)
		return
	}
	head[key] = append([]string{}, list...)
}

// prependHeaderLocked 整体重写补头：tmp 文件写 头行+原内容，rename 原子替换。
// 在 j.mu 锁内做全量读+写+rename——journal 文件有界（消息+截断工具行），
// 重写代价可接受；与 appendLineLocked 同锁互斥，无并发写坏。调用方必须持有 j.mu。
func (j *agentCoreJournal) prependHeaderLocked(path, sessionID, projectPath, model, effort, createdAt, origin string, disabledMCP, disabledSkills []string, approvalMode string) {
	head := j.sessionHeaderLine(sessionID, projectPath, model, effort, createdAt, origin, disabledMCP, disabledSkills, approvalMode)
	if head == nil {
		return
	}
	old, err := os.ReadFile(path)
	if err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header backfill read failed session=%s err=%v", sessionID, err))
		return
	}
	tmp := path + ".tmp"
	var buf bytes.Buffer
	buf.Write(head)
	buf.Write(old)
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header backfill write failed session=%s err=%v", sessionID, err))
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header backfill rename failed session=%s err=%v", sessionID, err))
		_ = os.Remove(tmp)
		return
	}
	logger.Info(fmt.Sprintf("agentcore-journal: session header backfilled session=%s", sessionID))
}

// agentAIJournalAttachment 是 msg 行的附件引用（spec 2026-11-05-chat-image-
// pipeline §3.2）。Ref=相对持久缓存根；journal 不落绝对路径与 URL（网页经
// /api/agent/ai/attachment?ref= 取图）。Origin=phone|web。
type agentAIJournalAttachment struct {
	Name   string `json:"name"`
	Mime   string `json:"mime,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	Ref    string `json:"ref"`
	Origin string `json:"origin,omitempty"`
}

func (j *agentCoreJournal) appendUser(sessionID, messageID, text, ts string, attachments ...[]agentAIJournalAttachment) {
	j.appendMsg(sessionID, "user", messageID, text, ts, attachments...)
}

func (j *agentCoreJournal) appendAssistant(sessionID, messageID, text, ts string) {
	j.appendMsg(sessionID, "assistant", messageID, text, ts)
}

func (j *agentCoreJournal) appendMsg(sessionID, role, messageID, text, ts string, attachments ...[]agentAIJournalAttachment) {
	var atts []agentAIJournalAttachment
	if len(attachments) > 0 {
		atts = attachments[0]
	}
	// 守卫修正（spec §3.2）：text 空但带附件的纯图消息必须落盘——旧逻辑
	// 「text 空直接 return」是手机图在网页消失的治本断点①。空文本且无附件
	// 依旧不落（防空行垃圾）。
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" {
		return
	}
	if strings.TrimSpace(text) == "" && len(atts) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	key := sessionID + "\x00" + role + "\x00" + messageID
	if _, dup := j.written[key]; dup {
		return
	}
	row := map[string]interface{}{
		"kind":       "msg",
		"role":       role,
		"message_id": messageID,
		"text":       text,
		"ts":         ts,
	}
	if len(atts) > 0 {
		row["attachments"] = atts
	}
	if !j.appendLineLocked(sessionID, row) {
		// 写失败不记去重键：written 只记「已落盘」，IO 恢复后同消息可补写。
		return
	}
	if len(j.written) >= agentCoreJournalWrittenCap {
		j.written = make(map[string]struct{})
	}
	j.written[key] = struct{}{}
}

// appendEnd 落 run 终态行。status ∈ success|stopped|error。
func (j *agentCoreJournal) appendEnd(sessionID, status, errMsg string) {
	j.appendEndDiagnostics(sessionID, status, errMsg, nil)
}

// appendEndDiagnostics 落带诊断块的 run 终态行：diagnostics 里带回合数、
// 上下文规模、守卫连击与终止真因，诊断面板据此在重开旧会话时还原「这一轮
// 为什么死」。写失败与 appendEnd 家族同款吞掉。
func (j *agentCoreJournal) appendEndDiagnostics(sessionID, status, errMsg string, diag *agentCoreDiagnostics) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	row := map[string]interface{}{
		"kind":   "end",
		"status": status,
		"ts":     time.Now().UTC().Format(time.RFC3339),
	}
	if errMsg != "" {
		row["error"] = errMsg
	}
	if payload := diag.payload(); payload != nil {
		row["diagnostics"] = payload
	}
	j.appendLineLocked(sessionID, row)
}

// appendAssistantTurn 落逐回合 transcript 行（kind=turn）：本回合 assistant
// 的完整文本 + thinking + 停止原因 + 本回合发起的工具名。这是「完整对话」
// 留档的核心——run 末的 appendAssistant 只写整轮累计的最终输出，会丢掉
// 工具调用之间的中间叙述与 thinking；本函数每回合一行把它们补齐。
//
// 与 appendMsg 的去重无关：每回合必然一条（回合序号 turn 单调递增），不需要
// 去重键。读侧（agent_inventory_agentcore）目前只认 session/msg/tool 三类，
// 未知 kind 直接跳过，故新增 turn 行对仪表盘回放无副作用、向后兼容。
//
// 空回合（text/thinking 都空且无工具）不落，避免无意义空行。写失败只 Warn。
func (j *agentCoreJournal) appendAssistantTurn(sessionID string, turn int, text, thinking, stopReason string, tools []string) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	if strings.TrimSpace(text) == "" && strings.TrimSpace(thinking) == "" && len(tools) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	row := map[string]interface{}{
		"kind":        "turn",
		"turn":        turn,
		"role":        "assistant",
		"stop_reason": stopReason,
		"ts":          time.Now().UTC().Format(time.RFC3339),
	}
	if text != "" {
		row["text"] = text
	}
	if thinking != "" {
		row["thinking"] = thinking
	}
	if len(tools) > 0 {
		row["tools"] = tools
	}
	j.appendLineLocked(sessionID, row)
}

// appendTool 落 journal v2 工具行（kind=tool）。不参与 message_id 去重：
// 每次调用一行，是轨迹不是消息。写失败 Warn 吞掉。守卫与 appendEnd 家族
// 同款（nil/dir 空/sessionID 空直接返回）。
func (j *agentCoreJournal) appendTool(sessionID, tool, summary string, isError bool) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appendLineLocked(sessionID, map[string]interface{}{
		"kind":     "tool",
		"tool":     tool,
		"summary":  summary,
		"is_error": isError,
		"ts":       time.Now().UTC().Format(time.RFC3339),
	})
}

// appendSubagentStart 落 kind=subagent/event=start 行：子 agent 跑起来
// 即写一行（spec subagent-panel）。taskID 形如 agentcore-<messageID>-<name>-<n>，
// 自带回合 messageID 消歧。task=派发任务文本截断（调用方 rune 安全截断）——
// 崩溃后主 LLM 仍知道当时在做什么（中断注入 feat/subagent-interrupted-injection）；
// 旧行无此键，读侧容忍缺失。守卫与 appendTool 同款；写失败 Warn 吞掉。
func (j *agentCoreJournal) appendSubagentStart(sessionID, taskID, name, description, task string) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appendLineLocked(sessionID, map[string]interface{}{
		"kind":        "subagent",
		"event":       "start",
		"task_id":     taskID,
		"name":        name,
		"description": description,
		"task":        task,
		"ts":          time.Now().UTC().Format(time.RFC3339),
	})
}

// agentCoreSubagentInterrupted 是一条未终态子 agent 的对账摘要（中断注入用）。
type agentCoreSubagentInterrupted struct {
	TaskID    string
	Name      string
	Task      string // start 行 task 文本（旧 journal 行为空）
	StartedAt string
	ToolCalls int
	LastTool  string
	// LastText 是子 agent 最后一轮说的话（subagent_turn 行，L2）。中断重派
	// 时主 LLM 能接上下文，而不是只知道跑了几个工具。旧 journal 为空。
	LastText string
}

// agentCoreSubagentBriefingMaxChars 是续跑简报的总字符封顶（rune 数）。它要塞
// 进下一个子 run 的首轮 user 消息，撑太大会一开局就吃掉半个上下文窗口。
const agentCoreSubagentBriefingMaxChars = 6000

// subagentSidechainBriefing 把一个已死子 agent 的 sidechain 还原成「续跑简报」
// （L3，2026-11-09）。
//
// 为什么不「逐条重放消息」：sidechain 只落了每回合的 assistant 文本与 thinking
// ——**没有** tool_use 块，也没有对应的 tool_result（那两份在内核内存里，进程
// 一死就没了）。硬造一段 assistant 历史却不配 tool_use/tool_result，上游要么
// 400（消息结构非法），要么让模型以为自己「已经做过」却拿不到任何事实依据。
// 所以这里只做诚实的降级：把「上次跑到哪、说了什么」压成一段文字简报，作为
// 新一轮任务的前置说明——续跑是**接着干**，不是**假装没断过**。
//
// 返回空串表示该 task_id 没有可用的 sidechain（旧 journal / 未 tracked /
// 一回合都没跑完）——调用方据此走普通派发，不注入任何东西。
func (j *agentCoreJournal) subagentSidechainBriefing(sessionID, taskID string, maxChars int) string {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(taskID) == "" {
		return ""
	}
	if maxChars <= 0 {
		maxChars = agentCoreSubagentBriefingMaxChars
	}
	j.mu.Lock()
	path := filepath.Join(j.dir, agentCoreJournalFileName(sessionID))
	f, err := os.Open(path)
	if err != nil {
		j.mu.Unlock()
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), agentVibeJSONLMaxLineBytes)
	type turnRow struct {
		Turn int
		Text string
	}
	turns := make([]turnRow, 0, 8)
	name := ""
	for sc.Scan() {
		var row struct {
			Kind   string `json:"kind"`
			Event  string `json:"event"`
			TaskID string `json:"task_id"`
			Name   string `json:"name"`
			Turn   int    `json:"turn"`
			Text   string `json:"text"`
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil {
			continue
		}
		if strings.TrimSpace(row.TaskID) != taskID {
			continue
		}
		switch {
		case row.Kind == "subagent" && row.Event == "start" && name == "":
			name = row.Name
		case row.Kind == "subagent_turn":
			if text := strings.TrimSpace(row.Text); text != "" {
				turns = append(turns, turnRow{Turn: row.Turn, Text: text})
			}
		}
	}
	j.mu.Unlock()
	if len(turns) == 0 {
		return ""
	}
	var b strings.Builder
	if name != "" {
		b.WriteString("上一次尝试的子 agent 类型: " + name + "\n")
	}
	fmt.Fprintf(&b, "上一次尝试已进行 %d 个回合，逐回合进展如下（原文摘录）:\n", len(turns))
	used := 0
	for _, t := range turns {
		text := t.Text
		// 单条先各自封顶 1500 rune，避免「第一轮说了 8000 字」把后面的回合全挤掉
		// ——续跑最需要的是「最后一轮说到哪」。
		if runes := []rune(text); len(runes) > 1500 {
			text = string(runes[:1500]) + "…"
		}
		line := fmt.Sprintf("- 第 %d 回合: %s\n", t.Turn, text)
		if used+len(line) > maxChars*4 { // 粗算：maxChars 是 rune，1 rune 最坏 4 字节
			b.WriteString("- （更早的回合已省略）\n")
			break
		}
		used += len(line)
		b.WriteString(line)
	}
	return strings.TrimSpace(b.String())
}

// unfinishedSubagents 扫描会话 journal，返回 start 无 finish 的子 agent
// （行序=时间序）。调用时机=runAgentCore 装配 system prompt——本 run 尚未
// 派发，journal 内全部 start 行必属已死回合（同会话 run 串行）；本 run 后续
// 派发的子 agent 不受影响（装配一次性求值）。半行/坏行跳过（与详情读侧同
// 口径）。文件缺失/全终态返回 nil。
//
// 增量化（审计 2026-11-10 ④）：旧实现每次在 j.mu 内全文件重扫（行级 JSON
// 解析 O(整文件)，文件单调膨胀线性变慢，还堵住同一把锁上的所有 journal
// 写）。现按会话维护增量游标：复用时只读上次偏移之后的新行；truncateBefore
// /healMergeJournal 重写文件时精确失效。另设三重粗校验（文件变短/首行漂移/
// 游标边界非行首）兜外部改写，命中即整份重扫。
func (j *agentCoreJournal) unfinishedSubagents(sessionID string) []agentCoreSubagentInterrupted {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	path := filepath.Join(j.dir, agentCoreJournalFileName(sessionID))
	f, err := os.Open(path)
	if err != nil {
		// 文件消失（被删/换机）：游标一并作废。
		delete(j.unfinishedScan, sessionID)
		return nil
	}
	defer f.Close()

	if j.unfinishedScan == nil {
		j.unfinishedScan = make(map[string]*unfinishedScanState)
	}
	// 满额整体清零（与 written 的封顶哲学同款，复核 2026-11-10 ④ CONFIRMED
	// P2）：游标 map 无 prune 反查键（文件名是净化变换不可逆），长寿命进程
	// 否则单调增长。游标丢了只是下次整份重扫，正确性无损，换内存有界。
	if j.unfinishedScan[sessionID] == nil && len(j.unfinishedScan) >= agentCoreUnfinishedScanCap {
		j.unfinishedScan = make(map[string]*unfinishedScanState)
	}
	state := j.unfinishedScan[sessionID]
	size := int64(0)
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}
	firstLine := agentCoreJournalFirstLine(f)
	// 复用粗校验：文件变短（截断）/首行漂移（重写换头）/游标落在行中（前缀
	// 字节级漂移）任一命中 → 游标不可信，整份重扫。
	resume := state != nil && size >= state.offset && firstLine == state.firstLine &&
		agentCoreJournalBoundaryIsLineStart(f, state.offset)
	if !resume {
		state = &unfinishedScanState{firstLine: firstLine, entries: make(map[string]*unfinishedScanEntry)}
	}
	// 首行读取会把 fd 顶过预读窗口，扫描前必须显式定位：fresh 从 0 起，
	// resume 从游标起。
	if _, err := f.Seek(state.offset, io.SeekStart); err != nil {
		delete(j.unfinishedScan, sessionID)
		return nil
	}
	// 增量扫描：从游标续读完整行；无换行残尾留在磁盘，游标不越过它——下轮
	// 从同一偏移重读（若期间有后续写入紧贴残尾，拼出的坏行按半行容忍跳过）。
	// 锁内成本从 O(整文件) 降为 O(新增行)。
	br := bufio.NewReader(f)
	for {
		chunk, rerr := br.ReadBytes('\n')
		if rerr != nil {
			break // EOF（残尾留磁盘下轮重读）或读错：保留已得，尽力而为
		}
		state.offset += int64(len(chunk))
		state.scanLine(chunk[:len(chunk)-1])
	}
	j.unfinishedScan[sessionID] = state

	out := make([]agentCoreSubagentInterrupted, 0, len(state.order))
	for _, id := range state.order {
		if e := state.entries[id]; !e.finished {
			out = append(out, e.agentCoreSubagentInterrupted)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// agentCoreJournalFirstLine 读文件首行（不含换行）。读失败返回空串。
func agentCoreJournalFirstLine(f *os.File) string {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	first, _ := bufio.NewReader(f).ReadString('\n')
	return strings.TrimRight(first, "\n")
}

// agentCoreJournalBoundaryIsLineStart 校验 offset 处必须是行首（前一字节为
// 换行；offset==0 恒真）。防截断/搬迁把游标顶进某行的中间。
func agentCoreJournalBoundaryIsLineStart(f *os.File, offset int64) bool {
	if offset == 0 {
		return true
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], offset-1); err != nil || b[0] != '\n' {
		return false
	}
	return true
}

// healInterruptedSubagents 装配期对账补记（复核 2026-11-10 ⑤ CONFIRMED P2）：
// 进程硬崩溃时桥 defer 不跑（本回合 end 行与子 agent finish 行都不落），
// journal 留下 start 无 finish 僵尸行——读侧 interrupted 降级收紧后（只认
// start 之后的 end 行）这类僵尸不再被降级，面板永卡「运行中」。同会话 run
// 串行：装配新 run 的时刻 journal 内全部 start 无 finish 的子 agent 必属
// 已死回合（与 unfinishedSubagents 的「死」口径同源），此时逐个补落
// interrupted finish 行，读侧从此闭合。幂等：补过的 task_id 下次对账
// 不再出现在 unfinished 列表。调用方必须先取 unfinishedSubagents 的结果
// （先算后补，顺序反了列表恒空）。锁纪律：本方法自取 j.mu，调用方不得持锁。
func (j *agentCoreJournal) healInterruptedSubagents(sessionID string, unfinished []agentCoreSubagentInterrupted) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" || len(unfinished) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range unfinished {
		j.appendLineLocked(sessionID, map[string]interface{}{
			"kind":    "subagent",
			"event":   "finish",
			"task_id": e.TaskID,
			"status":  "interrupted",
			"summary": "上次运行中断，自动补记终态",
			"result":  "",
			"ts":      time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// appendSubagentTool 落子 agent 的工具轨迹行：与主会话工具行同 kind=tool，
// 但多带 subagent_task_id 关联字段——读侧将按 task_id 聚合(Task 3 落地)子 agent 轨迹，
// 不与主会话工具混流。守卫与 appendTool 同款；写失败 Warn 吞掉。
func (j *agentCoreJournal) appendSubagentTool(sessionID, taskID, tool, summary string, isError bool) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appendLineLocked(sessionID, map[string]interface{}{
		"kind":             "tool",
		"subagent_task_id": taskID,
		"tool":             tool,
		"summary":          summary,
		"is_error":         isError,
		"ts":               time.Now().UTC().Format(time.RFC3339),
	})
}

// appendSubagentTurn 落 kind=subagent_turn 行：子 run 的每回合 assistant 输出
// （L2 sidechain，2026-11-09）。此前子 run 的 LLM 往来只在内存——进程一死，
// 跑了半小时的成果全丢，journal 只剩工具名列表。落盘后崩溃可查、L3 续跑
// 有据可依。text/thinking 由调用方 rune 安全截断；tokens=本回合用量。
func (j *agentCoreJournal) appendSubagentTurn(sessionID, taskID string, turn int, text, thinking string, tokens int) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(taskID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	row := map[string]interface{}{
		"kind":    "subagent_turn",
		"task_id": taskID,
		"turn":    turn,
		"text":    text,
		"tokens":  tokens,
		"ts":      time.Now().UTC().Format(time.RFC3339),
	}
	// thinking 只在非空时写键（省字节；多数回合为空）。
	if thinking = strings.TrimSpace(thinking); thinking != "" {
		row["thinking"] = thinking
	}
	j.appendLineLocked(sessionID, row)
}

// appendSubagentFinish 落 kind=subagent/event=finish 行：status=completed|failed，
// 带 result 全文与 total_tokens/duration_ms 统计。守卫与 appendTool 同款；
// 写失败 Warn 吞掉。
func (j *agentCoreJournal) appendSubagentFinish(sessionID, taskID, status, summary, result string, totalTokens, durationMs int64) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appendLineLocked(sessionID, map[string]interface{}{
		"kind":         "subagent",
		"event":        "finish",
		"task_id":      taskID,
		"status":       status,
		"summary":      summary,
		"result":       result,
		"total_tokens": totalTokens,
		"duration_ms":  durationMs,
		"ts":           time.Now().UTC().Format(time.RFC3339),
	})
}

// appendApprovalAudit 落 kind=approval 审计行（决策/取消/超时统一入口，
// spec §4.5）。不参与去重、不进历史回放：读侧（inventory/history parser）
// 对未知 kind 忽略。尽力而为；写失败 Warn 吞掉。守卫与 appendTool 同款。
func (j *agentCoreJournal) appendApprovalAudit(sessionID, approvalID string, runSeq int, decision string) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appendLineLocked(sessionID, map[string]interface{}{
		"kind":        "approval",
		"approval_id": approvalID,
		"run_seq":     runSeq,
		"decision":    decision,
		"ts":          time.Now().UTC().Format(time.RFC3339),
	})
}

// appendLineLocked 追加一行；调用方必须持有 j.mu。写失败 Warn 后吞掉，
// 返回是否写成功（mkdir/open/write 任一失败即 false）——appendMsg 靠它
// 决定去重键只记「已落盘」。目录懒创建在此处（O_CREATE 建不了缺失的父
// 目录）——steer 先于 run 到达时 appendUser 是第一个写者，不能依赖
// ensureSessionHeader 先建目录。dirReady 置位后目录被运行期删除不会自愈
// （持续 Warn 失败），仅下次 run 的 ensureSessionHeader 走 Stat 缺失分支
// 时重建——尽力而为语义下的既定取舍。
func (j *agentCoreJournal) appendLineLocked(sessionID string, row map[string]interface{}) bool {
	if !j.dirReady {
		if err := os.MkdirAll(j.dir, 0o700); err != nil {
			logger.Warn(fmt.Sprintf("agentcore-journal: mkdir failed session=%s err=%v", sessionID, err))
			return false
		}
		j.dirReady = true
	}
	line, err := jsonMarshalLine(row)
	if err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: marshal failed session=%s err=%v", sessionID, err))
		return false
	}
	path := filepath.Join(j.dir, agentCoreJournalFileName(sessionID))
	// O_RDWR（不是 O_WRONLY）：下面的半行探测要 ReadAt 读尾字节，Linux 上
	// O_WRONLY 的 ReadAt 返回 EBADF。O_APPEND 保证 Write 总是落到 EOF。
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: open failed session=%s err=%v", sessionID, err))
		return false
	}
	defer f.Close()
	// 残行容忍（写侧）：上一进程可能死在写一半，文件尾没有换行。O_APPEND
	// 直接追加会让新行粘在残行尾巴上、两条一起坏掉（reader 侧双双跳过）。
	// 读尾字节探测，残行先补一个换行封口；残行本身不可修复，交给 reader
	// 按「解析失败跳过」降级。
	if fi, statErr := f.Stat(); statErr == nil && fi.Size() > 0 {
		var tail [1]byte
		if _, rdErr := f.ReadAt(tail[:], fi.Size()-1); rdErr == nil && tail[0] != '\n' {
			if _, wErr := f.Write([]byte{'\n'}); wErr != nil {
				logger.Warn(fmt.Sprintf("agentcore-journal: repair torn tail failed session=%s err=%v", sessionID, wErr))
				return false
			}
		}
	}
	if _, err := f.Write(line); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: append failed session=%s err=%v", sessionID, err))
		return false
	}
	return true
}

// pruneLocked 保留 mtime 最新的 agentCoreJournalMaxFiles 个文件（最旧先删）。
// 在锁内执行；被删文件若仍有活跃写入者，Linux 上写入继续进入已 unlink 的
// inode——该会话后续留痕静默丢失，这是尽力而为 journal 可接受的损失模式，
// 不做「跳过打开中的文件」的复杂化。newSessionID 不参与删除（刚建的）。
func (j *agentCoreJournal) pruneLocked(newSessionID string) {
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return
	}
	keep := agentCoreJournalMaxFiles
	protected := agentCoreJournalFileName(newSessionID)
	type candidate struct {
		name    string
		modTime time.Time
	}
	var files []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || e.Name() == protected {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, candidate{name: e.Name(), modTime: info.ModTime()})
		}
	}
	if len(files) < keep {
		return
	}
	// 最旧在前。
	sort.Slice(files, func(i, k int) bool { return files[i].modTime.Before(files[k].modTime) })
	for _, f := range files[:len(files)-keep+1] {
		if err := os.Remove(filepath.Join(j.dir, f.name)); err != nil {
			logger.Warn(fmt.Sprintf("agentcore-journal: prune %s failed err=%v", f.name, err))
		}
	}
}

// jsonMarshalLine 序列化为一行 JSON（含结尾 \n）。
func jsonMarshalLine(row map[string]interface{}) ([]byte, error) {
	raw, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// agentCoreJournalOriginFor 头行来源统一推导（run 路径与斜杠命令路径共用，
// Task 12 也调它）：以发起 socket 推导——remote 常驻 socket=phone，本地聊天
// socket（aisock-* 前缀）=dashboard；socket 标记缺失（旧调用方/TUI/测试路径）
// 才回落 surface（只认 "phone"，normalizeAgentAISurface 白名单仅此一词——
// 刻意不动它）。phone 会话未来被本机续聊时，origin 忠实记录这一回合是谁发的。
func agentCoreJournalOriginFor(originSocketID, surface string) string {
	switch {
	case originSocketID == agentAIRemoteSocketID:
		return "phone"
	case strings.HasPrefix(originSocketID, "aisock-"):
		return "dashboard"
	case surface == "phone":
		return "phone"
	default:
		return "unknown"
	}
}

func agentCoreJournalOrigin(run agentAIRun) string {
	return agentCoreJournalOriginFor(run.originSocketID, run.surface)
}

// truncateBefore 按「文件位置语义」截断 journal(spec §A.1):保留头行与
// 位置先于 beforeMessageID 所在 msg 行的全部行;end/turn/tool/approval 等
// 非 msg 行归属其前最近的 msg 行所在区间(随宿主保留/丢弃);目标前没有
// 任何 msg 行时只留头行(孤儿非 msg 行丢弃)。目标 msg 行不存在或文件缺失
// 幂等成功(仅日志;「已截断过」与「id 不匹配」两成因不可区分,接受模糊,
// 终态展示以 journal 刷新为准)。同一 beforeMessageID 重复调用幂等,支持
// 撤回重试。锁内全读→tmp→rename,prependHeaderLocked 同款套路。
// 返回 applied=是否实际发生截断。
func (j *agentCoreJournal) truncateBefore(sessionID, beforeMessageID string) (bool, error) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(beforeMessageID) == "" {
		return false, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	path := filepath.Join(j.dir, agentCoreJournalFileName(sessionID))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	lines := splitJSONLLines(data)
	if len(lines) == 0 {
		return false, nil
	}
	// 头行:首行是合法 session 头则原样保留,否则不造头(读侧容忍无头)。
	var buf bytes.Buffer
	var first map[string]interface{}
	start := 0
	if json.Unmarshal(lines[0], &first) == nil && first["kind"] == "session" {
		buf.Write(lines[0])
		buf.WriteByte('\n')
		start = 1
	}
	// 线性扫描:定位目标 msg 行;msg 行携带 message_id,非 msg 行归属区间。
	isMsg := make([]bool, len(lines))
	target := -1
	for i := start; i < len(lines); i++ {
		var row map[string]interface{}
		if json.Unmarshal(lines[i], &row) != nil || row["kind"] != "msg" {
			continue
		}
		isMsg[i] = true
		if target < 0 {
			if id, _ := row["message_id"].(string); id == beforeMessageID {
				target = i
			}
		}
	}
	if target < 0 {
		logger.Info(fmt.Sprintf("agentcore-journal: truncate no-op (target absent) session=%s before=%s", sessionID, beforeMessageID))
		return false, nil
	}
	firstKeep := -1
	for i := start; i < target; i++ {
		if isMsg[i] {
			firstKeep = i
			break
		}
	}
	kept := 0
	if firstKeep >= 0 {
		kept = target - firstKeep // 首个 msg 行之前的孤儿非 msg 行不计入保留区间,并随本次截断一并丢弃(不写入保留区)
		for i := firstKeep; i < target; i++ {
			buf.Write(lines[i])
			buf.WriteByte('\n')
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	// 整文件重写 → 装配期增量游标失效（unfinishedSubagents 下次整份重扫）。
	delete(j.unfinishedScan, sessionID)
	logger.Info(fmt.Sprintf("agentcore-journal: truncated session=%s before=%s kept=%d/%d lines", sessionID, beforeMessageID, kept, len(lines)-start))
	return true, nil
}
