package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	rej := isAgentAIEffortRejected(effortProbeOldReject)
	if !rej.rejected || rej.flagUnsupported ||
		!reflect.DeepEqual(rej.levels, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("old reject: %+v", rej)
	}
	if rej := isAgentAIEffortRejected(effortProbeNewWarning); rej.rejected {
		t.Fatalf("new warning must not count as rejected: %+v", rej)
	}
	if rej := isAgentAIEffortRejected("API Error: 502\n"); rej.rejected {
		t.Fatalf("plain error must not count as rejected: %+v", rej)
	}
}

// 极老世代(2026-01 的 claude 2.1.17 一类)根本没注册 --effort 这个 flag:
// commander 在参数解析阶段就报 "error: unknown option '--effort'" 并 exit 1。
// 这不是"值被拒"(没有合法清单可钳),而是"flag 不存在"(重试应去掉 flag)。
// 2026-09-29 事故:agent 解析到 ~/.local/bin claude 2.1.17,--effort max 秒挂
// 且探测/重试两道防线都把它当"未知不设限"放行了。
const effortFlagUnknownReject = "error: unknown option '--effort'\n"

func TestIsAgentAIEffortRejectedFlagUnknown(t *testing.T) {
	rej := isAgentAIEffortRejected(effortFlagUnknownReject)
	if !rej.rejected {
		t.Fatalf("flag-unknown reject must count as rejected: %+v", rej)
	}
	if !rej.flagUnsupported {
		t.Fatalf("flag-unknown reject must set flagUnsupported: %+v", rej)
	}
	if len(rej.levels) != 0 {
		t.Fatalf("flag-unknown reject has no level list: %+v", rej)
	}
	// 相近但不同的 flag 报错不能误伤。
	if rej := isAgentAIEffortRejected("error: unknown option '--efforts'\n"); rej.rejected {
		t.Fatalf("unrelated unknown option must not count: %+v", rej)
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

// ---- flag 级拒绝:探测归类 + 缓存学习 + spawn 前去档 ----

func TestProbeClaudeEffortCapabilityFlagUnsupported(t *testing.T) {
	dir := t.TempDir()
	path := writeFakeCLI(t, dir, "fakeclaude-old",
		"echo \""+strings.TrimSuffix(effortFlagUnknownReject, "\n")+"\"; exit 1\n")

	probe, ok := probeClaudeEffortCapability(path)
	if !ok || !probe.ok || !probe.flagUnsupported || len(probe.levels) != 0 {
		t.Fatalf("probeClaudeEffortCapability = (ok=%v, %+v), want ok with flagUnsupported", ok, probe)
	}
	// 清单视角:flag 不存在 = 没有可钳清单(nil,而非"未知不设限"之外的含义丢失)。
	if got := probeClaudeEffortLevels(path); got != nil {
		t.Fatalf("probeClaudeEffortLevels(flag-unknown) = %v, want nil", got)
	}
	// 探测结论必须落缓存:spawn 路径凭缓存就能去档,不再现场探测。
	if !claudeEffortFlagUnsupportedCached(path) {
		t.Fatalf("flag-unsupported probe result must be cached for %s", path)
	}
	// 未探测过的路径:缓存只读查询必须返回 false,绝不阻塞 spawn。
	if claudeEffortFlagUnsupportedCached(filepath.Join(dir, "never-probed")) {
		t.Fatalf("unprobed path must not be flagged")
	}
}

func TestRememberClaudeEffortFlagUnsupported(t *testing.T) {
	dir := t.TempDir()
	path := writeFakeCLI(t, dir, "fakeclaude-learn", "echo \"2.1.17 (Claude Code)\"\n")
	if claudeEffortFlagUnsupportedCached(path) {
		t.Fatalf("fresh path must not be flagged before learning")
	}
	rememberClaudeEffortFlagUnsupported(path)
	if !claudeEffortFlagUnsupportedCached(path) {
		t.Fatalf("runtime-learned flag rejection must be remembered for %s", path)
	}
	rememberClaudeEffortFlagUnsupported("")
	if claudeEffortFlagUnsupportedCached("") {
		t.Fatalf("empty path must not be flagged")
	}
}

func TestEffortDropForBinary(t *testing.T) {
	dir := t.TempDir()
	stale := writeFakeCLI(t, dir, "fakeclaude-stale", "echo \"2.1.17 (Claude Code)\"\n")
	rememberClaudeEffortFlagUnsupported(stale)

	if got := effortDropForBinary("max", stale); got != "" {
		t.Fatalf("effortDropForBinary(max, known-stale) = %q, want dropped", got)
	}
	fresh := writeFakeCLI(t, dir, "fakeclaude-fresh", "echo \"2.1.280 (Claude Code)\"\n")
	if got := effortDropForBinary("max", fresh); got != "max" {
		t.Fatalf("effortDropForBinary(max, unprobed) = %q, want passthrough", got)
	}
	if got := effortDropForBinary("  ", stale); got != "  " {
		t.Fatalf("effortDropForBinary(blank, known-stale) = %q, want passthrough (nothing to send)", got)
	}
}

// ---- 版本探针热路径安全:失败负缓存 + 只读窥视 ----

// probeCLIVersion 失败必须负缓存(TTL 内不重复 spawn):它被放在了每回合的
// fallback lookup 与日志路径上,持久失败的二进制若每调用都重探,每次最多阻塞 2s。
func TestProbeCLIVersionNegativeCachesFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-only")
	}
	_, err := exec.LookPath("bash")
	require.NoError(t, err)

	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	script := filepath.Join(t.TempDir(), "no-version-claude")
	require.NoError(t, os.WriteFile(script, []byte(
		"#!/bin/bash\n"+
			"echo x >> "+counter+"\n"+
			"echo \"Claude Code dev build\"\n"+
			"exit 0\n",
	), 0o755))

	if got := probeCLIVersion(script); got != "" {
		t.Fatalf("first probe = %q, want empty (no x.y.z in output)", got)
	}
	if got := probeCLIVersion(script); got != "" {
		t.Fatalf("second probe = %q, want empty", got)
	}
	raw, err := os.ReadFile(counter)
	require.NoError(t, err, "failing --version must have executed once")
	if n := len(strings.Split(strings.TrimRight(string(raw), "\n"), "\n")); n != 1 {
		t.Fatalf("failing probe executed %d times, want 1 (negative cache must suppress re-spawns within TTL)", n)
	}
}

// cachedCLIVersion 只读窥视:命中成功缓存返回版本;未缓存绝不 spawn。
func TestCachedCLIVersionPeekOnly(t *testing.T) {
	dir := t.TempDir()
	if got := cachedCLIVersion(filepath.Join(dir, "never-probed")); got != "" {
		t.Fatalf("cachedCLIVersion(unprobed) = %q, want empty without spawning", got)
	}
	path := writeFakeCLI(t, dir, "peek-claude", "echo \"2.1.280 (Claude Code)\"\n")
	if got := probeCLIVersion(path); got != "2.1.280" {
		t.Fatalf("probeCLIVersion = %q", got)
	}
	if got := cachedCLIVersion(path); got != "2.1.280" {
		t.Fatalf("cachedCLIVersion = %q, want warmed 2.1.280", got)
	}
}

// 并发竞态:探测失败落地不得冲掉运行时学习到的 flagUnsupported 结论
// (冲掉的代价=该二进制多死一次 spawn)。探测先起跑、学习落在飞行中。
func TestProbeFailureDoesNotClobberLearnedFlagUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-only")
	}
	_, err := exec.LookPath("bash")
	require.NoError(t, err)

	dir := t.TempDir()
	path := writeFakeCLI(t, dir, "slow-garbage-claude", "sleep 0.5; echo \"garbage output\"\n")

	type probeResult struct {
		probe cliCapabilityProbe
		ok    bool
	}
	done := make(chan probeResult, 1)
	go func() {
		probe, ok := probeClaudeEffortCapability(path)
		done <- probeResult{probe, ok}
	}()
	// 脚本要睡 0.5s,此刻探测必然仍在飞行中,学习恰好落在探测前。
	time.Sleep(100 * time.Millisecond)
	rememberClaudeEffortFlagUnsupported(path)

	res := <-done
	if !res.ok || !res.probe.flagUnsupported {
		t.Fatalf("probe result = (ok=%v, %+v), want preserved flagUnsupported", res.ok, res.probe)
	}
	if !claudeEffortFlagUnsupportedCached(path) {
		t.Fatalf("learned flagUnsupported was clobbered by a failed probe for %s", path)
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
