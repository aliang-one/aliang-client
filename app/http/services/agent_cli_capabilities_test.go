package services

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---- 解析:claude 对未知 --effort 的两种世代输出 ----

// 旧版(≤2.1.156 一类)commander 硬拒:参数校验失败即退,exit 1,清单在报错里。
const effortProbeOldReject = "error: option '--effort <level>' argument 'ultracode' is invalid. It must be one of: low, medium, high, xhigh, max\n"

// 新版(2.1.280 一代)不再硬拒:打印警告+清单后继续运行(后续会因探针模型名无效而快退)。
const effortProbeNewWarning = "Warning: Unknown --effort value '__aliang_probe__' — ignoring it and using the default effort. Valid values: low, medium, high, xhigh, max.\n" +
	"[claude-code:unrecognized_model] {\"model\":\"__aliang_probe_model__\"}\n"

func TestParseCLIEffortLevelsFromOldHardReject(t *testing.T) {
	got := parseCLIEffortLevels(effortProbeOldReject)
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseCLIEffortLevels(old reject) = %v, want %v", got, want)
	}
}

func TestParseCLIEffortLevelsFromNewWarning(t *testing.T) {
	got := parseCLIEffortLevels(effortProbeNewWarning)
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseCLIEffortLevels(new warning) = %v, want %v", got, want)
	}
}

func TestParseCLIEffortLevelsReturnsNilWithoutPattern(t *testing.T) {
	for _, out := range []string{"", "some random stderr\n", "API Error: 502\n"} {
		if got := parseCLIEffortLevels(out); got != nil {
			t.Fatalf("parseCLIEffortLevels(%q) = %v, want nil", out, got)
		}
	}
}

func TestParseCLIEffortLevelsCapsAndDedups(t *testing.T) {
	out := "error: option '--effort <level>' argument 'x' is invalid. It must be one of: low, LOW, medium, high, xhigh, max, , extra9, extra10, extra11, extra12, extra13\n"
	got := parseCLIEffortLevels(out)
	if len(got) > maxProbedEffortLevels {
		t.Fatalf("levels = %d, want capped at %d", len(got), maxProbedEffortLevels)
	}
	seen := map[string]bool{}
	for _, l := range got {
		if seen[l] {
			t.Fatalf("duplicate level %q in %v", l, got)
		}
		seen[l] = true
	}
	if !seen["low"] || !seen["max"] {
		t.Fatalf("levels %v lost core entries", got)
	}
}

// ---- isAgentAIEffortRejected:仅硬拒世代视为"被拒"(警告世代 CLI 自己继续跑) ----

func TestIsAgentAIEffortRejectedOnlyForHardReject(t *testing.T) {
	rejected, levels := isAgentAIEffortRejected(effortProbeOldReject)
	if !rejected || !reflect.DeepEqual(levels, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("old reject: rejected=%v levels=%v", rejected, levels)
	}
	if rejected, _ := isAgentAIEffortRejected(effortProbeNewWarning); rejected {
		t.Fatalf("new warning must not count as rejected")
	}
	if rejected, _ := isAgentAIEffortRejected("API Error: 502\n"); rejected {
		t.Fatalf("plain error must not count as rejected")
	}
}

// ---- 版本解析 ----

func TestParseCLIVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2.1.280 (Claude Code)\n", "2.1.280"},
		{"codex-cli 0.144.5\n", "0.144.5"},
		{"1.18.4\n", "1.18.4"},
		{"claude 2.1.156 (Claude Code)", "2.1.156"},
		{"", ""},
		{"no version here", ""},
	}
	for _, c := range cases {
		if got := parseCLIVersion(c.in); got != c.want {
			t.Fatalf("parseCLIVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---- 全局阶梯钳制 ----

func TestClampEffortToSupported(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		supported []string
		want      string
	}{
		{"请求空→不钳", "", []string{"low"}, ""},
		{"supported缺失→不钳", "ultracode", nil, "ultracode"},
		{"ultracode→max", "ultracode", []string{"low", "medium", "high", "xhigh", "max"}, "max"},
		{"无max档→xhigh", "ultracode", []string{"low", "medium", "high", "xhigh"}, "xhigh"},
		{"max→high", "max", []string{"low", "medium", "high"}, "high"},
		{"请求本身支持→原样", "high", []string{"low", "medium", "high"}, "high"},
		{"大小写归一", "ULTRAcode", []string{"low", "max"}, "max"},
		{"请求不在阶梯→从顶向下", "megaspecial", []string{"medium"}, "medium"},
		{"全不支持→空(交CLI默认)", "ultracode", []string{"turbo"}, ""},
		{"supported用原始拼写返回", "ultracode", []string{"Low", "MAX"}, "MAX"},
	}
	for _, c := range cases {
		if got := clampEffortToSupported(c.requested, c.supported); got != c.want {
			t.Fatalf("%s: clampEffortToSupported(%q, %v) = %q, want %q", c.name, c.requested, c.supported, got, c.want)
		}
	}
}

// ---- 探针(用假 CLI 脚本走真实 exec 路径) ----

func writeFakeCLI(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake cli: %v", err)
	}
	return path
}

func TestProbeCLIVersionViaFakeCLI(t *testing.T) {
	dir := t.TempDir()
	path := writeFakeCLI(t, dir, "fakeclaude", "echo \"2.1.280 (Claude Code)\"\n")
	if got := probeCLIVersion(path); got != "2.1.280" {
		t.Fatalf("probeCLIVersion = %q, want 2.1.280", got)
	}
	if got := probeCLIVersion(filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("probeCLIVersion(missing) = %q, want empty", got)
	}
}

func TestProbeClaudeEffortLevelsViaFakeCLI(t *testing.T) {
	dir := t.TempDir()
	path := writeFakeCLI(t, dir, "fakeclaude2", "echo \""+strings.TrimSuffix(effortProbeNewWarning, "\n")+"\"\n")
	got := probeClaudeEffortLevels(path)
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("probeClaudeEffortLevels = %v, want %v", got, want)
	}
	// 探不到清单 → nil(未知,不设限)
	quiet := writeFakeCLI(t, dir, "fakeclaude3", "echo \"hello\"\n")
	if got := probeClaudeEffortLevels(quiet); got != nil {
		t.Fatalf("probeClaudeEffortLevels(quiet) = %v, want nil", got)
	}
}

// ---- detectAgentTools 接线:可用 tool 带上 Version+Efforts ----

func TestDetectAgentToolsWithReportsCapabilities(t *testing.T) {
	dir := t.TempDir()
	// 同一脚本同时响应 --version 探针与 effort 探针(两种调用都输出固定内容)。
	claudePath := writeFakeCLI(t, dir, "claude-fake",
		"echo \"2.1.156 (Claude Code)\"; echo \""+strings.TrimSuffix(effortProbeNewWarning, "\n")+"\"\n")
	codexPath := writeFakeCLI(t, dir, "codex-fake", "echo \"codex-cli 0.144.5\"\n")
	look := func(name string) (string, error) {
		switch name {
		case "claude", "claudecode":
			return claudePath, nil
		case "codex":
			return codexPath, nil
		case "opencode":
			return "", os.ErrNotExist
		}
		return "", os.ErrNotExist
	}

	tools := detectAgentToolsWith(look)
	byID := map[string]modelsAgentToolView{}
	for _, tool := range tools {
		byID[tool.ID] = modelsAgentToolView{version: tool.Version, efforts: tool.Efforts, available: tool.Available}
	}

	claude, ok := byID["claudecode"]
	if !ok || !claude.available {
		t.Fatalf("claudecode tool missing or unavailable: %+v", byID)
	}
	if claude.version != "2.1.156" {
		t.Fatalf("claudecode version = %q, want 2.1.156", claude.version)
	}
	if !reflect.DeepEqual(claude.efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("claudecode efforts = %v", claude.efforts)
	}

	codex, ok := byID["codex"]
	if !ok || !codex.available {
		t.Fatalf("codex tool missing or unavailable: %+v", byID)
	}
	if codex.version != "0.144.5" {
		t.Fatalf("codex version = %q, want 0.144.5", codex.version)
	}
	if len(codex.efforts) == 0 {
		t.Fatalf("codex efforts = %v, want static suffix list", codex.efforts)
	}

	// pi/gemini 仅注册检测定义;look 找不到时应保持不可用而非报错
	for _, id := range []string{"pi", "gemini"} {
		tool, ok := byID[id]
		if !ok {
			t.Fatalf("tool %s not registered in defs", id)
		}
		if tool.available {
			t.Fatalf("tool %s should be unavailable when binary missing", id)
		}
	}
}

// modelsAgentToolView 只用于断言的小视图,避免直接比较结构体。
type modelsAgentToolView struct {
	version   string
	efforts   []string
	available bool
}
