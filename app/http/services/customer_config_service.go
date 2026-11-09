package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/google/uuid"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/model"
	sshin "aliang.one/nursorgate/inbound/ssh"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/mirror"
	"aliang.one/nursorgate/processor/routing"
	"aliang.one/nursorgate/processor/rules"
	"aliang.one/nursorgate/processor/statistic"

	gossh "golang.org/x/crypto/ssh"
)

const customerConfigFilePath = "~/.aliang/config.json"
const startupLocalCustomerBaseConfigPath = "./config.json"

type CustomerConfigService struct {
	store *storage.SoftwareConfigStore
}

type CustomerConfigCommitResult struct {
	Version  uint64
	Customer map[string]interface{}
}

type CustomerConfigValidationError struct {
	err error
}

func (e *CustomerConfigValidationError) Error() string {
	if e == nil || e.err == nil {
		return "invalid customer config"
	}
	return e.err.Error()
}

func (e *CustomerConfigValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func NewCustomerConfigService() *CustomerConfigService {
	return &CustomerConfigService{store: storage.NewSoftwareConfigStore()}
}

func (s *CustomerConfigService) GetPresetAIRuleProviders() []config.AIRuleProviderPreset {
	return config.PresetAIRuleProviders
}

func (s *CustomerConfigService) GetCommittedCustomerConfig() (map[string]interface{}, uint64, error) {
	coordinator := config.GetEffectiveConfigCommitCoordinator()
	if committed := coordinator.LastCommittedSnapshot(); committed != nil && strings.TrimSpace(committed.Content) != "" {
		customer, err := extractCustomerFromConfigContent(committed.Content)
		if err != nil {
			return nil, 0, err
		}
		return customer, coordinator.Version(), nil
	}

	globalCfg := config.GetGlobalConfig()
	if globalCfg == nil {
		return map[string]interface{}{}, coordinator.Version(), nil
	}

	raw, err := json.Marshal(globalCfg)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal global config: %w", err)
	}

	customer, err := extractCustomerFromConfigContent(string(raw))
	if err != nil {
		return nil, 0, err
	}
	return customer, coordinator.Version(), nil
}

func (s *CustomerConfigService) UpdateCommittedCustomerConfig(payload []byte) (*CustomerConfigCommitResult, error) {
	if len(payload) == 0 {
		return nil, &CustomerConfigValidationError{err: errors.New("request body is empty")}
	}

	globalCfg, err := resolveBaseConfigForCustomerUpdate()
	if err != nil {
		return nil, err
	}

	mergedContent, nextCfg, err := mergeCustomerPayload(globalCfg, payload)
	if err != nil {
		return nil, &CustomerConfigValidationError{err: err}
	}

	if err := nextCfg.Validate(); err != nil {
		return nil, &CustomerConfigValidationError{err: err}
	}

	// mesh 自指拒绝(spec §3.4/§7 层0):config 层拿不到本机 device_id(反向
	// 依赖会成环),由提交路径显式校验——把自己列为 peers 目标 = 1 跳自环,
	// 保存期直接拒。device_id 为空(未注册首启)时 ValidateMesh 自行跳过。
	if err := nextCfg.ValidateMesh(currentAgentDeviceIDForNotify()); err != nil {
		return nil, &CustomerConfigValidationError{err: err}
	}

	nextCfgRaw, err := json.Marshal(nextCfg)
	if err != nil {
		return nil, fmt.Errorf("marshal validated config: %w", err)
	}

	nextSnapshot := &config.EffectiveConfigSnapshot{
		UUID:     uuid.NewString(),
		Software: "runtime",
		Name:     "customer-config",
		FilePath: customerConfigFilePath,
		Version:  "",
		Format:   models.ConfigFormatJSON,
		Content:  string(nextCfgRaw),
	}

	coordinator := config.GetEffectiveConfigCommitCoordinator()
	commitResult, err := coordinator.Commit(
		nextSnapshot,
		func(snapshot *config.EffectiveConfigSnapshot) error {
			if snapshot == nil {
				return errors.New("file snapshot is required")
			}
			var fileCfg config.Config
			if err := json.Unmarshal([]byte(snapshot.Content), &fileCfg); err != nil {
				return fmt.Errorf("decode snapshot content for file persist: %w", err)
			}
			expandedPath, err := resolveServiceConfigPath(snapshot.FilePath)
			if err != nil {
				return fmt.Errorf("expand file path: %w", err)
			}
			return config.SaveConfigToFile(&fileCfg, expandedPath)
		},
		func(snapshot *config.EffectiveConfigSnapshot) error {
			if snapshot == nil {
				return errors.New("db snapshot is required")
			}
			if s == nil || s.store == nil {
				return errors.New("customer config store is not initialized")
			}
			if err := s.store.SaveEffectiveConfigSnapshot(models.SoftwareEffectiveConfigSnapshot{
				Software:       snapshot.Software,
				ConfigUUID:     snapshot.UUID,
				ConfigName:     snapshot.Name,
				ConfigFilePath: snapshot.FilePath,
				ConfigVersion:  snapshot.Version,
				ConfigFormat:   snapshot.Format,
				SnapshotJSON:   snapshot.Content,
			}); err != nil {
				return err
			}

			var committedCfg config.Config
			if err := json.Unmarshal([]byte(snapshot.Content), &committedCfg); err != nil {
				return fmt.Errorf("decode snapshot content for memory commit: %w", err)
			}
			previousCfg := config.GetGlobalConfig()
			config.SetGlobalConfig(&committedCfg)
			if err := outbound.GetRegistry().RefreshCustomerProxy(&committedCfg); err != nil {
				return fmt.Errorf("refresh customer proxy registry: %w", err)
			}
			if err := refreshRoutingRuntimeAfterCustomerConfigCommit(&committedCfg); err != nil {
				return fmt.Errorf("refresh routing runtime: %w", err)
			}
			clearRoutingDecisionCache()
			closeDisabledAIAcceleratedConnections(previousCfg, &committedCfg)
			mirror.InitGlobalForwarder()
			ReconcileSSHIngressFromConfig(&committedCfg)
			ReconcileUnifiedIngressBinding(&committedCfg)
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	customer, err := extractCustomerFromConfigContent(mergedContent)
	if err != nil {
		return nil, err
	}

	return &CustomerConfigCommitResult{
		Version:  commitResult.Version,
		Customer: customer,
	}, nil
}

func refreshRoutingRuntimeAfterCustomerConfigCommit(committedCfg *config.Config) error {
	if committedCfg == nil {
		return nil
	}

	activeCanonical := config.GetRoutingApplyStore().ActiveCanonicalSchema()
	aliangEnabled := true
	socksEnabled := true
	ingressMode := "tun"
	if activeCanonical != nil {
		aliangEnabled = activeCanonical.Egress.ToAliang.Enabled
		// 保留 store 已有的 toSocks 开关状态：customer 配置保存不重置用户开关
		// （store 为空时用默认 true，与既有行为一致）。
		socksEnabled = activeCanonical.Egress.ToSocks.Enabled
		if activeCanonical.Ingress.Mode == "http" || activeCanonical.Ingress.Mode == "tun" {
			ingressMode = activeCanonical.Ingress.Mode
		}
	}

	canonical, err := routing.CompileCanonicalRoutingFromRuntimeInputs(committedCfg, model.RulesSettings{
		AliangEnabled: aliangEnabled,
		SocksEnabled:  socksEnabled,
	})
	if err != nil {
		return err
	}
	canonical.Ingress.Mode = ingressMode

	raw, err := json.Marshal(canonical)
	if err != nil {
		return fmt.Errorf("marshal runtime routing schema: %w", err)
	}

	_, err = config.GetRoutingApplyStore().Apply(raw, func(cfg *config.CanonicalRoutingSchema) (any, error) {
		return routing.CompileRuntimeSnapshot(cfg)
	})
	return err
}

// ReconcileSSHIngressFromConfig 把给定配置的 SSH 入口期望态作用到运行面。
// **两个调用点共用本函数(防两处漂移)**:① customer 配置提交协调路径
// (UpdateCommittedCustomerConfig 的内存提交回调,端口/凭证变更即重启,
// 重复提交同配置不重启);② core 启动序列(cmd/core.go initializeCoreSubsystems
// 与 cmd/start.go runStart,配置加载完成后)——重启后自动恢复已启用入口。
// EffectiveSSHIngress nil(未配置/总开关关)→期望停;非 nil→翻译成
// inbound/ssh.RunConfig 交 Reconcile 启停。
//
// 失败仅日志不上抛:提交侧不因入口回滚整套配置(其余运行面刷新已生效,
// 协调器恒回调不做内容去重,下次提交重试);启动侧不阻断 boot(服务面非
// 关键路径,下次提交或下次重启重试)。
//
// Bind host 语义:Server 监听 config.ServiceBindHost()(默认 127.0.0.1,
// 显式 --host 放宽)——与 56431/56432「默认安全」姿态一致;LAN 开放入口
// 需显式 --host,属部署姿态(配置里无 host 字段,刻意不发明)。
func ReconcileSSHIngressFromConfig(cfg *config.Config) {
	effective := cfg.EffectiveSSHIngress()
	if effective == nil {
		// 期望停:不触 cache 目录/agent 单例——无入口配置的提交/启动零额外副作用。
		sshin.Reconcile(nil, "", nil)
		return
	}
	rc := buildSSHIngressRunConfig(effective)
	hostKeyPath := sshIngressHostKeyPath()
	if rc == nil || hostKeyPath == "" {
		// 全量凭证解析失败(手改文件绕过保存校验)或 cache 目录不可解析:
		// 开着门无人能进的入口毫无意义,按期望停处理(镜像 Validate
		// 「至少一把钥匙」规则),Warn 已在各自 helper 内记。
		sshin.Reconcile(nil, "", nil)
		return
	}
	// LocalNodeID 注入缝:防环 AppendHop 读的节点标识经 AgentService
	// currentDeviceID 活取(闭包绑定单例指针,读时求值);ForwardGate 留
	// nil=全放行(凭证列表即 ACL)——customer.mesh 的转发门禁/ACL 随组网
	// 分支落地后改注入门禁函数,spec §3 裁定与 inbound/ssh RunConfig.
	// ForwardGate 注释互指(合并集成缝)。
	sshin.Reconcile(rc, hostKeyPath, GetSharedAgentService().currentDeviceID)
}

// ReconcileUnifiedIngressBinding 让统一入口 56432 的实际绑定地址跟上配置的
// 期望地址(spec §6.2 / D2)。期望地址 = config.UnifiedIngressListenAddrFor:
// mesh 总开关生效 → 0.0.0.0(对端 LAN 可拨入),否则回环。开/关组网会改变
// 期望地址,而 listener 只在 StartMitmHttp 的绑定瞬间读一次地址——不重绑
// 就等于开关不生效(开了组网却还只绑回环 = LAN 拨入被拒)。
//
// 与 ReconcileSSHIngressFromConfig 同族、同调用点(customer 配置提交协调
// 路径 + core/start/tray 启动防漂移),共享「期望态 → 运行态」语义。差别在
// 作用对象:本函数只重启 listener 的 accept 面,在途连接不受影响(它们持有
// 自己的 conn,与 listener 脱钩)。
//
// 两个前置判据缺一不可:
//  1. listener 确实在跑(httpProxyIsRunningProbe)——没跑就不管:用户下次
//     起代理时 StartMitmHttp 会按那时的新地址绑定,这里抢先起口会绕过
//     run service 的状态机(以及「停代理=停监听」的 D4 契约)。
//  2. 地址确实不同——相同地址重启是纯抖动(断开在途 accept 面,收益为零)。
//
// 刻意的「模式无关」:绑定面只由 mesh 总开关决定(D2),与 HTTP/TUN 入口
// 模式正交。不按模式加闸,避免「TUN 模式下 56432 恰在跑但绑错」这类漏修。
//
// 失败仅日志不上抛(同 ReconcileSSHIngressFromConfig):提交侧不因入口回滚
// 整套配置,下次提交或下次起代理自然重试。
func ReconcileUnifiedIngressBinding(cfg *config.Config) {
	expected := config.UnifiedIngressListenAddrFor(cfg)
	if !httpProxyIsRunningProbe() {
		return // 代理没在跑:起口是 run service 的职责,这里不越权
	}
	current := unifiedIngressListenAddrProbe()
	if current == "" || current == expected {
		return
	}

	logger.Info(fmt.Sprintf("统一入口绑定地址变更 %s → %s,重启 listener", current, expected))
	httpStopRunner()
	// 起口走容忍变体(评审 I1):重绑失败 Error 留待重试,不在 goroutine 里
	// Fatal 杀进程。
	go httpRestartRunner()
}

// 导出的 IngressHostKeyFileName。与 host key 指纹 API(ssh_ingress_handler
// 的 NewSSHIngressHandlerFromCache)同源同口径——编译器强制展示指纹与
// Server.Start 落盘钥同文件(错位=展示指纹≠实服务钥,Task 6 交接项)。
// 解析失败不阻断提交:Warn+空串,调用方按期望停处理。
func sshIngressHostKeyPath() string {
	dir, err := cache.GetCacheSubdir("ssh")
	if err != nil {
		logger.Warn("resolve ssh ingress host key dir failed, ingress stays stopped: " + err.Error())
		return ""
	}
	return filepath.Join(dir, sshin.IngressHostKeyFileName)
}

// buildSSHIngressRunConfig 把归一化后的 customer.ssh_ingress 翻译成
// inbound/ssh.RunConfig:authorized_keys 行逐条 gossh.ParseAuthorizedKey。
// 单条解析失败跳过并 Warn——配置层 Validate 已在保存通道拦截,此处防御
// 手改文件绕过校验的坏值(半坏配置按好部分继续跑)。两列表全部解析失败
// 返回 nil,交给调用方按期望停处理。
func buildSSHIngressRunConfig(effective *config.SSHIngressConfig) *sshin.RunConfig {
	authorized := parseSSHIngressAuthorizedKeys("authorized_keys", effective.AuthorizedKeys)
	mesh := parseSSHIngressAuthorizedKeys("mesh_authorized_keys", effective.MeshAuthorizedKeys)
	if len(authorized) == 0 && len(mesh) == 0 {
		logger.Warn("ssh_ingress enabled but no authorized key parses, ingress stays stopped")
		return nil
	}
	return &sshin.RunConfig{
		// 专用口哨兵映射(spec §6.4 修订):0 与旧默认 56442 一律=跟随统一口
		// (RunConfig.Port=0,不自持监听);其他显式端口才开常驻专用口。存量
		// 配置里的 56442 升级后不得凭空复活成专用口。
		Port:           effectivePortForRunConfig(effective.Port),
		AuthorizedKeys: authorized,
		MeshKeys:       mesh,
		// ForwardGate 合并集成缝,见 ReconcileSSHIngressFromConfig 注释。
		ForwardGate: nil,
	}
}

// effectivePortForRunConfig 把配置层端口翻译成运行面端口:0(未配)与
// 56442(旧默认哨兵)→ 0(跟随统一口 56432);其他显式值原样(开专用口)。
// 见 inbound/ssh.RunConfig.Port 与 config.SSHIngressDefaultPort 注释。
func effectivePortForRunConfig(port uint16) uint16 {
	if port == 0 || port == config.SSHIngressDefaultPort {
		return 0
	}
	return port
}

// parseSSHIngressAuthorizedKeys 逐行解析 authorized_keys 行为公钥身份;
// 坏行跳过+Warn(保存通道已拦,此处防御),返回可解析子集。
func parseSSHIngressAuthorizedKeys(field string, lines []string) []gossh.PublicKey {
	keys := make([]gossh.PublicKey, 0, len(lines))
	for _, line := range lines {
		pub, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			logger.Warn(fmt.Sprintf("ssh_ingress.%s key skipped (unparseable): %v", field, err))
			continue
		}
		keys = append(keys, pub)
	}
	return keys
}

func clearRoutingDecisionCache() {
	if engine := rules.GetEngine(); engine != nil {
		engine.ClearCache()
	}
}

func IsCustomerConfigValidationError(err error) bool {
	var target *CustomerConfigValidationError
	return errors.As(err, &target)
}

func mergeCustomerPayload(baseCfg *config.Config, payload []byte) (string, *config.Config, error) {
	if baseCfg == nil {
		return "", nil, errors.New("global config is not initialized")
	}

	customerPatch, err := normalizeCustomerPayload(payload)
	if err != nil {
		return "", nil, err
	}

	baseRaw, err := json.Marshal(baseCfg)
	if err != nil {
		return "", nil, fmt.Errorf("marshal current config: %w", err)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(baseRaw, &root); err != nil {
		return "", nil, fmt.Errorf("decode current config: %w", err)
	}

	var currentCustomer map[string]interface{}
	if rawCustomer, ok := root["customer"]; ok && len(rawCustomer) > 0 {
		if err := json.Unmarshal(rawCustomer, &currentCustomer); err != nil {
			return "", nil, fmt.Errorf("decode current customer config: %w", err)
		}
	}
	if currentCustomer == nil {
		currentCustomer = map[string]interface{}{}
	}

	// 键对齐必须发生在 deepMerge 之前：patch 键与存量键大小写不同时，deepMerge
	// 按大小写敏感的 JSON 键合并会并存两个键（且只增不减），前端随之出现
	// 「Anthropic」「anthropic」两张卡。先对齐到存量拼写让 patch 自然覆盖，
	// 之后的折叠归一只负责治愈存量文件自身的污染。
	canonicalizeCustomerPatchAIRulesKeys(currentCustomer, customerPatch)
	mergedCustomer := deepMergeJSONObjects(currentCustomer, customerPatch)
	normalizeCustomerAIRulesRaw(mergedCustomer)
	mergedCustomerRaw, err := json.Marshal(mergedCustomer)
	if err != nil {
		return "", nil, fmt.Errorf("marshal merged customer config: %w", err)
	}
	root["customer"] = mergedCustomerRaw

	mergedRaw, err := json.Marshal(root)
	if err != nil {
		return "", nil, fmt.Errorf("marshal merged config: %w", err)
	}

	var nextCfg config.Config
	if err := json.Unmarshal(mergedRaw, &nextCfg); err != nil {
		return "", nil, err
	}

	return string(mergedRaw), &nextCfg, nil
}

func closeDisabledAIAcceleratedConnections(previousCfg, nextCfg *config.Config) {
	disabledDomains := disabledAIAccelerationDomains(previousCfg, nextCfg)
	if len(disabledDomains) == 0 {
		return
	}

	clearRoutingDecisionCache()

	closed := statistic.DefaultManager.CloseTrackedConnectionsByMetadata(func(metadata *M.Metadata) bool {
		if metadata == nil || metadata.Route != "RouteToALiang" || metadata.HostName == "" {
			return false
		}
		return mirror.MatchesAnyDomain(metadata.HostName, disabledDomains)
	})

	if closed > 0 {
		logger.Info(fmt.Sprintf("Closed %d accelerated connections after AI rule disable", closed))
	}
}

func disabledAIAccelerationDomains(previousCfg, nextCfg *config.Config) []string {
	if previousCfg == nil || previousCfg.Customer == nil || len(previousCfg.Customer.AIRules) == 0 {
		return nil
	}

	var nextRules map[string]*config.CustomerAIRuleSetting
	if nextCfg != nil && nextCfg.Customer != nil {
		nextRules = nextCfg.Customer.AIRules
	}

	disabled := make(map[string]struct{})
	for providerKey, previousRule := range previousCfg.Customer.AIRules {
		if previousRule == nil || previousRule.Enble == nil || !*previousRule.Enble {
			continue
		}

		nextRule, ok := config.FindCustomerAIRule(nextRules, providerKey)
		if ok && nextRule != nil && nextRule.Enble != nil && *nextRule.Enble {
			continue
		}

		for _, domain := range previousRule.Include {
			normalized := strings.ToLower(strings.TrimSpace(domain))
			if normalized == "" {
				continue
			}
			disabled[normalized] = struct{}{}
		}
	}

	if len(disabled) == 0 {
		return nil
	}

	domains := make([]string, 0, len(disabled))
	for domain := range disabled {
		domains = append(domains, domain)
	}
	return domains
}

func normalizeCustomerPayload(payload []byte) (map[string]interface{}, error) {
	var rawRoot map[string]json.RawMessage
	if err := json.Unmarshal(payload, &rawRoot); err != nil {
		return nil, fmt.Errorf("invalid json format: %w", err)
	}

	if len(rawRoot) == 0 {
		return nil, errors.New("customer config payload is required")
	}

	if rawCustomer, ok := rawRoot["customer"]; ok {
		if len(rawRoot) > 1 {
			forbidden := make([]string, 0, len(rawRoot)-1)
			for key := range rawRoot {
				if key == "customer" {
					continue
				}
				forbidden = append(forbidden, key)
			}
			sort.Strings(forbidden)
			return nil, fmt.Errorf("customer.%s is forbidden: editable customer fields are [proxy ai_rules proxy_rules traffic_mirror http1_drop model_mapping custom_env_vars geoip agentcore_llm agentcore peer_mcp mesh ssh_ingress]", forbidden[0])
		}
		rawRoot = map[string]json.RawMessage{}
		if err := json.Unmarshal(rawCustomer, &rawRoot); err != nil {
			return nil, fmt.Errorf("invalid customer json format: %w", err)
		}
	} else {
		if err := validateEditableCustomerKeys(rawRoot); err != nil {
			return nil, err
		}
	}

	normalized := make(map[string]interface{}, len(rawRoot))
	for key, rawValue := range rawRoot {
		var value interface{}
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return nil, fmt.Errorf("invalid customer.%s format: %w", key, err)
		}
		normalized[key] = value
	}

	return normalized, nil
}

func validateEditableCustomerKeys(rawRoot map[string]json.RawMessage) error {
	for key := range rawRoot {
		// 字段清单与 processor/config/types.go customerUnknownKeyErrors 的文案逐字同步,改一处必改三处(两 switch + 三文案)。
		switch key {
		case "proxy", "ai_rules", "proxy_rules", "traffic_mirror", "http1_drop", "model_mapping", "custom_env_vars", "geoip", "agentcore_llm", "agentcore", "peer_mcp", "mesh", "ssh_ingress":
		default:
			return fmt.Errorf("customer.%s is forbidden: editable customer fields are [proxy ai_rules proxy_rules traffic_mirror http1_drop model_mapping custom_env_vars geoip agentcore_llm agentcore peer_mcp mesh ssh_ingress]", key)
		}
	}
	return nil
}

func deepMergeJSONObjects(base, patch map[string]interface{}) map[string]interface{} {
	if len(base) == 0 && len(patch) == 0 {
		return map[string]interface{}{}
	}

	merged := make(map[string]interface{}, len(base)+len(patch))
	for key, value := range base {
		merged[key] = cloneJSONValue(value)
	}

	for key, value := range patch {
		if value == nil {
			continue
		}

		baseMap, baseIsMap := merged[key].(map[string]interface{})
		patchMap, patchIsMap := value.(map[string]interface{})
		if baseIsMap && patchIsMap {
			merged[key] = deepMergeJSONObjects(baseMap, patchMap)
			continue
		}
		merged[key] = cloneJSONValue(value)
	}

	return merged
}

func cloneJSONValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		cloned := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			cloned[key] = cloneJSONValue(child)
		}
		return cloned
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for i, child := range typed {
			cloned[i] = cloneJSONValue(child)
		}
		return cloned
	default:
		if typed == nil {
			return nil
		}
		if reflect.TypeOf(typed).Kind() == reflect.Slice {
			return typed
		}
		return typed
	}
}

func extractCustomerFromConfigContent(content string) (map[string]interface{}, error) {
	if strings.TrimSpace(content) == "" {
		return map[string]interface{}{}, nil
	}

	var root map[string]interface{}
	if err := json.Unmarshal([]byte(content), &root); err != nil {
		return nil, fmt.Errorf("decode config content: %w", err)
	}

	customer, ok := root["customer"].(map[string]interface{})
	if !ok || customer == nil {
		return map[string]interface{}{}, nil
	}

	normalizeCustomerAIRulesRaw(customer)

	return customer, nil
}

// canonicalizeCustomerPatchAIRulesKeys 就地对齐 patch 中 ai_rules 的键：
// ① 已停用 provider（如 vscode）直接剥除，不允许经 API 复活；
// ② 与存量键大小写不敏感匹配的键改写为存量拼写，使 deepMerge 覆盖而非复制；
// ③ 其余已知 provider 对齐出厂拼写（CanonicalAIRuleProviderKey）。
// 存量存在多个大小写变体时，对齐到字典序最小的变体——它正是折叠归一的基线条目，
// patch 值必须覆盖基线才能在后续 OR 折叠中胜出（否则 disable 会被吞掉）。
func canonicalizeCustomerPatchAIRulesKeys(currentCustomer, customerPatch map[string]interface{}) {
	if customerPatch == nil {
		return
	}
	rawPatchRules, ok := customerPatch["ai_rules"].(map[string]interface{})
	if !ok || len(rawPatchRules) == 0 {
		return
	}
	rawBaseRules, _ := currentCustomer["ai_rules"].(map[string]interface{})

	patchKeys := make([]string, 0, len(rawPatchRules))
	for key := range rawPatchRules {
		patchKeys = append(patchKeys, key)
	}
	sort.Strings(patchKeys)

	aligned := make(map[string]interface{}, len(rawPatchRules))
	for _, key := range patchKeys {
		if config.IsRemovedAIRuleProvider(key) {
			continue
		}
		target := key
		if baseline := baselineAIRuleBaseKey(rawBaseRules, key); baseline != "" {
			target = baseline
		} else {
			target = config.CanonicalAIRuleProviderKey(key)
		}
		aligned[target] = rawPatchRules[key]
	}
	customerPatch["ai_rules"] = aligned
}

// baselineAIRuleBaseKey 返回存量 ai_rules 中与 key 大小写不敏感匹配、且字典序最小
// 的键（即折叠归一将采用的基线拼写）；无匹配返回 ""。
func baselineAIRuleBaseKey(rawBaseRules map[string]interface{}, key string) string {
	if len(rawBaseRules) == 0 {
		return ""
	}
	normalized := config.NormalizeAIProviderKey(key)
	baseline := ""
	for baseKey := range rawBaseRules {
		if config.NormalizeAIProviderKey(baseKey) != normalized {
			continue
		}
		if baseline == "" || baseKey < baseline {
			baseline = baseKey
		}
	}
	return baseline
}

// normalizeCustomerAIRulesRaw 就地规范化 customer 原始 map 中的 ai_rules 子树：
// 大小写变体折叠为出厂拼写、剥除已停用 provider（见 config.NormalizeCustomerAIRules）。
// 合并路径调用它防止 patch 与存量键大小写不同时产生重复键；视图路径调用它保证
// 前端在任何一次保存之前就能看到折叠后的形态。ai_rules 是全类型化结构，
// 经 typed 往返是安全的（UnmarshalJSON 同时消化 enable/exclude 历史别名）。
func normalizeCustomerAIRulesRaw(customer map[string]interface{}) {
	if customer == nil {
		return
	}
	rawRules, ok := customer["ai_rules"].(map[string]interface{})
	if !ok || len(rawRules) == 0 {
		return
	}

	serialized, err := json.Marshal(rawRules)
	if err != nil {
		return
	}
	var typed map[string]*config.CustomerAIRuleSetting
	if err := json.Unmarshal(serialized, &typed); err != nil {
		return
	}
	normalized := config.NormalizeCustomerAIRules(typed)
	if len(normalized) == 0 {
		delete(customer, "ai_rules")
		return
	}

	reserialized, err := json.Marshal(normalized)
	if err != nil {
		return
	}
	var back map[string]interface{}
	if err := json.Unmarshal(reserialized, &back); err != nil {
		return
	}
	customer["ai_rules"] = back
}

func resolveBaseConfigForCustomerUpdate() (*config.Config, error) {
	if globalCfg := config.GetGlobalConfig(); globalCfg != nil {
		return globalCfg, nil
	}

	coordinator := config.GetEffectiveConfigCommitCoordinator()
	if committed := coordinator.LastCommittedSnapshot(); committed != nil && strings.TrimSpace(committed.Content) != "" {
		var committedCfg config.Config
		if err := json.Unmarshal([]byte(committed.Content), &committedCfg); err != nil {
			return nil, fmt.Errorf("decode committed snapshot content: %w", err)
		}
		return &committedCfg, nil
	}

	for _, candidatePath := range customerUpdateBaseConfigCandidates() {
		fileCfg, found, err := readConfigFromPath(candidatePath)
		if err != nil {
			return nil, err
		}
		if found {
			return fileCfg, nil
		}
	}

	return bootstrapBaseConfigForCustomerUpdate(), nil
}

func bootstrapBaseConfigForCustomerUpdate() *config.Config {
	return &config.Config{
		Core: &config.CoreConfig{
			APIServer: "https://sub2api.liang.home",
		},
		Customer: &config.CustomerConfig{},
	}
}

func customerUpdateBaseConfigCandidates() []string {
	return []string{
		startupLocalCustomerBaseConfigPath,
		customerConfigFilePath,
	}
}

func readConfigFromPath(path string) (*config.Config, bool, error) {
	resolvedPath, err := resolveServiceConfigPath(path)
	if err != nil {
		return nil, false, fmt.Errorf("expand config file path %q: %w", path, err)
	}

	raw, err := os.ReadFile(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read config file %q: %w", resolvedPath, err)
	}

	var fileCfg config.Config
	if err := json.Unmarshal(raw, &fileCfg); err != nil {
		return nil, false, fmt.Errorf("decode config file %q: %w", filepath.Clean(resolvedPath), err)
	}

	return &fileCfg, true, nil
}
