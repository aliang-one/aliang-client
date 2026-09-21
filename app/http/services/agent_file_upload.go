package services

// 单文件下载（COS 中转）的 agent 侧直传 handler。
// spec: 外层 workspace docs/superpowers/specs/2026-09-21-cos-file-download-design.md §4.1
// 安全：路径解析复用 resolveAgentProjectContentPath；上传前 Stat 复核大小 ≤ max_bytes
// （防列表元数据过期）；单设备同时 1 个上传。注意：本 handler 的 writeJSON 失败【必须
// 记日志】——不复用 agent_detail.go 的 `_ = writeJSON` 静默吞错模式（server 会对账）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

const (
	agentFileUploadProgressInterval = time.Second
	agentFileUploadProgressFraction = 0.05
)

type agentFileUploadJob struct {
	requestID string
	cancel    context.CancelFunc
}

var (
	agentFileUploadMu     sync.Mutex
	agentFileUploadActive *agentFileUploadJob
)

// 上传 client：不设整体 Timeout（长上传是本意，靠 ctx cancel），但必须有
// ResponseHeaderTimeout——body 发完后对端不回响应头（云 LB 挂死）若无限等待
// 会永久占用设备级单飞槽。该超时从 body 写完起算，不误伤慢上传。
// 注意 ResponseHeaderTimeout 是 Transport 的字段：克隆 DefaultTransport（保留
// proxy/keepalive 等默认值）后在其上覆盖，勿裸 &http.Transport{}。
func newAgentFileUploadHTTPClient(headerTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:gosec // 显式克隆默认传输，仅覆盖 ResponseHeaderTimeout
	transport.ResponseHeaderTimeout = headerTimeout
	return &http.Client{Transport: transport}
}

var agentFileUploadHTTPClient = newAgentFileUploadHTTPClient(60 * time.Second)

// handleAgentFileUpload streams the resolved file to the presigned COS URL
// with a streaming PUT (no full-buffer), emitting throttled progress and a
// terminal result/error. Single-flight: one upload per device at a time.
func handleAgentFileUpload(msg map[string]interface{}, writeJSON func(interface{}) error) {
	requestID := remoteString(msg, "request_id")
	emitErr := func(err error) {
		if werr := writeJSON(agentFileErrorPayload(requestID, err)); werr != nil {
			logger.Warn(fmt.Sprintf("[file.upload] write error payload failed (request %s): %v", requestID, werr))
		}
	}
	agentFileUploadMu.Lock()
	if agentFileUploadActive != nil {
		agentFileUploadMu.Unlock()
		emitErr(errors.New("another file upload is in progress"))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &agentFileUploadJob{requestID: requestID, cancel: cancel}
	agentFileUploadActive = job
	agentFileUploadMu.Unlock()

	go func() {
		defer cancel() // 父 ctx 是 Background，防将来改动引入泄漏
		defer func() {
			agentFileUploadMu.Lock()
			if agentFileUploadActive == job {
				agentFileUploadActive = nil
			}
			agentFileUploadMu.Unlock()
		}()
		handleAgentFileUploadRun(ctx, msg, writeJSON)
	}()
}

// url.Error.Error() 会内嵌完整预签名 URL（含 q-signature），上行 server/日志前必须剥离
func sanitizeUploadError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("upload %s failed: %v", strings.ToLower(ue.Op), ue.Err)
	}
	return err
}

func handleAgentFileUploadRun(ctx context.Context, msg map[string]interface{}, writeJSON func(interface{}) error) {
	requestID := remoteString(msg, "request_id")
	emitErr := func(err error) {
		if werr := writeJSON(agentFileErrorPayload(requestID, err)); werr != nil {
			logger.Warn(fmt.Sprintf("[file.upload] write error payload failed (request %s): %v", requestID, werr))
		}
	}
	projectPath, err := resolveAgentProjectPath(remoteString(msg, "project_path"))
	if err != nil {
		emitErr(err)
		return
	}
	targetPath, err := resolveAgentProjectContentPath(projectPath, remoteString(msg, "path"))
	if err != nil {
		emitErr(err)
		return
	}
	maxBytes := remoteInt(msg, "max_bytes", 0)
	info, err := os.Stat(targetPath)
	if err != nil {
		emitErr(err)
		return
	}
	if info.IsDir() {
		emitErr(errors.New("path is a directory"))
		return
	}
	// Pre-flight size re-check against the real file, not the (possibly stale)
	// listing metadata the server used when issuing the presigned URL.
	if maxBytes > 0 && info.Size() > int64(maxBytes) {
		emitErr(fmt.Errorf("file exceeds download limit (%d > %d bytes)", info.Size(), maxBytes))
		return
	}
	uploadURL := remoteString(msg, "upload_url")
	if uploadURL == "" {
		emitErr(errors.New("missing upload_url"))
		return
	}
	file, err := os.Open(targetPath)
	if err != nil {
		emitErr(err)
		return
	}
	defer file.Close()

	// 进度计数 reader：Read 由 http client 的 transport 写循环单 goroutine 调用，
	// emit 的节流状态（lastSent/lastAt）因此天然单线程；计数器用 atomic 仅作防御。
	// emit 里的 writeJSON 走 WS writeMu 串行，且【失败只记日志不中断上传】。
	var sent int64
	lastSent := int64(0)
	lastAt := time.Now()
	emit := func() {
		n := atomic.LoadInt64(&sent)
		// Cheap early-exit first: the time compare is done on the cached clock
		// read below only when the byte-delta check doesn't already fire.
		if n-lastSent < int64(float64(info.Size())*agentFileUploadProgressFraction) &&
			time.Since(lastAt) < agentFileUploadProgressInterval {
			return
		}
		lastSent, lastAt = n, time.Now()
		if werr := writeJSON(map[string]interface{}{
			"type":           models.AgentEventFileUploadProgress,
			"request_id":     requestID,
			"uploaded_bytes": n,
			"total_bytes":    info.Size(),
		}); werr != nil {
			logger.Warn(fmt.Sprintf("[file.upload] write progress failed (request %s): %v", requestID, werr))
		}
	}
	body := &progressReader{r: file, n: &sent, onRead: emit}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, body)
	if err != nil {
		emitErr(sanitizeUploadError(err))
		return
	}
	req.ContentLength = info.Size()
	contentType := mime.TypeByExtension(filepath.Ext(targetPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := agentFileUploadHTTPClient.Do(req)
	if err != nil {
		emitErr(sanitizeUploadError(err)) // 含 context canceled（file.upload.cancel 触发）
		return
	}
	defer resp.Body.Close()
	// Drain a bounded slice of the response so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		emitErr(fmt.Errorf("upload failed with status %d", resp.StatusCode))
		return
	}
	if werr := writeJSON(map[string]interface{}{
		"type":        models.AgentEventFileUploadResult,
		"request_id":  requestID,
		"ok":          true,
		"size_bytes":  info.Size(),
		"http_status": resp.StatusCode,
	}); werr != nil {
		logger.Warn(fmt.Sprintf("[file.upload] write result failed (request %s): %v", requestID, werr))
	}
}

// progressReader wraps the file body so every chunk read by the HTTP
// transport advances the shared counter and pokes the throttled emitter.
// Concurrency: Read is only ever called from the transport's single write
// goroutine, so no internal locking is needed; the atomic counter exists so
// emit always reads a consistent value even if that invariant ever changes.
type progressReader struct {
	r      io.Reader
	n      *int64
	onRead func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		atomic.AddInt64(p.n, int64(n))
		p.onRead()
	}
	return n, err
}

// handleAgentFileUploadCancel aborts the in-flight upload whose request_id
// matches. Mismatches (stale cancel, already-finished job) are no-ops.
func handleAgentFileUploadCancel(msg map[string]interface{}) {
	requestID := remoteString(msg, "request_id")
	agentFileUploadMu.Lock()
	defer agentFileUploadMu.Unlock()
	if agentFileUploadActive != nil && agentFileUploadActive.requestID == requestID {
		agentFileUploadActive.cancel()
	}
}
