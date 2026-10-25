package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/shlex"

	"aliang.one/nursorgate/app/agentcore"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// 内置内核（provider = "agentcore"）的宿主侧接线。
//
// 内核（app/agentcore）禁止 import 本包，所有与宿主状态耦合的能力——出网地址、
// shell 策略、只读围栏、审批——都在这里实现后以窄接口注入。内核自己只负责
// 「跑 agent loop + 发 Claude stream-json 帧」，因此本文件之外没有第二个地方
// 需要知道 provider=="agentcore" 的分支。

// agentCoreDefaultBaseURL 是平台 AI 代理的地址。56432 由 core/owner 进程持有，
// 任何回环:56432 的连接都会被 shouldForceAliangRoute 强制路由到 aliang 隧道，
// 由 relay 层注入 Authorization-Inner。走这条路不需要我们自己管凭据。
const agentCoreDefaultBaseURL = "http://127.0.0.1:56432"

// agentCoreDefaultModel 是手机没显式传模型时的兜底。
//
// 必须是一个**本账号组真能路由**的 id。曾经写的是 claude-sonnet-4-5（真实
// Anthropic id，指望网关做 model mapping），但实测该组对 claude-* 一律返回
// model_not_found，只有 glm-* 通。手机每次建会话都会显式传 model（那是权威
// 来源），这里只是「传空」时的保命值——保命值本身不能是必然失败的。
//
// 与 PhoneServer 的 SEED_PROVIDER_CATALOG、手机 MODEL_PRESETS_BY_PROVIDER
// 的 agentcore 条目保持同一套 id。
const agentCoreDefaultModel = "glm-5.2"

// agentCoreSettingsEnv 从 ~/.claude/settings.json 的 env 块里取一个值。
// 与 claudeAuthEnvOverlay（agent_ai.go）同一套查找口径：先看子进程 env，
// 再看 settings.json——quick-setup 写的 ANTHROPIC_BASE_URL 就落在后者。
func agentCoreSettingsEnv(key string) string {
	home := agentHome()
	if home == "" {
		return ""
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		// 不存在/不可读：没有可回退的东西，静默。
		return ""
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		logger.Warn(fmt.Sprintf("agentcore: 无法解析 settings %s: %v", settingsPath, err))
		return ""
	}
	return strings.TrimSpace(settings.Env[key])
}

// agentCoreEnvOrSettings 按「子进程 env → settings.json」的顺序取一个值。
func agentCoreEnvOrSettings(key string) string {
	if v, ok := agentChildProcessEnvValue(key); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return agentCoreSettingsEnv(key)
}

// agentCoreGetUserAPIKeysFn 是取用户 API key 列表的入口（测试可替换）。
var agentCoreGetUserAPIKeysFn = auth.GetUserAPIKeys

// 内核 key 缓存。user-center 是网络调用（分页拉全量），不能每轮 run 都打。
// 成功缓存 30 分钟；失败只缓存 1 分钟——既不让一次网络抖动把用户钉在无 key
// 状态半小时，也不让每轮 run 都去重打。
var (
	agentCoreTokenMu     sync.Mutex
	agentCoreTokenCache  string
	agentCoreTokenExpiry time.Time
)

const (
	agentCoreTokenCacheTTL     = 30 * time.Minute
	agentCoreTokenFailCacheTTL = time.Minute
)

// agentCoreAuthToken 解析内核出网要带的用户 API key。
//
// 为什么必须带：网关 /v1/messages 的 key 解析有两条路——请求自带用户的 key 就
// 直接用它（source=user-api-key，不做分组门禁）；不带才落到 anthropic-duration
// 分组的兜底 key。实测新账号（allowed_groups 只有 default）在兜底路径上必得
//
//	group "anthropic-duration" not available for platform "anthropic"
//
// 而 CLI provider 因为 quick-setup 把 ANTHROPIC_AUTH_TOKEN 写成了用户自己的
// {{api_key}}，一直走的是第一条路。内核要与 CLI 同口径，否则「装了客户端就能
// 直接对话」这个承诺对新用户不成立。
//
// 取值顺序：
//  1. ANTHROPIC_AUTH_TOKEN（子进程 env → ~/.claude/settings.json）——零网络，
//     与 CLI 用的是同一个值，quick-setup 用户恒命中；
//  2. user-center 的 key 列表（带缓存）——没跑过 quick-setup 的新装用户兜底。
//
// 两条都拿不到就返回空串：内核照旧出网，行为退回「由代理按分组兜底」，不因为
// 拿不到 key 就把这一轮判死。
func agentCoreAuthToken() string {
	if v := agentCoreEnvOrSettings("ANTHROPIC_AUTH_TOKEN"); v != "" {
		return v
	}
	return agentCoreCachedUserAPIKey()
}

func agentCoreCachedUserAPIKey() string {
	agentCoreTokenMu.Lock()
	defer agentCoreTokenMu.Unlock()
	if time.Now().Before(agentCoreTokenExpiry) {
		return agentCoreTokenCache
	}

	key, err := agentCoreFetchUserAPIKey()
	ttl := agentCoreTokenCacheTTL
	if err != nil {
		logger.Warn(fmt.Sprintf("agentcore: 取用户 API key 失败，本轮不带凭据: %v", err))
	}
	if err != nil || key == "" {
		// 没有可用 key 与失败同价：都用短 TTL，等环境变化后尽快重试。
		ttl = agentCoreTokenFailCacheTTL
	}
	agentCoreTokenCache = key
	agentCoreTokenExpiry = time.Now().Add(ttl)
	return key
}

// agentCoreFetchUserAPIKey 从 user-center 的 key 列表里挑一把能用的。
//
// 优先 anthropic 平台的 key：命中 candidate 之后网关会拿这把 key 去请求上游，
// 平台不对的 key 在上游那一步仍然会失败。没有 anthropic key 才退回任意可用
// key——总比不带强（不带是必然的分组门禁）。一把都没有返回空串。
func agentCoreFetchUserAPIKey() (string, error) {
	keys, err := agentCoreGetUserAPIKeysFn()
	if err != nil {
		return "", err
	}
	fallback := ""
	for _, k := range keys {
		if !k.SecretAvailable || strings.EqualFold(strings.TrimSpace(k.Status), "inactive") {
			continue
		}
		key := strings.TrimSpace(k.Key)
		if key == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k.Provider), "anthropic") {
			return key, nil
		}
		if fallback == "" {
			fallback = key
		}
	}
	return fallback, nil
}

// agentCoreResetTokenCache 仅供测试：清掉进程内缓存。
func agentCoreResetTokenCache() {
	agentCoreTokenMu.Lock()
	agentCoreTokenCache = ""
	agentCoreTokenExpiry = time.Time{}
	agentCoreTokenMu.Unlock()
}

// agentCoreBaseURL 解析出网地址。
//
// 与 CLI provider 同口径：都走**回环**:56432，由 shouldForceAliangRoute 强制
// 路由到 aliang 隧道，relay 层注入 Authorization-Inner（标识用户/设备）。用户
// 自己的 API key 另由 agentCoreAuthToken 作为 Authorization 显式带上，两者不是
// 一回事（见 agentCoreAuthToken 注释）。
//
// 用户把 ANTHROPIC_BASE_URL 配成远端直连地址时（本机 settings.json 就是
// https://api.aliang.one），不照抄：那条路要完整的 mTLS 客户端凭据，内核没有。
// 宁可退回 56432 走隧道，也不要让「装了客户端却连不上」这种最难自查的失败发生。
func agentCoreBaseURL() string {
	if v := agentCoreEnvOrSettings("ANTHROPIC_BASE_URL"); v != "" {
		if agentCoreIsLoopbackBase(v) {
			return v
		}
		logger.Warn(fmt.Sprintf(
			"agentcore: ANTHROPIC_BASE_URL=%s 非回环地址，内核改走 %s 平台代理",
			v, agentCoreDefaultBaseURL))
	}
	return agentCoreDefaultBaseURL
}

// agentCoreIsLoopbackBase 判断 base URL 是否指向本机回环。
func agentCoreIsLoopbackBase(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

// agentCoreModel 解析模型名：本轮指定 → env/settings → 兜底。
//
// 手机每次建会话都会显式传 model，那是权威来源；本函数只负责「传空时也要
// 有东西能发出去」，且发出去的东西必须网关认。
//
// 首期**不传达 effort**。两条现成通道都用不了：模型名后缀是 codex 专用约定，
// applyCodexEffortSuffix 的注释写明网关不解析带后缀的 claude 模型名、上游调用
// 会直接坏掉；`--effort` 是 claude CLI 的 flag，进程内内核没有 argv 可传。
// 宁可不设，也不能把模型名拼坏。
func agentCoreModel(run agentAIRun) string {
	// 手机显式传的模型是权威来源，直接采用。
	if m := strings.TrimSpace(run.model); m != "" && !agentCoreModelUnusable(m) {
		return m
	}
	// 没传才看本机配置。ANTHROPIC_MODEL 是显式覆盖；
	// ANTHROPIC_DEFAULT_SONNET_MODEL 是 quick-setup 写的「默认档位映射」，
	// 本机就落在 glm-5.3[1m]——比硬编码兜底更贴近这台机器真实在用的模型。
	for _, key := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL"} {
		if m := agentCoreEnvOrSettings(key); m != "" && !agentCoreModelUnusable(m) {
			return m
		}
	}
	return agentCoreDefaultModel
}

// agentCoreModelUnusable 挡掉不能当模型名用的值。
//
// "<synthetic>" 是 claude CLI 自己造出来的占位名，网关不认识；空串会被网关
// 以 "model is required" 拒绝（agent_ai.go 也把 synthetic 当失败吞掉）。
// 解析到这种值时必须继续往下走兜底，而不是把它发出去。
func agentCoreModelUnusable(model string) bool {
	m := strings.TrimSpace(model)
	return m == "" || strings.EqualFold(m, "<synthetic>")
}

// agentCoreEnsureUpstream 探测并必要时自愈平台 AI 代理。
//
// 刻意不复用 ensureAIUpstreamReachableForRun：那个入口只读**子进程 env** 里的
// ANTHROPIC_BASE_URL，而内核的 56432 是代码里的兜底默认值（env/settings 都没
// 配时才生效）。用 ...ForRun 会在「什么都没配」这个最常见的场景下探测空地址、
// 直接放行，于是 56432 没起时用户看到的仍是裸的 Connection refused。
func agentCoreEnsureUpstream(ctx context.Context) error {
	return ensureAIUpstreamReachable(ctx, agentCoreBaseURL(), aiProxyGuardDeps{
		dial: func(ctx context.Context, hostPort string) error {
			d := net.Dialer{Timeout: 2 * time.Second}
			conn, err := d.DialContext(ctx, "tcp", hostPort)
			if err != nil {
				return err
			}
			return conn.Close()
		},
		heal: func() error {
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Post("http://"+config.DefaultManagementAddr+"/api/run/start", "application/json", nil)
			if err != nil {
				return err
			}
			resp.Body.Close()
			return nil
		},
		sleep: time.Sleep,
	})
}

// agentCoreSystemPrompt 组装 system prompt。
//
// 必须带上 agentAIOptionSystemPrompt：手机端的「方案选择」卡片靠 assistant
// 文本里的 aliang-options 围栏块触发（extractAgentAIOptionBlocks），CLI
// provider 是通过 --append-system-prompt 注入的，内核没有 CLI 可注入，只能
// 拼进 system。
//
// 首期不接 goal 的 task-report prompt（withGoalTaskReportSystemPrompt 只能
// 改 agentAITool，不适用于进程内内核）；goal 任务请继续用 CLI provider。
func agentCoreSystemPrompt() string {
	var b strings.Builder
	b.WriteString("You are Aliang Agent, a coding assistant running inside the Aliang remote agent. ")
	b.WriteString("You work directly in the user's project directory and can read, write and edit files, ")
	b.WriteString("search the codebase, and run shell commands through the provided tools. ")
	b.WriteString("Prefer doing the work over describing it: inspect the relevant files first, then make ")
	b.WriteString("the smallest change that solves the problem. Reply in the user's language.\n\n")
	b.WriteString(agentAIOptionSystemPrompt)
	return b.String()
}

// ---------------------------------------------------------------------------
// 会话历史 → 内核消息
// ---------------------------------------------------------------------------

// agentCoreMessages 把会话历史快照成内核的 messages。
//
// 为什么不用 run.prompt / run.freshPrompt：那两个是给 CLI 用的**扁平化转录**
// （buildAgentAIPrompt 把历史压成一段 "Conversation:\nUser: ..." 文本）。内核
// 自己维护多轮 messages，再喂一份扁平转录会把历史包两层，模型看到「历史里
// 又有一段历史」。
//
// 快照必须在 m.mu 内完成并校验 runSeq：run 是异步跑的，期间用户可能又发了
// 新消息（session.history 变了），拿脏历史会让这一轮凭空多出后续对话。
func (m *agentAIManager) agentCoreMessages(run agentAIRun) []agentcore.Message {
	m.mu.Lock()
	session := m.sessions[run.sessionID]
	var history []agentAIMessage
	if session != nil && session.runSeq == run.runSeq {
		history = append(history, session.history...)
	}
	m.mu.Unlock()

	msgs := make([]agentcore.Message, 0, len(history))
	// texts 与 msgs 同下标，保存纯文本原文——Message.Content 是 interface{}，
	// 追加附件清单时没法从它里面把字符串取回来。
	texts := make([]string, 0, len(history))
	for _, item := range history {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role != "user" && role != "assistant" {
			// 内核只认 user/assistant；其它角色（system 之类）由 System 字段
			// 承载，塞进 messages 会被上游拒绝。
			continue
		}
		if strings.TrimSpace(item.Content) == "" {
			continue
		}
		msgs = append(msgs, agentcore.TextMessage(role, item.Content))
		texts = append(texts, item.Content)
	}

	// 附件位置清单附在最后一条 user 消息上。首期 includeImages=false：内核
	// 还没有多模态输入通道，图片只能靠清单里的路径让模型自己去读。
	if suffix := agentAIAttachmentPromptSuffix(run.attachments, false); suffix != "" {
		if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
			texts[n-1] += suffix
			msgs[n-1] = agentcore.TextMessage("user", texts[n-1])
		} else {
			msgs = append(msgs, agentcore.TextMessage("user", strings.TrimSpace(suffix)))
		}
	}
	return msgs
}

// ---------------------------------------------------------------------------
// shell 策略
// ---------------------------------------------------------------------------

// agentCoreShellPolicy 把 services 里现成的 shell 解析/白名单逻辑包成内核的
// ShellPolicy。内核不许 import services，所以只能由这里注入。
type agentCoreShellPolicy struct {
	run agentAIRun
}

// agentCoreShellArgs 返回某个 shell 执行「一条命令串」所需的固定参数。
// 内核会把 baseArgs 原样拼在命令前面，所以这里必须给对。
func agentCoreShellArgs(path string) []string {
	switch strings.ToLower(filepath.Base(path)) {
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return []string{"-NoProfile", "-Command"}
	case "cmd", "cmd.exe":
		return []string{"/C"}
	default:
		return []string{"-c"}
	}
}

// Resolve 解析出可信的 shell 可执行文件。
func (p *agentCoreShellPolicy) Resolve(kind string) (string, []string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "bash":
		if runtime.GOOS == "windows" {
			// Windows 上 defaultAgentShell() 会退到 cmd/powershell，那会让
			// 「Bash」工具跑出完全不同的语法。宁可直接失败，让模型改用
			// PowerShell 工具，也不要静默换壳。
			if _, err := exec.LookPath("bash"); err != nil {
				return "", nil, fmt.Errorf("当前系统没有可用的 bash，请改用 PowerShell 工具")
			}
			path, err := resolveAgentShell("bash")
			if err != nil {
				return "", nil, err
			}
			return path, agentCoreShellArgs(path), nil
		}
		path, err := resolveAgentShell(defaultAgentShell())
		if err != nil {
			return "", nil, err
		}
		return path, agentCoreShellArgs(path), nil

	case "powershell":
		// Windows 优先内置 5.1，其余平台优先跨平台的 pwsh 7。
		candidates := []string{"pwsh", "powershell"}
		if runtime.GOOS == "windows" {
			candidates = []string{"powershell", "pwsh"}
		}
		for _, candidate := range candidates {
			if _, err := exec.LookPath(candidate); err != nil {
				continue
			}
			path, err := resolveAgentShell(candidate)
			if err != nil {
				return "", nil, err
			}
			return path, agentCoreShellArgs(path), nil
		}
		return "", nil, fmt.Errorf("当前系统没有可用的 PowerShell")

	default:
		return "", nil, fmt.Errorf("不支持的 shell 类型: %s", kind)
	}
}

// Allow 判定命令是否在 goal 的 allowed_commands 白名单内。
// 白名单为空 = 不强制（与 goalScopeAllowsToolCall 的语义一致）。
func (p *agentCoreShellPolicy) Allow(command string) (bool, string) {
	return agentCoreCommandAllowed(p.run.goalAllowedCommands, command)
}

// agentCoreCommandAllowed 逐段解析命令并按首 token 前缀匹配白名单。
// 与 goalScopeAllowsToolCall 里 bash 分支的判定完全一致，只是抽出来给
// PowerShell 复用。
func agentCoreCommandAllowed(allowed []string, command string) (bool, string) {
	if len(allowed) == 0 {
		return true, ""
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return true, ""
	}
	segments, ok := splitShellCommandSegments(command)
	if !ok {
		segments = []string{command}
	}
	for _, seg := range segments {
		tokens, err := shlex.Split(seg)
		if err != nil || len(tokens) == 0 {
			return false, "命令无法解析: " + seg
		}
		if !commandAllowed(tokens[0], allowed) {
			return false, "命令不在允许范围内: " + seg
		}
	}
	return true, ""
}

// ---------------------------------------------------------------------------
// 只读围栏
// ---------------------------------------------------------------------------

// agentCoreScopeCheck 是内核的 ToolEnv.ScopeCheck。
//
// 内核的 8 个工具类型全部未导出，宿主无法「摘掉」写工具，所以只读模式只能
// 在围栏这一层挡。这同时覆盖 goal fork 的只读 run（run.readOnly）和 goal 的
// allowed_scope（roots / commands）。
func agentCoreScopeCheck(run agentAIRun) func(string, map[string]interface{}) (bool, string) {
	return func(tool string, input map[string]interface{}) (bool, string) {
		lower := strings.ToLower(strings.TrimSpace(tool))

		if run.readOnly {
			switch lower {
			case "write", "edit", "multiedit", "notebookedit", "bash", "powershell":
				return false, "只读模式不允许修改文件或执行命令"
			}
		}

		// PowerShell 与 Bash 同语义，但 goalScopeAllowsToolCall 只对 "bash"
		// 做命令白名单，不补这一步就等于给了一条绕过 allowed_commands 的路。
		if lower == "powershell" {
			command, _ := input["command"].(string)
			if allowed, reason := agentCoreCommandAllowed(run.goalAllowedCommands, command); !allowed {
				return false, reason
			}
		}

		// 包装成 handleClaudeApprovalHook 的顶层形状：goalScopeAllowsToolCall
		// 从 tool_input 里读 command / file_path。
		raw := map[string]interface{}{
			"tool_name":  tool,
			"tool_input": input,
		}
		return goalScopeAllowsToolCall(tool, raw, run.projectPath, run.goalAllowedRoots, run.goalAllowedCommands)
	}
}

// ---------------------------------------------------------------------------
// run 入口
// ---------------------------------------------------------------------------

// runAgentCore 跑一次内置内核的 run。
//
// 与 runCLIPass 的关系：spawn 换成了进程内调用，但**对外的事件契约完全一样**
// —— ai.run.started → (ai.session.bound) → ai.delta* → ai.done / ai.error。
// 手机端、服务端、审计都不需要知道这一轮是内核跑的。
//
// 帧的产出路径：内核 Emit 写 io.Pipe 写入端，读端交给 streamAgentAIStdout
// （format=claude_stream_json）。delta 去重、工具活动追踪、session_id 捕获、
// ai.command 配对全部由那条既有管线免费提供。
func (m *agentAIManager) runAgentCore(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter) {
	defer m.clearRunning(run.sessionID, run.runSeq, writeJSON)
	if run.cancel != nil {
		defer run.cancel()
	}

	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   run.sessionID,
		"message_id":   agentAssistantMessageID(run.messageID),
		"provider":     run.provider,
		"mode":         run.mode,
		"project_path": run.projectPath,
		"state":        "running",
	})

	// 平台代理没起时先自愈一次，否则用户看到的是裸的 Connection refused。
	if err := agentCoreEnsureUpstream(ctx); err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return
	}

	messages := m.agentCoreMessages(run)
	if len(messages) == 0 {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, fmt.Errorf("会话没有可发送的消息")))
		return
	}

	// 进度上报：与 CLI 路径同一套节拍与字段，手机端的「已改 N 个文件」直接复用。
	var filesMu sync.Mutex
	filesTouched := make(map[string]struct{})
	fileSink := func(fp string) {
		if fp = strings.TrimSpace(fp); fp == "" {
			return
		}
		filesMu.Lock()
		filesTouched[fp] = struct{}{}
		filesMu.Unlock()
		run.activity.markProgress()
	}
	progressStop := make(chan struct{})
	defer close(progressStop)
	go func() {
		ticker := time.NewTicker(agentAIRunProgressInterval)
		defer ticker.Stop()
		emit := func() {
			filesMu.Lock()
			touched := len(filesTouched)
			filesMu.Unlock()
			_ = writeJSON(map[string]interface{}{
				"type":                models.AgentEventAIRunProgress,
				"session_id":          run.sessionID,
				"message_id":          agentAssistantMessageID(run.messageID),
				"files_touched_count": touched,
				"git_changed_count":   countGitChanged(run.projectPath),
			})
		}
		for {
			select {
			case <-progressStop:
				return
			case <-ticker.C:
				emit()
			}
		}
	}()

	limiter := &agentAIOutputLimiter{
		meter: newOutputMeter(agentAIOutputRateWindow, agentAIOutputRateBytes, int64(agentAIOutputCapBytes)),
	}

	// 流式管线：内核写 → 管道 → streamStructuredAIDelta。
	pipeReader, pipeWriter := io.Pipe()
	var output strings.Builder
	var outMu sync.Mutex
	var lastRetry claudeRetryInfo
	var capturedResumeSessionID string

	// 会话绑定：内核的 session id 由我们自己发（system/init 就带），第一次拿到
	// 时落库并发 ai.session.bound。与 CLI 路径同一个 confirmBinding 口径。
	var bindingOnce sync.Once
	var bindingErr error
	onNativeSessionID := func(nativeSessionID string) {
		bindingOnce.Do(func() {
			record, err := m.confirmBinding(run.sessionID, run.provider, nativeSessionID, run.bindingVersion)
			if err != nil {
				bindingErr = err
				_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
				if run.cancel != nil {
					run.cancel()
				}
				return
			}
			_ = writeJSON(map[string]interface{}{
				"type":              models.AgentEventAISessionBound,
				"session_id":        run.sessionID,
				"message_id":        run.messageID,
				"provider":          run.provider,
				"native_session_id": record.NativeSessionID,
				"source_session_id": record.NativeSessionID,
				"binding_version":   record.BindingVersion,
				"binding_state":     record.State,
			})
		})
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		streamAgentAIStdout(pipeReader, agentAIOutputClaudeStreamJSON, run, writeJSON, limiter, func(text string) {
			outMu.Lock()
			appendAgentAIHistoryCapture(&output, text)
			outMu.Unlock()
		}, fileSink, &lastRetry, &capturedResumeSessionID, onNativeSessionID)
	}()

	// 内核的 session id：优先沿用上一轮的（resume 语义），没有就自己发一个。
	kernelSessionID := strings.TrimSpace(run.resumeSessionID)

	// steer 通道必须在 agentcore.Run 之前挂上：用户可能在第一帧之前就插话，
	// 那批消息要能排进队列被第一次 GetSteeringMessages 取走。
	coreSteer := newAgentAICoreSteerControl(run, writeJSON)
	m.attachCoreSteer(run.sessionID, run.runSeq, coreSteer)

	cfg := &agentcore.Config{
		// 带上用户自己的 API key：网关靠它走 user-api-key 通道，不做分组门禁。
		// 不带的话，不在 anthropic-duration 组里的账号会被 403 挡下（见
		// agentCoreAuthToken 注释）。拿不到 key 时 WithAPIKey("") 等价于不带，
		// 行为退回「由代理按分组兜底」。
		Client:    agentcore.NewClient(agentCoreBaseURL()).WithAPIKey(agentCoreAuthToken()),
		Model:     agentCoreModel(run),
		System:    agentCoreSystemPrompt(),
		Tools:     agentcore.DefaultRegistry(),
		SessionID: kernelSessionID,
		Env: &agentcore.ToolEnv{
			ProjectPath: run.projectPath,
			ScopeCheck:  agentCoreScopeCheck(run),
			ShellPolicy: &agentCoreShellPolicy{run: run},
		},
		Approve: m.agentCoreApprove(run, writeJSON),
		// 内核不 import 日志包，这里把宿主 logger 包一层递进去（签名不同：
		// logger.Warn 是 variadic interface{}）。目前只承载截断这类无事件帧的
		// 内部决策。
		Logf: func(format string, args ...interface{}) {
			logger.Warn(fmt.Sprintf(format, args...))
		},
		Emit: func(frame []byte) error {
			_, err := pipeWriter.Write(frame)
			return err
		},
		Hooks: agentCoreLoopHooks(m, run),
	}

	result, runErr := agentcore.Run(ctx, cfg, messages)

	// 先关写入端让读端自然收尾，再等它把最后一帧消费完。
	_ = pipeWriter.Close()
	wg.Wait()

	if bindingErr != nil {
		return
	}

	if ctx.Err() != nil {
		// 被停止：内核已经发过 interrupted 终止帧（非错误），这里只收敛状态。
		status, errMsg := agentAIRunStoppedStatus(run.activity, limiter)
		finalizeRunSubagents(run, writeJSON)
		statusPayload := map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": run.sessionID,
			"status":     status,
		}
		if errMsg != "" {
			statusPayload["error"] = errMsg
		}
		_ = writeJSON(statusPayload)
		return
	}

	// 内核的终止帧已经以 is_error=true 发过 → 流式管线已转成 ai.error，不再补。
	// runErr 非空但 IsError 为 false 的情形（理论上只有「内核内部报错但没走到
	// 终止帧」）才需要宿主兜底。
	if result != nil && result.IsError {
		finalizeRunSubagents(run, writeJSON)
		return
	}
	if runErr != nil {
		finalizeRunSubagents(run, writeJSON)
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, runErr))
		return
	}

	outMu.Lock()
	assistantOutput := output.String()
	outMu.Unlock()
	filesMu.Lock()
	filesTouchedCount := len(filesTouched)
	filesMu.Unlock()

	m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)
	m.setAgentAIResumeSessionIDIfEmpty(run.sessionID, run.runSeq, capturedResumeSessionID)
	// 刻意跳过 ccPeerSessionJSONL / appendLineageMarker：那是 Claude CLI 的
	// transcript 谱系修补，内核没有对应的 JSONL 文件。
	if blocks := extractAgentAIOptionBlocks(assistantOutput); len(blocks) > 0 {
		m.emitOptionRequest(run, writeJSON, blocks)
	}

	done := map[string]interface{}{
		"type":                models.AgentEventAIDone,
		"session_id":          run.sessionID,
		"message_id":          agentAssistantMessageID(run.messageID),
		"files_touched_count": filesTouchedCount,
		"git_changed_count":   countGitChanged(run.projectPath),
	}
	if sid := strings.TrimSpace(capturedResumeSessionID); sid != "" {
		// 字段名沿用 claude_session_id：服务端按这个键持久化 sourceSessionId
		// 并在下一轮回填 resume_session_id，与 provider 无关。
		done["claude_session_id"] = sid
	}
	finalizeRunSubagents(run, writeJSON)
	_ = writeJSON(done)
}

// agentCoreLoopHooks 组装内核的循环钩子。
//
// 首期只接 steering：用户在运行中插的话必须在**下一次** LLM 调用前进入上下文，
// 这正是 GetSteeringMessages 的时机。PrepareRequest / FinishTurn 留空——前者
// 的诉求（改 system / max_tokens）已在 Config 里静态给定，后者没有消费方。
func agentCoreLoopHooks(m *agentAIManager, run agentAIRun) agentcore.LoopHooks {
	return agentcore.LoopHooks{
		GetSteeringMessages: func() []agentcore.Message {
			return m.drainAgentCoreSteering(run)
		},
	}
}

// ---------------------------------------------------------------------------
// steering（回合中插话）
// ---------------------------------------------------------------------------

// agentAICoreSteerControl 是内置内核 run 的 steer 通道。
//
// 与 claude 的 stdin 通道不同，内核没有可写的管道：插话只能排队，由
// LoopHooks.GetSteeringMessages 在**下一次 LLM 调用前**取走并折进上下文。
// 因此这里的 deliver 永远返回 "queued"（不是 "applied"）——真正「已进入
// 上下文」的时刻只有 drain 那一刻，那时才发 applied ack。把 deliver 直接
// 报成 applied 会撒谎：消息可能还在队列里 run 就结束了。
//
// 三个结果口径与 codex/claude 通道一致：queued / applied / not_running。
type agentAICoreSteerControl struct {
	mu        sync.Mutex
	sessionID string
	runSeq    int
	write     agentTerminalWriter
	closed    bool
	queue     []agentAISteerMessage
}

func newAgentAICoreSteerControl(run agentAIRun, write agentTerminalWriter) *agentAICoreSteerControl {
	return &agentAICoreSteerControl{
		sessionID: run.sessionID,
		runSeq:    run.runSeq,
		write:     write,
	}
}

// deliver 把一条 steer 排进队列。closed 之后拒绝（run 已结束）。
//
// ImageBlocks 被忽略：内核首期没有多模态输入通道（与 agentCoreMessages 的
// includeImages=false 同因）。文本附件已在 ai.steer 入口并进 Content，所以
// 只有纯图片插话会退化成「图片丢失」，不会丢文字。
func (c *agentAICoreSteerControl) deliver(msg agentAISteerMessage) (string, error) {
	if c == nil {
		return "not_running", errors.New("agentcore steer control is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "not_running", errors.New("agentcore turn ended before steer was applied")
	}
	c.queue = append(c.queue, msg)
	return "queued", nil
}

// drain 取走全部排队插话，转成内核消息，并逐条发 applied ack。
//
// 由内核在每次 LLM 调用前调用（run goroutine）。取走即视为「已进入上下文」：
// 返回的消息会被 loop.go 立刻 append 进 history，紧接着就是请求组装。
func (c *agentAICoreSteerControl) drain() []agentcore.Message {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	queued := c.queue
	c.queue = nil
	write := c.write
	sessionID := c.sessionID
	c.mu.Unlock()

	out := make([]agentcore.Message, 0, len(queued))
	for _, item := range queued {
		out = append(out, agentcore.TextMessage("user", item.Content))
		emitAgentAISteerAck(write, sessionID, item.MessageID, "applied", "", "")
	}
	return out
}

// close 是 run 结束时的兜底：把仍未取走的插话逐个 ack 成 not_running，
// 否则手机上的气泡会永远停在 pending。
func (c *agentAICoreSteerControl) close(result, errMsg, code string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	queued := c.queue
	c.queue = nil
	c.closed = true
	c.mu.Unlock()
	for _, item := range queued {
		emitAgentAISteerAck(c.write, c.sessionID, item.MessageID, result, errMsg, code)
	}
}

// drainAgentCoreSteering 是 LoopHooks.GetSteeringMessages 的实现。
//
// runSeq 必须校验：run 是异步的，会话上挂的通道可能已经属于下一轮了，
// 拿错通道会把别人的插话折进这一轮的上下文。
func (m *agentAIManager) drainAgentCoreSteering(run agentAIRun) []agentcore.Message {
	m.mu.Lock()
	session := m.sessions[run.sessionID]
	var control *agentAICoreSteerControl
	if session != nil && session.runSeq == run.runSeq {
		control = session.coreSteer
	}
	m.mu.Unlock()
	if control == nil {
		return nil
	}
	return control.drain()
}

// attachCoreSteer 把内核 steer 通道挂到会话上，供 ai.steer 路由查找。
// 与 attachClaudeSteer 同口径：只在 runSeq 仍匹配时挂载。
func (m *agentAIManager) attachCoreSteer(sessionID string, runSeq int, control *agentAICoreSteerControl) {
	if control == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[sessionID]; session != nil && session.runSeq == runSeq {
		session.coreSteer = control
	}
}

// ---------------------------------------------------------------------------
// 审批
// ---------------------------------------------------------------------------

// agentCoreApprove 是内核的 Config.Approve。策略短路与 codex/claude 一致：
// auto_approve / auto_deny 本地直接判（不走云端往返），只有 require_approval
// 才真的问用户。
func (m *agentAIManager) agentCoreApprove(run agentAIRun, writeJSON agentTerminalWriter) func(context.Context, agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
	return func(ctx context.Context, call agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
		toolInput := marshalAgentAIRaw(call.Input)
		if svc := m.approvalService(); svc != nil {
			switch decision, matchedID := svc.evaluateApprovalDecision(call.ToolName, toolInput, run.projectPath); decision {
			case decisionAutoApprove:
				logger.Info(fmt.Sprintf("approval-hook: AUTO-APPROVE by policy rule=%s tool=%s session=%s (agentcore, no cloud round-trip)", matchedID, call.ToolName, run.sessionID))
				return agentcore.ApprovalResponse{Approved: true}, nil
			case decisionAutoDeny:
				logger.Info(fmt.Sprintf("approval-hook: AUTO-DENY by policy rule=%s tool=%s session=%s (agentcore)", matchedID, call.ToolName, run.sessionID))
				return agentcore.ApprovalResponse{Approved: false, Reason: "该操作被设备策略拒绝"}, nil
			case decisionRequireApproval:
				run.matchedRuleID = matchedID
				run.policyVersion = svc.effectiveApprovalPolicyForPath(run.projectPath).Version
			}
		}

		approvalCtx, cancel := context.WithTimeout(ctx, agentAIApprovalTimeout)
		defer cancel()
		response, err := m.requestApproval(approvalCtx, run, writeJSON, buildAgentCoreApprovalRequest(run, call))
		if err != nil {
			return agentcore.ApprovalResponse{}, err
		}
		switch response.Decision {
		case models.AgentAIApprovalDecisionAccept, models.AgentAIApprovalDecisionAcceptForSession:
			return agentcore.ApprovalResponse{Approved: true}, nil
		case models.AgentAIApprovalDecisionDecline:
			return agentcore.ApprovalResponse{Approved: false, Reason: "用户拒绝了这次操作"}, nil
		default:
			return agentcore.ApprovalResponse{Approved: false, Reason: "审批被取消，操作未执行"}, nil
		}
	}
}

// buildAgentCoreApprovalRequest 把内核的审批请求翻成宿主的审批请求。
// ID / session / message / provider / 可选项由 requestApproval 自己补。
func buildAgentCoreApprovalRequest(run agentAIRun, call agentcore.ApprovalRequest) agentAIApprovalRequest {
	command, _ := call.Input["command"].(string)
	command = strings.TrimSpace(command)

	kind := models.AgentAIApprovalKindTool
	if strings.EqualFold(call.ToolName, "bash") || strings.EqualFold(call.ToolName, "powershell") || command != "" {
		kind = models.AgentAIApprovalKindCommand
	}
	title := "Approve Aliang Agent tool use"
	if kind == models.AgentAIApprovalKindCommand {
		title = "Approve Aliang Agent command"
	}

	return agentAIApprovalRequest{
		Provider:  run.provider,
		Kind:      kind,
		Title:     title,
		Command:   command,
		CWD:       run.projectPath,
		ToolName:  call.ToolName,
		ToolInput: marshalAgentAIRaw(call.Input),
		Raw: marshalAgentAIRaw(map[string]interface{}{
			"tool_name":  call.ToolName,
			"tool_input": call.Input,
		}),
		MatchedRuleID: run.matchedRuleID,
		PolicyVersion: run.policyVersion,
	}
}
