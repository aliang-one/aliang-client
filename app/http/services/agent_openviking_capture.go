package services

// OpenViking 会话 capture(Phase2 spec §7.3):成功回合收尾旁路,把本回合的
// 用户消息与助手正文沉淀到 OpenViking(remember 受理 → 服务端 Compress→
// Archive→Memory Extraction 全后台)。
//
// 纪律(与记忆抽取旁路同族):
//   - detached + 60s 超时 + recover,任何失败只 Warn 绝不影响 run;
//   - 仅成功回合(调用点在 agentAIRunSideEffectsAllowed 分支内),中断/
//     stopped/失败/僵尸迟返回不捕获——被丢弃的输出没有沉淀价值;
//   - 转录只取用户消息与助手正文:tool/approval/end 记录(env/headers 值
//     红线家族)与 thinking 内部推理不进 capture;
//   - <openviking-context> 标记块剥离防自吞(官方插件教训:recall 注入的
//     内容回灌会自我强化)。
//
// 传输:按统一 MCP 注册表的 openviking 条目直连 MCP 端点(URL+Bearer 即 ensure
// 维护的真值;条目缺失/未启用 = 静默跳过,不视为错误)。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

const (
	// openVikingCaptureTimeout 单次 capture 上传超时(spec §7.3:60s)。
	openVikingCaptureTimeout = 60 * time.Second
	// openVikingCaptureMaxContent 单条消息正文的截断上限(助手正文可能很长;
	// 记忆抽取吃的是要点,不是全文)。
	openVikingCaptureMaxContent = 32 * 1024
)

// openVikingContextBlockRe recall 注入标记块(含未闭合首段,防拼接截断漏剥)。
var openVikingContextBlockRe = regexp.MustCompile(`(?s)<openviking-context>.*?</openviking-context>|<openviking-context>.*$`)

// openVikingProbeState §7.5 拨测状态:连续失败计数 + 当前健康位,供仪表盘卡
// (§7.7 批次)与 integrations 报告消费。
var (
	openVikingProbeFails atomic.Int32
	openVikingProbeOK    atomic.Bool
)

// OpenVikingProbeHealthy 拨测读点:最近一次探针是否健康(未跑过=false)。
func OpenVikingProbeHealthy() bool { return openVikingProbeOK.Load() }

// OpenVikingProbeConsecutiveFails 拨测读点:当前连续失败次数。
func OpenVikingProbeConsecutiveFails() int32 { return openVikingProbeFails.Load() }

// openvikingCaptureAsync 成功回合收尾旁路入口(调用点 agent_ai_agentcore.go
// 成功分支,与 agentCoreMemoryExtractAsync 相邻)。userText/assistantOutput
// 任一非空才上传;内部全兜底,绝不 panic 外泄。
func openvikingCaptureAsync(sessionID, userText, assistantOutput string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Warn(fmt.Sprintf("openviking: capture panic recovered session=%s err=%v", sessionID, r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), openVikingCaptureTimeout)
		defer cancel()
		if err := openvikingCaptureRun(ctx, sessionID, userText, assistantOutput); err != nil {
			logger.Warn(fmt.Sprintf("openviking: capture failed (不影响 run) session=%s err=%v", sessionID, err))
		}
	}()
}

// openvikingCaptureRun capture 同步本体(测试注入点)。
func openvikingCaptureRun(ctx context.Context, sessionID, userText, assistantOutput string) error {
	if !config.GetGlobalConfig().EffectiveOpenVikingEnabled() {
		return nil // 未启用:静默,非错误
	}
	endpoint, headers, ok := openvikingTransportFromRegistry()
	if !ok {
		return nil // 注册表无条目(ensure 未接线/已卸载):静默
	}
	var messages []map[string]interface{}
	if c := openvikingCaptureContent(userText); c != "" {
		messages = append(messages, map[string]interface{}{"role": "user", "content": c})
	}
	if c := openvikingCaptureContent(assistantOutput); c != "" {
		messages = append(messages, map[string]interface{}{"role": "assistant", "content": c})
	}
	if len(messages) == 0 {
		return nil
	}
	if err := openvikingRememberPost(ctx, endpoint, headers, messages); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("openviking: capture committed session=%s msgs=%d", sessionID, len(messages)))
	return nil
}

// openvikingCaptureContent 单条消息正文整备:剥 recall 标记块、去空白、按上限
// 截断(带截断标记,不静默丢尾)。
func openvikingCaptureContent(s string) string {
	s = openVikingContextBlockRe.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > openVikingCaptureMaxContent {
		s = s[:openVikingCaptureMaxContent] + "\n…[截断]"
	}
	return s
}

// openvikingTransportFromRegistry 从统一注册表取 openviking 条目的传输参数。
func openvikingTransportFromRegistry() (string, map[string]string, bool) {
	entries := aliangMCPEnabledEntries(mcpRegistryHome())
	e, ok := entries[openVikingManagedServerName]
	if !ok || !e.enabled() || strings.TrimSpace(e.URL) == "" {
		return "", nil, false
	}
	return e.URL, e.Headers, true
}

// openvikingRememberPost MCP JSON-RPC tools/call remember。受理即成功——抽取
// 在服务端后台跑,429 静默丢的观测缺口属服务端(已入审计遗留账)。
func openvikingRememberPost(ctx context.Context, endpoint string, headers map[string]string, messages []map[string]interface{}) error {
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      "remember",
			"arguments": map[string]interface{}{"messages": messages},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("remember status %d", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// §7.5 观测拨测:每 10 分钟轻探 /health;连续 3 次失败置「不可达」(edge 触发
// Warn 一次)。不做自动关停——静默降级是特性,但要可见。
// ---------------------------------------------------------------------------

const (
	openVikingProbeInterval = 10 * time.Minute
	openVikingProbeTimeout  = 5 * time.Second
	openVikingProbeFailMax  = 3
)

var openVikingProbeStart sync.Once

// StartOpenVikingProbe 启动拨测循环(boot 挂点;进程级一次)。
func StartOpenVikingProbe() {
	openVikingProbeStart.Do(func() {
		go func() {
			ticker := time.NewTicker(openVikingProbeInterval)
			defer ticker.Stop()
			for range ticker.C {
				openvikingProbeTick()
			}
		}()
	})
}

// openvikingProbeHealthURL 由 MCP 入口推导健康端点:去尾斜杠、剥 /mcp 后缀、
// 拼 /health(顺序不可换,否则尾斜杠形态剥不掉后缀)。
func openvikingProbeHealthURL(endpoint string) string {
	base := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	base = strings.TrimSuffix(base, "/mcp")
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/health"
}

// openvikingProbeTick 单轮拨测(测试注入点):未启用/无条目 → 状态复位跳过。
func openvikingProbeTick() {
	if !config.GetGlobalConfig().EffectiveOpenVikingEnabled() {
		openVikingProbeFails.Store(0)
		openVikingProbeOK.Store(false)
		return
	}
	endpoint, _, ok := openvikingTransportFromRegistry()
	if !ok {
		openVikingProbeFails.Store(0)
		openVikingProbeOK.Store(false)
		return
	}
	healthURL := openvikingProbeHealthURL(endpoint)
	if healthURL == "" {
		return
	}
	client := &http.Client{Timeout: openVikingProbeTimeout}
	resp, err := client.Get(healthURL)
	if err == nil {
		resp.Body.Close()
	}
	healthy := err == nil && resp != nil && resp.StatusCode == http.StatusOK
	if healthy {
		if openVikingProbeFails.Load() >= openVikingProbeFailMax {
			logger.Info("openviking: probe recovered (连续失败后恢复)")
		}
		openVikingProbeFails.Store(0)
		openVikingProbeOK.Store(true)
		return
	}
	fails := openVikingProbeFails.Add(1)
	openVikingProbeOK.Store(false)
	if fails == openVikingProbeFailMax {
		logger.Warn(fmt.Sprintf("openviking: probe unreachable (连续 %d 次失败,仪表盘置红;不做自动关停)", fails))
	}
}
