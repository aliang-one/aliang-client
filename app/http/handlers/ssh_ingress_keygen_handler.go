package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	sshin "aliang.one/nursorgate/inbound/ssh"
)

// SSHIngressKeygenHandler 一键生成 SSH 入口人用密钥对
// (POST /api/proxy/ssh-ingress/keygen,spec 2026-11-12 §1.2):
// 纯生成不落盘——私钥只在本次响应里出现一次,服务端内存即弃;
// 公钥入库仍走 /api/config/customer 保存链,本端点不碰配置(单写路径)。
// 无状态,两进程(core/user-agent)各持实例等价,与 hostkey/known-hosts
// handler 同惯例。双注册另一处 agentruntime server.go registerAgentRoutes
// (缺任一侧=404 透传,2026-10-21 教训;parity 测试
// app/agentruntime/route_parity_test.go 为护栏)。
type SSHIngressKeygenHandler struct{}

// NewSSHIngressKeygenHandler 无字段构造(端点无状态)。
func NewSSHIngressKeygenHandler() *SSHIngressKeygenHandler {
	return &SSHIngressKeygenHandler{}
}

// HandleKeygen POST=密钥信封;非 POST 一律 405(http.Error 直传状态码,
// 同 SSHIngressHandler.HandleHostkey 先例——走 common.Error 业务码会被
// ErrorCodeToHTTPStatus 折成 500)。
func (h *SSHIngressKeygenHandler) HandleKeygen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key, err := sshin.GenerateIngressKey()
	if err != nil {
		// 环境级故障(rand/编码),报错优于编造
		common.ErrorInternalServer(w, "Failed to generate SSH ingress key", map[string]interface{}{"error": err.Error()})
		return
	}
	common.Success(w, map[string]interface{}{"key": key})
}
