package services

// 互传的收件兜底与设备侧 HTTP 客户端：inbox 拉取（WS 信令从未到达的离线
// 补投场景靠它独立驱动接收，spec §5/§6.2）、confirm/parts/download-url 的
// URL 构造、transfer.cancel 的入口分发。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

// --------------------------------------------------------------------------
// 服务端 URL 构造（照 currentAgentStatusSyncURL 的服务级惯例，不动 config/types.go）
// --------------------------------------------------------------------------

func agentTransferServerURL(path string) string {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(cfg.AgentBaseURL(), "/") + path
}

// CreateTransferViaServer 本地触发端点（POST /api/agent/transfers，D13）的
// 转发底座：仪表盘请求体原样投给 PhoneServer POST /api/transfers，设备 JWT
// 由 callAgentServer 注入。路径解析/类型/能力位校验全在服务端与两端 agent。
func (s *AgentService) CreateTransferViaServer(payload map[string]interface{}) ([]byte, error) {
	if s == nil {
		return nil, errors.New("agent service unavailable")
	}
	return s.callAgentServer(http.MethodPost, agentTransferServerURL("/api/transfers"), payload, "")
}

// --------------------------------------------------------------------------
// inbox 拉取：WS 重连（agent.registered 臂）+ 每小时低频定时
// --------------------------------------------------------------------------

var agentTransferFetchInbox = func(s *AgentService) ([]map[string]interface{}, error) {
	raw, err := s.callAgentServer(http.MethodGet, agentTransferServerURL("/api/transfers/inbox"), nil, "")
	if err != nil {
		return nil, err
	}
	var body struct {
		Transfers []map[string]interface{} `json:"transfers"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	return body.Transfers, nil
}

var agentTransferInboxOnce sync.Once

// kickAgentTransferInbox 注册就绪后拉一次收件箱，并确保小时级定时器在跑。
// 只能经 agent.registered 臂触发（该臂本身就意味着「刚连上 + 已过注册门」）。
func (s *AgentService) kickAgentTransferInbox() {
	agentTransferInboxOnce.Do(func() {
		go s.agentTransferInboxTicker()
	})
	safeGo("transfer_inbox", func() { s.pullAgentTransferInbox() })
}

func (s *AgentService) agentTransferInboxTicker() {
	// 低频兜底：防「ready 后目标设备长期不重连」把传输拖到 7 天过期。
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		if !s.remoteControlAllowed() {
			continue
		}
		s.pullAgentTransferInbox()
	}
}

// pullAgentTransferInbox 拉收件箱并逐条驱动接收。每条复用 handleAgentTransferReceive
// （幂等：已在飞/已完成在 handler 内去重），外层 safeGo 已隔离 panic。
func (s *AgentService) pullAgentTransferInbox() {
	entries, err := agentTransferFetchInbox(s)
	if err != nil {
		// 拉取频率低（重连 + 每小时），warn 可接受；服务端不可达是常态噪音。
		logger.Warn(fmt.Sprintf("[TRANSFER] inbox pull failed: %v", err))
		return
	}
	if len(entries) == 0 {
		return
	}
	writeJSON := func(payload interface{}) error {
		w := s.currentRemoteWriter()
		if w == nil {
			return errors.New("remote agent writer unavailable (disconnected)")
		}
		return w(payload)
	}
	for _, entry := range entries {
		if entry == nil || strings.TrimSpace(fmt.Sprint(entry["transfer_id"])) == "" {
			continue
		}
		logger.Info(fmt.Sprintf("[TRANSFER] inbox driving transfer_id=%v", entry["transfer_id"]))
		s.handleAgentTransferReceive(entry, writeJSON)
	}
}

// --------------------------------------------------------------------------
// transfer.cancel：按 transfer_id 匹配在飞任务，不匹配为 no-op（spec §6.2）
// --------------------------------------------------------------------------

func (s *AgentService) handleAgentTransferCancel(msg map[string]interface{}, writeJSON func(interface{}) error) {
	ident := parseAgentTransferIdentity(msg)
	if ident.TransferID == "" {
		return
	}
	stoppedUpload := transferUploadSlot().cancelIfActive(ident.TransferID)
	stoppedReceive := transferReceiveSlot().cancelIfActive(ident.TransferID)
	if stoppedUpload {
		logger.Info(fmt.Sprintf("[TRANSFER] cancel hit upload transfer_id=%s", ident.TransferID))
	}
	if stoppedReceive {
		logger.Info(fmt.Sprintf("[TRANSFER] cancel hit receive transfer_id=%s", ident.TransferID))
	}
	if !stoppedUpload && !stoppedReceive {
		// 未在飞：可能已完成或从未开始——服务端终态吸收，这里静默即可。
		logger.Info(fmt.Sprintf("[TRANSFER] cancel no-op transfer_id=%s", ident.TransferID))
	}
}

// --------------------------------------------------------------------------
// .partial 清理（进程启动时清掉陈旧残留；7 天兜底）
// --------------------------------------------------------------------------

const agentTransferPartialStaleAge = 7 * 24 * time.Hour

// sweepAgentTransferPartials 清理收件根下超过 7 天的 .partial-*（进程崩溃
// 残留的孤儿块；7 天与服务端对象过期对齐）。随 inbox 定时器顺带跑。
func sweepAgentTransferPartials() {
	inbox, err := agentTransferInboxDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(inbox)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-agentTransferPartialStaleAge)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".partial-") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.IsDir() {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(inboxJoined(inbox, e.Name()))
		}
	}
}

func inboxJoined(inbox, name string) string {
	return inbox + string(os.PathSeparator) + name
}
