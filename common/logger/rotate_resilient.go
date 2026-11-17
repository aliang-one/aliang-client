package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// rotateResilientWriter 包一层带轮转的底层写器(lumberjack),为其失败的
// rotate 提供兜底直写路径。
//
// 背景缺陷(2026-11-16 0.196 生产实锤):core 与 user agent 双进程共享同一
// 日志文件(aliang_core.log),各自持有独立的 lumberjack.Logger。lumberjack
// 的 rotate 用 os.Rename 挪旧文件——Windows 下另一进程持打开句柄时 rename
// 必败(Go 的 syscall.Open 不带 FILE_SHARE_DELETE,golang/go#32088),失败后
// lumberjack 的 file 永久为 nil,此后每次 Write 都重入失败的 rotate,形成
// 「进程完全活着、文件日志一行不出」的永久静默丢弃。本包装把该失败模式
// 退化为「主路轮转不可用期间经 O_APPEND 直写兜底句柄继续落盘」:
//   - 主路 Write 成功 → 原样透传(并清理兜底句柄,防止轮转成功后兜底句柄
//     仍指向被改名的旧文件);
//   - 主路 Write 失败 → 经兜底句柄(同路径 O_APPEND 追加)落盘,消息不丢;
//   - 失败/恢复边沿各写一条节流面包屑到同目录 aliang-service.log(对齐
//     cmd/startup_trace_windows.go 的落点),让此类事故一行即可定谳。
//
// 双进程并发追加的行内交错在共享文件场景本就存在,非本包装新增回归。
type rotateResilientWriter struct {
	mu      sync.Mutex
	primary io.Writer
	path    string

	fallback     *os.File
	degraded     bool
	breadcrumbAt time.Time
}

// breadcrumbWindow 控制失败面包屑的节流窗口:窗口内只记首条,防磁盘级
// 故障时写放大。
const breadcrumbWindow = 10 * time.Minute

// breadcrumbName 面包屑文件名,与主日志同目录——core/agent 的日志目录
// 即 ProgramData\Aliang\logs(cmd/startup_trace_windows.go 同落点)。
const breadcrumbName = "aliang-service.log"

// breadcrumbMaxSize 与启动 trace 同款封顶,防无限增长。
const breadcrumbMaxSize = 128 * 1024

func newRotateResilientWriter(primary io.Writer, path string) *rotateResilientWriter {
	return &rotateResilientWriter{primary: primary, path: path}
}

func (w *rotateResilientWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.primary.Write(p)
	if err == nil {
		w.onPrimaryOK()
		return n, nil
	}

	w.noteFailure(err)
	if fb := w.fallbackHandle(); fb != nil {
		if nn, ferr := fb.Write(p); ferr == nil {
			// 兜底成功:吞掉主路错误——消息已落盘,调用方(异步 sink)
			// 本就忽略错误,透传只会诱发上游再挂一层。
			return nn, nil
		}
	}
	return n, err
}

// onPrimaryOK 主路恢复:关兜底句柄(轮转成功后旧兜底句柄指向被改名的旧
// 文件,必须弃用),失败态翻绿并记一条恢复面包屑。
func (w *rotateResilientWriter) onPrimaryOK() {
	if w.fallback != nil {
		_ = w.fallback.Close()
		w.fallback = nil
	}
	if w.degraded {
		w.degraded = false
		w.writeBreadcrumb("file logger recovered (rotation resumed)")
	}
}

// noteFailure 记录降级边沿并按窗口节流写面包屑。
func (w *rotateResilientWriter) noteFailure(err error) {
	first := !w.degraded
	w.degraded = true
	if !first && time.Since(w.breadcrumbAt) < breadcrumbWindow {
		return
	}
	w.breadcrumbAt = time.Now()
	w.writeBreadcrumb(fmt.Sprintf("file logger degraded: primary write failed (%v); falling back to direct append", err))
}

// fallbackHandle 惰性打开兜底直写句柄(同路径 O_APPEND)。
func (w *rotateResilientWriter) fallbackHandle() *os.File {
	if w.fallback != nil {
		return w.fallback
	}
	if w.path == "" {
		return nil
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return nil
	}
	w.fallback = f
	return f
}

// writeBreadcrumb 尽力写一条面包屑(忽略一切错误,绝不放大故障)。
func (w *rotateResilientWriter) writeBreadcrumb(line string) {
	if w.path == "" {
		return
	}
	path := filepath.Join(filepath.Dir(w.path), breadcrumbName)
	if info, err := os.Stat(path); err == nil && info.Size() >= breadcrumbMaxSize {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s [%s] %s\n", time.Now().Format(time.RFC3339), breadcrumbName, line)
}
