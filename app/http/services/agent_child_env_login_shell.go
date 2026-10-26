package services

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 登录 shell PATH 快照：把「用户在自己终端里启动 claude code」时的真实
// PATH（path_helper + .zprofile + .zshrc 全套）带给 agent 拉起的 AI 子进程。
//
// 为什么必须探针而不能静态拼目录：macOS 上 node@22 这类 keg-only 公式只在
// /opt/homebrew/opt/node@22/bin，/opt/homebrew/bin 里根本没有 node；用户
// PATH 在 .zshrc 里逐行 export，静态列举必漏。探针失败一律 fail-open：
// 回退继承 agent 进程现有环境（旧行为），绝不因环境探测把会话弄挂。

const loginShellPathMarker = "__ALIANG_PATH__"

// loginShellPathCache 缓存一次探针结果。PATH 在进程生命周期内基本不变，
// TTL 只为让「用户中途装了新工具」最终能被看见，不做主动失效。
var (
	loginShellPathMu      sync.Mutex
	loginShellPathValue   string
	loginShellPathOK      bool
	loginShellPathFetched time.Time
	// loginShellPathCacheTTL 到期后下次调用重新探针。成功与失败统一按
	// TTL 缓存（负缓存语义），否则无 shell 宿主上每次调用都会重新探针。
	loginShellPathCacheTTL = 15 * time.Minute
	// loginShellPathRefreshing 保证同一时刻只有一个探针在跑；等待方拿现值。
	loginShellPathRefreshing atomic.Bool
)

// parseLoginShellPathOutput 从探针输出里切出 PATH。取末次标记之后到首个
// 换行（或结尾）的段落：printf 是 shell 最后一次 stdout 写入，取末次出现严格
// 更稳；.zshrc 的欢迎语/回显噪声只会出现在标记前，不会污染。
func parseLoginShellPathOutput(output string) (string, bool) {
	idx := strings.LastIndex(output, loginShellPathMarker)
	if idx < 0 {
		return "", false
	}
	rest := output[idx+len(loginShellPathMarker):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || !strings.Contains(rest, ":") {
		return "", false
	}
	return rest, true
}

// mergeLoginShellPath 用登录 PATH 替换 env 里的 PATH 条目，其余原样保留。
// loginPath 为空（探针失败）时原样返回。
func mergeLoginShellPath(env []string, loginPath string) []string {
	if strings.TrimSpace(loginPath) == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "PATH="+loginPath)
}

// cachedLoginShellPath 带缓存的快照入口。任何失败返回 ("", false)。
//
// 锁纪律：锁内绝不跑探针。探针是登录+交互 shell（最长 5s），持锁会把
// agentChildProcessEnvValue 的全部调用方（上游自愈探测、claude 策略探针
// 等）串行挡住。TTL 到期后由 CAS 选中一个调用者在锁外探针，其余调用者
// 立即拿现值返回（首次可能 fail-open，探针完成即恢复）。
func cachedLoginShellPath() (string, bool) {
	loginShellPathMu.Lock()
	// 门只看时间不看 OK：成功与失败统一按 TTL 缓存（负缓存语义）。
	if time.Since(loginShellPathFetched) < loginShellPathCacheTTL {
		path, ok := loginShellPathValue, loginShellPathOK
		loginShellPathMu.Unlock()
		return path, ok
	}
	loginShellPathMu.Unlock()

	if !loginShellPathRefreshing.CompareAndSwap(false, true) {
		loginShellPathMu.Lock()
		defer loginShellPathMu.Unlock()
		return loginShellPathValue, loginShellPathOK
	}
	defer loginShellPathRefreshing.Store(false)

	path, ok := loginShellPathSnapshot(5 * time.Second)
	loginShellPathMu.Lock()
	loginShellPathValue, loginShellPathOK = path, ok
	loginShellPathFetched = time.Now()
	loginShellPathMu.Unlock()
	return path, ok
}

// augmentEnvWithLoginShellPath 是 agentChildProcessEnv 的 PATH 合并通道。
func augmentEnvWithLoginShellPath(env []string) []string {
	path, ok := cachedLoginShellPath()
	if !ok {
		return env
	}
	return mergeLoginShellPath(env, path)
}
