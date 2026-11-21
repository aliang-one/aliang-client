package services

// agent_listener_boot.go 监听器镜像 boot 接线(specs/005-event-listener §2/FR-3,
// plan Task 10,docs/superpowers/plans/2026-11-20-event-listener-1a.md):
// LoadListenerMirror = 全量拉取优先、本地文件回落。StartEventListenerWatchers
// 本体在 Task 9(agent_listener_filetail.go),boot 不重包装;本文件只负责把
// 「先镜像、后 watcher」的顺序契约落在函数注释 + TestListenerBootOrder 上。
//
// 进程归属(Task 10 评审 C1 定案):镜像与 watcher 归 **WS 持有进程 =
// user-agent runtime**(app/agentruntime/server.go StartLocalServer,与
// StartAgentRegistrationKeepalive 同点接线);cmd 侧 start.go/core.go 刻意
// 不接——其 EnsureRemoteConnection 触发是跨进程派发,分裂身份/共享 home
// 拓扑下双进程共写 listener-tails offset 文件会互相踩进度丢事件。
// exactly-once 由 cmd/boot_wiring_audit_test.go 的 TestListenerWiringAudit
// 对 server.go 拦(1b8c36b 教训制度化)。
//
// 全量拉取含同账号外设备行(spawn 闸在 syncListenerWatcherForMirror 按
// source_device_id==本机 过滤,boot 路径自动受保护);镜像保留全量供 UI 展示。
//
// 容错口径(FR-3):boot 绝不被云端不可达阻塞——拉取失败 Warn 降级、回落本地
// 镜像;LoadListenerMirror 无 error 返回(log-only),server.go 调用点保持一行。
//
// 缝纪律(同 listenerMirrorHome/listenerEventUplink):listenerCloudBaseURL /
// listenerCloudFetchAll 为包级 func var、init() 接线默认实现(防
// initialization cycle 的防御性定型,见 agent_listener_filetail.go 文件头注);
// 测试 swap 后还原即回默认。Task 11 的 REST 代理(agent_listener_api.go)
// 复用同两缝,勿另起炉灶。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// listenerCloudFetchTimeout boot 全量拉取单次预算:与 agentHTTPTimeout 同值,
// boot 路径有界——云端挂死 8s 后降级回落本地。boot 不重试(断联窗口的增量
// 由重连后 listener.upsert 下行推送 + 下次 boot 全量拉取兜底)。
const listenerCloudFetchTimeout = 8 * time.Second

// listenerCloudFetchBodyLimit 响应体读侧上限(4MiB):镜像行 ~1KB 量级,账号级
// 注册表远触不到上限;触限即截断 → 解码失败 → 回落本地,失败可见不静默。
const listenerCloudFetchBodyLimit = 4 << 20

var (
	// listenerCloudBaseURL 云端 API base 解析缝(默认
	// resolveListenerCloudBaseURL,测试/Task 11 复用)。
	listenerCloudBaseURL func() (string, error)
	// listenerCloudFetchAll 全量拉取缝(默认 fetchAllListenerMirrorsFromCloud)。
	listenerCloudFetchAll func() ([]ListenerMirror, error)
)

func init() {
	listenerCloudBaseURL = resolveListenerCloudBaseURL
	listenerCloudFetchAll = fetchAllListenerMirrorsFromCloud
}

// resolveListenerCloudBaseURL 解析云端 API base(http(s) 形态)。派生链与
// EnsureRemoteConnection 的 WS URL(currentAgentWebSocketURL,agent_remote_ws.go)
// 同源同闸:同一 config 源(currentAgentServerURL → cfg.AgentBaseURL,未配置即
// 错)、同一 scheme 白名单、同一 dev 重写(localhost:5174→:4000);差异仅两处
// ——scheme 归一方向相反(ws/wss→http/https),base path 保留(与既有
// /api/agent/* HTTP 调用方 approval-policy/mesh-roster 的 TrimRight(base)+path
// 拼接口径一致,支持带路径的反代 base)。
func resolveListenerCloudBaseURL() (string, error) {
	base := strings.TrimSpace(currentAgentServerURL())
	if base == "" {
		return "", errors.New("agent server is not configured")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "http", "https":
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	default:
		return "", fmt.Errorf("unsupported agent server scheme: %s", parsed.Scheme)
	}
	if (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1") && parsed.Port() == "5174" {
		parsed.Host = parsed.Hostname() + ":4000"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// fetchAllListenerMirrorsFromCloud GET /api/agent/listeners 全量拉取本 JWT
// 账号的监听器注册表(server 设备路由,plan Task 3)。auth 材料与远端 WS 握手
// 同源:remoteConnectionSnapshot()(Authorization + X-Aliang-Device-ID,
// agent_remote_ws.go);未注册/未启用/无凭据 → 错误(调用方回落本地,不重试)。
// 响应三形态容忍(Task 11 跨仓漂移修正):设备面实发 {listeners:[...]}
// (PhoneServer modules/routes/listeners.ts 真实 wire 形态);信封
// {code,msg,data}/裸数组为前向兼容(unwrapAgentServerData 既有口径)。
// 行内未知键容忍(与 listener.upsert 帧同口径,server 加字段不要求 agent
// 同步升级)。未识别形态(listeners 键缺席/裸 null)一律报错 → 调用方回落
// 本地旧镜像——未识别形状 ≠ 空账号,不当空表覆写镜像;合法空表形态为
// {listeners:[]} 或裸 []。
func fetchAllListenerMirrorsFromCloud() ([]ListenerMirror, error) {
	svc := GetSharedAgentService()
	if svc == nil {
		return nil, errors.New("no shared agent service")
	}
	identity, shouldRun := svc.remoteConnectionSnapshot()
	if !shouldRun || identity.authorization == "" || identity.deviceID == "" {
		return nil, errors.New("agent remote identity unavailable (not registered/enabled or no authorization)")
	}
	base, err := listenerCloudBaseURL()
	if err != nil {
		return nil, err
	}
	endpoint := base + "/api/agent/listeners"

	ctx, cancel := context.WithTimeout(context.Background(), listenerCloudFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", identity.authorization)
	req.Header.Set("X-Aliang-Device-ID", identity.deviceID)
	resp, err := svc.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, listenerCloudFetchBodyLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("agent server %s: status %d", sanitizeAgentEndpoint(endpoint), resp.StatusCode)
	}
	data, err := unwrapAgentServerData(raw)
	if err != nil {
		return nil, err
	}
	// 三形态分发(与 ListenerListViaCloud 同款容忍):裸数组 / {listeners:[...]}
	// / 信封 data 数组(unwrap 已剥)。listeners 用指针判键缺席——两分支成功
	// 路径产出的 rows 恒非 nil(裸 [] 即空表),无需 nil 归一。
	var rows []ListenerMirror
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, fmt.Errorf("decode listeners payload: %w", err)
		}
	} else {
		var payload struct {
			Listeners *[]ListenerMirror `json:"listeners"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, fmt.Errorf("decode listeners payload: %w", err)
		}
		if payload.Listeners == nil {
			return nil, fmt.Errorf("decode listeners payload: listeners key absent")
		}
		rows = *payload.Listeners
	}
	return rows, nil
}

// LoadListenerMirror boot 全量镜像入口(plan Task 10;user-agent runtime 的
// StartLocalServer 调用一次,cmd/boot_wiring_audit_test.go 的
// TestListenerWiringAudit 对 server.go 拦 exactly-once——进程归属见文件头注)。
// 全量拉取优先:成功 → saveListenerMirrors 全量覆写(store 的缓存刷新式
// save 即 boot 落盘入口,Task 7 设计);失败(离线/未注册/落盘失败)→
// Warn 降级、回落 listListenerMirrors()(本地文件 read-through,FR-3 离线
// 只读)。无 error 返回(log-only):boot 不被云端不可达阻塞。
//
// 调用顺序契约:必须先于 StartEventListenerWatchers(先镜像后 watcher,
// 1b8c36b 教训)——顺序由 TestListenerBootOrder 钉死;接线顺序见
// app/agentruntime/server.go 注释。
func LoadListenerMirror() {
	rows, fetchErr := listenerCloudFetchAll()
	if fetchErr == nil {
		if err := saveListenerMirrors(rows); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] boot 镜像落盘失败,回落本地旧镜像: %v", err))
		} else {
			logger.Info(fmt.Sprintf("[AGENT-LISTENER] boot 全量拉取完成 listeners=%d", len(rows)))
			return
		}
	} else {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] boot 全量拉取失败,回落本地镜像(FR-3 离线容忍): %v", fetchErr))
	}
	// 回落:本地文件 read-through 预热缓存,后续 watcher/UI 读走缓存。
	_ = listListenerMirrors()
}
