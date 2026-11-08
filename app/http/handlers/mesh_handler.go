package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/mesh/lanbeacon"
)

// MeshHandler mesh 面端点(Task 10:LAN beacon 候选只读;Task 14 追加
// /api/mesh/status 状态聚合)。beacon 源取进程级默认实例(组装层
// SetDefault/Stop 交接,Task 14 生命周期接线),handler 不持有生命周期、
// 未装配时候选恒空——离线面板拿空表是合法态,不是 5xx。
type MeshHandler struct {
	beacon func() *lanbeacon.Beacon
}

// NewMeshHandler 构造 handler,候选源钉 lanbeacon.Default()(取调用时值,
// 组装层可后注册/清空)。
func NewMeshHandler() *MeshHandler {
	return &MeshHandler{beacon: lanbeacon.Default}
}

// lanPeersResponse GET /api/mesh/lan_peers 的 data 载荷。
type lanPeersResponse struct {
	Peers []lanbeacon.LanPeerCandidate `json:"peers"`
}

// HandleLANPeers GET /api/mesh/lan_peers:当前 beacon 收到的 LAN 候选
// (按 last_seen 降序),供仪表盘离线模式点选;点选后由用户手填
// customer.mesh.peers——不做候选→配置自动同步(Task 14 前端职责)。
func (h *MeshHandler) HandleLANPeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// common.Error 收业务码(100-299),直传 http.StatusMethodNotAllowed
		// 会被映射成 500;此处须真实 405,同 HandlePeerGates 惯例。
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var beacon *lanbeacon.Beacon
	if h.beacon != nil {
		beacon = h.beacon()
	}
	common.Success(w, lanPeersResponse{Peers: beacon.Candidates()})
}
