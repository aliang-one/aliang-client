package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/app/mesh/lanbeacon"
	"aliang.one/nursorgate/app/tunnel"
	"aliang.one/nursorgate/processor/config"
)

// MeshHandler mesh 面端点(Task 10:LAN beacon 候选只读;Task 14 追加
// /api/mesh/status 状态聚合)。beacon 源取进程级默认实例(组装层
// SetDefault/Stop 交接,Task 14 生命周期接线),handler 不持有生命周期、
// 未装配时候选恒空——离线面板拿空表是合法态,不是 5xx。
//
// 全部数据源走注入缝(生产钉进程级单例/服务):beacon 候选、全局配置
// (peers/开关现值)、本机 device_id(services 常驻身份)、LAN 监听口端口
// (app/tunnel 装配态)。写路径不做独立 POST——mesh 配置走既有
// /api/config/customer 的 customer.mesh(计划 Task 14 决策,避免 peer_mcp
// 两段式复杂度)。
type MeshHandler struct {
	beacon   func() *lanbeacon.Beacon
	config   func() *config.Config
	deviceID func() string
	lanPort  func() int
}

// NewMeshHandler 构造 handler,候选源钉 lanbeacon.Default()(取调用时值,
// 组装层可后注册/清空);其余缝钉生产单例。
func NewMeshHandler() *MeshHandler {
	return &MeshHandler{
		beacon:   lanbeacon.Default,
		config:   config.GetGlobalConfig,
		deviceID: services.GetSharedAgentService().CurrentDeviceID,
		lanPort:  tunnel.MeshLANPort,
	}
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

// meshStatusResponse GET /api/mesh/status 的 data 载荷。peers/lan_peers 恒
// 数组(空配置是合法态,null 会逼前端判空);开关取 Effective* 现值(与
// 运行时读取同一语义:nil 总开关=默认开,nil forward=默认关)。Effective
// 是 mesh 出口的生效门(= enable ∧ ssh_ingress 可用,SPEC §10.3 依赖降级
// 一处生效)——前端用它区分「开关开了但 SSH 入口关着导致 mesh 未生效」。
type meshStatusResponse struct {
	DeviceID      string                       `json:"device_id"`
	Enable        bool                         `json:"enable"`
	Effective     bool                         `json:"effective"`
	ForwardEnable bool                         `json:"forward_enable"`
	LANListenPort int                          `json:"lan_listen_port"`
	Peers         []config.MeshPeer            `json:"peers"`
	LANPeers      []lanbeacon.LanPeerCandidate `json:"lan_peers"`
}

// HandleStatus GET /api/mesh/status:仪表盘组网页状态聚合——本机 device_id、
// 总开关/forward_enable 现值、LAN 监听口端口(手填对端地址须知本机端口)、
// 已配 peers、beacon 候选。全只读,写路径走 /api/config/customer。
func (h *MeshHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := meshStatusResponse{
		Enable:   true, // nil 总开关=默认开,与 MeshConfig.EffectiveEnabled 同语义
		Peers:    []config.MeshPeer{},
		LANPeers: []lanbeacon.LanPeerCandidate{},
	}
	if h.deviceID != nil {
		resp.DeviceID = h.deviceID()
	}
	if h.lanPort != nil {
		resp.LANListenPort = h.lanPort()
	}
	if h.config != nil {
		if cfg := h.config(); cfg != nil {
			// Effective 的守卫只有 cfg 非 nil:MeshEffectivelyEnabled 对 nil
			// Customer/Mesh 安全(nil mesh 总开关=默认开),mesh 键缺失而
			// ssh 开启时真值为 true——若也锁进 Mesh!=nil 守卫,这种已生效态
			// 会被误报成 false,正是本字段要防的「已启用却不通没法定位」。
			resp.Effective = cfg.MeshEffectivelyEnabled()
			if cfg.Customer != nil && cfg.Customer.Mesh != nil {
				mesh := cfg.Customer.Mesh
				resp.Enable = mesh.EffectiveEnabled()
				resp.ForwardEnable = mesh.EffectiveForwardEnabled()
				if len(mesh.Peers) > 0 {
					resp.Peers = mesh.Peers
				}
			}
		}
	}
	if h.beacon != nil {
		if beacon := h.beacon(); beacon != nil {
			resp.LANPeers = beacon.Candidates()
		}
	}
	common.Success(w, resp)
}
