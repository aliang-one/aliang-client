package services

// agent_mcp_registry.go 统一 MCP 注册表(alianggate 第一方 MCP 资产)。
//
// 文件:<agentHome>/.aliang/mcp.json。三内核消费同一合并视图:
//   agentcore   直读(agentCoreMCPRawServersIn 加第三源,本文件同名键覆盖);
//   claudecode  claudeTierMCPArgs 合并注入(注册表不受 projectMCPTrusted 门控);
//   codex       applyAliangMCPCodexSync 维护 [mcp_servers.aliang-mcp-<name>] 托管块。
// 遗留源(~/.claude.json 顶层 + 插件 .mcp.json)继续生效,合并序:
//   claude.json → 插件 → 注册表(同名后者胜)→ 项目 .mcp.json(仅 claude 侧,信任闸不变)。
//
// 写盘纪律与 peer 注册同款:全量往返保留未知键(json.Decoder UseNumber)、
// SetEscapeHTML(false)、tmp+rename 原子写(writePeerMCPFileAtomic)、内容未变幂等跳写。
// 安全:env/headers 值只进 transport 构造,永不进日志;名称禁 aliang- 前缀(平台保留)。
//
// spec: docs/superpowers/specs/2026-11-05-aliang-mcp-registry-design.md

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"aliang.one/nursorgate/common/logger"
)

const (
	// aliangMCPServerNameMax 对齐 sanitizeAgentCoreMCPSegment 的段宽口径(64)。
	aliangMCPServerNameMax = 64
	// aliangMCPServerMaxCount 对齐 agentCoreMCPServerCap(写通道防呆上限)。
	aliangMCPServerMaxCount = 24
	// aliangMCPReservedPrefix 平台保留前缀:peer 托管名 aliang-peer 与本文件
	// codex 托管名 aliang-mcp-* 都落在该族下,用户条目禁用避免双向踩踏。
	aliangMCPReservedPrefix = "aliang-"
	// aliangMCPCodexPrefix codex config.toml 托管块表头前缀。
	aliangMCPCodexPrefix = "aliang-mcp-"

	aliangMCPRegistryVersion = 1
)

// aliangMCPServerNameRe 单一真值源:段宽引用 aliangMCPServerNameMax,勿再写字面量。
var aliangMCPServerNameRe = regexp.MustCompile(fmt.Sprintf("^[a-zA-Z0-9_-]{1,%d}$", aliangMCPServerNameMax))

// aliangMCPServerEntry 一条注册表条目(写通道强类型形态)。
type aliangMCPServerEntry struct {
	Type    string            `json:"type,omitempty"`    // ""|stdio|http|sse
	Command string            `json:"command,omitempty"` // stdio 必填
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"` // http/sse 必填
	Headers map[string]string `json:"headers,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"` // nil=启用
}

func (e aliangMCPServerEntry) enabled() bool { return e.Enabled == nil || *e.Enabled }

// aliangMCPRegistryPath 注册表落盘路径。
func aliangMCPRegistryPath(home string) string {
	return filepath.Join(home, ".aliang", "mcp.json")
}

// loadAliangMCPRegistryRoot 读注册表整文件为通用 map(UseNumber 保数字字面量,
// 未知顶层键/未知 server 字段全量保留供往返)。文件缺失返回 existed=false。
func loadAliangMCPRegistryRoot(home string) (root map[string]interface{}, existed bool, err error) {
	path := aliangMCPRegistryPath(home)
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, readErr)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return root, true, nil
}

// aliangMCPEntryUsable 读通道形态判定:type 归一(stdio 缺省),stdio 需非空
// command,http/sse 需非空 url;其余形态 fail-closed 跳过。返回归一 type。
func aliangMCPEntryUsable(_ string, raw map[string]interface{}) (string, bool) {
	typ := "stdio"
	if v, ok := raw["type"].(string); ok && strings.TrimSpace(v) != "" {
		typ = strings.ToLower(strings.TrimSpace(v))
	}
	switch typ {
	case "stdio":
		if cmd, _ := raw["command"].(string); strings.TrimSpace(cmd) != "" {
			return typ, true
		}
	case "http", "sse":
		if u, _ := raw["url"].(string); strings.TrimSpace(u) != "" {
			return typ, true
		}
	}
	return typ, false
}

// readAliangMCPRegistryInto 把注册表启用条目并入 claude 形态 dst(同名键覆盖
// 遗留源),返回并入条目数。跳过语义(fail-closed):
//   - 名称触碰平台保留前缀 aliang- / enabled 非 bool / 形态不合法(缺
//     command/url、未知 transport)→ Warn 后跳过;
//   - enabled=false(布尔)与 server 值非 map 的条目 → 静默跳过;
//   - 并入时剥掉注册表专属 enabled 键(claude 形态无此字段,语义已由跳过表达);
//   - version 非 1 仅 Warn(前向兼容:不拒读、不迁移、不改写);
//   - 文件缺失/坏文件零并入(容忍不降级,对齐 readMCPServersFile 姿态)。
//
// 消费方:agentCoreMCPRawServersIn / claudeTierMCPArgs / MCP 清单采集。
func readAliangMCPRegistryInto(home string, dst map[string]interface{}) int {
	if home == "" || dst == nil {
		return 0
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil {
		logger.Warn(fmt.Sprintf("aliang-mcp: 注册表不可读,跳过该源: %v", err))
		return 0
	}
	if !existed {
		return 0
	}
	if v, has := root["version"]; has {
		if n, ok := v.(json.Number); ok && n.String() != fmt.Sprint(aliangMCPRegistryVersion) {
			logger.Warn(fmt.Sprintf("aliang-mcp: 注册表 version=%s 高于本程序,按前向兼容读取", n.String()))
		}
	}
	servers, _ := root["servers"].(map[string]interface{})
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	count := 0
	for _, name := range names {
		if strings.HasPrefix(name, aliangMCPReservedPrefix) {
			logger.Warn(fmt.Sprintf("aliang-mcp: 条目 %s 触碰平台保留前缀 %s,读侧跳过(防遮蔽托管条目)", name, aliangMCPReservedPrefix))
			continue
		}
		raw, ok := servers[name].(map[string]interface{})
		if !ok {
			continue
		}
		enabled := true
		if ev, has := raw["enabled"]; has {
			b, ok := ev.(bool)
			if !ok {
				logger.Warn(fmt.Sprintf("aliang-mcp: 条目 %s 的 enabled 非布尔,fail-closed 跳过", name))
				continue
			}
			enabled = b
		}
		if !enabled {
			continue
		}
		typ, usable := aliangMCPEntryUsable(name, raw)
		if !usable {
			logger.Warn(fmt.Sprintf("aliang-mcp: 条目 %s 形态不合法(type=%s 缺 command/url),跳过", name, typ))
			continue
		}
		entry := make(map[string]interface{}, len(raw))
		for k, v := range raw {
			entry[k] = v
		}
		delete(entry, "enabled")
		dst[name] = entry
		count++
	}
	return count
}

// validateAliangMCPEntry 写通道校验(spec §5.2)。读通道容忍、写通道 fail-closed。
// 长度上限由正则 {1,aliangMCPServerNameMax} 拦截,无单独长度分支。
func validateAliangMCPEntry(name string, e aliangMCPServerEntry) error {
	if !aliangMCPServerNameRe.MatchString(name) {
		return fmt.Errorf("server 名称须为 ^[a-zA-Z0-9_-]{1,%d}$, got %q", aliangMCPServerNameMax, name)
	}
	if strings.HasPrefix(name, aliangMCPReservedPrefix) {
		return fmt.Errorf("server 名称不允许保留前缀 %q", aliangMCPReservedPrefix)
	}
	switch strings.ToLower(strings.TrimSpace(e.Type)) {
	case "", "stdio":
		if strings.TrimSpace(e.Command) == "" {
			return fmt.Errorf("stdio 条目必须提供 command")
		}
	case "http", "sse":
		if strings.TrimSpace(e.URL) == "" {
			return fmt.Errorf("%s 条目必须提供 url", strings.ToLower(strings.TrimSpace(e.Type)))
		}
	default:
		return fmt.Errorf("type 须为 stdio|http|sse, got %q", e.Type)
	}
	return nil
}

// aliangMCPEntryToMap 结构体→注册表 map 形态(仅非零字段;Enabled 仅显式非 nil
// 时落盘)。type 与校验同口径归一(TrimSpace+ToLower)后落盘,防把 "HTTP" 等
// 非规范字节冻进注册表。数字字段不存在于本结构,无 UseNumber 需求。
func aliangMCPEntryToMap(e aliangMCPServerEntry) map[string]interface{} {
	out := map[string]interface{}{}
	if typ := strings.ToLower(strings.TrimSpace(e.Type)); typ != "" {
		out["type"] = typ
	}
	if e.Command != "" {
		out["command"] = e.Command
	}
	if len(e.Args) > 0 {
		out["args"] = e.Args
	}
	if len(e.Env) > 0 {
		out["env"] = e.Env
	}
	if e.URL != "" {
		out["url"] = e.URL
	}
	if len(e.Headers) > 0 {
		out["headers"] = e.Headers
	}
	if e.Enabled != nil {
		out["enabled"] = *e.Enabled
	}
	return out
}

// saveAliangMCPRegistryRoot 全量往返落盘:SetEscapeHTML(false)+缩进 2 +
// tmp+rename 原子写 + 内容未变幂等跳写。目录不存在自动创建。
func saveAliangMCPRegistryRoot(home string, root map[string]interface{}) error {
	path := aliangMCPRegistryPath(home)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, buf.Bytes()) {
		return nil // 幂等跳写
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return writePeerMCPFileAtomic(path, buf.Bytes())
}

// upsertAliangMCPServerForTest 校验→全量往返→原子写(home 注入,测试与
// service 层共用本体)。version 键缺失时补 1,已有值(含更高版本)原样保留。
func upsertAliangMCPServerForTest(home, name string, entry aliangMCPServerEntry) error {
	if err := validateAliangMCPEntry(name, entry); err != nil {
		return err
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil {
		return err // 坏文件不静默覆盖,让用户先修
	}
	if !existed {
		root = map[string]interface{}{"version": json.Number(fmt.Sprint(aliangMCPRegistryVersion)), "servers": map[string]interface{}{}}
	}
	if _, has := root["version"]; !has {
		root["version"] = json.Number(fmt.Sprint(aliangMCPRegistryVersion))
	}
	servers, _ := root["servers"].(map[string]interface{})
	if servers == nil {
		servers = map[string]interface{}{}
		root["servers"] = servers
	}
	if _, existed := servers[name]; !existed {
		// 只数现存键,与读通道的 enabled/畸形口径无关:写通道上限按条目数防呆
		names := 0
		for range servers {
			names++
		}
		if names >= aliangMCPServerMaxCount {
			return fmt.Errorf("注册表条目数已达上限 %d", aliangMCPServerMaxCount)
		}
	}
	servers[name] = aliangMCPEntryToMap(entry)
	return saveAliangMCPRegistryRoot(home, root)
}

// deleteAliangMCPServerForTest 删条目;返回是否存在。
func deleteAliangMCPServerForTest(home, name string) (bool, error) {
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil || !existed {
		return false, err
	}
	servers, _ := root["servers"].(map[string]interface{})
	if _, ok := servers[name]; !ok {
		return false, nil
	}
	delete(servers, name)
	return true, saveAliangMCPRegistryRoot(home, root)
}

// aliangMCPEnabledEntries 读通道强类型视图:启用且形态合法的条目
// (codex 托管块同步的数据源)。畸形 Warn 跳过。
func aliangMCPEnabledEntries(home string) map[string]aliangMCPServerEntry {
	out := map[string]aliangMCPServerEntry{}
	raw := map[string]interface{}{}
	readAliangMCPRegistryInto(home, raw)
	for name, v := range raw {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		b, err := json.Marshal(m)
		if err != nil {
			logger.Warn(fmt.Sprintf("aliang-mcp: 条目 %s 序列化失败,跳过: %v", name, err))
			continue
		}
		var e aliangMCPServerEntry
		if err := json.Unmarshal(b, &e); err != nil {
			logger.Warn(fmt.Sprintf("aliang-mcp: 条目 %s 反序列化失败,跳过: %v", name, err))
			continue
		}
		out[name] = e
	}
	return out
}
