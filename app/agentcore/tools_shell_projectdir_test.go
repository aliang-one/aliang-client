package agentcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-11-25 线上事故:手机端把字面 "~" 当 project_path 发上来,runShell 直接
// 把它落进 cmd.Dir。os/exec 不做波浪号展开,Windows 上每条命令都在执行前死在
// 「chdir ~: The system cannot find the file specified.」,模型只能盲试,直到
// 触发「连续 5 次执行失败」保险丝整局报废。这里锁定两层防线:ProjectPath 先
// 展开 ~;目录不存在时给出可行动的报错而不是甩 cryptic 的 chdir 失败。

func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	// macOS 上 /var 是 /private/var 的软链:不解析的话,shell 里 pwd 打印的
	// 真实路径与 t.TempDir() 返回值对不上。
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("HOME", resolved)
	t.Setenv("USERPROFILE", resolved)
	return resolved
}

func TestRunShellExpandsTildeProjectDir(t *testing.T) {
	home := setTestHome(t)
	env := &ToolEnv{ProjectPath: "~", ShellPolicy: shPolicy()}

	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{"command": "pwd"}, env)
	if res.IsError {
		t.Fatalf("ProjectPath=\"~\" 应展开到主目录后执行,却报错: %s", res.Content)
	}
	if got := strings.TrimSpace(res.Content); got != home {
		t.Fatalf("pwd = %q, want home %q", got, home)
	}
}

func TestRunShellExpandsTildeSubdirProjectDir(t *testing.T) {
	home := setTestHome(t)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	env := &ToolEnv{ProjectPath: "~/proj", ShellPolicy: shPolicy()}

	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{"command": "pwd"}, env)
	if res.IsError {
		t.Fatalf("ProjectPath=\"~/proj\" 应展开后执行,却报错: %s", res.Content)
	}
	if got := strings.TrimSpace(res.Content); got != proj {
		t.Fatalf("pwd = %q, want %q", got, proj)
	}
}

func TestRunShellMissingProjectDirFailsWithActionableError(t *testing.T) {
	setTestHome(t)
	env := &ToolEnv{ProjectPath: filepath.Join(t.TempDir(), "nope"), ShellPolicy: shPolicy()}

	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{"command": "echo hi"}, env)
	if !res.IsError {
		t.Fatalf("项目目录不存在时应报错,却成功返回: %s", res.Content)
	}
	if !strings.Contains(res.Content, "项目目录不存在") {
		t.Fatalf("报错应指出项目目录问题,得到: %s", res.Content)
	}
	// 关键回归点:不能再把 cryptic 的 os/exec chdir 错误甩给模型——那正是
	// 2026-11-25 事故里模型无法理解、只会盲试重试的形态。
	if strings.Contains(res.Content, "chdir") {
		t.Fatalf("报错不应携带原始 chdir 失败,得到: %s", res.Content)
	}
}
