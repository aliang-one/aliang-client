package handlers

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	sshout "aliang.one/nursorgate/outbound/proxy/ssh"
)

// knownHostsFileName 与 ssh.New 的落盘口径一致
// (outbound/proxy/ssh/ssh.go: filepath.Join(hostsDir, "known_hosts"))。
const knownHostsFileName = "known_hosts"

// SSHKnownHostsHandler 管理 SSH TOFU 指纹库(/api/proxy/ssh/known-hosts):
// GET 列表 / DELETE 单条。无状态纯文件操作——双注册(core routes.go 与
// user-agent registerAgentRoutes)各持一个实例等价。
type SSHKnownHostsHandler struct {
	hosts *sshout.KnownHosts
}

// NewSSHKnownHostsHandler 用 known_hosts 文件全路径构造(测试注临时目录)。
func NewSSHKnownHostsHandler(knownHostsPath string) *SSHKnownHostsHandler {
	return &SSHKnownHostsHandler{hosts: sshout.NewKnownHosts(knownHostsPath)}
}

// NewSSHKnownHostsHandlerFromCache 按生产口径构造:cache/ssh/known_hosts,
// 与 outbound registry 建 SSH 代理的 hostsDir 同源(同一份 TOFU 库)。
// GetCacheSubdir 失败不阻断启动:Warn + 空路径降级——读侧 List 返回空、
// 删除侧先判存在一律 404,不会向未知路径写盘。同 LogService 对
// GetCacheSubdir 失败的静默降级惯例,另加启动告警。
func NewSSHKnownHostsHandlerFromCache() *SSHKnownHostsHandler {
	dir, err := cache.GetCacheSubdir("ssh")
	if err != nil {
		logger.Warn(fmt.Sprintf("resolve ssh known_hosts dir failed, known-hosts API degraded to empty: %v", err))
		return NewSSHKnownHostsHandler("")
	}
	return NewSSHKnownHostsHandler(filepath.Join(dir, knownHostsFileName))
}

// knownHostItem 是列表项:存储键 "host:port" 拆成 host/port 两字段(IPv6 host
// 不带方括号;port 保持字符串,DELETE 原样回传即重组出同一键)。
type knownHostItem struct {
	Host        string `json:"host"`
	Port        string `json:"port"`
	Fingerprint string `json:"fingerprint"`
	Algorithm   string `json:"algorithm"`
	AddedAt     string `json:"added_at"`
}

// HandleKnownHosts GET=列表(空库返回空数组)/ DELETE=按 host+port 删单条。
// 方法分流沿用 HandleCustomerConfig 的 switch 风格;鉴权与 /api/config/customer
// 同档(core 默认中间件栈,user-agent 侧 loopbackOnly),无额外会话关卡。
func (h *SSHKnownHostsHandler) HandleKnownHosts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleList(w)
	case http.MethodDelete:
		h.handleRemove(w, r)
	default:
		// common.Error 收业务码(100-299),直传 http.StatusMethodNotAllowed 会被
		// 映射成 500;此处须真实 405,同 HandlePeerMcp 的 http.Error 惯例。
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *SSHKnownHostsHandler) handleList(w http.ResponseWriter) {
	entries := h.hosts.List()
	items := make([]knownHostItem, 0, len(entries)) // 未配置 → 空数组而非 null
	for _, e := range entries {
		host, port, err := net.SplitHostPort(e.Host)
		if err != nil {
			// 历史脏行(无端口形态):整串当 host 原样透出,不因单行弃全表。
			host, port = e.Host, ""
		}
		items = append(items, knownHostItem{
			Host:        host,
			Port:        port,
			Fingerprint: e.Fingerprint,
			Algorithm:   e.Algorithm,
			AddedAt:     e.AddedAt.UTC().Format(time.RFC3339),
		})
	}
	common.Success(w, map[string]interface{}{"known_hosts": items})
}

func (h *SSHKnownHostsHandler) handleRemove(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	port := strings.TrimSpace(r.URL.Query().Get("port"))
	if host == "" || port == "" {
		common.ErrorBadRequest(w, "host and port query parameters are required", nil)
		return
	}
	// KnownHosts.Remove 对缺失键静默成功,404 语义在 handler 侧补:先查存在再删。
	// 键重组走 JoinHostPort(IPv6 方括号口径与 chain.hostPort 一致)。
	key := net.JoinHostPort(host, port)
	found := false
	for _, e := range h.hosts.List() {
		if e.Host == key {
			found = true
			break
		}
	}
	if !found {
		common.ErrorNotFound(w, fmt.Sprintf("known_hosts entry %s not found", key))
		return
	}
	if err := h.hosts.Remove(key); err != nil {
		common.ErrorInternalServer(w, "Failed to remove known_hosts entry", map[string]interface{}{"error": err.Error()})
		return
	}
	common.Success(w, map[string]interface{}{
		"message": "known_hosts entry removed",
		"host":    host,
		"port":    port,
	})
}
