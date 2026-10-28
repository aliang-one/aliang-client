package services

// agentcore（AliangCode）调用记录 journal：会话回合期间把对话逐行追加到
// <agentHome()>/.aliang/ai-sessions/<sessionID>.jsonl，供仪表盘 AI 会话
// 历史列表与详情回放扫描。设计文档：
// docs/superpowers/specs/2026-10-26-agentcore-journal-design.md
//
// 尽力而为留痕：任何写失败只 Warn，绝不影响 run。

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

// ensureSessionHeader 保证会话文件存在且第一行是头行；只在文件不存在时写。
// 顺带触发 prune（在锁内）。
func (j *agentCoreJournal) ensureSessionHeader(sessionID, projectPath, model, createdAt string) {
	if j == nil || j.dir == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	path := filepath.Join(j.dir, agentCoreJournalFileName(sessionID))
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: mkdir failed session=%s err=%v", sessionID, err))
		return
	}
	j.dirReady = true
	line, err := jsonMarshalLine(map[string]interface{}{
		"kind":         "session",
		"session_id":   sessionID,
		"project_path": projectPath,
		"model":        model,
		"created_at":   createdAt,
	})
	if err != nil {
		logger.Warn(fmt.Sprintf("agentcore-journal: header marshal failed session=%s err=%v", sessionID, err))
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
