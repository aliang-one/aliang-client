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
	"sync"

	"aliang.one/nursorgate/common/logger"
	"github.com/BurntSushi/toml"
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
	// IncludeTools/ExcludeTools 消费侧(agentcore)过滤名单;claude 载荷剥除。
	IncludeTools []string `json:"include_tools,omitempty"`
	ExcludeTools []string `json:"exclude_tools,omitempty"`
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
//   - 并入时剥掉注册表专属 enabled 键(claude 形态无此字段,语义已由跳过表达)
//     并把非空 type 归一为小写(与写通道同口径;防手改 "HTTP" 在清单层被丢、
//     在 agentcore/flags 层连上的层间差异);
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
		// 读侧 type 归一:存在且非空字符串时写回小写(trim 后)结果。写通道
		// 落盘已同口径归一,此处兜住手改文件——否则 "HTTP" 会在清单层
		// (精确 switch)被丢、在 agentcore 层(EqualFold)连上,两层各说各话。
		if tv, ok := entry["type"].(string); ok && strings.TrimSpace(tv) != "" {
			entry["type"] = strings.ToLower(strings.TrimSpace(tv))
		}
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
	if len(normalizeAgentCoreMCPToolList(e.IncludeTools)) > agentCoreMCPToolListCap {
		return fmt.Errorf("include_tools 归一后超上限 %d", agentCoreMCPToolListCap)
	}
	if len(normalizeAgentCoreMCPToolList(e.ExcludeTools)) > agentCoreMCPToolListCap {
		return fmt.Errorf("exclude_tools 归一后超上限 %d", agentCoreMCPToolListCap)
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
	if norm := normalizeAgentCoreMCPToolList(e.IncludeTools); len(norm) > 0 {
		out["include_tools"] = norm
	}
	if norm := normalizeAgentCoreMCPToolList(e.ExcludeTools); len(norm) > 0 {
		out["exclude_tools"] = norm
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

// tomlQuote TOML basic string 转义(控制字符含 DEL(0x7F)转 \uXXXX,其余原样;
// 裸 0x7F 在 TOML basic string 里是解析错误,评审实证)。
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7F {
				b.WriteString(fmt.Sprintf(`\u%04X`, r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// aliangMCPCodexBlockLines 生成一个托管块的行序列。codex 无 headers 通道,
// registry headers 不进 codex(spec §6.4);http/sse 只带 url;env 用 inline
// table 保持块内无子表头(块扫描按「[ 开头的行」定界,子表头会撕开块)。
func aliangMCPCodexBlockLines(key string, e aliangMCPServerEntry) []string {
	lines := []string{"[mcp_servers." + key + "]"}
	if strings.TrimSpace(e.URL) != "" {
		return append(lines, "url = "+tomlQuote(e.URL))
	}
	lines = append(lines, "command = "+tomlQuote(e.Command))
	if len(e.Args) > 0 {
		parts := make([]string, 0, len(e.Args))
		for _, a := range e.Args {
			parts = append(parts, tomlQuote(a))
		}
		lines = append(lines, "args = ["+strings.Join(parts, ", ")+"]")
	}
	if len(e.Env) > 0 {
		keys := make([]string, 0, len(e.Env))
		for k := range e.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, tomlQuote(k)+" = "+tomlQuote(e.Env[k]))
		}
		lines = append(lines, "env = {"+strings.Join(pairs, ", ")+"}")
	}
	return lines
}

// aliangMCPCodexHeaderKey 判定一行是否规范形态的 [mcp_servers.aliang-mcp-*]
// 托管表头;是则返回键名(如 aliang-mcp-blender),否则返回 ""。规范形态=
// 去空白后前缀匹配+右括号收尾+无引号+键内无点(带点是子表头,属用户内容,
// 摘块与门禁两处共用同一判据——两份拷贝漂移会互相打脸,故收敛于此)。
func aliangMCPCodexHeaderKey(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "[mcp_servers."+aliangMCPCodexPrefix) && strings.HasSuffix(trimmed, "]") && !strings.Contains(trimmed, "\"") {
		key := strings.TrimSuffix(strings.TrimPrefix(trimmed, "[mcp_servers."), "]")
		if !strings.Contains(key, ".") {
			return key
		}
	}
	return ""
}

// scanAliangMCPCanonicalManagedHeaders 文本扫描 config.toml 中规范形态的
// [mcp_servers.aliang-mcp-*] 表头(判据见 aliangMCPCodexHeaderKey)。
func scanAliangMCPCanonicalManagedHeaders(raw string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		if key := aliangMCPCodexHeaderKey(line); key != "" {
			out[key] = true
		}
	}
	return out
}

// applyAliangMCPCodexSyncForTest 把 config.toml 的 aliang-mcp-* 托管块集合
// 对齐到注册表启用条目集合(多块 diff:期望集内的块重写,期望集外的托管块
// 删除,其余内容逐字保留)。安全:
//   - 用户文件不可解析 → 拒写(codex 自身同样读不了,fail-closed);
//   - TOML 解析出托管键但文本扫描找不到规范表头(引号/空格/尾注释形态)
//     → 拒写,提示手工规范(防重复表定义打碎文件,peer 注册同款纪律);
//   - 写前自检:拼出的内容必须自己解析得过(如规范块+孤儿子表 env 与新块
//     inline env 构成重复键定义)→ 拒写,原文件字节不动;
//   - 字节无变化 → 幂等跳写;文件缺失且期望集为空 → 不建文件。
func applyAliangMCPCodexSyncForTest(home string) error {
	path := filepath.Join(home, ".codex", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	desired := map[string]aliangMCPServerEntry{}
	for name, e := range aliangMCPEnabledEntries(home) {
		// 读通道只拦保留前缀,名称合法性是写通道纪律;手改注册表可塞进
		// a.b 这类名——它会产出 [mcp_servers.aliang-mcp-a.b],codex 理解成
		// 子表头,直接拒出。
		if !aliangMCPServerNameRe.MatchString(name) {
			logger.Warn(fmt.Sprintf("aliang-mcp: 条目 %s 名称不合规范,codex 托管块跳过", name))
			continue
		}
		desired[aliangMCPCodexPrefix+name] = e
	}
	hasFile := err == nil
	if !hasFile && len(desired) == 0 {
		return nil
	}
	if hasFile {
		var cfg codexConfigFile
		if _, perr := toml.Decode(string(raw), &cfg); perr != nil {
			return fmt.Errorf("parse %s: %w;拒绝在无法解析的用户文件上拼块,请先修复 config.toml", path, perr)
		}
		canonical := scanAliangMCPCanonicalManagedHeaders(string(raw))
		for key := range cfg.MCPServers {
			if !strings.HasPrefix(key, aliangMCPCodexPrefix) {
				continue
			}
			if !canonical[key] {
				return fmt.Errorf("codex config 的托管条目 %s 表头不是规范形态 [mcp_servers.%s];拒绝拼接以免打碎文件,请先手工规范表头", key, key)
			}
		}
	}
	// 摘除全部规范托管块 span,其余行原样保留(判据与门禁同一函数)。
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	var kept []string
	i := 0
	for i < len(lines) {
		if aliangMCPCodexHeaderKey(lines[i]) == "" {
			kept = append(kept, lines[i])
			i++
			continue
		}
		i++ // 跳过表头行
		for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			i++ // 跳到下一表头/文件尾
		}
	}
	// 追加期望块(键序确定)。
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := kept
	for _, k := range keys {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, aliangMCPCodexBlockLines(k, desired[k])...)
	}
	if len(out) == 0 && !hasFile {
		return nil
	}
	content := strings.Join(out, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n" // 尾换行与 peer 的块构造口径一致
	}
	if hasFile && content == string(raw) {
		return nil // 幂等跳写
	}
	// 写前自检:自己产出的内容必须自己解析得过(孤儿子表等形态会拼出重复
	// 键定义);失败=拒写,原文件字节不动,与坏文件 fail-closed 同姿态。
	if _, perr := toml.Decode(content, &codexConfigFile{}); perr != nil {
		return fmt.Errorf("aliang-mcp: codex 托管块同步产出不可解析内容,拒绝写盘: %w", perr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return writePeerMCPFileAtomic(path, []byte(content))
}

// EnsureAliangMCPCodexMatchesRegistryForTest 是 home 可注入的自愈本体:
// 纠正「注册表已写但 codex 拼块被拒后用户已手工修复」与「用户手删托管块」
// 的漂移。只 Warn 不返回错误——自愈永不阻断启动。
func EnsureAliangMCPCodexMatchesRegistryForTest(home string) {
	aliangMCPRegistryMu.Lock()
	defer aliangMCPRegistryMu.Unlock()
	if home == "" {
		logger.Warn("aliang-mcp: startup codex sync skipped, agent home unavailable")
		return
	}
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		logger.Warn(fmt.Sprintf("aliang-mcp: startup codex sync failed (注册表不受影响,修复 config.toml 后下次启动补齐): %v", err))
	}
}

// EnsureAliangMCPCodexMatchesRegistry 生产入口(agentHome 注入)。
func EnsureAliangMCPCodexMatchesRegistry() {
	EnsureAliangMCPCodexMatchesRegistryForTest(agentHome())
}

// ---------------------------------------------------------------------------
// HTTP service 层(GET/PUT/DELETE)。文件层本体见上;此处只做 home 注入、
// 两段式编排与 DTO 形态转换。
// ---------------------------------------------------------------------------

// mcpRegistryHome 是本文件 service 方法取 home 的注入点(测试 swap,禁并行;
// swap 期间不得有后台调用方——写 API 走 HTTP 请求同步路径,天然满足)。
var mcpRegistryHome = agentHome

// aliangMCPRegistryMu 串行化注册表写路径:HTTP 已暴露 PUT/DELETE,双请求重叠
// 时「读-改-写」会丢更新(原子写只防撕裂不防丢失;注册表是真相源且无自愈)。
// 覆盖面=本文件三个写入口(Upsert/Delete/Ensure 自愈)整体两段式单元,对照
// agentMCPInventoryMu 先例。与 peer 家族对 ~/.codex/config.toml 的写跨族
// 不互斥=documented-accept(peer 侧无锁,两侧各有自愈兜底)。
var aliangMCPRegistryMu sync.Mutex

// AliangMCPServerRequest 是 PUT 通道的请求形态(handler DTO)。
type AliangMCPServerRequest struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Enabled *bool             `json:"enabled"`
	// IncludeTools/ExcludeTools 消费侧(agentcore)过滤名单;claude 载荷剥除。
	// 与 aliangMCPServerEntry 同名同型同序(结构体转换护栏见下)。
	IncludeTools []string `json:"include_tools"`
	ExcludeTools []string `json:"exclude_tools"`
}

// entry 字段必须与 aliangMCPServerEntry 逐一同名同型同序,结构体转换才是合法
// 的——将来任一侧字段漂移会在编译期炸(这正是想要的护栏),别用删转换改写
// 逐字段拷贝的方式「修」,那会静默吞掉新字段的透传。
func (r AliangMCPServerRequest) entry() aliangMCPServerEntry {
	return aliangMCPServerEntry(r)
}

// AliangMCPSyncResult 是注册表写 API 的统一响应骨架。两段式:第一段(注册表
// 落盘)失败在 RegistryErr;第二段(codex 托管块)失败在 CodexSync.Error 且
// 不回滚第一段(spec §7.1——注册表已是真值,自愈兜底)。
type AliangMCPSyncResult struct {
	Existed     bool               `json:"existed,omitempty"`
	RegistryErr string             `json:"registry_error,omitempty"`
	CodexSync   AliangMCPCodexSync `json:"codex_sync"`
}

type AliangMCPCodexSync struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// AliangMCPRegistry GET 视图:注册表原文(本地管理须见原文;文件在用户自己
// 机器上)。缺文件返回空表视图。
func (s *AgentService) AliangMCPRegistry() (map[string]interface{}, error) {
	home := mcpRegistryHome()
	if home == "" {
		return nil, fmt.Errorf("agent home unavailable")
	}
	root, existed, err := loadAliangMCPRegistryRoot(home)
	if err != nil {
		return nil, err
	}
	if !existed {
		return map[string]interface{}{"version": json.Number(fmt.Sprint(aliangMCPRegistryVersion)), "servers": map[string]interface{}{}}, nil
	}
	return root, nil
}

// UpsertAliangMCPServer PUT 语义:新增或整体替换。
func (s *AgentService) UpsertAliangMCPServer(name string, req AliangMCPServerRequest) AliangMCPSyncResult {
	aliangMCPRegistryMu.Lock()
	defer aliangMCPRegistryMu.Unlock()
	res := AliangMCPSyncResult{}
	home := mcpRegistryHome()
	if home == "" {
		res.RegistryErr = "agent home unavailable"
		return res
	}
	if err := upsertAliangMCPServerForTest(home, name, req.entry()); err != nil {
		res.RegistryErr = err.Error()
		return res
	}
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		res.CodexSync = AliangMCPCodexSync{OK: false, Error: err.Error()}
		return res
	}
	res.CodexSync = AliangMCPCodexSync{OK: true}
	return res
}

// DeleteAliangMCPServer DELETE 语义。
func (s *AgentService) DeleteAliangMCPServer(name string) AliangMCPSyncResult {
	aliangMCPRegistryMu.Lock()
	defer aliangMCPRegistryMu.Unlock()
	res := AliangMCPSyncResult{}
	home := mcpRegistryHome()
	if home == "" {
		res.RegistryErr = "agent home unavailable"
		return res
	}
	existed, err := deleteAliangMCPServerForTest(home, name)
	if err != nil {
		res.RegistryErr = err.Error()
		return res
	}
	res.Existed = existed
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		res.CodexSync = AliangMCPCodexSync{OK: false, Error: err.Error()}
		return res
	}
	res.CodexSync = AliangMCPCodexSync{OK: true}
	return res
}
