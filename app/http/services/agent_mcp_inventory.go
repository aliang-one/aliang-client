package services

// agent_mcp_inventory.go 采集设备级 MCP 服务器清单(claude/codex),脱敏后
// 挂到 AgentTool.MCPServers 随 tools[] 上报(spec:
// docs/superpowers/specs/2026-10-13-mcp-inventory-report-design.md)。
// env 值在任何路径都不进入返回结果;解析失败 fail-soft 返回 nil。

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

const (
	mcpNameMaxLen           = 64
	mcpCommandMaxLen        = 512
	mcpURLMaxLen            = 512
	mcpServerMaxCount       = 24
	mcpTokenRedactMinLen    = 32 // argv token 超过此长度且无空白 → 打码
	mcpURLQueryRedactMinLen = 24 // URL query 值超过此长度 → 打码
)

// redactMCPToken 把疑似密钥形态的 argv token 打码——宁可错杀:MCP args 里
// 带 API key 是常态,只有「短」或「含空白(自然语句)」才放行。
func redactMCPToken(token string) string {
	if len(token) > mcpTokenRedactMinLen && !strings.ContainsAny(token, " \t") {
		return fmt.Sprintf("<redacted:%d>", len(token))
	}
	return token
}

// utf16Truncate 按 UTF-16 码元截断并保证不劈开代理对。服务端 zod max() 按
// JS string length(码元)计长,星面字符(如 emoji)1 rune=2 码元——客户端
// 若按 rune 计,贴限载荷会被服务端整条拒绝(HTTP 同步 400)。
func utf16Truncate(s string, max int) string {
	runes := []rune(s)
	units := 0
	for i, r := range runes {
		u := 1
		if r > 0xFFFF {
			u = 2
		}
		if units+u > max {
			return string(runes[:i])
		}
		units += u
	}
	return s
}

// redactMCPCommand 把 argv join 成命令行,逐 token 脱敏,整体按 UTF-16
// 码元截 512(对齐服务端 zod 长度语义;先过 truncateAgentText 保 TrimSpace)。
func redactMCPCommand(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, redactMCPToken(arg))
	}
	return utf16Truncate(truncateAgentText(strings.Join(parts, " "), mcpCommandMaxLen), mcpCommandMaxLen)
}

// redactMCPURL 打码 query 中超过阈值长度的值(键保留);userinfo 一律打码、
// fragment 一律丢弃——非 query 形态的凭据同样不得外泄;整体按 UTF-16 码元
// 截 512(对齐服务端 zod 长度语义)。
func redactMCPURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.RawQuery == "" && u.User == nil && u.Fragment == "") {
		return utf16Truncate(truncateAgentText(raw, mcpURLMaxLen), mcpURLMaxLen)
	}
	if u.RawQuery != "" {
		q := u.Query()
		for key, vals := range q {
			for i, v := range vals {
				if len(v) > mcpURLQueryRedactMinLen {
					vals[i] = "<redacted>"
				}
			}
			q[key] = vals
		}
		u.RawQuery = q.Encode()
	}
	if u.User != nil {
		u.User = url.User("<redacted>")
	}
	u.Fragment = ""
	u.RawFragment = ""
	return utf16Truncate(truncateAgentText(u.String(), mcpURLMaxLen), mcpURLMaxLen)
}

// truncateMCPName 把 MCP 服务器名称截到 64(先复用 truncateAgentText 做
// rune 安全截断+TrimSpace,再按 UTF-16 码元收紧,对齐服务端 zod 语义)。
func truncateMCPName(name string) string {
	return utf16Truncate(truncateAgentText(name, mcpNameMaxLen), mcpNameMaxLen)
}

// agentMCPServersFromClaudeRaw 把 claude 形态的原始 mcpServers 条目转成
// 脱敏摘要。名字按字典序输出(确定性);非法/缺字段的条目逐条丢弃;
// 未知 transport 丢弃(fail-closed);数量截 mcpServerMaxCount。
func agentMCPServersFromClaudeRaw(named map[string]interface{}, source string) []models.AgentMCPServer {
	names := make([]string, 0, len(named))
	for name := range named {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]models.AgentMCPServer, 0, len(names))
	for _, name := range names {
		if len(out) >= mcpServerMaxCount {
			break
		}
		v, ok := named[name].(map[string]interface{})
		if !ok {
			continue
		}
		nm := truncateMCPName(name)
		if nm == "" {
			continue // 全空白长名经 TrimSpace 截断后为空:跳过(服务端 schema min(1) 会拒掉整个条目)
		}
		typ, _ := v["type"].(string)
		if typ == "" {
			typ = "stdio"
		}
		switch typ {
		case "stdio":
			command, _ := v["command"].(string)
			if command == "" {
				continue
			}
			argv := []string{command}
			if rawArgs, ok := v["args"].([]interface{}); ok {
				for _, a := range rawArgs {
					if s, ok := a.(string); ok {
						argv = append(argv, s)
					}
				}
			}
			out = append(out, models.AgentMCPServer{
				Name: nm, Transport: "stdio",
				Command: redactMCPCommand(argv), Source: source,
			})
		case "http", "sse":
			u, _ := v["url"].(string)
			if u == "" {
				continue
			}
			out = append(out, models.AgentMCPServer{
				Name: nm, Transport: typ,
				URL: redactMCPURL(u), Source: source,
			})
		default:
			continue // 未知 transport:宁可少报(fail-closed)
		}
	}
	return out
}

// agentMCPFileStamp 是缓存失效判定的文件指纹。零值 comparable,直接 == 比较。
// (Task 6 将在其上扩展清单缓存;此处先落地最小形态。)
type agentMCPFileStamp struct {
	path    string
	exists  bool
	modTime time.Time
	size    int64
}

// stampAgentMCPFile 采集单个文件的指纹;缺失文件的 stamp 仅含 path(exists=false)。
func stampAgentMCPFile(path string) agentMCPFileStamp {
	if st, err := os.Stat(path); err == nil {
		return agentMCPFileStamp{path: path, exists: true, modTime: st.ModTime(), size: st.Size()}
	}
	return agentMCPFileStamp{path: path}
}

// claudeMCPServerInventoryIn 采集 <home> 下的 Claude 设备级 MCP 清单:
// <home>/.claude.json 顶层 mcpServers(source=user)+ 已启用插件 MCP
// (source=plugin)。项目级 .mcp.json 不采集(随 cwd 变化,非设备能力)。
// 所有读取以传入 home 为根,不回落 agentHome()。
//
// 两态返回契约:stamps 恒非 nil(= 本次读过/统计过的全部文件指纹,
// 任一文件从缺失变存在或内容变更即缓存失效);清单为空时 out 为 nil
// 但 stamps 照交——否则新机器(检测到 claude 却从未跑过)会把空清单
// 永久缓存,用户后来创建 ~/.claude.json 也刷不出来。nil stamps 仅可能
// 出现在上游拦截路径(home 为空时本函数不应被调用)。
func claudeMCPServerInventoryIn(home string) ([]models.AgentMCPServer, []agentMCPFileStamp) {
	claudeJSON := filepath.Join(home, ".claude.json")
	settingsJSON := filepath.Join(home, ".claude", "settings.json")
	registryJSON := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	claudeStamp := stampAgentMCPFile(claudeJSON)
	settingsStamp := stampAgentMCPFile(settingsJSON)
	registryStamp := stampAgentMCPFile(registryJSON)
	stamps := []agentMCPFileStamp{claudeStamp, settingsStamp, registryStamp}
	// 无任何 claude 配置:清单为空,但 stamps 必须照交——缓存据此感知
	// 「配置后来出现」(任一文件从缺失变存在即失效)。
	if !claudeStamp.exists && !registryStamp.exists {
		return nil, stamps
	}
	userRaw := map[string]interface{}{}
	_ = readMCPServersFile(claudeJSON, userRaw)
	pluginRaw := map[string]interface{}{}
	for _, inst := range enabledUserScopePluginInstallsIn(home) {
		declPath := filepath.Join(inst.installPath, ".mcp.json")
		stamps = append(stamps, stampAgentMCPFile(declPath))
		readPluginMCPDeclaration(inst.installPath, inst.pluginName, pluginRaw)
	}
	out := agentMCPServersFromClaudeRaw(userRaw, "user")
	out = append(out, agentMCPServersFromClaudeRaw(pluginRaw, "plugin")...)
	if len(out) > mcpServerMaxCount {
		out = out[:mcpServerMaxCount]
	}
	if len(out) == 0 {
		return nil, stamps
	}
	return out, stamps
}

// codexMCPConfig 对应 ~/.codex/config.toml 的 [mcp_servers.<name>] 表。
// Env 读取即弃:字段只是把 env 消化掉,值永不进入输出。
type codexMCPConfig struct {
	Command string                 `toml:"command"`
	Args    []string               `toml:"args"`
	URL     string                 `toml:"url"`
	Env     map[string]interface{} `toml:"env"`
}

type codexConfigFile struct {
	MCPServers map[string]codexMCPConfig `toml:"mcp_servers"`
}

// codexMCPServerInventoryIn 采集 ~/.codex/config.toml 的 MCP 清单。
// command(+args)→ stdio;url → 统一归类 http(codex 配置不区分 sse/http);
// 两者皆无的条目丢弃;url 与 command 并存时 url 优先(报 http);
// 单条目类型不匹配(如 args 混入数字)会使整文件不可解码→归零——
// codex 自身(serde 强类型)同样无法加载此类配置,语义对齐;
// 畸形 TOML fail-soft 返回 nil。
// 两态契约与 claude 侧一致:stamps 恒非 nil(缺文件/空清单也照交指纹),
// 否则缓存会把空清单永久缓存,「配置后来出现」永远感知不到。
func codexMCPServerInventoryIn(home string) ([]models.AgentMCPServer, []agentMCPFileStamp) {
	configPath := filepath.Join(home, ".codex", "config.toml")
	stamp := stampAgentMCPFile(configPath)
	if !stamp.exists {
		// 缺失也要交指纹,否则缓存永驻空结果(配置后来出现感知不到)
		return nil, []agentMCPFileStamp{stamp}
	}
	var cfg codexConfigFile
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		logger.Warn(fmt.Sprintf("mcp-inventory: unparseable codex config %s: %v", configPath, err))
		return nil, []agentMCPFileStamp{stamp}
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]models.AgentMCPServer, 0, len(names))
	for _, name := range names {
		if len(out) >= mcpServerMaxCount {
			break
		}
		srv := cfg.MCPServers[name]
		// 与 claude 解析器对齐:截断后空名(全空白键)跳过,
		// 否则服务端 schema min(1) 会拒掉整个工具条目。
		nm := truncateMCPName(name)
		if nm == "" {
			continue
		}
		switch {
		case srv.URL != "":
			out = append(out, models.AgentMCPServer{
				Name: nm, Transport: "http",
				URL: redactMCPURL(srv.URL), Source: "config",
			})
		case srv.Command != "":
			out = append(out, models.AgentMCPServer{
				Name: nm, Transport: "stdio",
				Command: redactMCPCommand(append([]string{srv.Command}, srv.Args...)),
				Source:  "config",
			})
		default:
			continue
		}
	}
	if len(out) == 0 {
		return nil, []agentMCPFileStamp{stamp}
	}
	return out, []agentMCPFileStamp{stamp}
}

// agentMCPInventoryCacheEntry 是单个「home+toolID」槽的缓存条目:
// 上次解析的文件指纹 + 解析结果。
type agentMCPInventoryCacheEntry struct {
	stamps []agentMCPFileStamp
	result []models.AgentMCPServer
}

var (
	agentMCPInventoryMu     sync.Mutex
	agentMCPInventoryByHome = map[string]map[string]agentMCPInventoryCacheEntry{} // home -> toolID -> entry
)

// agentMCPStampsUnchanged 对 stamp 清单逐项重 stat;任一指纹变化(出现/
// 消失/mtime/size)即失效。复合源(claude 多文件)任一变更都会触发重解析。
func agentMCPStampsUnchanged(stamps []agentMCPFileStamp) bool {
	for _, s := range stamps {
		if stampAgentMCPFile(s.path) != s {
			return false
		}
	}
	return true
}

// cachedAgentMCPServers 以「home+toolID」为槽缓存解析结果;命中时返回副本
// (防调用方改写缓存)。parse 只在缓存失效时被调。
// parse 契约:返回的 stamps 必须非 nil(空配置也要交指纹)——nil stamps 会被
// agentMCPStampsUnchanged 视为恒未变,导致空结果永驻缓存。
func cachedAgentMCPServers(home, toolID string, parse func(home string) ([]models.AgentMCPServer, []agentMCPFileStamp)) []models.AgentMCPServer {
	agentMCPInventoryMu.Lock()
	defer agentMCPInventoryMu.Unlock()
	byTool := agentMCPInventoryByHome[home]
	if byTool == nil {
		byTool = map[string]agentMCPInventoryCacheEntry{}
		agentMCPInventoryByHome[home] = byTool
	}
	entry, ok := byTool[toolID]
	if ok && agentMCPStampsUnchanged(entry.stamps) {
		return append([]models.AgentMCPServer(nil), entry.result...)
	}
	result, stamps := parse(home)
	byTool[toolID] = agentMCPInventoryCacheEntry{stamps: stamps, result: result}
	return append([]models.AgentMCPServer(nil), result...)
}

// agentMCPServersFor 是 detectAgentToolsWith 的 MCP 采集入口:仅 claude/
// claudecode/codex 采集(其余 CLI 返回 nil);agentHome 不可得返回 nil。
func agentMCPServersFor(toolID string) []models.AgentMCPServer {
	home := mcpInventoryHome()
	if home == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(toolID)) {
	case "claude", "claudecode":
		return cachedAgentMCPServers(home, "claude", claudeMCPServerInventoryIn)
	case "codex":
		return cachedAgentMCPServers(home, "codex", codexMCPServerInventoryIn)
	default:
		return nil
	}
}

// mcpInventoryFor 是 detectAgentToolsWith 的 MCP 采集注入点(测试替换)。
// 测试 swap 期间不得有后台 detectAgentTools 调用方;包内测试禁 t.Parallel
// (见 agent_terminal_detach_test.go 说明)。
var mcpInventoryFor = agentMCPServersFor

// mcpInventoryHome 是 agentMCPServersFor 取 home 的注入点(测试替换)。
// 测试 swap 期间不得有后台 detectAgentTools 调用方;包内测试禁 t.Parallel
// (见 agent_terminal_detach_test.go 说明)。
var mcpInventoryHome = agentHome
