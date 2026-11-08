package handlers

import (
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	sshin "aliang.one/nursorgate/inbound/ssh"
)

// SSHIngressHandler 暴露 SSH 入口 host key 指纹(/api/proxy/ssh-ingress/hostkey):
// 只读。生成职责单一归 inbound/ssh 的 Server.Start——handler 若也触发生成,
// 双进程(core routes.go 与 user-agent registerAgentRoutes)各持实例并发命中
// 缺失文件时会各自生成、原子写 rename 最后者胜,与 Server 已加载的钥错位
// (展示指纹≠实际服务钥);故文件缺失按「入口尚未启动」404 语义返回。
// spec §7「不存在时触发生成」按此修正:读取复用 inbound/ssh 导出的
// ReadHostKeyInfo(生成设施同包同源),不做 EnsureHostKey 式导出。
type SSHIngressHandler struct {
	hostKeyPath string
}

// NewSSHIngressHandler 用 host key 文件全路径构造(测试注 t.TempDir)。
func NewSSHIngressHandler(hostKeyPath string) *SSHIngressHandler {
	return &SSHIngressHandler{hostKeyPath: hostKeyPath}
}

// NewSSHIngressHandlerFromCache 按生产口径构造:cache/ssh/ + inbound/ssh 导出
// 的 IngressHostKeyFileName(编译器强制与 Server.Start 落盘同源,同出口侧
// KnownHostsFileName 收敛先例)。GetCacheSubdir 失败不阻断启动:Warn + 空路径
// 降级——空路径读取必 fs.ErrNotExist,GET 走 404 语义(同
// NewSSHKnownHostsHandlerFromCache 的静默降级惯例+启动告警)。
func NewSSHIngressHandlerFromCache() *SSHIngressHandler {
	dir, err := cache.GetCacheSubdir("ssh")
	if err != nil {
		logger.Warn("resolve ssh ingress host key dir failed, hostkey API degraded to not-started: " + err.Error())
		return NewSSHIngressHandler("")
	}
	return NewSSHIngressHandler(filepath.Join(dir, sshin.IngressHostKeyFileName))
}

// sshIngressHostkey 是指纹展示载荷:generated_at 取文件 mtime(RFC3339,
// UTC——与 known_hosts 条目 AddedAt 同口径)。
type sshIngressHostkey struct {
	Fingerprint string `json:"fingerprint"`
	Algorithm   string `json:"algorithm"`
	GeneratedAt string `json:"generated_at"`
}

// HandleHostkey GET=指纹信封;非 GET 一律 405。鉴权与 /api/proxy 代理族同档
// (core 默认中间件栈,user-agent 侧 loopbackOnly),无额外会话关卡。双注册
// 另一处 agentruntime server.go registerAgentRoutes(缺任一侧=404 透传,
// 2026-10-21 教训;parity 正则已覆盖 /api/proxy)。
func (h *SSHIngressHandler) HandleHostkey(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w)
	default:
		// common.Error 收业务码(100-299),直传 http.StatusMethodNotAllowed 会被
		// ErrorCodeToHTTPStatus 映射成 500;此处须真实 405,同 SSHKnownHostsHandler
		// HandleKnownHosts 的 http.Error 先例。
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *SSHIngressHandler) handleGet(w http.ResponseWriter) {
	info, err := sshin.ReadHostKeyInfo(h.hostKeyPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// host key 由 Server.Start 生成(spec §7 措辞按职责单一修正):缺失=
		// 入口从未启动过,404 语义+明确文案,不触发生成(见类型注释竞态分析)。
		common.ErrorNotFound(w, "SSH 入口尚未启动,host key 未生成")
		return
	case err != nil:
		// 文件在但损坏/不可读:显式 500(环境已坏;Server.Start 下次启动会
		// 重生成,但当下无法给出真实指纹,报错优于编造)。
		common.ErrorInternalServer(w, "Failed to read SSH ingress host key", map[string]interface{}{"error": err.Error()})
		return
	}
	common.Success(w, map[string]interface{}{"hostkey": sshIngressHostkey{
		Fingerprint: info.Fingerprint,
		Algorithm:   info.Algorithm,
		GeneratedAt: info.GeneratedAt.Format(time.RFC3339),
	}})
}
