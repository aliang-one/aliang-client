package services

// 设备间互传·发送链：transfer.upload → 解析 source_path →（目录则流式 zip）→
// 分块 PUT COS → 收集 ETag → transfer.result 回传 etags/size/sha256。
//
// 与 file.upload（手机下载）有意分离：那边是全局单飞单 PUT；这边独立并发槽
// （2 个）+ multipart 分块 + 流式打包。可复用的只有原语：progressReader、
// sanitizeUploadError、newAgentFileUploadHTTPClient 构造模式、5%/1s 节流常量。
//
// 幂等键是 transfer_id（D1）：request_id 只是当次下发关联，重推同 id 一律按
// 在飞/已完成去重，绝不二次打包。

import (
	"bytes"
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
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

const (
	// 分块尺寸钳制：下限防碎片化（COS 除末块外要求 ≥1MiB），上限防内存爆。
	agentTransferPartSizeMin = int64(1) << 20
	agentTransferPartSizeMax = int64(256) << 20
	agentTransferPartDefault = int64(64) << 20

	agentTransferPartAttempts   = 3              // 单块直传重试
	agentTransferPartRetryDelay = 2 * time.Second // 重试退避（固定，够 COS 抖动恢复）
)

// agentTransferHTTPClient 长上传专用：不设整体 Timeout（ctx cancel 是唯一
// 出口），ResponseHeaderTimeout 从 body 写完起算，防云 LB 挂死占槽。
var agentTransferHTTPClient = newAgentFileUploadHTTPClient(120 * time.Second)

// agentTransferETagPattern COS 返回的 ETag 形状（可能带引号）。畸形 ETag 在
// 设备端就地判失败——带病 etag 到服务端只会在 Complete 时炸出更难懂的错误。
var agentTransferETagPattern = regexp.MustCompile(`^"?[A-Za-z0-9-]+"?$`)

// errAgentTransferResignNeeded 分块 PUT 撞上签名过期类状态码（403/404/409），
// 调用方应走 /parts 重签后重试该块。
var errAgentTransferResignNeeded = errors.New("transfer part url needs resign")

// --------------------------------------------------------------------------
// transfer.upload 载荷解析
// --------------------------------------------------------------------------

type agentTransferUploadPlan struct {
	key      string
	uploadID string
	partSize int64
	parts    map[int]string // part_number → 预签名 PUT URL
}

func parseAgentTransferUploadPlan(msg map[string]interface{}) (*agentTransferUploadPlan, error) {
	raw, ok := msg["upload"].(map[string]interface{})
	if !ok {
		return nil, errors.New("transfer.upload missing upload plan")
	}
	plan := &agentTransferUploadPlan{
		key:      remoteString(raw, "key"),
		uploadID: remoteString(raw, "upload_id"),
		partSize: agentTransferInt64(raw, "part_size"),
		parts:    make(map[int]string),
	}
	if plan.key == "" || plan.uploadID == "" {
		return nil, errors.New("transfer.upload plan missing key/upload_id")
	}
	plan.partSize = clampAgentTransferPartSize(plan.partSize)
	rawParts, ok := raw["parts"].([]interface{})
	if !ok || len(rawParts) == 0 {
		return nil, errors.New("transfer.upload plan has no presigned parts")
	}
	for _, item := range rawParts {
		part, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		num := int(agentTransferInt64(part, "part_number"))
		url := remoteString(part, "url")
		if num > 0 && url != "" {
			plan.parts[num] = url
		}
	}
	if len(plan.parts) == 0 {
		return nil, errors.New("transfer.upload plan parts are all invalid")
	}
	return plan, nil
}

func clampAgentTransferPartSize(size int64) int64 {
	if size <= 0 {
		return agentTransferPartDefault
	}
	if size < agentTransferPartSizeMin {
		return agentTransferPartSizeMin
	}
	if size > agentTransferPartSizeMax {
		return agentTransferPartSizeMax
	}
	return size
}

// --------------------------------------------------------------------------
// /parts 重签（D4 惰性分批的续批入口）。包级变量以便测试替身替换。
// --------------------------------------------------------------------------

var agentTransferFetchParts = func(s *AgentService, transferID string, fromPart int) (map[int]string, error) {
	path := fmt.Sprintf("/api/transfers/%s/parts?from=%d", url.PathEscape(transferID), fromPart)
	raw, err := s.callAgentServer(http.MethodGet, agentTransferServerURL(path), nil, "")
	if err != nil {
		return nil, err
	}
	var body struct {
		Upload struct {
			Parts []struct {
				PartNumber int    `json:"part_number"`
				URL        string `json:"url"`
			} `json:"parts"`
		} `json:"upload"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make(map[int]string, len(body.Upload.Parts))
	for _, p := range body.Upload.Parts {
		if p.PartNumber > 0 && p.URL != "" {
			out[p.PartNumber] = p.URL
		}
	}
	if len(out) == 0 {
		return nil, errors.New("transfer parts resign returned no urls")
	}
	return out, nil
}

// --------------------------------------------------------------------------
// handler
// --------------------------------------------------------------------------

func (s *AgentService) handleAgentTransferUpload(msg map[string]interface{}, writeJSON func(interface{}) error) {
	ident := parseAgentTransferIdentity(msg)
	if ident.TransferID == "" {
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, "", transferErrBadFilename, errors.New("transfer.upload missing transfer_id"))
		return
	}
	plan, err := parseAgentTransferUploadPlan(msg)
	if err != nil {
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrUploadFailed, err)
		return
	}
	sourcePath, err := resolveAgentTransferSourcePath(msg)
	if err != nil {
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrPathNotFound, err)
		return
	}

	// 结果帧双通道（审计 P1）：etags 帧只走 WS 时，断连瞬间 writer 为 nil
	// 会静默丢帧——etags 永远到不了服务端 → 行停 uploading → 租约超时 →
	// failed + Abort → 2GB 白传整包报废。包装 writeJSON，凡 transfer.result
	// 帧同时 POST HTTP（对齐 receive 侧 confirm 的「HTTP 为准」双保险）。
	writeJSON = agentTransferUploadResultWriter(s, ident, writeJSON)

	ctx, cancel := context.WithCancel(context.Background())
	ok, duplicate := transferUploadSlot().acquire(ident.TransferID, cancel)
	if !ok {
		cancel()
		if duplicate {
			// 同 id 在飞：WS 重推 / hello 重驱的重复投递，幂等忽略。
			logger.Info(fmt.Sprintf("[TRANSFER] upload duplicate delivery ignored transfer_id=%s", ident.TransferID))
			return
		}
		// 槽满：非终态 busy，服务端保留 pending 退避重推（receiver_busy 的上传侧对应物）。
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrUploaderBusy, errors.New("another transfer upload is in progress"))
		return
	}
	defer transferUploadSlot().release(ident.TransferID)
	defer cancel()

	logger.Info(fmt.Sprintf("[TRANSFER] upload begin transfer_id=%s source=%s dir_archive=%t part_size=%d parts_first=%d",
		ident.TransferID, sourcePath, ident.IsDirectoryArchive, plan.partSize, len(plan.parts)))

	startAgentTransferUpload(ctx, s, ident, plan, sourcePath, writeJSON)
}

func startAgentTransferUpload(ctx context.Context, s *AgentService, ident agentTransferIdentity, plan *agentTransferUploadPlan, sourcePath string, writeJSON func(interface{}) error) {
	src, totalHint, err := openAgentTransferSource(ident, sourcePath)
	if err != nil {
		code := transferErrPathNotFound
		if errors.Is(err, errAgentTransferPathTypeMismatch) {
			code = transferErrPathTypeMismatch
		}
		_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, code, err)
		return
	}
	defer src.Close()

	hasher := sha256.New()
	throttle := newAgentTransferThrottle(totalHint)
	var uploaded int64
	var bytesRead int64
	etags := make([]map[string]interface{}, 0, 8)

	buf := make([]byte, plan.partSize)
	for partNumber := 1; ; partNumber++ {
		if ctx.Err() != nil {
			_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrCancelled, ctx.Err())
			return
		}
		url, err := agentTransferPartURL(ctx, s, plan, ident.TransferID, partNumber)
		if err != nil {
			_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrUploadFailed, err)
			return
		}
		if err := agentTransferHostAllowed(url); err != nil {
			_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrUploadFailed, err)
			return
		}

		n, readErr := io.ReadFull(src, buf)
		if n > 0 {
			bytesRead += int64(n)
			if ident.MaxBytes > 0 && bytesRead > ident.MaxBytes {
				_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrTooLarge,
					fmt.Errorf("transfer payload exceeds max_bytes %d", ident.MaxBytes))
				return
			}
			hasher.Write(buf[:n]) // 哈希按读到的字节算，与上传成败无关
		}
		// n==0 且 EOF：整除边界。partNumber==1 时仍要 PUT 一块空块——
		// COS Complete 至少要 1 part（空文件/空目录归档的唯一出路）。
		if n == 0 && (readErr == io.EOF) && partNumber > 1 {
			break
		}
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrUploadFailed, readErr)
			return
		}

		etag, err := putAgentTransferPartWithResign(ctx, s, plan, ident, url, buf[:n], partNumber, &uploaded, throttle, writeJSON)
		if err != nil {
			code := transferErrUploadFailed
			if errors.Is(err, context.Canceled) {
				code = transferErrCancelled
			}
			_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, code, err)
			return
		}
		etags = append(etags, map[string]interface{}{"part_number": partNumber, "etag": etag})
		if readErr != nil {
			break // EOF / UnexpectedEOF：最后一块
		}
	}

	// 流式 zip 的打包 goroutine 可能还在写 trailer，等它收尾并收割错误。
	if zw, ok := src.(*zipStreamWriter); ok {
		if err := zw.Wait(); err != nil {
			_ = emitAgentTransferFailure(writeJSON, ident.RequestID, ident.TransferID, transferErrUploadFailed, err)
			return
		}
	}

	payload := agentTransferResultPayload(ident.RequestID, ident.TransferID, true)
	payload["etags"] = etags
	payload["size_bytes"] = uploaded
	payload["sha256"] = hex.EncodeToString(hasher.Sum(nil))
	if err := writeJSON(payload); err != nil {
		// 写帧失败必须记日志（server 会对账/重驱），不许静默吞错。
		logger.Warn(fmt.Sprintf("[TRANSFER] upload result write failed transfer_id=%s err=%v", ident.TransferID, err))
	}
	logger.Info(fmt.Sprintf("[TRANSFER] upload done transfer_id=%s parts=%d bytes=%d", ident.TransferID, len(etags), uploaded))
}

// openAgentTransferSource 按登记类型打开数据源：文件直开；目录走流式 zip
// （总量未知，total=0，节流退化为纯时间闸）。文件/目录与登记不符即拒。
func openAgentTransferSource(ident agentTransferIdentity, sourcePath string) (io.ReadCloser, int64, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, fmt.Errorf("transfer source not found: %w", err)
		}
		return nil, 0, err
	}
	if info.IsDir() != ident.IsDirectoryArchive {
		return nil, 0, fmt.Errorf("%w: is_dir=%t registered_archive=%t", errAgentTransferPathTypeMismatch, info.IsDir(), ident.IsDirectoryArchive)
	}
	if info.IsDir() {
		w, err := newZipStreamWriter(sourcePath)
		if err != nil {
			return nil, 0, err
		}
		return w, 0, nil
	}
	f, err := os.Open(sourcePath)
	if err != nil {
		return nil, 0, err
	}
	if ident.MaxBytes > 0 && info.Size() > ident.MaxBytes {
		f.Close()
		return nil, 0, fmt.Errorf("%w: file size %d exceeds max_bytes %d", errAgentTransferTooLarge, info.Size(), ident.MaxBytes)
	}
	return f, info.Size(), nil
}

// 哨兵错误：openAgentTransferSource 用 %w 包裹，调用方 errors.Is 归类 error_code。
var (
	errAgentTransferPathTypeMismatch = errors.New("transfer source type mismatch")
	errAgentTransferTooLarge         = errors.New("transfer source exceeds max_bytes")
)

// agentTransferPartURL 取第 partNumber 块的预签名 URL；首批用尽走 /parts 续签
// （D4），续签结果合并进 plan 供后续块复用。
func agentTransferPartURL(ctx context.Context, s *AgentService, plan *agentTransferUploadPlan, transferID string, partNumber int) (string, error) {
	if url := plan.parts[partNumber]; url != "" {
		return url, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	fetched, err := agentTransferFetchParts(s, transferID, partNumber)
	if err != nil {
		return "", fmt.Errorf("resign parts from %d failed: %w", partNumber, err)
	}
	for num, url := range fetched {
		if plan.parts[num] == "" {
			plan.parts[num] = url
		}
	}
	url := plan.parts[partNumber]
	if url == "" {
		return "", fmt.Errorf("resign parts missing part %d", partNumber)
	}
	// 域钉死的单一收口：首批下发、D4 续批、403/404/409 重签三类服务端来源
	// 的 URL 全部在此过围栏（主循环 :219 的校验保留作双保险）。
	if err := agentTransferHostAllowed(url); err != nil {
		return "", err
	}
	return url, nil
}

// putAgentTransferPartWithResign 带重签的单块直传：PUT 撞签名过期哨兵时，
// 清掉 plan 内该块 URL 强制 agentTransferPartURL 走 /parts 重签，再原地重试
// 同一块（chunk 还在 buffer，源流不前进、哈希不受影响）。重签预算 1 次，
// 再撞即失败——防重签端点持续给坏 URL 时死循环。
func putAgentTransferPartWithResign(ctx context.Context, s *AgentService, plan *agentTransferUploadPlan, ident agentTransferIdentity, url string, chunk []byte, partNumber int, uploaded *int64, throttle *agentTransferThrottle, writeJSON func(interface{}) error) (string, error) {
	for resigns := 0; ; resigns++ {
		etag, err := s.putAgentTransferPart(ctx, url, chunk, partNumber, uploaded, throttle, ident, writeJSON)
		if err == nil || !errors.Is(err, errAgentTransferResignNeeded) || resigns >= 1 {
			return etag, err
		}
		delete(plan.parts, partNumber) // 强制重签
		fresh, ferr := agentTransferPartURL(ctx, s, plan, ident.TransferID, partNumber)
		if ferr != nil {
			return "", fmt.Errorf("resign part %d failed: %w", partNumber, ferr)
		}
		url = fresh
	}
}

// putAgentTransferPart 单块直传：Content-Length 明确、body 过 progressReader
// 出进度、单块重试 3 次、签名过期类状态码返回重签哨兵。成功返回原样 ETag。
func (s *AgentService) putAgentTransferPart(ctx context.Context, url string, chunk []byte, partNumber int, uploaded *int64, throttle *agentTransferThrottle, ident agentTransferIdentity, writeJSON func(interface{}) error) (string, error) {
	var lastErr error
	for attempt := 0; attempt < agentTransferPartAttempts; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if attempt > 0 {
			select {
			case <-time.After(agentTransferPartRetryDelay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		var uploadedInPart int64
		body := &progressReader{
			r: bytes.NewReader(chunk),
			n: &uploadedInPart,
			onRead: func() {
				now := time.Now()
				done := atomic.LoadInt64(uploaded) + atomic.LoadInt64(&uploadedInPart)
				if throttle.shouldEmit(done, now) {
					_ = writeJSON(agentTransferProgressPayload(ident.RequestID, ident.TransferID, agentTransferDirectionUpload, done, throttle.total, partNumber))
				}
			},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
		if err != nil {
			return "", err
		}
		req.ContentLength = int64(len(chunk))
		resp, err := agentTransferHTTPClient.Do(req)
		if err != nil {
			lastErr = sanitizeUploadError(err)
			continue
		}
		etag := strings.TrimSpace(resp.Header.Get("ETag"))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			if !agentTransferETagPattern.MatchString(etag) {
				lastErr = fmt.Errorf("part %d returned missing or malformed etag", partNumber)
				continue
			}
			atomic.AddInt64(uploaded, uploadedInPart)
			_ = writeJSON(agentTransferProgressPayload(ident.RequestID, ident.TransferID, agentTransferDirectionUpload, atomic.LoadInt64(uploaded), throttle.total, partNumber))
			return etag, nil
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusConflict {
			return "", errAgentTransferResignNeeded
		}
		lastErr = fmt.Errorf("upload part %d failed with status %d", partNumber, resp.StatusCode)
	}
	// 取消落在末次重试的 Do 中途时，原生 ctx 错误优先于脱敏后的 lastErr
	// （后者丢了 errors.Is 链，会被误报成 upload_failed 终态）。
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return "", lastErr
}

// agentTransferUploadResultWriter 包装上传链的 writeJSON：每个
// transfer.result 帧（成功或失败）在走 WS 之外同步转投 HTTP 回执端点。
// WS 写失败不再等于结果丢失；HTTP 也失败才真正丢（双通道同时断）。
func agentTransferUploadResultWriter(s *AgentService, ident agentTransferIdentity, writeJSON func(interface{}) error) func(interface{}) error {
	return func(payload interface{}) error {
		err := writeJSON(payload)
		if m, ok := payload.(map[string]interface{}); ok &&
			remoteString(m, "type") == models.AgentEventTransferResult {
			go agentTransferPostUploadResult(s, ident.TransferID, m)
		}
		return err
	}
}

// agentTransferPostUploadResult HTTP 上传回执（包级变量以便测试替身替换）。
// 服务端与 WS 帧走同一 handleUploadResult 路径，重复帧被 CAS 吸收。
var agentTransferPostUploadResult = func(s *AgentService, transferID string, payload map[string]interface{}) error {
	if s == nil || transferID == "" {
		return nil
	}
	_, err := s.callAgentServer(http.MethodPost,
		agentTransferServerURL("/api/transfers/"+url.PathEscape(transferID)+"/upload-result"), payload, "")
	return err
}
