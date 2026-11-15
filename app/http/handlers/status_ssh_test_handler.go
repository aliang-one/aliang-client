// status_ssh_test_handler.go — 客户端「测试连接」handler：
// POST /api/proxy/ssh/test。载荷与 customer.proxy.ssh 的 SSH 面同构
// （server_host/server_port/username/password/private_key/key_passphrase/
// jump_servers），后端不落盘、不触碰注册表——用请求里的凭据现场做一次
// 一次性链路探测（outbound sshout.Probe：认证→握手→TOFU→通道→写入），
// 返回分层结论（ok/阶段/错误文本/延迟/host key 指纹）。
//
// 路由注册双处（parity 纪律）：core routes.go + agentruntime server.go，
// /api/proxy 前缀被 route_parity_test.go 护栏覆盖。
package handlers

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	sshout "aliang.one/nursorgate/outbound/proxy/ssh"
)

// sshTestRequest 是测试连接的请求载荷（与 SSHProxyConfig 同构但独立声明：
// 测试是短命无状态操作，不共用存储形态的校验语义）。
type sshTestRequest struct {
	Server        string           `json:"server_host"`
	ServerPort    uint16           `json:"server_port"`
	Username      string           `json:"username"`
	Password      string           `json:"password,omitempty"`
	PrivateKey    string           `json:"private_key,omitempty"`
	KeyPassphrase string           `json:"key_passphrase,omitempty"`
	JumpServers   []sshTestJumpRaw `json:"jump_servers,omitempty"`
}

type sshTestJumpRaw struct {
	Server        string `json:"server_host"`
	ServerPort    uint16 `json:"server_port"`
	Username      string `json:"username"`
	Password      string `json:"password,omitempty"`
	PrivateKey    string `json:"private_key,omitempty"`
	KeyPassphrase string `json:"key_passphrase,omitempty"`
}

// HandleSSHTest POST /api/proxy/ssh/test：现场探测，无副作用。
func HandleSSHTest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		handleSSHTestPost(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleSSHTestPost(w http.ResponseWriter, r *http.Request) {
	var req sshTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.Error(w, common.CodeBadRequest, "invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}
	// 基本完整性（详细校验归前端提示层与保存路径的 Validate）：字段缺失
	// 直接 400，不打到网络层。
	if req.Server == "" || req.ServerPort == 0 || req.Username == "" {
		common.Error(w, common.CodeBadRequest, "server_host, server_port and username are required", nil)
		return
	}
	if req.Password == "" && req.PrivateKey == "" {
		common.Error(w, common.CodeBadRequest, "password or private_key is required", nil)
		return
	}

	final := sshout.NodeFromAddrPort(req.Server, req.ServerPort, req.Username, req.Password, req.PrivateKey, req.KeyPassphrase)
	jumps := make([]sshout.NodeConfig, 0, len(req.JumpServers))
	for _, j := range req.JumpServers {
		if j.Server == "" || j.ServerPort == 0 || j.Username == "" || (j.Password == "" && j.PrivateKey == "") {
			common.Error(w, common.CodeBadRequest, "each jump server requires server_host, server_port, username and one credential", nil)
			return
		}
		jumps = append(jumps, sshout.NodeFromAddrPort(j.Server, j.ServerPort, j.Username, j.Password, j.PrivateKey, j.KeyPassphrase))
	}

	// TOFU 库与出站链落盘同源（cache/ssh/known_hosts）：探测复用同一份
	// 指纹库——已钉扎的服务器在测试连接时同样执行 mismatch 拒连（测试
	// 的意义就是暴露这些）；解析失败降级为 nil（跳过 TOFU，不阻塞探测）。
	hosts := probeKnownHosts()

	res := sshout.Probe(final, jumps, hosts, sshout.ProbeTarget{})
	if !res.OK {
		logger.Debug("SSH test connection failed: stage=" + string(res.Stage) + " err=" + res.Error)
	}
	common.Success(w, map[string]interface{}{
		"ok":                   res.OK,
		"stage":                string(res.Stage),
		"error":                res.Error,
		"latency_millis":       res.LatencyMillis,
		"host_key_fingerprint": res.HostKeyFpr,
	})
}

// probeKnownHosts 按生产口径解析 TOFU 库；失败返回 nil（探测跳过指纹
// 校验并在日志留痕）。
func probeKnownHosts() *sshout.KnownHosts {
	dir, err := cache.GetCacheSubdir("ssh")
	if err != nil {
		logger.Warn("resolve ssh known_hosts dir for test connection, TOFU check skipped: " + err.Error())
		return nil
	}
	return sshout.NewKnownHosts(filepath.Join(dir, sshout.KnownHostsFileName))
}
