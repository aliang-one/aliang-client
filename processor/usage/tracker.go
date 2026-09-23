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
			// 预算检查放在回调最前：以非 jsonl 为主的目录树同样受 2s 上限
			// 约束，不会无界空走过预算。
			if time.Now().After(deadline) {
				completePass = false
				return fs.SkipAll
			}
			if err != nil {
				return nil // 目录可能随时变动，跳过不可达子树
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			seen = append(seen, path)
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
	// 文件被截断/重建（备份恢复等）：尺寸小于水位时归零重扫。两个方向的
	// 偏差均为 spec §7 容忍的感性误差：截断 ⇒ 重扫重复计入；截断后又长回
	// 更大 ⇒ 水位未复位，重新长出的 [0,offset) 区间被跳过造成有界少计。
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

	// 注意顺序：先落桶再推水位——崩溃在两者之间只会造成有界的重复累计
	//（重读重加），绝不会静默丢数据（质量审查确认的刻意取舍）。
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
