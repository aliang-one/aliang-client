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

// 模型 id 的唯一权威来源是手机（会话设置里选的那个）。本机不再持有任何
// 「兜底模型」，因此这里没有 const——但下面这段结论要留住，它是手机侧
// MODEL_PRESETS_BY_PROVIDER 里 agentcore 条目的依据：
//
//   - 必须是一个**本账号组真能路由**的 id。曾经写过 claude-sonnet-4-5（真实
//     Anthropic id，指望网关做 model mapping），实测该组对 claude-* 一律返回
//     model_not_found，只有 glm-* 通。
//   - 视觉能力：2026-10-26 实测（容器内真出网，四象限四色图按 左上/右上/
//     左下/右下 提问）glm-5.3 与 glm-5.3-flash[1m] 是真视觉（4/4 全对，5.3
//     甚至自己读出 PNG 的 8×8 尺寸），而 glm-5.2 全盲——洋红纯色图答「棕色」，
//     损坏图它自己说「只提供了一个图片的URL链接」。图片附件走 image 块直达
//     模型，所以手机侧可选模型里必须至少有一个看得见图的。
//   - `[1m]` 后缀 = 1M 上下文窗口。手机没选窗口时，模型自带窗口就是上限。
//
// 与 PhoneServer 的 SEED_PROVIDER_CATALOG、手机 MODEL_PRESETS_BY_PROVIDER
// 的 agentcore 条目保持同一套 id。

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

// agentCoreModel 解析本轮模型名：唯一来源是手机下发的值。
//
// 这里刻意没有兜底链了。旧行为是「手机没传 → ANTHROPIC_MODEL →
// ANTHROPIC_DEFAULT_SONNET_MODEL → 硬编码 glm-5.3」，代价是用户以为在 A 模型
// 上对话、实际跑在 B 上：账单对不上、能力对不上（视觉、上下文窗口、effort 支持
// 全都按模型而异），而错配在日志里一个字都不留。
//
// 新的约定：参数由下发方（手机）补全；补全不了就返回空串，调用方明确拒绝开跑，
// 让用户回手机把模型选上。宁可拒绝，也不要静默换模型。
//
// effort 走第三条通道：请求体的 output_config.effort（Claude Messages API
// 语义）。模型名后缀是 codex 专用约定——applyCodexEffortSuffix 的注释写明
// 网关不解析带后缀的 claude 模型名；`--effort` 是 claude CLI 的 flag，进程内
// 内核没有 argv。而 output_config.effort 正是 claude CLI 收到 --effort 后往
// 上游发的同一个字段，claude_code 路径已验证网关/GLM 接受，内核复用同一
// 通道即可。档位归一在内核侧 NormalizeEffortLevel（无法识别的静默丢弃）。
func agentCoreModel(run agentAIRun) string {
	m := strings.TrimSpace(run.model)
	if agentCoreModelUnusable(m) {
		return ""
	}
	return m
}

// agentCoreModelUnusable 挡掉不能当模型名用的值。
//
// "<synthetic>" 是 claude CLI 自己造出来的占位名，网关不认识；空串会被网关
// 以 "model is required" 拒绝（agent_ai.go 也把 synthetic 当失败吞掉）。
// 这两种值一律视作「没给模型」，由调用方拒绝开跑。
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

// scanEnvValue 从 env 键值对里取变量（存在且非空才算命中）。
// 与 agentChildProcessEnvValue 不同：后者空值也算命中且每次重建 env；
// 判 TERM/LANG 这类兜底必须用本函数（TERM= 会被后者误判为已配置）。
func scanEnvValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if index := strings.IndexByte(kv, '='); index > 0 && kv[:index] == key {
			if value := kv[index+1:]; value != "" {
				return value, true
			}
		}
	}
	return "", false
}

// agentCoreToolEnv 组装内核工具（Bash/PowerShell）子进程环境：登录 shell
// PATH 快照 + TERM/LANG 兜底。GUI/systemd 启动的 agent 进程没有这两样，
// 不兜底则子进程里部分 CLI 降级、locale 失真（对照 agent_terminal.go 手机
// 终端已补 TERM 的先例）。
func agentCoreToolEnv() []string {
	env := agentChildProcessEnv()
	if _, ok := scanEnvValue(env, "TERM"); !ok {
		env = append(env, "TERM=xterm-256color")
	}
	if _, ok := scanEnvValue(env, "LANG"); !ok {
		if _, ok := scanEnvValue(env, "LC_ALL"); !ok {
			env = append(env, "LANG=en_US.UTF-8")
		}
	}
	return env
}

// cachedOSVersion 取一次 "Darwin 25.2.0" 式内核版本，缓存进程生命周期。
// uname 失败（如 windows）退回 runtime.GOOS，不阻塞会话。
var (
	osVersionOnce sync.Once
	osVersionStr  string
)

func cachedOSVersion() string {
	osVersionOnce.Do(func() {
		out, err := exec.Command("uname", "-sr").Output()
		if err != nil {
			osVersionStr = runtime.GOOS
			return
		}
		osVersionStr = strings.TrimSpace(string(out))
	})
	return osVersionStr
}

func agentCoreIsGitRepo(projectPath string) bool {
	if strings.TrimSpace(projectPath) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(projectPath, ".git"))
	// .git 是目录（普通仓库）或文件（worktree）都算。
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
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
func agentCoreSystemPrompt(projectPath string) string {
	return agentCoreSystemPromptWithContext(projectPath, "")
}

// agentCoreSystemPromptWithContext 在基础 system prompt 后追加会话级上下文
// （system 角色历史，如 initial_context）。子 agent 调 agentCoreSystemPrompt：
// 它继承主 run 的身份与环境说明，但不该继承会话初始上下文。
func agentCoreSystemPromptWithContext(projectPath, sessionContext string) string {
	var b strings.Builder
	b.WriteString("You are Aliang Agent, a coding assistant running inside the Aliang remote agent. ")
	b.WriteString("You work directly in the user's project directory and can read, write and edit files, ")
	b.WriteString("search the codebase, and run shell commands through the provided tools. ")
	b.WriteString("Prefer doing the work over describing it: inspect the relevant files first, then make ")
	b.WriteString("the smallest change that solves the problem. Reply in the user's language.\n\n")

	// 环境块：CLI provider 场景由 claude CLI 自动注入（cwd/git/平台/OS 版本/
	// 日期），内核没有 CLI，没人注入 → 模型对环境全盲，只能靠可能失真的 Bash
	// 探测猜环境（曾误报「没有 nodejs」）。这里对齐 CLI 的注入行为。
	gitMark := "no"
	if agentCoreIsGitRepo(projectPath) {
		gitMark = "yes"
	}
	b.WriteString("\n<env>\n")
	if strings.TrimSpace(projectPath) == "" {
		b.WriteString("Working directory: (not provided)\n")
	} else {
		fmt.Fprintf(&b, "Working directory: %s\n", projectPath)
	}
	fmt.Fprintf(&b, "Is a git repository: %s\n", gitMark)
	fmt.Fprintf(&b, "Platform: %s\n", runtime.GOOS)
	fmt.Fprintf(&b, "OS Version: %s\n", cachedOSVersion())
	fmt.Fprintf(&b, "Today's date: %s\n", time.Now().Format("2006-01-02"))
	b.WriteString("</env>\n\n")

	// 会话初始上下文（fork 快照 / goal 上下文 / 其它 system 角色历史）。
	// CLI 通道由 buildAgentAIPrompt 以 "System: ..." 留在对话里；内核的
	// messages 收不了 system 角色，只能挂到 system prompt 上，否则这一通道
	// 上初始上下文等于不存在。
	if ctx := strings.TrimSpace(sessionContext); ctx != "" {
		b.WriteString("<session-context>\n")
		b.WriteString(ctx)
		b.WriteString("\n</session-context>\n\n")
	}

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
//
// 附件分两类落位：
//   - 图片 → 真正的 image 内容块（与 Claude 走同一条 buildClaudeImageBlocks，
//     形状完全一致），附在最后一条 user 消息上；
//   - 文本/其它 → 位置清单 + 内联正文（agentAIAttachmentPromptSuffix）。
//
// 图片为什么不能只靠清单里的路径：内核的 read 工具是**文本**工具，模型照着
// 路径读回来的是「看起来是二进制文件，拒绝读取」。清单对图片等于没给。
func (m *agentAIManager) agentCoreMessages(run agentAIRun) ([]agentcore.Message, string) {
	m.mu.Lock()
	session := m.sessions[run.sessionID]
	var history []agentAIMessage
	initialContext := ""
	if session != nil && session.runSeq == run.runSeq {
		history = append(history, session.history...)
		initialContext = session.initialContext
	}
	m.mu.Unlock()

	msgs, texts := agentCoreMessagesFromHistory(history)
	// 与 messages 用同一份快照：system 角色历史（initial_context）走 System
	// 字段，见 agentCoreSystemContextFromHistory。历史里已无 system 条目时
	// （如旧版本落库已把它冲掉、或尾部裁剪把它裁掉）回落 session.initialContext
	// ——立身上下文宁可重复兜底，也不能在内核通道上凭空消失。
	sessionContext := agentCoreSystemContextFromHistory(history, initialContext)

	// 图片附件 → image 块。这里的 run.attachments 在 runCLI 里已经下载过
	// （downloadAgentAIAttachments 设了 Path 并清掉 URL），所以不再下载一次。
	//
	// 上限沿用 buildClaudeImageBlocks 的 7.5MiB（而非内核 Read 工具的 5MiB）：
	// 上游是网关后面的任意 Messages 兼容模型，真实限制本地无法验证。这里选宽
	// 闸的理由是失败形态——超限会被上游明确拒绝（ai.error 可见），而本地先挡
	// 会把它降级成「路径清单」，模型还得自己 Read，而 Read 同样有 5MiB 闸，
	// 等于彻底看不到。宁可让上游判，也不要静默丢掉。
	//
	// 编码失败不能把整轮 run 拖死：附件是辅助信息，模型至少该拿到文字。降级
	// 成清单里的一行路径 + 一句说明，并把原因写进日志（用户看不到这个错误，
	// 只有日志能还原）。
	imageBlocks, err := buildClaudeImageBlocks(run.attachments)
	if err != nil {
		logger.Warn(fmt.Sprintf("agentcore: 图片附件编码失败，本轮退化为文本清单 session=%s: %v", run.sessionID, err))
		imageBlocks = nil
	}

	// 文本类附件（含内联正文）照旧走清单。
	//
	// 图片是否进清单取决于上一步编码成不成功：成功时 includeImages=false，
	// 图片已由 image 块承载，清单里再列一遍路径只会诱导模型去 read 它；失败
	// 时 includeImages=true，让模型至少知道「有一张图，在这个路径」，还能自己
	// 用 Read 工具去取（Read 已支持图片），而不是彻底看不见。
	includeImages := len(imageBlocks) == 0
	if suffix := agentAIAttachmentPromptSuffix(run.attachments, includeImages); suffix != "" {
		if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
			texts[n-1] += suffix
			msgs[n-1] = agentcore.TextMessage("user", texts[n-1])
		} else {
			msgs = append(msgs, agentcore.TextMessage("user", strings.TrimSpace(suffix)))
		}
	}

	if len(imageBlocks) > 0 {
		msgs = attachAgentCoreImageBlocks(msgs, imageBlocks)
	}
	return msgs, sessionContext
}

// agentCoreMessagesFromHistory 把宿主历史条目转成内核消息。
//
// texts 与 msgs 同下标，保存纯文本原文——Message.Content 是 interface{}，
// 追加附件清单时没法从它里面把字符串取回来。
//
// 手动 /compact 也走这条转换：压缩的必须是「如果现在开一轮 run 会发给上游的
// 东西」，两处各写一份迟早漂移。
func agentCoreMessagesFromHistory(history []agentAIMessage) ([]agentcore.Message, []string) {
	msgs := make([]agentcore.Message, 0, len(history))
	texts := make([]string, 0, len(history))
	for _, item := range history {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role != "user" && role != "assistant" {
			// 内核只认 user/assistant；其它角色（system 之类）由 System 字段
			// 承载（见 agentCoreSystemContextFromHistory）。
			continue
		}
		// 剥掉回灌进来的手机契约块：它只该在注入帧里出现一次，跟着历史进
		// messages 会变成模型自己给自己重复下指令。
		content := stripAgentAIPhoneContract(item.Content)
		if strings.TrimSpace(content) == "" {
			continue
		}
		msgs = append(msgs, agentcore.TextMessage(role, content))
		texts = append(texts, content)
	}
	return msgs, texts
}

// agentCoreSystemContextFromHistory 收集 system 角色的历史内容，交给内核的
// Config.System 承载；历史里没有 system 条目时回落 fallback（会话的
// initialContext 字段）。
//
// 内核的 messages 只收 user/assistant（上游会拒绝 system 角色），但 system
// 角色的历史不是噪音：ai.session.create / ai.run.start 的 initial_context
// （fork 快照、goal 重规划上下文）就以 role=system 存在历史首部。CLI 通道
// 靠 buildAgentAIPrompt 把它渲染成 "System: ..." 留在对话里，内核通道此前
// 直接丢弃 —— 手机新建会话的初始上下文在 AgentCore 上等于不存在。
//
// fallback 是纵深防御的第二层：session.initialContext 与历史首部的 system
// 条目本就同源（create/lazy-create 两处同时写入），历史条目丢失（尾部裁剪、
// 旧版本压缩落库冲掉）时它仍能兜住；两者都在时以历史为准，不重复拼接。
func agentCoreSystemContextFromHistory(history []agentAIMessage, fallback string) string {
	var parts []string
	for _, item := range history {
		if strings.ToLower(strings.TrimSpace(item.Role)) != "system" {
			continue
		}
		content := stripAgentAIPhoneContract(item.Content)
		if strings.TrimSpace(content) == "" {
			continue
		}
		parts = append(parts, content)
	}
	if ctx := strings.Join(parts, "\n\n"); strings.TrimSpace(ctx) != "" {
		return ctx
	}
	return strings.TrimSpace(fallback)
}

// agentCoreHistoryFromMessages 是 agentCoreMessagesFromHistory 的逆变换：
// 把压缩后的内核消息落回宿主历史。
//
// 只保留有文字的块：tool_use / tool_result 离开 run 之后没有配对对象，
// 落进历史会在下一轮被当成孤立的 user/assistant 文本，反而污染上下文。
// 摘要消息是普通字符串，会原样保留。
func agentCoreHistoryFromMessages(messages []agentcore.Message) []agentAIMessage {
	now := time.Now().UTC()
	out := make([]agentAIMessage, 0, len(messages))
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		text := strings.TrimSpace(agentCoreMessagePlainText(msg.Content))
		if text == "" {
			continue
		}
		out = append(out, agentAIMessage{Role: role, Content: text, CreatedAt: now})
	}
	return out
}

// agentCoreMessagePlainText 抽取一条消息里进历史用的纯文本。
func agentCoreMessagePlainText(content interface{}) string {
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		return c
	case []agentcore.ContentBlock:
		parts := make([]string, 0, len(c))
		for _, block := range c {
			if block.Type != agentcore.BlockTypeText {
				continue
			}
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		raw, err := json.Marshal(content)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

// replaceAgentCoreHistory 用压缩后的消息替换会话 history（自动压缩落库）。
//
// runSeq 守卫与 appendAssistantHistory 一致：run 进行中用户又发了新消息时
// session 的 runSeq 已经前进，此时不能拿本 run 的快照覆盖——新消息会丢。
// 跳过即可，下一轮 run 会用自己的快照重新压缩。
func (m *agentAIManager) replaceAgentCoreHistory(sessionID string, runSeq int, messages []agentcore.Message) {
	history := agentCoreHistoryFromMessages(messages)
	if len(history) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || session.runSeq != runSeq {
		return
	}
	// 压缩产物只含 user/assistant（内核 messages 不收其它角色），若原样落库，
	// 旧历史里的 system 条目（历史首部的 initial_context：fork 快照 / goal
	// 上下文）会被永久抹掉——而摘要请求的 messages 同样不含 system（它由内核
	// Config.System 承载，从历史派生），摘要补不回来。落库前把旧历史里的
	// 非 user/assistant 条目按原相对顺序拼回新历史头部。
	if preserved := agentCorePreservedSystemHistory(session.history); len(preserved) > 0 {
		history = append(preserved, history...)
	}
	session.history = trimAgentAIHistoryForSession(session, history)
	session.lastActiveAt = time.Now().UTC()
}

// agentCorePreservedSystemHistory 摘出历史里所有非 user/assistant 条目
// （initial_context 等 system 条目），保持原相对顺序。返回独立切片，不与
// 入参共享底层数组——调用方要在 m.mu 内把它拼进新历史，别名会写坏旧历史。
func agentCorePreservedSystemHistory(history []agentAIMessage) []agentAIMessage {
	out := make([]agentAIMessage, 0, len(history))
	for _, item := range history {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role == "user" || role == "assistant" || role == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

// handleAgentCoreCompactCommand 对内核对会话历史做一次真实压缩（/compact）。
//
// 与 codex 的原生线程压缩同构：命令不落成模型回合，而是宿主侧直接调内核的
// Compact()，用一次摘要请求把较早的历史折叠掉，然后把压缩后的历史写回会话。
// 之后再开 run 时 agentCoreMessages 送进内核的就是压缩后的上下文。
func (m *agentAIManager) handleAgentCoreCompactCommand(session *agentAISession, messageID string, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	m.mu.Lock()
	runSeq := session.runSeq
	projectPath := session.projectPath
	mode := session.mode
	model := session.model
	contextWindow := session.contextWindow
	history := append([]agentAIMessage(nil), session.history...)
	m.mu.Unlock()

	assistantID := agentAssistantMessageID(messageID)
	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   session.id,
		"message_id":   assistantID,
		"provider":     "agentcore",
		"mode":         mode,
		"project_path": projectPath,
		"state":        "running",
	})
	reply := func(line string) {
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventAIDelta,
			"session_id": session.id,
			"message_id": assistantID,
			"channel":    "stdout",
			"delta":      line + "\n",
		})
	}
	done := func() {
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventAIDone,
			"session_id": session.id,
			"message_id": assistantID,
		})
	}

	messages, _ := agentCoreMessagesFromHistory(history)
	if len(messages) == 0 {
		reply("当前会话还没有可压缩的上下文。先对话几轮，再发送 /compact。")
		done()
		return
	}

	// 摘要是一次普通 Messages 调用，大窗口下要读几十万 token，给足时间；
	// 但也必须有上限，否则上游挂住会让手机一直停在「压缩中」。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := agentCoreEnsureUpstream(ctx); err != nil {
		_ = writeJSON(agentAIErrorPayload(session.id, messageID, err))
		return
	}

	// /compact 也要模型（摘要得由模型写）。取模与 run 路径同源（agentCoreModel）：
	// 会话显式模型优先，未带则回落本机 env/settings，最终兜底 glm-5.3——与对话
	// 回合完全一致，绝不偷偷换档。agentCoreModel 不返回空值；Compact() 对空
	// Model 自有兜底校验，这里不再重复死分支。
	compactModel := agentCoreModel(agentAIRun{model: model})

	cfg := &agentcore.Config{
		Client:        agentcore.NewClient(agentCoreBaseURL()).WithAPIKey(agentCoreAuthToken()),
		Model:         compactModel,
		ContextWindow: contextWindow,
	}
	compacted, info, err := agentcore.Compact(ctx, cfg, messages, "manual")
	if err != nil {
		if errors.Is(err, agentcore.ErrNothingToCompact) {
			reply("上下文还比较短，无需压缩。")
			done()
			return
		}
		logger.Warn(fmt.Sprintf("agentcore: /compact 失败 session=%s: %v", session.id, err))
		_ = writeJSON(agentAIErrorPayload(session.id, messageID, err))
		return
	}
	m.replaceAgentCoreHistory(session.id, runSeq, compacted)
	reply(fmt.Sprintf("✓ 上下文已压缩：约 %s → %s tokens（%d 条早期消息已折叠为摘要）。",
		agentAITokenCountLabel(info.PreTokens), agentAITokenCountLabel(info.PostTokens), info.Removed))
	done()
}

// agentAITokenCountLabel 把 token 数渲染成 12.3k / 1.0m 这样的短标签。
func agentAITokenCountLabel(tokens int) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	if tokens < 1_000_000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(tokens)/1000), ".0") + "k"
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", float64(tokens)/1_000_000), "0"), ".") + "m"
}

// attachAgentCoreImageBlocks 把 image 块挂到最后一条 user 消息上。
//
// 为什么必须是最后一条 user：图片描述的是「这一轮用户说了什么」。挂到别的
// 位置，模型会把图当成历史上下文的一部分，答非所问。
//
// 最后一条是 assistant（历史以 assistant 收尾，用户还没说话）时不能硬塞进
// assistant 消息——Anthropic 不接受 assistant 消息里的 image 块。此时补一条
// 只含图片的 user 消息。
func attachAgentCoreImageBlocks(msgs []agentcore.Message, blocks []map[string]interface{}) []agentcore.Message {
	converted := make([]agentcore.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		source, _ := block["source"].(map[string]interface{})
		if source == nil {
			continue
		}
		src := &agentcore.ImageSource{}
		src.Type, _ = source["type"].(string)
		src.MediaType, _ = source["media_type"].(string)
		src.Data, _ = source["data"].(string)
		src.URL, _ = source["url"].(string)
		if src.Data == "" && src.URL == "" {
			continue
		}
		converted = append(converted, agentcore.ContentBlock{Type: agentcore.BlockTypeImage, Source: src})
	}
	if len(converted) == 0 {
		return msgs
	}

	if n := len(msgs); n > 0 && msgs[n-1].Role == "user" {
		// 把纯文本 user 消息升级成块消息：文字块在前，图片在后（与 Claude 的
		// 排列一致，模型先读指令再看图）。
		blocksOut := make([]agentcore.ContentBlock, 0, len(converted)+1)
		if text, ok := msgs[n-1].Content.(string); ok && strings.TrimSpace(text) != "" {
			blocksOut = append(blocksOut, agentcore.ContentBlock{Type: agentcore.BlockTypeText, Text: text})
		}
		blocksOut = append(blocksOut, converted...)
		msgs[n-1] = agentcore.BlocksMessage("user", blocksOut)
		return msgs
	}
	return append(msgs, agentcore.BlocksMessage("user", converted))
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
			// 纵深保险：readOnly run 的 MCP 注入已在 runAgentCore 接入点剥离
			//（raws=nil），这里再挡一道 mcp__ 前缀，防将来注入条件与围栏
			// 条件漂移时露出执行面。
			if strings.HasPrefix(lower, "mcp__") {
				return false, "只读模式不允许调用 MCP 工具"
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

// newAgentCoreEmit 返回一个可被 ctx 解除的 Emit 函数。
//
// 内核的每一帧都经 io.Pipe 交给流式管线；pipeWriter.Write 会一直阻塞到读端
// 消费（读端还有限流器与网络写出）。若读端卡住，写侧就永久阻塞，agentcore.Run
// 再也返回不了——看门狗的 cancel 也就形同虚设（2026-10-27 事故的候选卡点）。
//
// 这里用一条守护 goroutine 把 ctx.Done 翻译成 pipe 关闭，让阻塞中的 Write
// 立刻返回 io.ErrClosedPipe（读端侧收到的才是 ctx 的取消错误），Run 得以退出。
// stop 必须 defer 调用，否则 ctx 永不取消的路径（正常完成）会泄漏这条 goroutine。
func newAgentCoreEmit(ctx context.Context, writer *io.PipeWriter) (emit func([]byte) error, stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = writer.CloseWithError(ctx.Err())
		case <-done:
		}
	}()
	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }
	emit = func(frame []byte) error {
		_, err := writer.Write(frame)
		return err
	}
	return emit, stop
}

// bindAgentCoreNativeSession 把内核发来的 native session id 落成绑定并发
// ai.session.bound。返回的 bool 为 true 表示本回合已被判死或被新回合顶替，
// 这次绑定是僵尸迟到的产物，必须整帧丢弃（既不落库也不发帧）。
//
// 围栏必须在写入之前：一个忽略取消、卡住很久的僵尸完全可能在强拆撤销之后才
// 吐出 system/init，把 m.bindings 与 session.resumeSessionID 重新写回去——
// runStart 下一回合正是从 confirmed 绑定取 resumeID，污染照样成立。
//
// 单次语义由调用方的 bindingOnce 保证：一个回合只确认一次。
func (m *agentAIManager) bindAgentCoreNativeSession(run agentAIRun, writeJSON agentTerminalWriter, nativeSessionID string) (agentAIBindingRecord, bool, error) {
	if !m.agentAIRunSideEffectsAllowed(run.sessionID, run.runSeq, run.guard) {
		logger.Warn(fmt.Sprintf(
			"[ai.run.zombie_bind] session=%s runSeq=%d run=%s native=%s reason=stale_side_effects",
			run.sessionID, run.runSeq, run.runID, strings.TrimSpace(nativeSessionID),
		))
		return agentAIBindingRecord{}, true, nil
	}
	record, err := m.confirmBinding(run.sessionID, run.provider, nativeSessionID, run.bindingVersion)
	if err != nil {
		return agentAIBindingRecord{}, false, err
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
	return record, false, nil
}

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
	defer m.clearRunning(run.sessionID, run.runSeq, writeJSON, run.guard)
	// LIFO：本行后注册，先于 clearRunning 执行——guard 必须在会话复位前把自己
	// 标记为「已正常返回」，否则宽限计时器会误判成僵尸。
	//
	// 两处都必须带 guard：/clear 会把 runSeq 归零、下一回合复用同一个值，僵尸
	// 迟返回时若只比 runSeq，会把新回合的 cancel/activity/guard/done 一并清掉。
	defer m.markRunGuardDone(run.sessionID, run.runSeq, run.guard)

	// 调用记录落盘（AliangCode 留痕，spec §6）：入口写头行 + 首条 user 行，
	// 成功出口写 assistant 行，所有出口经 defer 落 end 行。写失败只 Warn。
	journal := m.coreJournal
	endStatus, endErrMsg := "error", ""
	defer func() { journal.appendEnd(run.sessionID, endStatus, endErrMsg) }()
	now := time.Now().UTC().Format(time.RFC3339)
	journal.ensureSessionHeader(run.sessionID, run.projectPath, agentCoreModel(run), now)
	if lastUser, ok := m.lastUserHistoryMessage(run.sessionID); ok {
		ts := now
		if !lastUser.CreatedAt.IsZero() {
			ts = lastUser.CreatedAt.UTC().Format(time.RFC3339)
		}
		journal.appendUser(run.sessionID, run.messageID, lastUser.Content, ts)
	}

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
		endErrMsg = err.Error()
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return
	}

	// 模型只认手机下发的值。没有就拒绝开跑，不做任何本机兜底——静默换模型
	// 会让账单、上下文窗口、视觉能力全部与用户的选择错配，而且事后无从追查。
	// 参数补全的责任在下发方（手机 / PhoneServer），不在这里。
	model := agentCoreModel(run)
	if model == "" {
		err := fmt.Errorf("未指定模型：本次运行没有收到模型名（收到的是 %q）。请在手机的会话设置里选择模型后重新发送", strings.TrimSpace(run.model))
		endErrMsg = err.Error()
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return
	}

	messages, sessionContext := m.agentCoreMessages(run)
	if len(messages) == 0 {
		endErrMsg = "会话没有可发送的消息"
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
	// 围栏与落库整段收在 bindAgentCoreNativeSession 里，便于单独覆盖僵尸形态。
	var bindingOnce sync.Once
	var bindingErr error
	onNativeSessionID := func(nativeSessionID string) {
		bindingOnce.Do(func() {
			if _, _, err := m.bindAgentCoreNativeSession(run, writeJSON, nativeSessionID); err != nil {
				bindingErr = err
				_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
				if run.cancel != nil {
					run.cancel()
				}
			}
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
	// activity 必须在 attachCoreSteer 之前赋值：attach 之后其他 goroutine 就能
	// 通过 coreSteerFor 拿到 control 并使用它，晚赋值会有「已挂载但 activity
	// 还是 nil」的窗口（nil 安全但丢活性信号）。
	coreSteer.activity = run.activity
	coreSteer.onStalled = func(reason string) {
		if guard := m.runGuardForActivity(run.activity); guard != nil {
			guard.kill(reason)
			return
		}
		if run.cancel != nil {
			run.activity.setKillReason(reason)
			run.cancel()
		}
	}
	m.attachCoreSteer(run.sessionID, run.runSeq, coreSteer)

	// MCP 工具注入（spec §3）：采集→建连→包桥→注册。全部失败也只是空桥，
	// run 照常（纯内置 8 工具）。内核 Registry 后注册者胜，桥与内置工具
	// 名空间不重叠（mcp__ 前缀），互不遮蔽。建连是逐 server 串行同步 dial、
	// 10s 超时/个、条目上限 24——最坏给 run 起跑加约 4 分钟。关停已解耦，
	// 建连未并行是 v1 取舍（spec §5 未承诺并行）。
	raws := agentCoreMCPRawServersIn(agentHome())
	if run.readOnly {
		// readOnly run 不注入任何 MCP 工具，对齐 claude 路径同一判据
		// （agent_ai.go runCLIPass 的 readOnly 分支，withAgentReadOnlyPolicy
		// 的 --strict-mcp-config {}）。判据唯一是 run.readOnly：非 fork 的
		// goal run（readOnly=false）保留 MCP；fork 会话的执行 run 也因
		// readOnly=true 被剥离（其 goalIdentity 非 nil，但 goalIdentity 不
		// 参与判定）。
		// 只 gate raws 不 gate 整个调用：closeMCP 恒非 nil，下面 defer 的
		// go closeMCP() 不会因 nil func panic。
		raws = nil
	}
	mcpTools, closeMCP := m.agentCoreMCPServerToolDefs(ctx, raws, func(tool, summary string, isErr bool) {
		m.coreJournal.appendTool(run.sessionID, tool, summary, isErr)
	})
	registry := agentcore.DefaultRegistry()
	for _, tool := range mcpTools {
		registry.Register(tool)
	}
	defer func() {
		// 慢关停与 run 收尾解耦（Task 2 评审挂账决策）：stdio 关停最坏
		// 10-15s/个（关 stdin→5s 等退出→SIGTERM→再 5s→SIGKILL），串行会
		// 拖住 clearRunning 与下一轮 run。SDK ClientSession.Close 幂等、
		// 进程退出兜底，堆积面受人类 run 节奏约束——fire-and-forget 是
		// v1 的合理取舍。安全前提还有一条：agentcore.Run 同步返回后已无
		// Execute 在飞、桥 per-run 不复用，关停不会撕正在调用的 session。
		go closeMCP()
	}()

	// client/env/approve/logf 构造一次、主 run cfg 与子 agent 桥两处共用：
	// 子 run 必须与主 run 用同一份出网凭据、同一道围栏/shell 策略、同一个
	// 审批闭包（审批贯穿，spec §7）——构造两份就会出两套语义。
	// 带上用户自己的 API key：网关靠它走 user-api-key 通道，不做分组门禁。
	// 不带的话，不在 anthropic-duration 组里的账号会被 403 挡下（见
	// agentCoreAuthToken 注释）。拿不到 key 时 WithAPIKey("") 等价于不带，
	// 行为退回「由代理按分组兜底」。
	kernelClient := agentcore.NewClient(agentCoreBaseURL()).WithAPIKey(agentCoreAuthToken())
	kernelEnv := &agentcore.ToolEnv{
		ProjectPath: run.projectPath,
		Env:         agentCoreToolEnv(),
		ScopeCheck:  agentCoreScopeCheck(run),
		ShellPolicy: &agentCoreShellPolicy{run: run},
	}
	kernelApprove := m.agentCoreApprove(run, writeJSON)
	// 内核不 import 日志包，这里把宿主 logger 包一层递进去（签名不同：
	// logger.Warn 是 variadic interface{}）。目前只承载截断这类无事件帧的
	// 内部决策。
	kernelLogf := func(format string, args ...interface{}) {
		logger.Warn(fmt.Sprintf(format, args...))
	}

	// 子 agent 注入（spec：docs/superpowers/specs/2026-10-27-agentcore-subagents-design.md）：
	// readOnly run 不注入（mirror MCP 只读决策——上方 raws=nil 同一判据）；
	// 定义集为空不注入。桥递归子 run（depth=1），MCP 桥/审批/journal/围栏
	// 全部复用主 run 的这一份。
	if !run.readOnly {
		if subTools := m.agentCoreSubagentTools(run, kernelClient, kernelEnv, kernelApprove, kernelLogf, mcpTools); len(subTools) > 0 {
			for _, tool := range subTools {
				registry.Register(tool)
			}
		}
	}

	emitFrame, stopEmit := newAgentCoreEmit(ctx, pipeWriter)
	defer stopEmit()

	cfg := &agentcore.Config{
		Client:    kernelClient,
		Model:     model,
		System:    agentCoreSystemPromptWithContext(run.projectPath, sessionContext),
		Tools:     registry,
		SessionID: kernelSessionID,
		// 会话的 effort 档位（手机选档 → server 透传 → 这里进请求体）。
		// 空串/无法识别的档位在内核侧被丢弃，不下发。
		Effort: run.effort,
		// 手机选定的上下文窗口驱动内核的自动压缩阈值；0 时内核用 200k 兜底。
		ContextWindow: run.contextWindow,
		Env:           kernelEnv,
		Approve:       kernelApprove,
		Logf:          kernelLogf,
		// 经 newAgentCoreEmit 包装：ctx 取消时关写端解除阻塞，僵尸写侧不再
		// 挂死消费侧（AI run 韧性 L2）。
		Emit:  emitFrame,
		Hooks: agentCoreLoopHooks(m, run),
	}

	result, runErr := agentcore.Run(ctx, cfg, messages)

	// 先关写入端让读端自然收尾，再等它把最后一帧消费完。
	_ = pipeWriter.Close()
	wg.Wait()

	if bindingErr != nil {
		endErrMsg = bindingErr.Error()
		return
	}

	if ctx.Err() != nil {
		// 被停止：内核可能已发过 interrupted 终止帧；若 pipe 已随 cancel 关闭，
		// 该帧会被丢弃（内核忽略这处写错误）。这里统一收敛状态。
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
		endStatus = "stopped"
		if errMsg != "" {
			endErrMsg = errMsg
		}
		return
	}

	// 内核的终止帧已经以 is_error=true 发过 → 流式管线已转成 ai.error，不再补。
	// runErr 非空但 IsError 为 false 的情形（理论上只有「内核内部报错但没走到
	// 终止帧」）才需要宿主兜底。
	if result != nil && result.IsError {
		// 真因不再吞掉：优先取内核结构化 ErrMessage，其次 runErr。写进 journal
		// 的 end 记录 + 日志，否则线上只剩笼统的「agentcore run failed」无法复盘
		// （2026-10-28 ai_3p18eg1aYQvE 事故）。
		reason := strings.TrimSpace(result.ErrMessage)
		if reason == "" && runErr != nil {
			reason = runErr.Error()
		}
		if reason == "" {
			reason = "agentcore run failed"
		}
		endErrMsg = reason
		logger.Warn(fmt.Sprintf("[agentcore] run failed session=%s msg=%s err=%s",
			run.sessionID, run.messageID, reason))
		finalizeRunSubagents(run, writeJSON)
		return
	}
	if runErr != nil {
		endErrMsg = runErr.Error()
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

	if m.agentAIRunSideEffectsAllowed(run.sessionID, run.runSeq, run.guard) {
		m.appendAssistantHistory(run.sessionID, run.runSeq, run.messageID, assistantOutput)
		journal.appendAssistant(run.sessionID, agentAssistantMessageID(run.messageID), assistantOutput, time.Now().UTC().Format(time.RFC3339))
		endStatus = "success"
		m.setAgentAIResumeSessionIDIfEmpty(run.sessionID, run.runSeq, capturedResumeSessionID)
		// 刻意跳过 ccPeerSessionJSONL / appendLineageMarker：那是 Claude CLI 的
		// transcript 谱系修补，内核没有对应的 JSONL 文件。
		//
		// 方案请求必须一起关在围栏内：emitOptionRequest 只看 runSeq，而 /clear
		// 归零后新回合复用同一个值。僵尸迟返回若照样写 session.pendingOption，
		// 新回合正在等用户选择的那张卡会被顶掉，用户点下去 option_id 对不上，
		// 选择被静默丢弃。
		if blocks := extractAgentAIOptionBlocks(assistantOutput); len(blocks) > 0 {
			m.emitOptionRequest(run, writeJSON, blocks)
		}
	} else {
		// 僵尸迟返回：强拆之后 runSeq 未变但已打 finalizedRunSeq 标记（或该回合
		// 早已被新回合顶替），这些非帧副作用会污染 Retry 新回合的历史/恢复点，
		// 必须丢弃（spec §2.4 / §3.2.2）。
		// endStatus 保持默认 "error"：deferred journal.appendEnd 必须体现
		// 「这一轮没有落库的成功结果」，否则 journal 会出现 end(success) 却查无
		// assistant 行的自相矛盾记录。
		//
		// 恢复点与绑定的撤销**不在这里**：判死路径必先 kill() → cancel()，僵尸
		// 迟返回一定先在上面 ctx.Err() != nil 的 stopped 早退处 return，走不到本段
		// （本段只在取消被忽略、run 正常返回等边缘形态才可达）。撤销已经由
		// finalizeStalledGuard 在身份校验的同一临界区内做掉，这里只兜历史/日志
		// 这类非帧写入。
		guardMounted := run.guard != nil
		m.mu.Lock()
		session := m.sessions[run.sessionID]
		runSeqSame := session != nil && session.runSeq == run.runSeq
		finalized := session != nil && session.finalizedRunSeq == run.runSeq
		m.mu.Unlock()
		logger.Warn(fmt.Sprintf("[ai.run.zombie_discard] session=%s runSeq=%d run=%s guard_mounted=%t runseq_same=%t finalized=%t reason=stale_side_effects",
			run.sessionID, run.runSeq, run.runID, guardMounted, runSeqSame, finalized))
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
// steering 与收尾追补共用同一个 drain：前者在**每次** LLM 调用前取走运行中
// 插话（这正是插话生效的时机）；后者兜住「插话在最后一轮流式尾声才排进队列、
// 已经没有下一次调用」的情形——不追补的话插话会悬在队列里没有回复，直到用户
// 下一条消息才被看到（2026-10-26 实测）。两个钩子先到先得，drain 排空队列，
// 不会重复投递。PrepareRequest / FinishTurn 留空——前者的诉求（改 system /
// max_tokens）已在 Config 里静态给定，后者没有消费方。
func agentCoreLoopHooks(m *agentAIManager, run agentAIRun) agentcore.LoopHooks {
	// 逐回合 transcript 落 journal 的回合序号（run 内 1 起）。
	turnN := 0
	return agentcore.LoopHooks{
		GetSteeringMessages: func() []agentcore.Message {
			return m.drainAgentCoreSteering(run)
		},
		GetFollowUpMessages: func() []agentcore.Message {
			return m.drainAgentCoreSteering(run)
		},
		// 每回合把 assistant 完整文本 + thinking + 工具名落 journal（kind=turn），
		// 补齐 run 末单条 assistant 丢失的中间叙述与 thinking。围栏与收尾
		// assistant 行同款：僵尸回合（guard 被换/runSeq 顶替）的 transcript 同样
		// 不得落盘，否则 journal 会出现 turn 行却查无 assistant 行的自相矛盾。
		OnTurnEnd: func(tr agentcore.TurnResult) {
			if !m.agentAIRunSideEffectsAllowed(run.sessionID, run.runSeq, run.guard) {
				return
			}
			turnN++
			tools := make([]string, 0, len(tr.ToolCalls))
			for _, tc := range tr.ToolCalls {
				if tc.Name != "" {
					tools = append(tools, tc.Name)
				}
			}
			m.coreJournal.appendAssistantTurn(run.sessionID, turnN, tr.Text, tr.Thinking, tr.StopReason, tools)
		},
		// 自动压缩替换了历史 → 同步落回会话 history。不落的话 next run 又从
		// 旧历史重建，压缩白做（而且每轮 run 都会重新烧一次摘要请求）。
		OnCompact: func(messages []agentcore.Message, info agentcore.CompactInfo) {
			m.replaceAgentCoreHistory(run.sessionID, run.runSeq, messages)
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
	// activity 用于把「插话被取走」计入 run 活性（spec §3.3.1）：内核 drain 发生
	// 在下一次 LLM 调用前，那一瞬间说明回合确实活着。
	activity *agentAIActivity
	// onStalled 在活性窗到期且判定回合无响应时回调（生产接线 = guardian kill）。
	// nil = 完全不起活性窗（既不发失败 ack 也不判死），仅供测试使用。
	onStalled func(reason string)
	// pending 记每条排队插话的活性窗计时器，drain/close 时停掉。
	pending map[string]*time.Timer
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
// ImageBlocks 与 Content 一起入队，由 drain 转成「text 块 + image 块」。
// 注意 ImageBlocks 在 ai.steer 入口就已经是 base64（见 agentAISteerMessage
// 的注释），排队期间不依赖任何临时文件。
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
	c.armLivenessLocked(msg.MessageID)
	return "queued", nil
}

// armLivenessLocked 给一条刚入队的插话起活性窗。调用方必须持有 c.mu。
//
// activity 为 nil 时直接不起窗（fail-safe）：没有活性就分不清「死」和「在等
// 审批」，宁可不判死也不误杀合法等待。生产路径在 attachCoreSteer 之前一定
// 赋好 activity，所以这只在接线错误时退化。
//
// onStalled 为 nil 时同样不起窗（同义 fail-safe）：没有收尾回调就只剩一条
// 失败 ack，判死也无从收敛，不如不判。仅供测试使用。
func (c *agentAICoreSteerControl) armLivenessLocked(messageID string) {
	if steerLivenessWindow <= 0 || c.onStalled == nil || c.activity == nil {
		return
	}
	if c.pending == nil {
		c.pending = make(map[string]*time.Timer)
	}
	if timer := c.pending[messageID]; timer != nil {
		timer.Stop()
	}
	c.pending[messageID] = time.AfterFunc(steerLivenessWindow, func() { c.expire(messageID) })
}

// expire 在活性窗到期时检查插话是否仍未被取走。
//
// 仍在队列且回合处于非等待态、且已静默满一个完整窗口 → 判死：给手机一条失败
// ack（code=turn_stalled），再触发 guardian 收尾。等待态（工具/子代理/审批）
// 与仍在出字的回合都是合法静默，插话继续排队，由各自天花板 + stale 扫描器
// 收敛，这里不误报（spec §3.3.4）。
func (c *agentAICoreSteerControl) expire(messageID string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	// 合法静默有两种，都不能判死：
	//   1) 等待态（审批/工具/子代理）——各自有天花板兜底；
	//   2) 回合仍在出字——delta 会 bump lastActivityAt，距今还没满一个窗口。
	// 只有「非等待态 且 已静默 ≥ 一个完整窗口」才判死（与 stale 扫描器同一口径）。
	// 到期即作废本窗、不动队列：不重置，避免静默等待被无限续期。
	if c.activity != nil && (c.activity.idlePaused() || c.activity.idleFor() < steerLivenessWindow) {
		delete(c.pending, messageID)
		c.mu.Unlock()
		return
	}
	// 判死即消费：在持锁段把它从队列里摘掉，否则解锁后 drain 可能同一瞬间
	// 取走它并发一条 applied ack，造成同一 messageID 先 applied 后 error 双帧。
	stillQueued := false
	kept := c.queue[:0]
	for _, item := range c.queue {
		if item.MessageID == messageID {
			stillQueued = true
			continue
		}
		kept = append(kept, item)
	}
	c.queue = kept
	delete(c.pending, messageID)
	write := c.write
	sessionID := c.sessionID
	runSeq := c.runSeq
	onStalled := c.onStalled
	var idle time.Duration
	if c.activity != nil {
		idle = c.activity.idleFor()
	}
	c.mu.Unlock()

	if !stillQueued {
		return
	}
	logger.Warn(fmt.Sprintf("[ai.steer.stalled] session=%s runSeq=%d message_id=%s idle=%s stall_cb_wired=%t",
		sessionID, runSeq, messageID, idle, onStalled != nil))
	emitAgentAISteerAck(write, sessionID, messageID, "error", "回合无响应已判死，请重试", "turn_stalled")
	if onStalled != nil {
		onStalled("steer_stalled")
	}
}

// stopLivenessLocked 停掉若干条插话的活性窗。调用方必须持有 c.mu。
func (c *agentAICoreSteerControl) stopLivenessLocked(ids []string) {
	for _, id := range ids {
		if timer := c.pending[id]; timer != nil {
			timer.Stop()
			delete(c.pending, id)
		}
	}
}

// drain 取走全部排队插话，转成内核消息，并逐条发 applied ack。
//
// 由内核在每次 LLM 调用前调用（run goroutine）。取走即视为「已进入上下文」：
// 返回的消息会被 loop.go 立刻 append 进 history，紧接着就是请求组装。
//
// 带图插话发成块消息（text 块 + image 块），纯文字仍发纯字符串——单块文本
// 用字符串能让上游少一层解析，也更贴近真实 claude-code 的请求形状。
func (c *agentAICoreSteerControl) drain() []agentcore.Message {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	queued := c.queue
	c.queue = nil
	write := c.write
	sessionID := c.sessionID
	ids := make([]string, 0, len(queued))
	for _, item := range queued {
		ids = append(ids, item.MessageID)
	}
	c.stopLivenessLocked(ids)
	c.mu.Unlock()

	out := make([]agentcore.Message, 0, len(queued))
	for _, item := range queued {
		out = append(out, agentCoreSteerMessage(item))
		// drain 发生在下一次 LLM 调用前，是回合活着的直接证据；bump 是原子的，
		// 不需要持锁（此处已在锁外）。
		if c.activity != nil {
			c.activity.bump()
		}
		emitAgentAISteerAck(write, sessionID, item.MessageID, "applied", "", "")
	}
	return out
}

// agentCoreSteerMessage 把一条排队插话转成内核消息。
//
// item.ImageBlocks 是 ai.steer 入口就编好的 base64（不是路径）：插话可能排队
// 很久才被 drain，路径指向的临时文件早被清理了。这里只做形状转换。
func agentCoreSteerMessage(item agentAISteerMessage) agentcore.Message {
	images := make([]agentcore.ContentBlock, 0, len(item.ImageBlocks))
	for _, block := range item.ImageBlocks {
		source, _ := block["source"].(map[string]interface{})
		if source == nil {
			continue
		}
		src := &agentcore.ImageSource{}
		src.Type, _ = source["type"].(string)
		src.MediaType, _ = source["media_type"].(string)
		src.Data, _ = source["data"].(string)
		src.URL, _ = source["url"].(string)
		if src.Data == "" && src.URL == "" {
			continue
		}
		images = append(images, agentcore.ContentBlock{Type: agentcore.BlockTypeImage, Source: src})
	}
	if len(images) == 0 {
		return agentcore.TextMessage("user", item.Content)
	}
	blocks := make([]agentcore.ContentBlock, 0, len(images)+1)
	if strings.TrimSpace(item.Content) != "" {
		blocks = append(blocks, agentcore.ContentBlock{Type: agentcore.BlockTypeText, Text: item.Content})
	}
	blocks = append(blocks, images...)
	return agentcore.BlocksMessage("user", blocks)
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
	// 关通道时把所有未到期的活性窗一并停掉：否则已 close 的通道上还会有
	// AfterFunc 在活性窗到期后醒来读 c.activity，造成无谓的判定与日志。
	for id, timer := range c.pending {
		timer.Stop()
		delete(c.pending, id)
	}
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

// boundaryApprovalReason 把边界守卫的规则 ID 翻译成审批卡上的人话原因。
// 三个内核（agentcore/claude CLI/codex）的审批卡共用：规则命中时边界原因
// 优先于各自上游自带的原因；未命中返回空串，卡片保持原样。
func boundaryApprovalReason(ruleID string) string {
	switch ruleID {
	case "outside-project-path":
		return "目标路径在项目目录之外，需要您确认是否放行"
	case "outside-project-bash":
		return "命令引用了项目目录之外的路径，需要您确认是否放行"
	}
	return ""
}

// agentCoreApprove 是内核的 Config.Approve。策略短路与 codex/claude 一致：
// auto_approve / auto_deny 本地直接判（不走云端往返），只有 require_approval
// 才真的问用户。
func (m *agentAIManager) agentCoreApprove(run agentAIRun, writeJSON agentTerminalWriter) func(context.Context, agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
	return func(ctx context.Context, call agentcore.ApprovalRequest) (agentcore.ApprovalResponse, error) {
		toolInput := marshalAgentAIRaw(call.Input)
		if svc := m.approvalService(); svc != nil {
			decision, matchedID := svc.evaluateApprovalDecision(call.ToolName, toolInput, run.projectPath)
			// MCP 默认门（spec §7）：没有显式设备规则命中（matchedID 为空 =
			// 落到策略 DefaultDecision）的 mcp__* 工具一律要求手机审批——
			// 显式规则（含放行）优先于本默认。已知例外：服务端若下发空 id
			// 规则（现无生产方），显式放行会被本门改判 require（fail-safe
			// 方向，宁可多问一次手机）。
			if matchedID == "" && strings.HasPrefix(call.ToolName, "mcp__") {
				logger.Info(fmt.Sprintf("approval-hook: MCP default gate require_approval tool=%s session=%s matched=none", call.ToolName, run.sessionID))
				decision = decisionRequireApproval
			}
			switch decision {
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
	// 边界守卫升级为审批后，卡片必须说清「为什么问您」——否则用户看到的
	// 只有一个 rule id，无从判断点同意意味着什么。
	reason := boundaryApprovalReason(run.matchedRuleID)

	return agentAIApprovalRequest{
		Provider:  run.provider,
		Kind:      kind,
		Title:     title,
		Reason:    reason,
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
