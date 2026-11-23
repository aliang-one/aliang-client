//go:build windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"aliang.one/nursorgate/processor/setup"
	"golang.org/x/sys/windows"
)

// crashLogMaxBytes 是崩溃日志的粗粒度封顶:超限删旧重开。崩溃日志的可用性
// 优先于历史完整性,与 writeStartupTrace 的 128KB 截断同一取舍。
const crashLogMaxBytes = 16 << 20

// redirectCrashOutput 把进程的 stderr(fd 2)重定向到 crash 日志文件。
//
// 为什么必须有它(2026-11-23 win196 158 次静默崩溃的定谳教训):SCM 拉起的
// 服务进程没有 stderr 句柄,而 Go 的 panic / runtime fatal error(不可
// recover、不写文件日志、不产生 WER dump)只往 fd 2 打栈——服务态下这些
// 栈直接进黑洞,进程死了日志里干干净净,取证只能靠控制台手动复现。
//
// 可行性依据(Go 1.26 runtime/os_windows.go write1):runtime 每次写 fd 1/2
// 都现调 _GetStdHandle,不缓存句柄——进程内 SetStdHandle 即生效,无需子进程
// 包装。wintun 等走 stderr 的 C 层输出同样落文件(os.Stderr 重赋值只覆盖
// os 层写入,不覆盖 C 层,但两者至少都进了这个文件)。
func redirectCrashOutput() {
	dir := setup.CoreLogDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, "aliang_crash.log")
	if info, err := os.Stat(path); err == nil && info.Size() > crashLogMaxBytes {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	// 先 SetStdHandle(接管 runtime fatal/panic 的 fd 2 通道),再重赋
	// os.Stderr(接管 Go 代码里的显式 stderr 写)。双保险互不替代。
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err != nil {
		f.Close()
		return
	}
	os.Stderr = f
	writeStartupTrace("crash output redirected to %s", path)
	// 开机横幅:进程身份打进日志,多进程/多世对齐取证用。
	fmt.Fprintf(f, "==== %s pid=%d crash log opened ====\n", time.Now().Format(time.RFC3339), os.Getpid())
}
