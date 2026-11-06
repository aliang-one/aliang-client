package handlers

// agent_mcp_registry_handler.go 统一 MCP 注册表 API(GET/PUT/DELETE)。
// 写面属设备本地配置读写,与 peer/mcp 同族走 proxyIfNeeded 落到 user-agent
// runtime(core 进程只是透传,注册表文件在 agent 家目录)。
import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
)

const aliangMCPRequestMaxBytes = 64 << 10 // 64KB,防呆

// HandleAliangMCPRegistry GET /api/agent/mcp — 注册表原文。
func (h *AgentHandler) HandleAliangMCPRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	reg, err := h.service.AliangMCPRegistry()
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	common.Success(w, reg)
}

// HandleAliangMCPServerPut PUT /api/agent/mcp/servers/{name}
// 校验失败=400 带原因(spec §7.1);成功 200,body 携带 codex_sync 两段式结果。
func (h *AgentHandler) HandleAliangMCPServerPut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		common.ErrorBadRequest(w, "server name required", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, aliangMCPRequestMaxBytes)
	var req services.AliangMCPServerRequest
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}
	res := h.service.UpsertAliangMCPServer(name, req)
	if res.RegistryErr != "" {
		common.ErrorBadRequest(w, res.RegistryErr, nil)
		return
	}
	common.Success(w, res)
}

// HandleAliangMCPServerDelete DELETE /api/agent/mcp/servers/{name}
// 不存在=404(spec §7.1);成功 200(body.existed=true)。
func (h *AgentHandler) HandleAliangMCPServerDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.proxyIfNeeded(w, r) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		common.ErrorBadRequest(w, "server name required", nil)
		return
	}
	res := h.service.DeleteAliangMCPServer(name)
	if res.RegistryErr != "" {
		common.ErrorBadRequest(w, res.RegistryErr, nil)
		return
	}
	if !res.Existed {
		// common.Error 收业务码(100-299),直传 http 状态会映射成 500——
		// 真实 404 须用 http.Error(peer/mcp 的 405 同款惯例)。
		http.Error(w, "server not found: "+name, http.StatusNotFound)
		return
	}
	common.Success(w, res)
}
