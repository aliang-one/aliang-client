# agentcore 环境上下文对齐 claude code 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** aliangcode（agentcore 内核）与 agent 拉起的所有 AI 子进程，看到与用户在自己终端里启动 claude code 一致的环境：真实用户 PATH（node/npm 等）、TERM/LANG 兜底、系统提示里的环境块。

**Architecture:** 三层修复。(1) 在所有 AI 子进程的共同咽喉 `agentChildProcessEnv()` 注入「登录 shell PATH 快照」（探针 `$SHELL -l -i -c` 一次性取用户终端真实 PATH，带缓存、CAS 防并发探针、失败回退），CLI provider / codex / goal / agentcore 一处全修；(2) 内核 `ToolEnv` 增加 `Env` 字段，`runShell` 显式传给子进程，构造点喂入快照环境 + TERM/LANG 兜底；(3) `agentCoreSystemPrompt` 注入与 claude CLI 同形的环境块（cwd/git/平台/OS 版本/日期）。

**Tech Stack:** Go（`app/agentcore` 内核 + `app/http/services` 宿主），`go test`，无新依赖。

---

## 背景与根因（2026-10-27 排查结论）

1. **执行层**：`runShell`（`app/agentcore/tools_shell.go:118`）从不设 `cmd.Env` → 子进程继承 agent 进程环境；Mac 上 agent 由 Aliang.app 从 GUI 启动，实测 `PATH=/usr/bin:/bin:/usr/sbin:/sbin`，无 TERM、无 LANG。本机 node@22 是 homebrew keg-only（`/opt/homebrew/opt/node@22/bin`），PATH 注入只在 `~/.zshrc`（仅交互 shell）→ aliangcode 报「没有 nodejs」是真实执行环境，不是幻觉。
2. **env 合并缺位**：`agentChildProcessEnv()`（`app/http/services/agent_ai.go:219`，含 Windows 注册表用户 PATH 合并）CLI/goal 子进程 5 处在用，agentcore 链路 0 处——`ToolEnv`（`app/agentcore/tools.go:60`）没有 env 字段可传。
3. **上下文层**：`agentCoreSystemPrompt()`（`app/http/services/agent_ai_agentcore.go:299`）无任何环境块；CLI provider 下这些由 claude CLI 自动注入。

**关键设计决策：为什么用「登录 shell 快照」而不是静态目录拼接** —— keg-only 的 node@22 证明静态列举必漏（`/opt/homebrew/bin` 里根本没有 node）；只有跑一次用户的登录+交互 shell 拿 `$PATH` 才能真正等于「用户终端里启动 claude code」的环境。探针失败（无 shell/超时/fish 解析不出）一律 fail-open：回退继承 agent 进程现有环境（旧行为），绝不因环境探测把会话弄挂。

**已知坑（memory 教训 + 评审结论，执行时必读）：**
- 跨平台测试文件名**不得**带 `_windows`/`_darwin` 等 GOOS 尾段。**且测试文件若不加 build tag，其引用的所有符号必须在全平台可编译**——纯逻辑放无 tag 实现文件，探针拆 GOOS 文件（P1 修复后的结构，见下）。
- services 包测试禁用 `&agentAIManager{}`（nil map panic），必须 `newAgentAIManager()`。
- worktree 首次 `go build` 需从主仓 `cp -R app/website/dist`（embed 占位）。
- services 包 `-race` 有预存 flake，验证一律不加 `-race`。
- 提交标题全中文「新增：/修复：」，不用 feat:/fix:。
- 回调 goroutine 里禁 `t.Fatal`，用 `t.Error` + `return`。
- `agent_ai_agentcore.go` 已 import os/exec/runtime/sync/time/fmt/filepath（评审确认），Task 5 无需补 import。
- 探针**不得持锁执行**（评审 P2）：探针最长 5s，持锁会把 `agentChildProcessEnvValue` 的全部调用方（agent_ai_proxy_guard.go:69、agent_ai_claude_policy.go:58 等）串行挡住。用 CAS 保证同时只有一个探针在跑，等待方返回现值。

## 文件结构（评审 P1 修复后的跨平台安全切分）

- Modify: `app/agentcore/tools.go` —— `ToolEnv` 加 `Env []string` 字段
- Modify: `app/agentcore/tools_shell.go` —— `runShell` 应用 `env.Env`
- Create: `app/http/services/agent_child_env_login_shell.go` —— **无 tag**：marker 常量、`parseLoginShellPathOutput`、`mergeLoginShellPath`、缓存四变量 + CAS 刷新、`cachedLoginShellPath`、`augmentEnvWithLoginShellPath`（全平台可编译）
- Create: `app/http/services/agent_child_env_login_shell_unix.go`（`//go:build !windows`）—— 仅 `loginShellPathSnapshot`
- Create: `app/http/services/agent_child_env_login_shell_windows.go`（`//go:build windows`）—— 仅 `loginShellPathSnapshot`（返回 `("", false)`；Windows 走既有注册表通道）
- Create: `app/http/services/agent_child_env_login_shell_test.go` —— **无 tag 中性命名**：纯函数 + merge + augment 用例（引用的符号全部在无 tag 文件里，Windows 编译安全）
- Create: `app/http/services/agent_child_env_login_shell_live_test.go`（`//go:build !windows`）—— 真探针用例
- Modify: `app/http/services/agent_ai.go:219-233` —— `agentChildProcessEnv` 接线
- Create: `app/http/services/agent_ai_child_env_test.go` —— 接线测试
- Modify: `app/http/services/agent_ai_agentcore.go` —— `agentCoreToolEnv()`/`scanEnvValue()`/环境块 + 构造点 751 行
- Test: `app/agentcore/tools_test.go`（复用 `fakeShellPolicy`/`shPolicy()`）、`app/http/services/agent_ai_agentcore_test.go`

---

### Task 0: 建 worktree（不动主工作区）

**注意**：主工作区有并行会话的未跟踪文档，**禁止 `git stash -u`**。worktree add 不要求干净工作区，直接建。

- [ ] **Step 1: 建 worktree**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add ../alianggate-env-parity -b feature/agentcore-env-parity
```

- [ ] **Step 2: 拷贝 embed 资源 + 计划文档**

```bash
cp -R app/website/dist ../alianggate-env-parity/app/website/dist
cp docs/superpowers/plans/2026-10-27-agentcore-env-parity.md ../alianggate-env-parity/docs/superpowers/plans/
```

- [ ] **Step 3: 验证构建基线**

```bash
cd ../alianggate-env-parity && go build ./...
```

预期：编译通过（gopls 对 worktree 的误报一律无视，以 go build/go test 为准）。

---

### Task 1: 内核 ToolEnv.Env 字段 + runShell 应用

**Files:**
- Modify: `app/agentcore/tools.go`（`ToolEnv`，约 59-73 行）
- Modify: `app/agentcore/tools_shell.go`（`runShell`，约 118-121 行）
- Test: `app/agentcore/tools_test.go`

- [ ] **Step 1: 写失败测试**

在 `app/agentcore/tools_test.go` 追加（`fakeShellPolicy`/`shPolicy()` 已在该文件 658-684 行）：

```go
// runShell 必须把 ToolEnv.Env 传给子进程：这是「用户终端环境对齐」的落地点。
// env.Env 为 nil 时保持旧行为（继承宿主进程），保证兼容。
func TestRunShellAppliesToolEnv(t *testing.T) {
	toolEnv := &ToolEnv{
		ShellPolicy: shPolicy(),
		Env:         []string{"PATH=/usr/bin:/bin", "AGENTCORE_ENV_PROBE=hello"},
	}
	out := runShell(context.Background(), "bash",
		map[string]interface{}{"command": `printf 'PROBE=%s' "$AGENTCORE_ENV_PROBE"`}, toolEnv)
	if out.IsError || !strings.Contains(out.Content, "PROBE=hello") {
		t.Fatalf("env 未传入子进程: %+v", out)
	}

	// nil Env：继承宿主（测试进程没有该变量），不应报错。
	out = runShell(context.Background(), "bash",
		map[string]interface{}{"command": `printf 'PROBE=%s' "$AGENTCORE_ENV_PROBE"`},
		&ToolEnv{ShellPolicy: shPolicy()})
	if out.IsError || strings.Contains(out.Content, "PROBE=hello") {
		t.Fatalf("nil Env 应继承宿主进程，不应看到探针变量: %+v", out)
	}
}
```

（`context`、`strings` 已在测试文件 import；缺了就补。）

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/agentcore/ -run TestRunShellAppliesToolEnv -v
```

预期：FAIL——`PROBE=` 后为空（Env 被忽略）。

- [ ] **Step 3: 最小实现**

`tools.go` 的 `ToolEnv` 加字段（放 `ShellPolicy` 之后）：

```go
	// Env 是子进程（Bash/PowerShell）的完整环境。nil = 继承宿主进程环境
	// （旧行为）。宿主通过这里下发「用户登录 shell 快照 + TERM/LANG 兜底」，
	// 否则 GUI/systemd 启动的 agent 只有系统四件套 PATH，node/npm 等用户级
	// 工具全部不可见——aliangcode「没有 nodejs」误报的执行层根因。
	Env []string
```

`tools_shell.go` 的 `runShell`，在 `cmd.Dir = ...` 之后加：

```go
	if env.Env != nil {
		cmd.Env = env.Env
	}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/agentcore/ -run TestRunShellAppliesToolEnv -v && go test ./app/agentcore/
```

预期：PASS，全包无回归。

- [ ] **Step 5: 提交**

```bash
git add app/agentcore/tools.go app/agentcore/tools_shell.go app/agentcore/tools_test.go
git commit -m "新增:内核 ToolEnv 支持 Env 字段,shell 子进程显式继承宿主下发环境

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: 登录 shell PATH 快照（探针/缓存/合并）

**Files:**
- Create: `app/http/services/agent_child_env_login_shell.go`（无 tag）
- Create: `app/http/services/agent_child_env_login_shell_unix.go`（`//go:build !windows`）
- Create: `app/http/services/agent_child_env_login_shell_windows.go`（`//go:build windows`）
- Test: `app/http/services/agent_child_env_login_shell_test.go`（无 tag）+ `agent_child_env_login_shell_live_test.go`（`//go:build !windows`）

- [ ] **Step 1: 写失败测试（无 tag 文件，全平台编译安全）**

`agent_child_env_login_shell_test.go`：

```go
package services

import (
	"strings"
	"testing"
)

// 纯函数：解析探针输出。.zshrc 可能往 stdout 噪声输出，靠标记切片保证鲁棒。
func TestParseLoginShellPathOutput(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"干净输出", "__ALIANG_PATH__/usr/bin:/bin", "/usr/bin:/bin", true},
		{"前噪声", "loading profile...\n__ALIANG_PATH__/a:/b", "/a:/b", true},
		{"后换行", "__ALIANG_PATH__/a:/b\n", "/a:/b", true},
		{"无标记", "/a:/b", "", false},
		{"标记后为空", "__ALIANG_PATH__", "", false},
		{"标记后无冒号", "__ALIANG_PATH__/single", "", false},
	}
	for _, tc := range cases {
		got, ok := parseLoginShellPathOutput(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// 纯函数：合并语义——替换 PATH、保留其余、不产生重复键、PATH 恒唯一。
func TestMergeLoginShellPath(t *testing.T) {
	base := []string{"HOME=/x", "PATH=/usr/bin:/bin", "TERM=dumb"}
	got := mergeLoginShellPath(base, "/opt/homebrew/bin:/usr/bin:/bin")
	want := []string{"HOME=/x", "TERM=dumb", "PATH=/opt/homebrew/bin:/usr/bin:/bin"}
	if len(got) != len(want) {
		t.Fatalf("长度不等: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项: got %q want %q", i, got[i], want[i])
		}
	}
	// 探针失败（空串）必须原样返回，fail-open。
	if same := mergeLoginShellPath(base, ""); len(same) != 3 || same[1] != "PATH=/usr/bin:/bin" {
		t.Fatalf("探针失败应原样返回: %v", same)
	}
}

// 缓存包装器端到端：快照失败（windows 探针恒 false / unix 无 shell）时
// PATH 原样保留；成功时 PATH 被替换且条目唯一。
func TestAugmentEnvWithLoginShellPath(t *testing.T) {
	base := []string{"PATH=/usr/bin:/bin", "HOME=/x"}
	augmented := augmentEnvWithLoginShellPath(base)
	count := 0
	for _, kv := range augmented {
		if strings.HasPrefix(kv, "PATH=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("PATH 应恰有一条, got %d: %v", count, augmented)
	}
}
```

`agent_child_env_login_shell_live_test.go`（`//go:build !windows`，真探针）：

```go
//go:build !windows

package services

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// 真探针：必须能拿到含冒号的非空 PATH。linux 极简容器可能没有 zsh/bash，
// 探针失败降级为 Skip（fail-open 是产品语义）；darwin 必须成功。
func TestLoginShellPathSnapshotLive(t *testing.T) {
	path, ok := loginShellPathSnapshot(5 * time.Second)
	if !ok || path == "" || !strings.Contains(path, ":") {
		if runtime.GOOS == "darwin" {
			t.Fatalf("登录 shell PATH 探针失败: ok=%v path=%q", ok, path)
		}
		t.Skipf("无可用登录 shell，fail-open: ok=%v", ok)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run 'TestParseLoginShellPathOutput|TestMergeLoginShellPath|TestAugmentEnvWithLoginShellPath|TestLoginShellPathSnapshotLive' -v
```

预期：编译失败（函数未定义）。

- [ ] **Step 3: 实现**

`agent_child_env_login_shell.go`（无 tag，全平台可编译）：

```go
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
	// loginShellPathCacheTTL 到期后下次调用重新探针。
	loginShellPathCacheTTL = 15 * time.Minute
	// loginShellPathRefreshing 保证同一时刻只有一个探针在跑；等待方拿现值。
	loginShellPathRefreshing atomic.Bool
)

// parseLoginShellPathOutput 从探针输出里切出 PATH。取标记之后到首个换行
// （或结尾）的段落：.zshrc 的欢迎语/回显噪声只会出现在标记前，不会污染。
func parseLoginShellPathOutput(output string) (string, bool) {
	idx := strings.Index(output, loginShellPathMarker)
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
// 锁纪律（评审 P2）：**锁内绝不跑探针**。探针是登录+交互 shell（最长 5s），
// 持锁会把 agentChildProcessEnvValue 的全部调用方（上游自愈探测、claude
// 策略探针等）串行挡住。TTL 到期后由 CAS 选中一个调用者在锁外探针，其余
// 调用者立即拿现值返回（首次可能 fail-open，探针完成即恢复）。
func cachedLoginShellPath() (string, bool) {
	loginShellPathMu.Lock()
	// 门只看时间不看 OK：成功与失败统一按 TTL 缓存（负缓存语义）。
	// 否则在无 shell/慢 shell 的宿主上每次调用都会重新探针（最长 5s），
	// 有消息流量时等于持续后台探针循环。
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
```

`agent_child_env_login_shell_unix.go`：

```go
//go:build !windows

package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// loginShellPathSnapshot 跑一次「登录+交互」shell 取 $PATH。-l 吃
// .zprofile/path_helper，-i 吃 .zshrc——用户的终端正是两者叠加，少一个都
// 不是「一样的环境」。marker 定位保证 .zshrc 回显噪声不污染解析。
//
// fish 的 $PATH 是空格连接的 list，printf "$PATH" 出来无冒号会解析失败
// （fail-open，无害）；fish 用户走 `string join` 分支拿到冒号形 PATH。
func loginShellPathSnapshot(timeout time.Duration) (string, bool) {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" || !isRegularFilePath(shell) {
		for _, candidate := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
			if isRegularFilePath(candidate) {
				shell = candidate
				break
			}
		}
	}
	if shell == "" {
		return "", false
	}
	probe := `printf '` + loginShellPathMarker + `%s' "$PATH"`
	if strings.EqualFold(filepath.Base(shell), "fish") {
		probe = `printf '` + loginShellPathMarker + `%s' (string join : $PATH)`
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", probe)
	output, err := cmd.Output()
	if err != nil {
		// 交互 shell 可能因 job control 警告等返回非零，但 stdout 仍有效；
		// 只有拿不出标记才算失败。
		if parsed, ok := parseLoginShellPathOutput(string(output)); ok {
			return parsed, true
		}
		return "", false
	}
	return parseLoginShellPathOutput(string(output))
}

// isRegularFilePath 判定候选 shell 存在且是常规文件。
func isRegularFilePath(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
```

（`isRegularFilePath` 已用 rg 全包查重无撞名；若实现时有新增撞名以编译器为准改前缀。）

`agent_child_env_login_shell_windows.go`：

```go
//go:build windows

package services

import "time"

// Windows 不做登录 shell 探针：用户级 PATH 已由 augmentEnvWithWindowsUserPath
// 从注册表合并（见 agent_terminal_env_windows.go），那是 Windows 上的等价
// 通道。这里恒失败 → augmentEnvWithLoginShellPath 天然 no-op。
func loginShellPathSnapshot(timeout time.Duration) (string, bool) {
	return "", false
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run 'TestParseLoginShellPathOutput|TestMergeLoginShellPath|TestAugmentEnvWithLoginShellPath|TestLoginShellPathSnapshotLive' -v
```

预期：4 个 PASS。本机 `TestLoginShellPathSnapshotLive` 返回的 PATH 应含 `/opt/homebrew/opt/node@22/bin`（真机验证点）。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_child_env_login_shell.go app/http/services/agent_child_env_login_shell_unix.go app/http/services/agent_child_env_login_shell_windows.go app/http/services/agent_child_env_login_shell_test.go app/http/services/agent_child_env_login_shell_live_test.go
git commit -m "新增:登录 shell PATH 快照,AI 子进程获得与用户终端一致的用户级工具链

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: 接线 agentChildProcessEnv（全家族生效）

**Files:**
- Modify: `app/http/services/agent_ai.go:219-233`（`agentChildProcessEnv`）
- Test: `app/http/services/agent_ai_child_env_test.go`（新建）

- [ ] **Step 1: 写失败测试**

`agent_ai_child_env_test.go`（import：`strings`/`testing`/`time` + config 包，路径照 `agent_ai.go` 的 import 抄）：

```go
package services

import (
	"strings"
	"testing"
	"time"

	"<config 包路径，照 agent_ai.go 抄>"
)

// agentChildProcessEnv 必须合并登录 shell PATH：这是 claude/codex/goal/
// agentcore 所有 AI 子进程的共同咽喉。直接喂缓存伪造值，保证确定性。
func TestAgentChildProcessEnvMergesLoginShellPath(t *testing.T) {
	testCacheLoginShellPath(t, "/fake/login/bin:/usr/bin")

	env := agentChildProcessEnv()
	paths := envPaths(env)
	if len(paths) != 1 {
		t.Fatalf("PATH 应恰一条: %v", paths)
	}
	if paths[0] != "/fake/login/bin:/usr/bin" {
		t.Fatalf("PATH 应为登录快照值, got %q", paths[0])
	}
}

// 客户自定义 env 覆盖必须仍然压过登录快照（显式配置 > 自动探测）。
func TestAgentChildProcessEnvCustomOverrideWins(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	testCacheLoginShellPath(t, "/fake/login/bin")
	setTestGlobalConfigCustomEnv(t, map[string]string{"PATH": "/custom/bin"})

	env := agentChildProcessEnv()
	if got := envPaths(env); len(got) != 1 || got[0] != "/custom/bin" {
		t.Fatalf("自定义覆盖应获胜, got %v", got)
	}
}

func envPaths(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			out = append(out, kv[5:])
		}
	}
	return out
}

// testCacheLoginShellPath 临时改写登录 PATH 缓存，用完还原。
func testCacheLoginShellPath(t *testing.T, path string) {
	t.Helper()
	loginShellPathMu.Lock()
	oldValue, oldOK, oldAt := loginShellPathValue, loginShellPathOK, loginShellPathFetched
	t.Cleanup(func() {
		loginShellPathMu.Lock()
		loginShellPathValue, loginShellPathOK, loginShellPathFetched = oldValue, oldOK, oldAt
		loginShellPathMu.Unlock()
	})
	loginShellPathValue, loginShellPathOK = path, path != ""
	loginShellPathFetched = time.Now()
	loginShellPathMu.Unlock()
}

// setTestGlobalConfigCustomEnv：用 config.SetGlobalConfig 临时替换带
// CustomEnvVars 的全局配置，**还原只走助手内部的 t.Cleanup**（测试体里不要再
// defer，双重还原冗余）。字段名以 EffectiveCustomEnvVars 实现为准，实现阶段
// 第一步先读它。**必须处理 nil**：services 测试按文件名字母序执行，本文件
// 排在多数 config.SetGlobalConfig 测试之前，运行时全局配置很可能还是 nil——
// `clone := *cfg` 会 panic。写法：
//
//	old := config.GetGlobalConfig()
//	if old == nil {
//		fresh := &config.Config{} // 改 CustomEnvVars 字段
//		...SetGlobalConfig(fresh)
//	} else {
//		clone := *old
//		...SetGlobalConfig(&clone)
//	}
//	t.Cleanup(func() { config.SetGlobalConfig(old) }) // old 可为 nil，agent_ai.go:225 已容忍
```

（`setTestGlobalConfigCustomEnv`/`restoreTestGlobalConfig` 的具体实现：`cfg := config.GetGlobalConfig()` 存 old，`clone := *cfg` 浅拷贝改 CustomEnvVars 字段，`config.SetGlobalConfig(&clone)`；Cleanup 里 `config.SetGlobalConfig(old)`。若 config 类型不支持浅拷贝语义再按实际结构调整。）

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run 'TestAgentChildProcessEnv' -v
```

预期：FAIL——PATH 仍是 os.Environ() 的原值。

- [ ] **Step 3: 接线（一行）**

`agent_ai.go` 的 `agentChildProcessEnv`，在 `augmentEnvWithWindowsUserPath` 之后加：

```go
	base := augmentEnvWithWindowsUserPath(os.Environ())
	// macOS/Linux：注入登录 shell PATH 快照，让 AI 子进程看到用户终端同款
	// 工具链（node/npm 等）。Windows 探针恒失败，走上面的注册表合并通道。
	base = augmentEnvWithLoginShellPath(base)
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run 'TestAgentChildProcessEnv' -v
```

预期：PASS。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai.go app/http/services/agent_ai_child_env_test.go
git commit -m "修复:AI 子进程环境接入登录 shell PATH 快照,agent 拉起的 claude/codex/goal 同步获得用户工具链

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: agentcore 构造点注入 + TERM/LANG 兜底

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go`（新助手 + 构造点 751 行）
- Test: `app/http/services/agent_ai_agentcore_test.go`

- [ ] **Step 1: 写失败测试**

```go
// 内核工具子进程必须有 TERM/LANG 兜底：GUI/systemd 启动的 agent 进程两者皆缺，
// 无 TERM 部分命令行工具降级，无 LANG 按 C locale 跑（中文/排序失真）。
// 顺序敏感：先断言兜底（TERM/LANG 空），再改值断言不覆盖（t.Setenv 逐层还原）。
func TestAgentCoreToolEnvTTYDefaults(t *testing.T) {
	t.Setenv("TERM", "")
	t.Setenv("LANG", "")
	env := agentCoreToolEnv()
	if v, ok := scanEnvValue(env, "TERM"); !ok || v != "xterm-256color" {
		t.Fatalf("TERM 兜底缺失: %q", v)
	}
	if v, ok := scanEnvValue(env, "LANG"); !ok || v != "en_US.UTF-8" {
		t.Fatalf("LANG 兜底缺失: %q", v)
	}

	t.Setenv("TERM", "screen")
	env = agentCoreToolEnv()
	if v, _ := scanEnvValue(env, "TERM"); v != "screen" {
		t.Fatalf("TERM 不应被覆盖: %q", v)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run TestAgentCoreToolEnvTTYDefaults -v
```

预期：编译失败（`agentCoreToolEnv`/`scanEnvValue` 未定义）。

- [ ] **Step 3: 实现**

`agent_ai_agentcore.go` 加助手（放 `agentCoreSystemPrompt` 附近）：

```go
// scanEnvValue 从 env 键值对里取变量（存在且非空才算命中）。
func scanEnvValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if index := strings.IndexByte(kv, '='); index > 0 && kv[:index] == key {
			if value := kv[index+1:]; value != "" {
				return value, true
			}
		}
	}
	return "", false
}

// agentCoreToolEnv 组装内核工具（Bash/PowerShell）子进程环境：登录 shell
// PATH 快照 + TERM/LANG 兜底。GUI/systemd 启动的 agent 进程没有这两样，
// 不兜底则子进程里部分 CLI 降级、locale 失真（对照 agent_terminal.go:1670
// 手机终端已补 TERM 的先例）。
func agentCoreToolEnv() []string {
	env := agentChildProcessEnv()
	if _, ok := scanEnvValue(env, "TERM"); !ok {
		env = append(env, "TERM=xterm-256color")
	}
	if _, ok := scanEnvValue(env, "LANG"); !ok {
		if _, ok := scanEnvValue(env, "LC_ALL"); !ok {
			env = append(env, "LANG=en_US.UTF-8")
		}
	}
	return env
}
```

构造点（751 行）加 `Env`：

```go
		Env: &agentcore.ToolEnv{
			ProjectPath: run.projectPath,
			Env:         agentCoreToolEnv(),
			ScopeCheck:  agentCoreScopeCheck(run),
			ShellPolicy: &agentCoreShellPolicy{run: run},
		},
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run TestAgentCoreToolEnvTTYDefaults -v && go test ./app/agentcore/
```

预期：PASS / agentcore 全包绿。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_test.go
git commit -m "修复:aliangcode 工具子进程注入用户环境与 TERM/LANG 兜底,环境盲区闭环

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 5: 系统提示环境块（对齐 claude CLI）

**Files:**
- Modify: `app/http/services/agent_ai_agentcore.go`（`agentCoreSystemPrompt` 299 行 + 调用点 745 行）
- Test: `app/http/services/agent_ai_agentcore_test.go`

- [ ] **Step 1: 写失败测试**

```go
// 系统提示必须带环境块：CLI provider 由 claude CLI 自动注入，内核没有 CLI，
// 不补齐则模型对平台/cwd/日期全盲，只能靠（可能失真的）Bash 探测猜环境。
func TestAgentCoreSystemPromptIncludesEnvBlock(t *testing.T) {
	dir := t.TempDir()
	prompt := agentCoreSystemPrompt(dir)
	if !strings.Contains(prompt, "Working directory: "+dir) {
		t.Fatalf("缺 cwd: %s", prompt)
	}
	if !strings.Contains(prompt, "Is a git repository: no") {
		t.Fatalf("临时目录应判非 git: %s", prompt)
	}
	if !strings.Contains(prompt, "Platform: "+runtime.GOOS) {
		t.Fatalf("缺平台: %s", prompt)
	}
	if !strings.Contains(prompt, "Today's date: "+time.Now().Format("2006-01-02")) {
		t.Fatalf("缺日期: %s", prompt)
	}
	if !strings.Contains(prompt, "OS Version: ") {
		t.Fatalf("缺 OS 版本: %s", prompt)
	}
	// aliang-options 卡片协议必须保留（手机方案选择依赖）。
	if !strings.Contains(prompt, agentAIOptionSystemPrompt) {
		t.Fatalf("options 协议丢失")
	}

	gitDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agentCoreSystemPrompt(gitDir), "Is a git repository: yes") {
		t.Fatalf("git 目录应判 yes")
	}
}
```

（先查现有 `TestAgentCoreSystemPrompt*` 是否存在；评审确认现签名无参、唯一调用点 745、无既有测试引用，签名改动安全。日期断言与生成之间跨午夜理论上会 flake，概率趋零，接受。）

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./app/http/services/ -run TestAgentCoreSystemPromptIncludesEnvBlock -v
```

预期：编译失败（签名未变）。

- [ ] **Step 3: 实现**

（`agent_ai_agentcore.go` 已 import os/exec/runtime/sync/time/fmt/filepath，无需补。）

```go
// cachedOSVersion 取一次 "Darwin 25.2.0" 式内核版本，缓存进程生命周期。
// uname 失败（如 windows）退回 runtime.GOOS，不阻塞会话。
var (
	osVersionOnce sync.Once
	osVersionStr  string
)

func cachedOSVersion() string {
	osVersionOnce.Do(func() {
		out, err := exec.Command("uname", "-sr").Output()
		if err != nil {
			osVersionStr = runtime.GOOS
			return
		}
		osVersionStr = strings.TrimSpace(string(out))
	})
	return osVersionStr
}

func agentCoreIsGitRepo(projectPath string) bool {
	if strings.TrimSpace(projectPath) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(projectPath, ".git"))
	// .git 是目录（普通仓库）或文件（worktree）都算。
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}
```

`agentCoreSystemPrompt(projectPath string)` 尾部、`agentAIOptionSystemPrompt` **之前**插入：

```go
	gitMark := "no"
	if agentCoreIsGitRepo(projectPath) {
		gitMark = "yes"
	}
	b.WriteString("\n<env>\n")
	if strings.TrimSpace(projectPath) == "" {
		b.WriteString("Working directory: (not provided)\n")
	} else {
		fmt.Fprintf(b, "Working directory: %s\n", projectPath)
	}
	fmt.Fprintf(b, "Is a git repository: %s\n", gitMark)
	fmt.Fprintf(b, "Platform: %s\n", runtime.GOOS)
	fmt.Fprintf(b, "OS Version: %s\n", cachedOSVersion())
	fmt.Fprintf(b, "Today's date: %s\n", time.Now().Format("2006-01-02"))
	b.WriteString("</env>\n\n")
```

调用点 745 行改 `System: agentCoreSystemPrompt(run.projectPath),`。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./app/http/services/ -run 'TestAgentCoreSystemPrompt' -v
```

预期：新用例 + 既有 system prompt 用例全 PASS。

- [ ] **Step 5: 提交**

```bash
git add app/http/services/agent_ai_agentcore.go app/http/services/agent_ai_agentcore_test.go
git commit -m "新增:aliangcode 系统提示环境块(cwd/git/平台/OS/日期),对齐 claude CLI 注入行为

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: 全链路验证

- [ ] **Step 1: 全量构建 + 相关包测试 + vet**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate-env-parity
go build ./... && go vet ./app/agentcore/ ./app/http/services/ && go test ./app/agentcore/ ./app/http/services/
```

预期：全绿（services 包大、耗时长属正常；不加 -race——services 包有预存 race flake）。vet 覆盖 copylocks（评审 P2 的回归护栏）。

- [ ] **Step 2: 真机冒烟（部署后）**

本机验证：重启本机 agent 后在仪表盘开 aliangcode 会话，让模型跑 `node --version && which node`，应返回 `/opt/homebrew/opt/node@22/bin/node` 与版本号。liang-dev 走标准部署管线（memory「liang-dev 双单元部署」）。

- [ ] **Step 3: 汇报与收尾**

按 superpowers:verification-before-completion 出验证证据；合并走 finishing-a-development-branch 流程（问用户 merge 还是留 review）。
