package services

// OpenViking 跨设备记忆面 ensure 自注册(Phase2 spec §7.2/§7.4)。
//
// boot 自愈纪律(与 peer 家族同款):只 Warn 不阻断启动;幂等(已满足即跳过
// 写盘);BYO 先行——claude.json 已有同名条目且内容不同(非本平台写入形态)
// 时不覆盖不劫持,注册表与 codex 块仍按真值对齐。
//
// 三处对齐:统一 MCP 注册表条目(http + Bearer + 管理面 excludeTools)、
// claude.json 顶层 mcpServers 条目、codex 托管块(经注册表同步器,headers
// inline 透传,§7.6)。managed 模式凭据来自云端铸 key 端点(spec §7.1);
// 端点未就绪时按增强层失败语义静默跳过本 boot,下个 boot 重试。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

const (
	// openVikingManagedServerName 注册表与 claude.json 的条目名。非 aliang-
	// 前缀:同套注册表同步器落 codex 托管块时自动冠 aliang-mcp- 前缀。
	openVikingManagedServerName = "openviking"
	// openVikingCredentialTimeout managed 铸 key 端点单次超时;boot 自愈不许长挂。
	openVikingCredentialTimeout = 10 * time.Second
)

// openVikingExcludeTools 管理面裁剪清单(spec §7.5):USER 角色 key 服务端本
// 就调不动 manage 类动作,注册表再裁一层=双保险防误触。
var openVikingExcludeTools = []string{
	"list_users", "list_groups", "get_acl", "set_acl", "privacy_tree", "privacy_upsert",
}

// EnsureOpenVikingMatchesConfig 生产入口(agentHome 注入;boot 挂点
// app/agentruntime/server.go,与 peer 自愈相邻)。
func EnsureOpenVikingMatchesConfig() {
	if err := EnsureOpenVikingMatchesConfigForTest(agentHome()); err != nil {
		logger.Warn(fmt.Sprintf("openviking: startup heal failed (不阻断 boot,修复后下次启动补齐): %v", err))
	}
}

// EnsureOpenVikingMatchesConfigForTest home 可注入的自愈本体。
func EnsureOpenVikingMatchesConfigForTest(home string) error {
	if home == "" {
		logger.Warn("openviking: startup heal skipped, agent home unavailable")
		return nil
	}
	// 注册表写 API 同款互斥:ensure 是注册表的第三个写入口(HTTP PUT/DELETE/
	// 本自愈),读-改-写重叠会丢更新(原子写只防撕裂不防丢失)。
	aliangMCPRegistryMu.Lock()
	defer aliangMCPRegistryMu.Unlock()
	return ensureOpenVikingLocked(home)
}

// ensureOpenVikingLocked 自愈本体。调用方必须已持 aliangMCPRegistryMu。
func ensureOpenVikingLocked(home string) error {
	cfg := config.GetGlobalConfig()
	if !cfg.EffectiveOpenVikingEnabled() {
		return cleanupOpenViking(home)
	}
	endpoint := cfg.EffectiveOpenVikingEndpoint()
	var apiKey string
	switch cfg.EffectiveOpenVikingMode() {
	case config.OpenVikingModeBYO:
		apiKey = strings.TrimSpace(cfg.Customer.OpenViking.APIKey)
		if apiKey == "" {
			return fmt.Errorf("byo 模式 api_key 为空:跳过本 boot(补齐配置后下次启动重试)")
		}
	default:
		key, mintedEndpoint, err := fetchOpenVikingCredential(cfg)
		if err != nil {
			// spec §7.1 失败语义:云端不可达/未部署 → 静默跳过本 boot,下 boot 重试。
			return fmt.Errorf("managed 铸 key 未就绪:跳过本 boot: %w", err)
		}
		apiKey = key
		if mintedEndpoint != "" {
			endpoint = mintedEndpoint // 响应 endpoint 优先(凭据签发方是真值)
		}
	}
	return applyOpenVikingRegistrationLocked(home, endpoint, apiKey)
}

// fetchOpenVikingCredential managed 模式凭据获取:POST
// <APIBaseURL>/api/agent/openviking/credential,携带设备 agent JWT(与
// /api/agent/status 同契约)→ {endpoint, api_key}(spec §7.1 lazy-mint)。
// 响应 endpoint 非空时优先(凭据签发方是真值);任何失败都返回 error,由调用
// 方走「跳过本 boot」语义,绝不阻断启动。
func fetchOpenVikingCredential(cfg *config.Config) (apiKey, endpoint string, err error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL()), "/")
	if base == "" {
		return "", "", fmt.Errorf("api_server 未配置")
	}
	url := base + "/api/agent/openviking/credential"
	client := &http.Client{Timeout: openVikingCredentialTimeout}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
	if err != nil {
		return "", "", err
	}
	if tok := strings.TrimSpace(GetSharedAgentService().UserAccessToken()); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("call %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("credential endpoint status %d", resp.StatusCode)
	}
	var body struct {
		Endpoint string `json:"endpoint"`
		APIKey   string `json:"api_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("decode credential response: %w", err)
	}
	if strings.TrimSpace(body.APIKey) == "" {
		return "", "", fmt.Errorf("credential endpoint returned empty api_key")
	}
	if ep := strings.TrimSpace(body.Endpoint); ep != "" {
		endpoint = ep
	} else {
		endpoint = cfg.EffectiveOpenVikingEndpoint()
	}
	return body.APIKey, endpoint, nil
}

// openVikingRegistryEntry 注册表条目形态(spec §7.2/§7.4):http + Bearer +
// 管理面裁剪。ExcludeTools 消费侧(agentcore)过滤,claude 载荷剥除。
func openVikingRegistryEntry(endpoint, apiKey string) aliangMCPServerEntry {
	return aliangMCPServerEntry{
		Type:         "http",
		URL:          endpoint,
		Headers:      map[string]string{"Authorization": "Bearer " + apiKey},
		ExcludeTools: append([]string(nil), openVikingExcludeTools...),
	}
}

// openVikingClaudeEntry claude.json 条目形态(http 型,与注册表同源)。
func openVikingClaudeEntry(endpoint, apiKey string) map[string]interface{} {
	return map[string]interface{}{
		"type": "http",
		"url":  endpoint,
		"headers": map[string]interface{}{
			"Authorization": "Bearer " + apiKey,
		},
	}
}

// applyOpenVikingRegistrationLocked 开启态对齐:注册表 upsert → codex 托管块
// 同步 → claude.json 条目。顺序即 spec §7.1 两段式纪律:注册表先落真值,后续
// 失败不回滚,自愈下 boot 补齐。
func applyOpenVikingRegistrationLocked(home, endpoint, apiKey string) error {
	if err := upsertAliangMCPServerForTest(home, openVikingManagedServerName, openVikingRegistryEntry(endpoint, apiKey)); err != nil {
		return fmt.Errorf("registry upsert: %w", err)
	}
	// codex 托管块随注册表同步器重算(headers inline 透传,§7.6 断点解除)。
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		return fmt.Errorf("codex sync: %w", err)
	}
	return applyOpenVikingClaudeEntry(home, openVikingClaudeEntry(endpoint, apiKey))
}

// applyOpenVikingClaudeEntry claude.json 顶层 mcpServers 条目对齐。已有条目
// 内容相同=幂等;不同=用户自有 BYO 形态,不覆盖不劫持(spec §7.2),注册表
// 与 codex 块已按真值对齐,claude 侧留给用户手工收敛。
func applyOpenVikingClaudeEntry(home string, desired map[string]interface{}) error {
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
		if root == nil {
			root = map[string]interface{}{} // 文档字面 null 解出 nil map,直接写键会 panic
		}
	case os.IsNotExist(err):
		root = map[string]interface{}{"mcpServers": map[string]interface{}{}}
	default:
		return fmt.Errorf("read %s: %w", path, err)
	}
	servers, _ := root["mcpServers"].(map[string]interface{})
	if servers == nil {
		servers = map[string]interface{}{}
		root["mcpServers"] = servers
	}
	if cur, ok := servers[openVikingManagedServerName]; ok {
		if claudeEntryEqual(cur, desired) {
			return nil // 幂等
		}
		logger.Warn(fmt.Sprintf("openviking: claude.json 既有 %s 条目与本平台形态不同,按 BYO 保留不覆盖", openVikingManagedServerName))
		return nil
	}
	servers[openVikingManagedServerName] = desired
	return writeOpenVikingClaudeConfig(path, root)
}

// writeOpenVikingClaudeConfig 全量往返原子写(SetEscapeHTML(false) 防转义改写
// 用户字节;内容未变幂等跳写)。
func writeOpenVikingClaudeConfig(path string, root map[string]interface{}) error {
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
	return writePeerMCPFileAtomic(path, buf.Bytes())
}

// claudeEntryEqual 深比较:统一走 JSON 字节(UseNumber 数字字面量原样保留,
// map 键序由 encoding/json 确定性排序),序列化形态差异不构成差异。
func claudeEntryEqual(a interface{}, b map[string]interface{}) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}

// cleanupOpenViking 关闭态对齐:注册表条目删除(连带 codex 块随同步器摘除);
// claude 条目仅在本平台写入形态时移除——用户自有 BYO 条目不劫持。
func cleanupOpenViking(home string) error {
	owned, err := deleteAliangMCPServerForTest(home, openVikingManagedServerName)
	if err != nil {
		return fmt.Errorf("registry delete: %w", err)
	}
	// codex 块随注册表同步器重算(幂等;期望集为空且无文件时不建文件)。
	if err := applyAliangMCPCodexSyncForTest(home); err != nil {
		return fmt.Errorf("codex sync: %w", err)
	}
	return cleanupOpenVikingClaude(home, owned)
}

// cleanupOpenVikingClaude 移除 claude.json 条目。owned=注册表曾有本平台条目
// (claude 条目即我们写的,连带移除);非 owned 时只移除本平台形态(url 指向
// 平台默认入口),其余按 BYO 保留。
func cleanupOpenVikingClaude(home string, owned bool) error {
	path := filepath.Join(home, ".claude.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	root := map[string]interface{}{}
	if err := dec.Decode(&root); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	servers, _ := root["mcpServers"].(map[string]interface{})
	if servers == nil {
		return nil
	}
	cur, ok := servers[openVikingManagedServerName]
	if !ok {
		return nil
	}
	if !owned && !openVikingLooksOurs(cur) {
		logger.Warn(fmt.Sprintf("openviking: claude.json 既有 %s 条目非本平台形态,按 BYO 保留", openVikingManagedServerName))
		return nil
	}
	delete(servers, openVikingManagedServerName)
	return writeOpenVikingClaudeConfig(path, root)
}

// openVikingLooksOurs 条目是否本平台写入形态:http 型且指向平台默认入口。
func openVikingLooksOurs(cur interface{}) bool {
	m, ok := cur.(map[string]interface{})
	if !ok {
		return false
	}
	typ, _ := m["type"].(string)
	if typ != "http" {
		return false
	}
	url, _ := m["url"].(string)
	return url == config.OpenVikingDefaultEndpoint || url == ""
}

// OpenVikingStatus 仪表盘卡读点(spec §7.7):配置态+注册态+拨测态一次取齐。
type OpenVikingStatus struct {
	Enabled      bool   `json:"enabled"`
	Mode         string `json:"mode"`
	Endpoint     string `json:"endpoint"`
	Registered   bool   `json:"registered"`
	ProbeHealthy bool   `json:"probe_healthy"`
	ProbeFails   int32  `json:"probe_fails"`
}

// OpenVikingStatus 组装卡读点(只读,不加锁;注册表读与写路径的容差同 peer 家族)。
func (s *AgentService) OpenVikingStatus() OpenVikingStatus {
	cfg := config.GetGlobalConfig()
	st := OpenVikingStatus{
		Enabled:      cfg.EffectiveOpenVikingEnabled(),
		Mode:         cfg.EffectiveOpenVikingMode(),
		Endpoint:     cfg.EffectiveOpenVikingEndpoint(),
		ProbeHealthy: OpenVikingProbeHealthy(),
		ProbeFails:   OpenVikingProbeConsecutiveFails(),
	}
	for name, e := range aliangMCPEnabledEntries(mcpRegistryHome()) {
		if name == openVikingManagedServerName && e.enabled() {
			st.Registered = true
		}
	}
	return st
}

// ApplyOpenVikingNow 显式 apply(spec §6 双通道之「显式 apply 端点」):开关切换
// 后不必等下个 boot,保存即对齐三处。失败原因原样上抛(HTTP 层转 500 透出)。
func (s *AgentService) ApplyOpenVikingNow() error {
	return EnsureOpenVikingMatchesConfigForTest(agentHome())
}
