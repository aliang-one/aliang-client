package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

// aliang-peer 跨设备 MCP 工具面的平台托管注册(spec 2026-11-03 §6)。
//
// 职责:把已存在的 `aliang mcp` stdio 服务端(cmd/mcp.go,server 名
// aliang-peer)注册进 claude(<home>/.claude.json 顶层 mcpServers)与 codex
// (<home>/.codex/config.toml [mcp_servers.aliang-peer])的原生配置,并按
// customer.peer_mcp 总开关做启动自愈对齐。总开关的持久化归 customer 配置
// 通道(仪表盘 → core 落盘/DB),本文件只管「内核配置文件的注册状态」;
// 两态允许短暂分歧,由状态回显如实呈现 + 启动自愈收敛。
//
// 硬约束:
//   - 内核配置是用户文件,合并写绝不语义重写:claude 侧全量 map 往返
//     (json.Decoder.UseNumber 保数字字面量),codex 侧文本级段拼接
//     (保注释/空行/键序,刻意不用 toml re-encode)。
//   - 家目录一律走 peerMCPHome(生产=agentHome(),root agent 下解析登录
//     用户家目录),不写字面 ~。
//   - 写失败只返回错误/记日志,绝不 panic、绝不阻断内核 run。

const peerMCPManagedServerName = "aliang-peer"

// peerMCPHome 与 mcpInventoryHome 同款的测试注入点:生产走 agentHome()。
var peerMCPHome = agentHome

// PeerMCPKernelState 单内核注册状态(诚实回显的取值域)。
type PeerMCPKernelState string

const (
	PeerMCPStateRegistered PeerMCPKernelState = "registered" // 条目在且 command 指向当前二进制
	PeerMCPStateStale      PeerMCPKernelState = "stale"      // 条目在但 command 与当前二进制不一致(含手工注册形态)
	PeerMCPStateMissing    PeerMCPKernelState = "missing"    // 条目不在
)

// PeerMCPApplyResult 单内核注册结果(HTTP 状态回显的数据形状)。
type PeerMCPApplyResult struct {
	Kernel string             `json:"kernel"`          // "claude" | "codex"
	State  PeerMCPKernelState `json:"state"`           // 应用后的状态(失败时为应用前状态)
	Error  string             `json:"error,omitempty"` // 非空=该内核应用失败,其余内核不受影响
}

// peerMCPCommand 返回注册进内核配置的 stdio 命令:当前二进制绝对路径 +
// mcp 子命令(与 cmd/mcp.go 头注释给出的手工接入形态一致)。
func peerMCPCommand() (string, []string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("resolve executable: %w", err)
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", nil, fmt.Errorf("abs executable path: %w", err)
	}
	return abs, []string{"mcp"}, nil
}

func peerMCPCommandMatches(actual string) bool {
	if strings.TrimSpace(actual) == "" {
		return false
	}
	command, _, err := peerMCPCommand()
	return err == nil && actual == command
}

// ---------------------------------------------------------------------------
// 状态读取(只读,不写)
// ---------------------------------------------------------------------------

// readPeerMCPClaudeEntry 读 .claude.json 顶层 mcpServers["aliang-peer"] 的
// command。文件缺失/键缺失 → ("", false, nil)。
func readPeerMCPClaudeEntry(home string) (string, bool, error) {
	path := filepath.Join(home, ".claude.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	var root struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", false, fmt.Errorf("parse %s: %w", path, err)
	}
	entry, ok := root.MCPServers[peerMCPManagedServerName]
	if !ok || strings.TrimSpace(entry.Command) == "" {
		return "", false, nil
	}
	return entry.Command, true, nil
}

// readPeerMCPCodexEntry 读 config.toml [mcp_servers.aliang-peer].command。
func readPeerMCPCodexEntry(home string) (string, bool, error) {
	path := filepath.Join(home, ".codex", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg codexConfigFile
	if _, err := toml.Decode(string(raw), &cfg); err != nil {
		return "", false, fmt.Errorf("parse %s: %w", path, err)
	}
	entry, ok := cfg.MCPServers[peerMCPManagedServerName]
	if !ok || strings.TrimSpace(entry.Command) == "" {
		return "", false, nil
	}
	return entry.Command, true, nil
}

func peerMCPKernelState(home, kernel string) (PeerMCPKernelState, error) {
	var (
		command string
		found   bool
		err     error
	)
	switch kernel {
	case "claude":
		command, found, err = readPeerMCPClaudeEntry(home)
	case "codex":
		command, found, err = readPeerMCPCodexEntry(home)
	default:
		return "", fmt.Errorf("unknown kernel %q", kernel)
	}
	if err != nil {
		return "", err
	}
	if !found {
		return PeerMCPStateMissing, nil
	}
	if peerMCPCommandMatches(command) {
		return PeerMCPStateRegistered, nil
	}
	return PeerMCPStateStale, nil
}

// peerMCPRegistrationStates 读两内核当前注册状态(不写)。
func peerMCPRegistrationStates(home string) ([]PeerMCPApplyResult, error) {
	out := make([]PeerMCPApplyResult, 0, 2)
	for _, kernel := range []string{"claude", "codex"} {
		state, err := peerMCPKernelState(home, kernel)
		if err != nil {
			return nil, err
		}
		out = append(out, PeerMCPApplyResult{Kernel: kernel, State: state})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 应用(写,逐内核 best-effort)
// ---------------------------------------------------------------------------

// ApplyPeerMCPRegistration 按 register 对齐两内核注册状态。已满足的内核
// 跳过写盘(幂等);单内核失败不影响另一内核;返回应用后的逐内核状态。
func ApplyPeerMCPRegistration(home string, register bool) []PeerMCPApplyResult {
	out := make([]PeerMCPApplyResult, 0, 2)
	for _, kernel := range []string{"claude", "codex"} {
		state, err := peerMCPKernelState(home, kernel)
		if err != nil {
			out = append(out, PeerMCPApplyResult{Kernel: kernel, State: PeerMCPStateMissing, Error: err.Error()})
			continue
		}
		if (register && state == PeerMCPStateRegistered) || (!register && state == PeerMCPStateMissing) {
			out = append(out, PeerMCPApplyResult{Kernel: kernel, State: state})
			continue
		}
		var applyErr error
		switch kernel {
		case "claude":
			applyErr = applyPeerMCPClaudeRegistration(home, register)
		case "codex":
			applyErr = applyPeerMCPCodexRegistration(home, register)
		}
		if applyErr != nil {
			out = append(out, PeerMCPApplyResult{Kernel: kernel, State: state, Error: applyErr.Error()})
			continue
		}
		newState, err := peerMCPKernelState(home, kernel)
		if err != nil {
			out = append(out, PeerMCPApplyResult{Kernel: kernel, State: state, Error: err.Error()})
			continue
		}
		out = append(out, PeerMCPApplyResult{Kernel: kernel, State: newState})
	}
	return out
}

// EnsurePeerMCPMatchesConfig 启动自愈(runtime 进程专用):按总开关对齐
// 注册状态——开=确保注册且 command 指向当前二进制;关=确保注销。逐内核
// best-effort,失败只 Warn 不阻断启动。
func EnsurePeerMCPMatchesConfig() {
	EnsurePeerMCPMatchesConfigForTest(peerMCPHome())
}

// EnsurePeerMCPMatchesConfigForTest 是 home 可注入的自愈本体。
func EnsurePeerMCPMatchesConfigForTest(home string) {
	if home == "" {
		logger.Warn("peer-mcp: startup heal skipped, agent home unavailable")
		return
	}
	register := config.GetGlobalConfig().EffectivePeerMCPEnabled()
	for _, r := range ApplyPeerMCPRegistration(home, register) {
		if r.Error != "" {
			logger.Warn(fmt.Sprintf("peer-mcp: startup heal kernel=%s register=%v err=%s", r.Kernel, register, r.Error))
		}
	}
}

// ---------------------------------------------------------------------------
// claude 侧:~/.claude.json 全量 map 往返
// ---------------------------------------------------------------------------

// applyPeerMCPClaudeRegistration 维护 .claude.json 顶层 mcpServers 的
// aliang-peer 条目。全量 map 往返保留其余顶层键与 server;UseNumber 让数字
// 字面量原样回写(时间戳不漂移)。键序经 encoding/json 确定性重排——
// claude CLI 每次自写同样会重排,无语义影响。
func applyPeerMCPClaudeRegistration(home string, register bool) error {
	path := filepath.Join(home, ".claude.json")
	root := map[string]interface{}{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&root); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	case os.IsNotExist(err):
		if !register {
			return nil // 没文件=没注册,幂等
		}
	default:
		return fmt.Errorf("read %s: %w", path, err)
	}

	servers, _ := root["mcpServers"].(map[string]interface{})
	if servers == nil {
		if !register {
			return nil
		}
		servers = map[string]interface{}{}
		root["mcpServers"] = servers
	}

	if register {
		command, args, err := peerMCPCommand()
		if err != nil {
			return err
		}
		servers[peerMCPManagedServerName] = map[string]interface{}{
			"type":    "stdio",
			"command": command,
			"args":    args,
		}
	} else {
		if _, ok := servers[peerMCPManagedServerName]; !ok {
			return nil // 幂等
		}
		delete(servers, peerMCPManagedServerName)
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return writePeerMCPFileAtomic(path, out)
}

// ---------------------------------------------------------------------------
// codex 侧:config.toml 文本级段拼接
// ---------------------------------------------------------------------------

// applyPeerMCPCodexRegistration 文本级维护 config.toml 的
// [mcp_servers.aliang-peer] 块:register=true 替换既有块(或文件尾追加),
// false 整块删除。既有块 = 表头行起到下一个 '[' 表头行/文件尾。只认官方
// 表头形态;文件其余内容(注释/空行/键序)原样保留——刻意不走 toml
// re-encode,整文件重编码会毁掉用户注释与排版。
func applyPeerMCPCodexRegistration(home string, register bool) error {
	path := filepath.Join(home, ".codex", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	command, args, err := peerMCPCommand()
	if err != nil {
		return err
	}
	header := "[mcp_servers." + peerMCPManagedServerName + "]"
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil // 空文件归一成零行
	}
	start, end := -1, len(lines)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if start < 0 {
			if trimmed == header {
				start = i
			}
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			end = i
			break
		}
	}

	trimTrailingBlank := func(in []string) []string {
		for len(in) > 0 && strings.TrimSpace(in[len(in)-1]) == "" {
			in = in[:len(in)-1]
		}
		return in
	}

	var out []string
	switch {
	case register && start < 0:
		out = trimTrailingBlank(lines)
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, buildPeerMCPTOMLBlock(command, args)...)
	case register: // 替换既有托管块(含块内旧注释)
		out = append(append(append([]string{}, lines[:start]...), buildPeerMCPTOMLBlock(command, args)...), lines[end:]...)
	case start < 0: // 注销但本来就没有:幂等
		return nil
	default: // 注销:整块移除
		out = trimTrailingBlank(append(append([]string{}, lines[:start]...), lines[end:]...))
		out = append(out, "")
	}
	if register {
		// 全新机器可能连 ~/.codex 都没有;注销路径要么早退要么目录必在,
		// 只有注册路径需要兜底建目录。
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
	}
	return writePeerMCPFileAtomic(path, []byte(strings.Join(out, "\n")))
}

// buildPeerMCPTOMLBlock 生成托管块;末尾空行元素用于 join 后收尾换行。
func buildPeerMCPTOMLBlock(command string, args []string) []string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = quickSetupTOMLQuote(a)
	}
	return []string{
		"[mcp_servers." + peerMCPManagedServerName + "]",
		"command = " + quickSetupTOMLQuote(command),
		"args = [" + strings.Join(quoted, ", ") + "]",
		"",
	}
}

// ---------------------------------------------------------------------------
// 原子写(保留原文件权限)
// ---------------------------------------------------------------------------

func writePeerMCPFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".aliang-peer-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename into %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// AgentService 门面(HTTP handler 用)
// ---------------------------------------------------------------------------

// PeerMCPStatus 返回总开关与两内核注册状态(诚实回显,不写任何文件)。
func (s *AgentService) PeerMCPStatus() map[string]interface{} {
	states, err := peerMCPRegistrationStates(peerMCPHome())
	registration := states
	if err != nil {
		registration = nil
	}
	return map[string]interface{}{
		"enabled":      config.GetGlobalConfig().EffectivePeerMCPEnabled(),
		"registration": registration,
		"status_error": errorStringOrNil(err),
	}
}

// SetPeerMCPRegistration 应用注册(逐内核 best-effort)并同步本进程内存
// 配置,返回应用后的状态。总开关本身的持久化走 customer.peer_mcp(仪表盘
// 两段式:先存配置再调本端点),本方法不写配置文件。
func (s *AgentService) SetPeerMCPRegistration(enable bool) map[string]interface{} {
	syncPeerMCPConfigInMemory(enable)
	home := peerMCPHome()
	results := ApplyPeerMCPRegistration(home, enable)
	for _, r := range results {
		if r.Error != "" {
			logger.Warn(fmt.Sprintf("peer-mcp: apply kernel=%s register=%v err=%s", r.Kernel, enable, r.Error))
		}
	}
	return map[string]interface{}{"enabled": enable, "registration": results}
}

// syncPeerMCPConfigInMemory 把开关同步进本进程内存全局配置:runtime 的配置
// 快照只在启动时加载(core 负责落盘与 DB),不同步的话 agentcore 注入门控
// 要等 agent 重启才生效。只动内存,磁盘持久化仍归 customer 配置通道。
func syncPeerMCPConfigInMemory(enable bool) {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return
	}
	if cfg.Customer == nil {
		cfg.Customer = &config.CustomerConfig{}
	}
	on := enable
	cfg.Customer.PeerMCP = &config.PeerMCPConfig{Enable: &on}
}

func errorStringOrNil(err error) interface{} {
	if err == nil {
		return nil
	}
	return err.Error()
}
