package services

// 设备间互传·接收链：transfer.receive → 收件围栏 → .partial 落盘（Range
// 续传 + If-Range 绑对象身份）→ sha256 校验 → 原子 rename（重名不覆盖）→
// 目录则解压 → HTTP confirm 双保险。
//
// 落盘是第三种变体（D5），与既有两种（临时目录随 run 删 / TUI 缓存哈希改名）
// 语义相反处：失败保留 .partial 供续传、保留原名、rename 前查重、无 TTL 清理。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
)

const (
	agentTransferDownloadAttempts   = 3              // GET URL 过期重签后的整段尝试上限
	agentTransferReceiveRetryDelay  = 2 * time.Second
	agentTransferReceiveHTTPTimeout = 0 // 不设整体超时：2GB 公网链路靠 ctx cancel（见 D16 讨论）
)

// --------------------------------------------------------------------------
// /download-url 重签与 /confirm（包级变量以便测试替身替换）
// --------------------------------------------------------------------------

var agentTransferFetchDownloadURL = func(s *AgentService, transferID string) (string, error) {
	raw, err := s.callAgentServer(http.MethodGet, agentTransferServerURL("/api/transfers/"+url.PathEscape(transferID)+"/download-url"), nil, "")
	if err != nil {
		return "", err
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	if strings.TrimSpace(body.URL) == "" {
		return "", errors.New("download-url resign returned empty url")
	}
	return body.URL, nil
}

var agentTransferSendConfirm = func(s *AgentService, transferID string, payload map[string]interface{}) error {
	_, err := s.callAgentServer(http.MethodPost, agentTransferServerURL("/api/transfers/"+url.PathEscape(transferID)+"/confirm"), payload, "")
	return err
}

// --------------------------------------------------------------------------
// handler
// --------------------------------------------------------------------------

func (s *AgentService) handleAgentTransferReceive(msg map[string]interface{}, writeJSON func(interface{}) error) {
	ident := parseAgentTransferIdentity(msg)
	// transfer_id 白名单闸（P1 复核项）：它会被拼进 .partial 落盘路径、marker
	// 文件名与服务端 API 路径，`../` 形态可穿透收件围栏写任意路径——在任何
	// 路径拼接之前就地拒绝。
	if !validAgentTransferID(ident.TransferID) {
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, "", transferErrBadFilename, errors.New("transfer.receive has an invalid transfer_id"))
		return
	}
	getURL := strings.TrimSpace(remoteString(msg, "get_url"))
	if getURL == "" {
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrDownloadFailed, errors.New("transfer.receive missing get_url"))
		return
	}
	objectETag := remoteString(msg, "object_etag")
	targetDir := remoteString(msg, "target_dir")
	// 阶段 2 LAN 直连候选(spec §6):服务端协商成功才带;畸形候选就地降级为
	// nil(纯 COS),绝不让坏帧打断既有链路。
	lanCandidate := parseAgentLanDirectCandidates(msg)

	// D6 幂等：已完成的重复投递不再收第二次，补发 confirm 自愈后返回。
	if marker, _ := readAgentTransferMarker(ident.TransferID); marker != nil && marker.State == transferMarkerStateDone {
		logger.Info(fmt.Sprintf("[TRANSFER] receive duplicate for finished transfer_id=%s, re-confirming", ident.TransferID))
		go sendAgentTransferConfirm(s, ident, marker.Path)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	ok, duplicate := transferReceiveSlot().acquire(ident.TransferID, cancel)
	if !ok {
		cancel()
		if duplicate {
			logger.Info(fmt.Sprintf("[TRANSFER] receive duplicate delivery ignored transfer_id=%s", ident.TransferID))
			return
		}
		// 单收槽被占：非终态 busy（D15），服务端保留 ready 退避重推。
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrReceiverBusy, errors.New("receiver is busy with another transfer"))
		return
	}
	defer transferReceiveSlot().release(ident.TransferID)
	defer cancel()

	logger.Info(fmt.Sprintf("[TRANSFER] receive begin transfer_id=%s file=%q dir=%q size=%d archive=%t lan=%t",
		ident.TransferID, ident.Filename, targetDir, ident.SizeBytes, ident.IsDirectoryArchive, lanCandidate != nil))
	startAgentTransferReceive(ctx, s, ident, getURL, objectETag, targetDir, lanCandidate, writeJSON)
}

func startAgentTransferReceive(ctx context.Context, s *AgentService, ident agentTransferIdentity, getURL, objectETag, targetDir string, lanCandidate *agentLanDirectCandidate, writeJSON func(interface{}) error) {
	inbox, err := agentTransferInboxDir()
	if err != nil {
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrInternal, err)
		return
	}
	safeName, err := sanitizeTransferFileName(ident.Filename)
	if err != nil {
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrBadFilename, err)
		return
	}
	landDir, err := agentTransferLandingDir(inbox, targetDir)
	if err != nil {
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrFenceEscape, err)
		return
	}
	if err := os.MkdirAll(landDir, 0o755); err != nil {
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrInternal, err)
		return
	}

	// 接收闸不信任线上字段：max_bytes 缺失（老服务端/漏发）时落本地常量，
	// 防「虚报 size_bytes 撑大整段下载」——线上值恒 0 会让 too_large 闸变
	// 死代码（P2 复核项）。
	if ident.MaxBytes <= 0 {
		ident.MaxBytes = agentTransferReceiveMaxBytes
	}

	partialPath := agentTransferPartialPath(inbox, ident.TransferID, safeName)

	// 阶段 2 LAN 直连腿(spec §6):候选在手就先试直连,任何失败(拨号/握手/
	// 中途/sha 不符)静默落回 COS——COS 对象是真值源,「现链路零改动」。
	// skip 类错误(partial 已有偏移/归档)同样直接走 COS,不算失败。
	via := "cos"
	lanFailStage := ""
	var finalSize int64
	var finalSHA string
	if lanCandidate != nil {
		size, sha, lanErr := attemptAgentTransferLANDownload(ctx, ident, lanCandidate, partialPath, writeJSON)
		switch {
		case lanErr == nil:
			finalSize, finalSHA, via = size, sha, "lan"
			logger.Info(fmt.Sprintf("[TRANSFER] lan direct complete transfer_id=%s bytes=%d", ident.TransferID, finalSize))
		case errors.Is(lanErr, errAgentTransferLanSkip):
			logger.Info(fmt.Sprintf("[TRANSFER] lan direct skipped transfer_id=%s: %v", ident.TransferID, lanErr))
		default:
			// 分级只用于日志/审计回带:A 端行为统一=落 COS。
			lanFailStage = classifyAgentLanFailStage(lanErr)
			logger.Info(fmt.Sprintf("[TRANSFER] lan direct failed transfer_id=%s stage=%s err=%v — falling back to COS",
				ident.TransferID, lanFailStage, lanErr))
			if cleanupErr := os.Remove(partialPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
				logger.Warn(fmt.Sprintf("[TRANSFER] lan partial cleanup failed transfer_id=%s err=%v", ident.TransferID, cleanupErr))
			}
		}
	}
	if via == "cos" {
		finalSize, finalSHA, err = downloadAgentTransferPayload(ctx, s, ident, getURL, objectETag, partialPath, writeJSON)
		if err != nil {
			handleAgentTransferReceiveFailure(ctx, s, ident, partialPath, err, writeJSON)
			return
		}
	}

	finalPath := agentTransferFinalPath(landDir, safeName)
	if ctx.Err() != nil {
		// 取消落在 rename 前：partial 由失败出口统一清理。
		handleAgentTransferReceiveFailure(ctx, s, ident, partialPath, ctx.Err(), writeJSON)
		return
	}
	if err := os.Rename(partialPath, finalPath); err != nil {
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrInternal, err)
		return
	}
	_ = os.Chmod(finalPath, 0o644)

	// 目录归档：解压到 <landDir>/<stem>/，成功后删掉 zip 本体（省一份盘）。
	if ident.IsDirectoryArchive {
		extractDir, err := extractAgentTransferArchive(ctx, landDir, finalPath, safeName)
		if err != nil {
			// zip 保留待查；半成品目录由解压器自身清理（失败即整体失败）。
			// 围栏类拒绝（zip-slip/炸弹/保留名）按错误码契约归 fence_escape，
			// 取消归 cancelled，普通解压失败才是 extract_failed（P2 复核项）。
			code := transferErrExtractFailed
			switch {
			case errors.Is(err, context.Canceled):
				code = transferErrCancelled
			case errors.Is(err, errAgentTransferZipUnsafe):
				code = transferErrFenceEscape
			}
			_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, code, err)
			return
		}
		finalPath = extractDir
	}

	// rename/解压之后才到的取消：清掉已落盘成品，按 cancelled 收尾——
	// 不写 done marker、不发 ok/confirm（取消必须完全失效，P2 复核项）。
	if ctx.Err() != nil {
		_ = os.RemoveAll(finalPath)
		handleAgentTransferReceiveFailure(ctx, s, ident, partialPath, ctx.Err(), writeJSON)
		return
	}

	_ = writeAgentTransferMarker(&agentTransferMarker{
		TransferID: ident.TransferID,
		State:      transferMarkerStateDone,
		Path:       finalPath,
		SizeBytes:  finalSize,
		SHA256:     finalSHA,
	})

	payload := agentTransferResultPayload(ident.RequestID, ident.TransferID, true)
	payload["path"] = finalPath
	payload["size_bytes"] = finalSize
	payload["sha256"] = finalSHA
	// 阶段 2 LAN 直连:实际走的路径回带(via=lan|cos),服务端据此落审计
	// (spec §6.3「全部协商结果服务端留痕」)。
	payload["via"] = via
	if lanFailStage != "" {
		payload["lan_fail_stage"] = lanFailStage
	}
	if err := writeJSON(payload); err != nil {
		logger.Warn(fmt.Sprintf("[TRANSFER] receive result write failed transfer_id=%s err=%v", ident.TransferID, err))
	}
	// HTTP confirm 双通道并存，HTTP 为准（断线丢 WS result 的兜底，spec §10）。
	go sendAgentTransferConfirm(s, ident, finalPath)
	logger.Info(fmt.Sprintf("[TRANSFER] receive done transfer_id=%s path=%s bytes=%d", ident.TransferID, finalPath, finalSize))
}

// handleAgentTransferReceiveFailure 统一失败出口：取消时清 .partial（显式
// 取消意味着放弃；网络失败保留 partial 供续传），磁盘满单独归类。
func handleAgentTransferReceiveFailure(ctx context.Context, s *AgentService, ident agentTransferIdentity, partialPath string, err error, writeJSON func(interface{}) error) {
	code := transferErrDownloadFailed
	switch {
	case errors.Is(err, context.Canceled):
		code = transferErrCancelled
		_ = os.Remove(partialPath)
		_ = writeAgentTransferMarker(&agentTransferMarker{TransferID: ident.TransferID, State: transferMarkerStateFailed})
	case isAgentTransferDiskFull(err):
		code = transferErrDiskFull
	case errors.Is(err, errAgentTransferTooLarge):
		code = transferErrTooLarge
	case errors.Is(err, errAgentTransferSizeMismatch):
		code = transferErrDownloadFailed
	case errors.Is(err, errAgentTransferShaMismatch):
		code = transferErrShaMismatch
		// sha 不匹配：成品与 partial 一并删（内容已不可信）。
		_ = os.Remove(partialPath)
		_ = writeAgentTransferMarker(&agentTransferMarker{TransferID: ident.TransferID, State: transferMarkerStateFailed})
	case errors.Is(err, errAgentTransferFence):
		code = transferErrFenceEscape
	}
	_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, code, err)
}

// --------------------------------------------------------------------------
// 下载主体：Range 续传 + If-Range 对象身份绑定（D16）+ 边下边算 sha256
// --------------------------------------------------------------------------

var errAgentTransferShaMismatch = errors.New("transfer sha256 mismatch")

// errAgentTransferSizeMismatch 实收字节数与登记 size_bytes 不符（含 416 断点
// 越界）——终态，不重试。
var errAgentTransferSizeMismatch = errors.New("transfer size mismatch")

// verifyAgentTransferCompletePartial 断点恰等于 size_bytes 的收尾校验：
// 前缀喂 hasher → sha 比对；命中即返回让上层走 rename/marker/confirm。
func verifyAgentTransferCompletePartial(ident agentTransferIdentity, partialPath string, offset int64) (int64, string, error) {
	hasher := sha256.New()
	if err := feedAgentTransferPartialPrefix(partialPath, hasher); err != nil {
		return 0, "", err
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(sum, strings.TrimSpace(ident.SHA256)) {
		return 0, "", fmt.Errorf("%w: completed partial hash mismatch", errAgentTransferShaMismatch)
	}
	return offset, sum, nil
}
var errAgentTransferFence = errors.New("transfer path escaped inbox fence")

func downloadAgentTransferPayload(ctx context.Context, s *AgentService, ident agentTransferIdentity, getURL, objectETag, partialPath string, writeJSON func(interface{}) error) (int64, string, error) {
	var lastErr error
	for attempt := 0; attempt < agentTransferDownloadAttempts; attempt++ {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		if attempt > 0 {
			select {
			case <-time.After(agentTransferReceiveRetryDelay):
			case <-ctx.Done():
				return 0, "", ctx.Err()
			}
			// 重签拿新 GET URL（403/404 → 签名过期；也覆盖 URL 被服务端轮换）。
			fresh, err := agentTransferFetchDownloadURL(s, ident.TransferID)
			if err != nil {
				lastErr = fmt.Errorf("resign download url failed: %w", err)
				continue
			}
			getURL = fresh
		}
		if err := agentTransferHostAllowed(getURL); err != nil {
			return 0, "", err
		}
		size, sum, err := attemptAgentTransferDownload(ctx, ident, getURL, objectETag, partialPath, writeJSON)
		if err == nil {
			return size, sum, nil
		}
		if errors.Is(err, errAgentTransferResignNeeded) {
			lastErr = err
			continue // 重签后整段重试
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, errAgentTransferShaMismatch) ||
			errors.Is(err, errAgentTransferSizeMismatch) || errors.Is(err, errAgentTransferTooLarge) ||
			isAgentTransferDiskFull(err) || errors.Is(err, errAgentTransferFence) {
			return 0, "", err // 非重试类
		}
		lastErr = err // 网络/服务端抖动：partial 已保留，下一轮从新偏移续传
	}
	if lastErr == nil {
		lastErr = errors.New("download failed")
	}
	return 0, "", lastErr
}

// attemptAgentTransferDownload 单次下载尝试。续传语义：
//   - 206：从 partial 已有偏移续传，hasher 先喂 partial 前缀（单遍，不重下）。
//   - 200（服务端忽略 Range / If-Range 失配 → 对象已换）：truncate 归零重下。
//   - 403/404：签名过期，回上层重签（errAgentTransferResignNeeded）。
func attemptAgentTransferDownload(ctx context.Context, ident agentTransferIdentity, getURL, objectETag, partialPath string, writeJSON func(interface{}) error) (int64, string, error) {
	offset, err := agentTransferPartialOffset(partialPath)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return 0, "", err
	}
	// 断点恰等于 size_bytes：数据已完整落在 partial，只是 rename/marker 前
	// 中断。直接走校验链（前缀喂 hasher → sha 比对），不再发 Range——否则
	// 416 三连烧光重试、完整数据被误判 download_failed（P2 复核项）。
	if ident.SizeBytes > 0 && offset == ident.SizeBytes {
		return verifyAgentTransferCompletePartial(ident, partialPath, offset)
	}
	resuming := offset > 0
	if resuming {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if objectETag != "" {
			req.Header.Set("If-Range", objectETag)
		}
	}
	resp, err := agentTransferHTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// 取消/超时原样上抛：sanitizeUploadError 会剥掉 errors.Is 链，
			// 把 context.Canceled 误判成可重试错误，多烧一轮重签。
			return 0, "", ctx.Err()
		}
		return 0, "", sanitizeUploadError(err)
	}
	defer resp.Body.Close()
	switch {
	case resuming && resp.StatusCode == http.StatusPartialContent:
		// 续传成立
	case resp.StatusCode == http.StatusOK:
		// 整文件（首下或服务端忽略 Range）：partial 归零，hasher 从头算。
		// 首下时 partial 还不存在，Truncate 的 NotExist 不是错误。
		resuming = false
		offset = 0
		if err := os.Truncate(partialPath, 0); err != nil && !os.IsNotExist(err) {
			return 0, "", err
		}
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		return 0, "", errAgentTransferResignNeeded
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// 断点越界（offset == size 已被短路接走）→ 对象被换/缩短，终态。
		return 0, "", fmt.Errorf("download range unsatisfiable at offset %d (expected size %d): %w", offset, ident.SizeBytes, errAgentTransferSizeMismatch)
	default:
		return 0, "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// 已校验 ident.SHA256 非空（服务端契约 Required），空值按 internal 拒收。
	if strings.TrimSpace(ident.SHA256) == "" {
		return 0, "", errors.New("transfer.receive missing sha256")
	}
	hasher := sha256.New()
	if resuming {
		// 续传：partial 前缀喂 hasher（对应「边下边算」的中段续接）。
		if err := feedAgentTransferPartialPrefix(partialPath, hasher); err != nil {
			return 0, "", err
		}
	}

	file, err := os.OpenFile(partialPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()

	var received int64 = offset
	throttle := newAgentTransferThrottle(ident.SizeBytes)
	body := &progressReader{
		r: resp.Body,
		n: &received,
		onRead: func() {
			// received 已被 progressReader 原子累加；这里只读。
			if throttle.shouldEmit(atomic.LoadInt64(&received), time.Now()) {
				_ = writeJSON(agentTransferProgressPayload(ident.RequestID, ident.TransferID, agentTransferDirectionReceive, atomic.LoadInt64(&received), ident.SizeBytes, 0))
			}
		},
	}
	// 越限闸：max_bytes 有值时收满即断（对端虚报 size 的兜底）。
	var limited io.Reader = body
	if ident.MaxBytes > 0 {
		limited = io.LimitReader(body, ident.MaxBytes-offset+1)
	}
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), limited)
	if ident.MaxBytes > 0 && offset+written > ident.MaxBytes {
		return 0, "", fmt.Errorf("%w: received %d exceeds max_bytes %d", errAgentTransferTooLarge, offset+written, ident.MaxBytes)
	}
	if copyErr != nil {
		// partial 已保留；ctx 取消归取消，其余按下载失败由上层决定重试。
		return 0, "", copyErr
	}
	// Content-Length 完整性：206/200 都应下满声明长度（浅校验，sha 是真闸）。
	if resp.ContentLength > 0 && written < resp.ContentLength {
		return 0, "", errors.New("download truncated by peer")
	}
	total := offset + written
	if total != ident.SizeBytes {
		return 0, "", fmt.Errorf("size mismatch: received %d, expected %d: %w", total, ident.SizeBytes, errAgentTransferSizeMismatch)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(sum, strings.TrimSpace(ident.SHA256)) {
		return total, sum, fmt.Errorf("%w: got %s want %s", errAgentTransferShaMismatch, sum, ident.SHA256)
	}
	_ = writeJSON(agentTransferProgressPayload(ident.RequestID, ident.TransferID, agentTransferDirectionReceive, total, ident.SizeBytes, 0))
	return total, sum, nil
}

// --------------------------------------------------------------------------
// 落盘辅助
// --------------------------------------------------------------------------

// agentTransferLandingDir 计算收件落盘目录：inbox + target_dir（规范化 +
// 前缀围栏）。target_dir 为空即 inbox 根。
func agentTransferLandingDir(inbox, targetDir string) (string, error) {
	inboxRoot := filepath.Clean(inbox)
	raw := strings.TrimSpace(targetDir)
	if raw == "" || raw == "." {
		return inboxRoot, nil
	}
	cleaned := filepath.Clean(strings.TrimPrefix(raw, "/"))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || filepath.IsAbs(cleaned) ||
		strings.Contains(cleaned, "\\") {
		return "", fmt.Errorf("%w: target_dir %q escapes inbox", errAgentTransferFence, targetDir)
	}
	joined := filepath.Join(inboxRoot, cleaned)
	if joined != inboxRoot && !strings.HasPrefix(joined, inboxRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: target_dir %q escapes inbox", errAgentTransferFence, targetDir)
	}
	return joined, nil
}

// agentTransferPartialPath .partial 命名：<inbox>/.partial-<transfer_id>-<safeName>。
// transfer_id 已在 cancel 路径校验过字符安全；safeName 已过 D9 清洗。
func agentTransferPartialPath(inbox, transferID, safeName string) string {
	return filepath.Join(inbox, fmt.Sprintf(".partial-%s-%s", transferID, safeName))
}

func agentTransferPartialOffset(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

// feedAgentTransferPartialPrefix 续传前把 partial 已有字节喂进 hasher。
func feedAgentTransferPartialPrefix(path string, hasher io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(hasher, f)
	return err
}

// agentTransferFinalPath 重名不覆盖：存在则追加 (1)/(2)…（保扩展名）。
func agentTransferFinalPath(dir, safeName string) string {
	candidate := filepath.Join(dir, safeName)
	ext := filepath.Ext(safeName)
	stem := strings.TrimSuffix(safeName, ext)
	for i := 1; ; i++ {
		if _, err := os.Lstat(candidate); err != nil {
			return candidate // 不存在（或 stat 失败按可写试）
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s(%d)%s", stem, i, ext))
	}
}

// extractAgentTransferArchive 解压归档到 <landDir>/<stem>(n)。zip 本体在解压
// 成功后删除；失败保留 zip 待查并返回错误。
func extractAgentTransferArchive(ctx context.Context, landDir, zipPath, safeName string) (string, error) {
	stem := safeName
	if ext := filepath.Ext(stem); strings.EqualFold(ext, ".zip") {
		stem = strings.TrimSuffix(stem, ext)
	}
	extractDir := agentTransferFinalPath(landDir, stem)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", err
	}
	if err := extractAgentTransferZip(ctx, zipPath, extractDir); err != nil {
		// 失败即整体失败：解压器不做部分解压；这里把半成品目录清掉。
		_ = os.RemoveAll(extractDir)
		return "", err
	}
	if err := os.Remove(zipPath); err != nil && !os.IsNotExist(err) {
		logger.Warn(fmt.Sprintf("[TRANSFER] extracted but failed to remove archive %s: %v", zipPath, err))
	}
	return extractDir, nil
}

// sendAgentTransferConfirm HTTP confirm（fire-and-forget；失败记日志，服务端
// 还有 WS result 通道与状态机对账兜底）。
func sendAgentTransferConfirm(s *AgentService, ident agentTransferIdentity, finalPath string) {
	if s == nil || ident.TransferID == "" {
		return
	}
	payload := map[string]interface{}{"path": finalPath}
	if err := agentTransferSendConfirm(s, ident.TransferID, payload); err != nil {
		logger.Warn(fmt.Sprintf("[TRANSFER] confirm failed transfer_id=%s err=%v", ident.TransferID, err))
	}
}


