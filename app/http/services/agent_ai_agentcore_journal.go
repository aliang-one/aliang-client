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

// agentCoreJournalWrittenCap 去重集合上限：超过即整体清空。去重只防同进程内
// 同一消息写两行，进程重启集合清零没有正确性问题（重发即新 messageID）。
const agentCoreJournalWrittenCap = 4096

type agentCoreJournal struct {
	mu       sync.Mutex
	dir      string
	dirReady bool
	written  map[string]struct{}
}

func newAgentCoreJournal(dir string) *agentCoreJournal {
	return &agentCoreJournal{dir: strings.TrimSpace(dir), written: make(map[string]struct{})}
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
func (j *agentCoreJournal) ensureSessionHeader(sessionID, projectPath, model, effort, createdAt, origin string) {
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
		j.prependHeaderLocked(path, sessionID, projectPath, model, effort, createdAt, origin)
		return
	}
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: mkdir failed session=%s err=%v", sessionID, err))
		return
	}
	j.dirReady = true
	line := j.sessionHeaderLine(sessionID, projectPath, model, effort, createdAt, origin)
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
func (j *agentCoreJournal) sessionHeaderLine(sessionID, projectPath, model, effort, createdAt, origin string) []byte {
	line, err := jsonMarshalLine(map[string]interface{}{
		"kind":         "session",
		"session_id":   sessionID,
		"project_path": projectPath,
		"model":        model,
		"effort":       effort,
		"created_at":   createdAt,
		"origin":       origin,
	})
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

// prependHeaderLocked 整体重写补头：tmp 文件写 头行+原内容，rename 原子替换。
// 在 j.mu 锁内做全量读+写+rename——journal 文件有界（消息+截断工具行），
// 重写代价可接受；与 appendLineLocked 同锁互斥，无并发写坏。调用方必须持有 j.mu。
func (j *agentCoreJournal) prependHeaderLocked(path, sessionID, projectPath, model, effort, createdAt, origin string) {
	head := j.sessionHeaderLine(sessionID, projectPath, model, effort, createdAt, origin)
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

func (j *agentCoreJournal) appendUser(sessionID, messageID, text, ts string) {
	j.appendMsg(sessionID, "user", messageID, text, ts)
}

func (j *agentCoreJournal) appendAssistant(sessionID, messageID, text, ts string) {
	j.appendMsg(sessionID, "assistant", messageID, text, ts)
}

func (j *agentCoreJournal) appendMsg(sessionID, role, messageID, text, ts string) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" || strings.TrimSpace(text) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	key := sessionID + "\x00" + role + "\x00" + messageID
	if _, dup := j.written[key]; dup {
		return
	}
	if !j.appendLineLocked(sessionID, map[string]interface{}{
		"kind":       "msg",
		"role":       role,
		"message_id": messageID,
		"text":       text,
		"ts":         ts,
	}) {
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
