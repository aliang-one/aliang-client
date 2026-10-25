package services

// 设备间文件互传（同账号）的两侧共用底座。spec:
// docs/superpowers/specs/2026-10-25-device-file-transfer-design.md
//
// 分工：agent_transfer_upload.go 是发送链（分块直传 COS），agent_transfer_receive.go
// 是接收链（Range 续传 + sha256 校验 + 收件围栏），agent_transfer_inbox.go 是
// 收件箱/对账 HTTP 客户端；本文件只放它们共享的原语——错误码、文件名清洗、
// 并发槽、进度节流、帧发射、COS 域钉死、收件根与持久去重标记。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// transfer.result.error_code。服务端按码决定终态与否：receiver_busy 是
// 非终态（保留 ready 指数退避重推），其余一律终态。
const (
	transferErrShaMismatch      = "sha_mismatch"
	transferErrFenceEscape      = "fence_escape"
	transferErrReceiverBusy     = "receiver_busy"
	transferErrUploaderBusy     = "uploader_busy"
	transferErrDiskFull         = "disk_full"
	transferErrDownloadFailed   = "download_failed"
	transferErrUploadFailed     = "upload_failed"
	transferErrCancelled        = "cancelled"
	transferErrTooLarge         = "too_large"
	transferErrPathNotFound     = "path_not_found"
	transferErrPathTypeMismatch = "path_type_mismatch"
	transferErrBadFilename      = "bad_filename"
	transferErrExtractFailed    = "extract_failed"
	transferErrInternal         = "internal"
)

const (
	agentTransferDirectionUpload  = "upload"
	agentTransferDirectionReceive = "receive"
)

// --------------------------------------------------------------------------
// 文件名清洗（D9）
// --------------------------------------------------------------------------

const agentTransferFileNameMaxBytes = 200

// sanitizeTransferFileName 清洗互传文件名。语义与 safeAttachmentFileName 相反：
// 那个是 ASCII 白名单（中文名整串变下划线，已有测试固化），互传要求保留原名，
// 因此这里保留 Unicode 字母数字与 `.` `-` `_` 空格，其余替换 `_`；危险输入
// **拒绝**而非静默改名——静默改名会把穿越企图变成一个看似无害的新文件。
// 拒绝：空名、路径分隔符（/ 与 \，Windows 上 \ 也是分隔符）、NUL、`.`/`..`、
// Windows 保留设备名（含 CON.txt 形态）、结尾点/空格。超长按字节截断（保留
// 扩展名），截断不算错误。这是接收端落盘前的真正围栏；服务端只做展示。
func sanitizeTransferFileName(name string) (string, error) {
	// 有意不做 TrimSpace：结尾点/空格在 Windows 落盘时会被系统静默吞掉，造成
	// 重名歧义（D9：危险输入拒绝而非静默改名）。只有全空白按空名拒。
	if strings.TrimSpace(name) == "" {
		return "", errors.New("transfer filename is empty")
	}
	if strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("transfer filename contains a path separator or NUL")
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("transfer filename is a path segment")
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			unicode.IsLetter(r), unicode.IsDigit(r),
			r == '.', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	cleaned := b.String()
	if len(cleaned) > agentTransferFileNameMaxBytes {
		ext := filepath.Ext(cleaned)
		stem := strings.TrimSuffix(cleaned, ext)
		budget := agentTransferFileNameMaxBytes - len(ext)
		if budget < 1 {
			cleaned = truncateAgentTransferNameBytes(cleaned, agentTransferFileNameMaxBytes)
		} else {
			cleaned = truncateAgentTransferNameBytes(stem, budget) + ext
		}
	}
	// 保留名与尾点/尾空格两道闸必须在**截断之后**对终名复查：否则超长截断
	// 可把一个已过检的名字截回保留名或以点结尾（如 "CONFIG."+196 个 A）。
	if isWindowsReservedTransferName(cleaned) {
		return "", fmt.Errorf("transfer filename uses a reserved device name")
	}
	if cleaned != strings.TrimRight(cleaned, ". ") {
		return "", fmt.Errorf("transfer filename ends with a dot or space")
	}
	return cleaned, nil
}

// isWindowsReservedTransferName 报告 name 的主名（第一个点之前）是否 Windows
// 保留设备名。无论当前平台是什么都拒收：同名文件将来落到 Windows 设备的
// 收件目录时会静默变成设备引用。
func isWindowsReservedTransferName(name string) bool {
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

func truncateAgentTransferNameBytes(s string, budget int) string {
	if budget < 1 || len(s) <= budget {
		return s
	}
	runes := []rune(s)
	for len(runes) > 1 && len(string(runes)) > budget {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

// --------------------------------------------------------------------------
// 错误脱敏
// --------------------------------------------------------------------------

// agentTransferSecretPattern 匹配错误文本里可能嵌带的预签名 URL 与签名参数。
// 预签名 URL 是纯 bearer 凭据，绝不能原样回传进 transfer.result.error。
var agentTransferSecretPattern = regexp.MustCompile(
	`(?i)(https?://[^\s]+|q-signature=[^&\s]+|q-ak=[^&\s]+|q-sign-time=[^&\s]+|uploadId=[^&\s]+|signature=[^&\s]+)`)

// sanitizeTransferError 产出可回传的错误文本：先过 sanitizeUploadError（剥
// *url.Error 的完整 URL，复用 file.upload 语义），再把残留文本里的预签名
// URL/签名参数打码，最后限长。返回值直接放进 transfer.result.error。
func sanitizeTransferError(err error) string {
	if err == nil {
		return ""
	}
	msg := sanitizeUploadError(err).Error()
	msg = agentTransferSecretPattern.ReplaceAllString(msg, "[redacted]")
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "transfer failed"
	}
	if len(msg) > 500 {
		// 按字节截断，保住 UTF-8 边界（避免多字节字符被拦腰切开）。
		cut := 500
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut]
	}
	return msg
}

// --------------------------------------------------------------------------
// COS 域钉死（spec §9.4 纵深防御）
// --------------------------------------------------------------------------

// agentTransferHostAllowed 校验字节面（分块 PUT / GET）URL：https、无显式
// 端口、host 必须是腾讯云 COS 源站域（*.myqcloud.com）。报错文本不回显
// URL（那会泄漏签名）。互传把上行常态化，须比既有 file.upload 收紧；测试
// 用假 COS 服务时整体替换此变量。
var agentTransferHostAllowed = func(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("transfer url is not parseable")
	}
	if u.Scheme != "https" {
		return errors.New("transfer url must be https")
	}
	if u.Port() != "" {
		return errors.New("transfer url must not carry an explicit port")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || !(host == "myqcloud.com" || strings.HasSuffix(host, ".myqcloud.com")) {
		return errors.New("transfer url host is not an allowed COS domain")
	}
	return nil
}

// --------------------------------------------------------------------------
// 并发槽（upload / receive 各一张，按 transfer_id 索引）
// --------------------------------------------------------------------------

type agentTransferJob struct {
	transferID string
	cancel     context.CancelFunc
}

// agentTransferSlot 按 transfer_id 索引的在飞任务表：同 id 幂等（重复投递
// 直接拒绝），容量满按 busy 拒绝。有意不动 file.upload 的全局单飞槽
// （agent_file_upload.go）——互传与手机下载是两条独立通道，互不占位。
type agentTransferSlot struct {
	mu   sync.Mutex
	jobs map[string]*agentTransferJob
	cap  int
}

func newAgentTransferSlot(capacity int) *agentTransferSlot {
	return &agentTransferSlot{jobs: make(map[string]*agentTransferJob), cap: capacity}
}

// acquire 登记在飞任务。返回 (ok, duplicate)：ok=false 且 duplicate=true 表示
// 同 id 已在飞（WS 重推/hello 重驱的重复投递，调用方幂等忽略）；ok=false 且
// duplicate=false 表示容量满（调用方按 busy 回非终态失败）。
func (s *agentTransferSlot) acquire(transferID string, cancel context.CancelFunc) (bool, bool) {
	if transferID == "" {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[transferID]; ok {
		return false, true
	}
	if len(s.jobs) >= s.cap {
		return false, false
	}
	s.jobs[transferID] = &agentTransferJob{transferID: transferID, cancel: cancel}
	return true, false
}

func (s *agentTransferSlot) release(transferID string) {
	s.mu.Lock()
	delete(s.jobs, transferID)
	s.mu.Unlock()
}

// cancelIfActive 取消在飞任务；未在飞（含已完成/从未开始）no-op 返回 false。
// transfer.cancel 按 transfer_id 匹配，不匹配即 no-op（spec §6.2）。
func (s *agentTransferSlot) cancelIfActive(transferID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[transferID]
	if !ok {
		return false
	}
	job.cancel()
	return true
}

// 发送并发 2（一句话触发两个传输不打架，见 AI 工具面评审 3.5 单槽太粗）；
// 接收并发 1（receiver_busy 语义的前提就是单收）。
// 槽经 atomic.Pointer 读取：测试重置与在飞 goroutine（含 cancel 臂的孤儿
// goroutine）之间才有 happens-before 边，包级裸变量替换会被 -race 判数据竞争。
var (
	agentTransferUploadSlotPtr  atomic.Pointer[agentTransferSlot]
	agentTransferReceiveSlotPtr atomic.Pointer[agentTransferSlot]
)

func init() { resetAgentTransferSlots() }

func transferUploadSlot() *agentTransferSlot  { return agentTransferUploadSlotPtr.Load() }
func transferReceiveSlot() *agentTransferSlot { return agentTransferReceiveSlotPtr.Load() }

func resetAgentTransferSlots() {
	agentTransferUploadSlotPtr.Store(newAgentTransferSlot(2))
	agentTransferReceiveSlotPtr.Store(newAgentTransferSlot(1))
}

func resetAgentTransferSlotsForTest() { resetAgentTransferSlots() }

// agentTransferIDPattern transfer_id 白名单：服务端签发的 tr_<nanoid> 形态。
// transfer_id 会被拼进 .partial 落盘路径与 marker 文件名，必须先过这道闸
// 才能进任何路径拼接（否则 ../ 形态可穿透收件围栏）。
var agentTransferIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validAgentTransferID(id string) bool { return agentTransferIDPattern.MatchString(id) }

// --------------------------------------------------------------------------
// 进度节流（5% / 1s，照 file.upload 常量语义）
// --------------------------------------------------------------------------

const (
	agentTransferProgressInterval = time.Second
	agentTransferProgressFraction = 0.05
)

// agentTransferThrottle 沿用 file.upload 的节流判定：delta<5% 且 elapsed<1s
// 才跳过，任一满足即发。total<=0（流式打包总量未知）退化为纯 1s 节流；
// 首帧必发。
type agentTransferThrottle struct {
	total    int64
	lastSent atomic.Int64 // unix nano；0 = 从未发过
	lastDone atomic.Int64
}

func newAgentTransferThrottle(total int64) *agentTransferThrottle {
	return &agentTransferThrottle{total: total}
}

func (t *agentTransferThrottle) shouldEmit(done int64, now time.Time) bool {
	last := t.lastSent.Load()
	if last == 0 {
		t.lastSent.Store(now.UnixNano())
		t.lastDone.Store(done)
		return true
	}
	elapsed := now.Sub(time.Unix(0, last))
	delta := done - t.lastDone.Load()
	if t.total > 0 && float64(delta) >= float64(t.total)*agentTransferProgressFraction {
		t.lastSent.Store(now.UnixNano())
		t.lastDone.Store(done)
		return true
	}
	if elapsed >= agentTransferProgressInterval {
		t.lastSent.Store(now.UnixNano())
		t.lastDone.Store(done)
		return true
	}
	return false
}

// --------------------------------------------------------------------------
// 帧组装
// --------------------------------------------------------------------------

// agentTransferInt64 从 JSON 消息取 int64。ReadJSON 出来的数字是 float64，
// 包级 remoteInt 返回 int，2GB 量级在 64 位平台没问题但别赌；这里直接按
// 数值类型解。
func agentTransferInt64(msg map[string]interface{}, key string) int64 {
	switch v := msg[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

type agentTransferIdentity struct {
	RequestID          string
	TransferID         string
	Filename           string
	SizeBytes          int64
	SHA256             string
	MaxBytes           int64
	IsDirectoryArchive bool
}

func parseAgentTransferIdentity(msg map[string]interface{}) agentTransferIdentity {
	return agentTransferIdentity{
		RequestID:          remoteString(msg, "request_id"),
		TransferID:         remoteString(msg, "transfer_id"),
		Filename:           remoteString(msg, "filename"),
		SizeBytes:          agentTransferInt64(msg, "size_bytes"),
		SHA256:             remoteString(msg, "sha256"),
		MaxBytes:           agentTransferInt64(msg, "max_bytes"),
		IsDirectoryArchive: remoteBool(msg, "is_directory_archive", false),
	}
}

func agentTransferProgressPayload(requestID, transferID, direction string, bytesDone, totalBytes int64, partNumber int) map[string]interface{} {
	p := map[string]interface{}{
		"type":        models.AgentEventTransferProgress,
		"transfer_id": transferID,
		"direction":   direction,
		"bytes_done":  bytesDone,
		"total_bytes": totalBytes,
	}
	if requestID != "" {
		p["request_id"] = requestID
	}
	if partNumber > 0 {
		p["part_number"] = partNumber
	}
	return p
}

func agentTransferResultPayload(requestID, transferID string, ok bool) map[string]interface{} {
	p := map[string]interface{}{
		"type":        models.AgentEventTransferResult,
		"transfer_id": transferID,
		"ok":          ok,
	}
	if requestID != "" {
		p["request_id"] = requestID
	}
	return p
}

// emitAgentTransferFailure 组装并写出失败帧。error 过 sanitizeTransferError
// （剥预签名 URL），error_code 决定服务端终态与否。
func emitAgentTransferFailure(writeJSON func(interface{}) error, requestID, transferID, code string, err error) error {
	p := agentTransferResultPayload(requestID, transferID, false)
	p["error_code"] = code
	p["error"] = sanitizeTransferError(err)
	if writeJSON == nil {
		return nil
	}
	return writeJSON(p)
}

// --------------------------------------------------------------------------
// 源路径解析
// --------------------------------------------------------------------------

// resolveAgentTransferSourcePath 解析 transfer.upload 的 source_path。互传
// 没有 project_path 语义（手机入口传设备上的路径），相对路径锚定登录用户
// 家目录（root agent 场景走 EffectiveAgentHome，防落到 /root）；~ 展开 +
// Abs + EvalSymlinks 归一。运营已移除读围栏（agent_detail.go 448-480），
// 发送端只做规范化，不做目录白名单。
func resolveAgentTransferSourcePath(msg map[string]interface{}) (string, error) {
	raw := strings.TrimSpace(remoteString(msg, "source_path"))
	if raw == "" {
		return "", errors.New("transfer.upload missing source_path")
	}
	if expanded, err := cache.ExpandHomePath(raw); err == nil {
		raw = expanded
	}
	if !filepath.IsAbs(raw) {
		if home := agentHome(); home != "" {
			raw = filepath.Join(home, raw)
		}
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs, nil
}

// --------------------------------------------------------------------------
// 收件根与持久去重标记（D6）
// --------------------------------------------------------------------------

// agentTransferInboxDir 收件根：<EffectiveAgentHome>/AliangInbox/。root agent
// 场景走 EffectiveAgentHome 解析登录用户家目录（防落 /var/root，TUI 缓存同
// 款教训）；env ALIANG_TRANSFER_INBOX_DIR 覆盖（照 ALIANG_ATTACHMENT_CACHE_DIR
// 的优先级模型）。
func agentTransferInboxDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("ALIANG_TRANSFER_INBOX_DIR")); v != "" {
		if abs, err := filepath.Abs(v); err == nil {
			return abs, nil
		}
	}
	home := agentHome()
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return "", errors.New("cannot determine transfer inbox home directory")
	}
	return filepath.Join(home, "AliangInbox"), nil
}

const agentTransferMarkerDirName = ".aliang-transfer"

// agentTransferReceiveMaxBytes 接收闸的本地兜底上限：线上 max_bytes 缺失时
// 生效（对端虚报 size_bytes 的防线，不依赖服务端字段下发）。取服务端
// TRANSFER_MAX_BYTES 的钳制上界同值，正常传输不会被误伤。
const agentTransferReceiveMaxBytes = int64(8) << 30

// agentTransferMarker 持久去重标记（D6）：WS 定向推送与 inbox 拉取可能重复
// 投递同一 transfer，内存去重挡不住进程重启，落盘到收件根的隐藏子目录。
type agentTransferMarker struct {
	TransferID string `json:"transfer_id"`
	State      string `json:"state"` // receiving | done | failed
	Path       string `json:"path,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	UpdatedAt  string `json:"updated_at"`
}

const (
	transferMarkerStateReceiving = "receiving"
	transferMarkerStateDone      = "done"
	transferMarkerStateFailed    = "failed"
)

func agentTransferMarkerPath(transferID string) (string, error) {
	root, err := agentTransferInboxDir()
	if err != nil {
		return "", err
	}
	if !validAgentTransferID(transferID) {
		return "", errors.New("invalid transfer id for marker")
	}
	return filepath.Join(root, agentTransferMarkerDirName, transferID+".json"), nil
}

// readAgentTransferMarker 读标记；不存在返回 (nil, nil)，损坏按不存在处理
// （宁可重收一次，也不让一个坏文件永久堵死该 transfer）。
func readAgentTransferMarker(transferID string) (*agentTransferMarker, error) {
	path, err := agentTransferMarkerPath(transferID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, nil
	}
	var m agentTransferMarker
	if err := json.Unmarshal(raw, &m); err != nil || m.TransferID == "" {
		return nil, nil
	}
	return &m, nil
}

// writeAgentTransferMarker 原子写标记（temp + rename，照 TUI 缓存变体的
// 手法，但失败不删最终文件——这里写的是全新文件，rename 失败即清理 temp）。
func writeAgentTransferMarker(m *agentTransferMarker) error {
	if m == nil || m.TransferID == "" {
		return errors.New("invalid transfer marker")
	}
	if m.UpdatedAt == "" {
		m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	dir, err := agentTransferMarkerPath(m.TransferID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dir), ".marker-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dir); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// removeAgentTransferMarker 清掉标记（failed 收尾时；保留会挡住服务端对同
// transfer 的合法重试）。
func removeAgentTransferMarker(transferID string) {
	if path, err := agentTransferMarkerPath(transferID); err == nil {
		_ = os.Remove(path)
	}
}
