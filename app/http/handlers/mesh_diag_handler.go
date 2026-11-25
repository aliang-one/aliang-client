package handlers

import (
	"fmt"
	"net/http"

	meshexit "aliang.one/nursorgate/outbound/proxy/mesh"
	egress "aliang.one/nursorgate/processor/egress"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/app/mesh/lanbeacon"
	"aliang.one/nursorgate/app/mesh/punch"
	"aliang.one/nursorgate/app/tunnel"
	"aliang.one/nursorgate/processor/config"
)

// MeshDiagHandler 组网诊断读面聚合端点(GET /api/mesh/diag,读面批 Task 5):
// 一次取齐「四重闸判定组 + roster 同步三态 + 运行态真值双布尔(beacon_running/
// lan_running,阶段 0 灭假绿)+ beacon 计数/候选 + 载体五桶 + 出口委托三元组 +
// 打洞 classify/sessions(含 memo)」,给 AI 组网诊断面与仪表盘当单点数据源。
// 全只读零副作用:beacon 只读计数快照、STUN 只报配置现值不活探测、账本只取
// 快照不清零(Candidates 读触发 TTL 惰性剪枝为既有读路径语义,/api/mesh/
// lan_peers 同款,非诊断副作用)。
//
// 闸语义权威在本端点:diag 是闸/运行态判定的超集权威面;/api/mesh/status 的
// 闸字段保留为仪表盘兼容面(降位,本批不动 status)。残余账:后续抽
// meshGateSnapshot 公共函数把两处闸推导单源化(评审 Issue 2 拍板,挂 TODO)。
//
// 数据源全部 func() 注入缝(与 MeshHandler 同款直注风格;生产钉进程级
// 单例/包级快照函数),nil 守卫零值安全——未装配=恒空数组+零值标量,
// 不 panic 不 5xx(离线/单进程形态拿空表是合法态)。
type MeshDiagHandler struct {
	config        func() *config.Config
	beacon        func() *lanbeacon.Beacon
	rosterSync    func() services.MeshRosterSyncSnapshot
	punchClassify func() services.MeshPunchClassifySnapshot
	punchSessions func() services.MeshPunchSessionsSnapshot
	carriers      func() meshexit.CarrierBucketSnapshot
	delegate      func() egress.DelegateDiagnostics

	// 运行态真值双缝(D2 假绿治理,与 MeshHandler 同款):Enable/Effective
	// 是配置推导,下面两缝才是「活着」事实——开关开而 beacon_running=false
	// 即装配假绿现场;lan_running=LAN 监听口(tunnel 装配态)。
	beaconRunning func() bool
	lanRunning    func() bool

	// 身份一致性四源缝(Task 6 /api/mesh/identity):state=进程内 device_id
	// 真值(services.StateDeviceIDReadonly 只读不生成——CurrentDeviceID 会走
	// ensureDeviceIdentityLocked 铸新身份,绝不可用)、identity 常驻文件
	// (services.LoadAgentIdentityFileReadonly,缺失=("",""))、beacon 广播
	// self(lanbeacon.Default().SelfID())、roster 本机条目在册
	// (services.MeshRosterEntryFor 布尔化)。全只读零副作用。
	stateDeviceID     func() string
	identityFile      func() (string, string)
	beaconSelfID      func() string
	rosterSelfPresent func(selfDeviceID string) bool
}

// NewMeshDiagHandler 构造 handler,各缝钉生产源:beacon=lanbeacon.Default()
// (取调用时值,组装层可后注册/清空;Stats/Candidates nil 接收者安全)、
// 打洞三账本=services 常驻单例、载体/委托=outbound/proxy/mesh 与
// processor/egress 的包级快照函数(读面批 Task 4;handlers→两包直 import
// 无环,两包零 app 反向依赖)。
func NewMeshDiagHandler() *MeshDiagHandler {
	return &MeshDiagHandler{
		config:        config.GetGlobalConfig,
		beacon:        lanbeacon.Default,
		rosterSync:    services.MeshRosterSyncStatus,
		punchClassify: services.MeshPunchClassifyStatus,
		punchSessions: services.MeshPunchSessionsStatus,
		carriers:      meshexit.SnapshotCarrierBuckets,
		delegate:      egress.SnapshotDelegateDiagnostics,

		beaconRunning: func() bool {
			if b := lanbeacon.Default(); b != nil {
				return b.Running()
			}
			return false
		},
		lanRunning: tunnel.MeshLANRunning,

		stateDeviceID: func() string {
			return services.GetSharedAgentService().StateDeviceIDReadonly()
		},
		identityFile: services.LoadAgentIdentityFileReadonly,
		beaconSelfID: func() string {
			if b := lanbeacon.Default(); b != nil {
				return b.SelfID()
			}
			return ""
		},
		rosterSelfPresent: func(selfDeviceID string) bool {
			_, _, ok := services.MeshRosterEntryFor(selfDeviceID)
			return ok
		},
	}
}

// meshDiagBeaconSection beacon 段:计数快照直接嵌入(Task 1 产物,字段全
// snake_case)+ LAN 候选(与 /api/mesh/lan_peers 同源同语义)+ beacon_running
// 运行态真值(与候选/计数并读判活,Task 1 注「判活须并读 Running()」)。
type meshDiagBeaconSection struct {
	Stats         lanbeacon.BeaconStats        `json:"stats"`
	LANPeers      []lanbeacon.LanPeerCandidate `json:"lan_peers"`
	BeaconRunning bool                         `json:"beacon_running"`
}

// meshDiagPunchSection 打洞段:classify 判型账本(A/B 两侧)+ sessions
// 快照聚合(cache/sockets/拨号计数/memo/B 侧在册)。memo 已按读面批拍定
// 并入 sessions(Task 3),本段不重复输出独立 memo 键——消费方读
// punch.sessions.memo.side_a/side_b。
type meshDiagPunchSection struct {
	Classify services.MeshPunchClassifySnapshot `json:"classify"`
	Sessions services.MeshPunchSessionsSnapshot `json:"sessions"`
}

// meshDiagResponse GET /api/mesh/diag 的 data 载荷(全 snake_case)。
// 四重闸判定组照 /api/mesh/status 既有推导形态:Enable 取 EffectiveEnabled
// 现值(nil 总开关=默认开),forward/punch/auto_peers 是让渡型默认关三态;
// Effective 是 mesh 出口生效门(= enable ∧ ssh_ingress 可用,SPEC §10.3),
// 见 HandleStatus 同注。stun_servers 恒数组(空配置合法态,null 会逼前端
// 判空);lan_peers/beacon 段与 punch 各数组段恒数组(见 emptyPunch* 守卫)。
type meshDiagResponse struct {
	Enable        bool                            `json:"enable"`
	Effective     bool                            `json:"effective"`
	ForwardEnable bool                            `json:"forward_enable"`
	PunchEnable   bool                            `json:"punch_enable"`
	AutoPeers     bool                            `json:"auto_peers"`
	RosterSync    services.MeshRosterSyncSnapshot `json:"roster_sync"`

	Beacon      meshDiagBeaconSection          `json:"beacon"`
	Carriers    meshexit.CarrierBucketSnapshot `json:"carriers"`
	Delegate    egress.DelegateDiagnostics     `json:"delegate"`
	Punch       meshDiagPunchSection           `json:"punch"`
	StunServers []string                       `json:"stun_servers"`

	// LANRunning LAN 监听口(tunnel 装配态)运行态真值,与 beacon_running
	// 同族「活着」事实(顶层=跨段维度,不属 beacon/beacon 闸任何一段)。
	LANRunning bool `json:"lan_running"`
}

// emptyPunchClassify/emptyPunchSessions nil seam(未注入缝)时的恒数组守卫
// 形态:classify 双侧/cache/sockets/memo 双侧/side_b 会话表全空数组,不
// null。生产访问器自身已同款兜底(MeshPunchClassifyStatus/SessionsStatus
// 均 nil 安全),此处守卫的是测试直构 &MeshDiagHandler{} 面。
func emptyPunchClassify() services.MeshPunchClassifySnapshot {
	return services.MeshPunchClassifySnapshot{
		SideA: []services.ClassifyOutcome{},
		SideB: []services.ClassifyOutcome{},
	}
}

func emptyPunchSessions() services.MeshPunchSessionsSnapshot {
	return services.MeshPunchSessionsSnapshot{
		Cache:   []punch.SessionSnapshotEntry{},
		Sockets: []services.MeshPunchSocketEntry{},
		Memo:    services.MeshPunchSessionsMemo{SideA: []punch.SkipMemoEntry{}, SideB: []punch.SkipMemoEntry{}},
		SideB:   services.MeshPunchBSideSnapshot{Sessions: []services.MeshPunchBSideSession{}},
	}
}

// HandleDiag GET /api/mesh/diag 组网诊断聚合。只读;写路径不变(mesh 配置
// 走 /api/config/customer 的 customer.mesh)。
func (h *MeshDiagHandler) HandleDiag(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// common.Error 收业务码(100-299),直传 http.StatusMethodNotAllowed
		// 会被映射成 500;此处须真实 405,同 HandleLANPeers/HandlePeerGates 惯例。
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := meshDiagResponse{
		Enable:      true, // nil 总开关=默认开,与 MeshConfig.EffectiveEnabled 同语义
		StunServers: []string{},
		Beacon: meshDiagBeaconSection{
			Stats:    lanbeacon.BeaconStats{},
			LANPeers: []lanbeacon.LanPeerCandidate{},
		},
		Carriers: meshexit.CarrierBucketSnapshot{},
		Delegate: egress.DelegateDiagnostics{},
		Punch: meshDiagPunchSection{
			Classify: emptyPunchClassify(),
			Sessions: emptyPunchSessions(),
		},
	}
	if h.beacon != nil {
		// Beacon.Stats/Candidates 均 nil 接收者安全:未装配默认实例=零计数+空表。
		beacon := h.beacon()
		resp.Beacon.Stats = beacon.Stats()
		resp.Beacon.LANPeers = beacon.Candidates()
	}
	if h.rosterSync != nil {
		resp.RosterSync = h.rosterSync()
	}
	// 运行态真值双缝(nil seam 零值安全:未注入即未装配,false 不 panic)。
	resp.Beacon.BeaconRunning = h.beaconRunning != nil && h.beaconRunning()
	resp.LANRunning = h.lanRunning != nil && h.lanRunning()
	if h.punchClassify != nil {
		resp.Punch.Classify = h.punchClassify()
	}
	if h.punchSessions != nil {
		resp.Punch.Sessions = h.punchSessions()
	}
	if h.carriers != nil {
		resp.Carriers = h.carriers()
	}
	if h.delegate != nil {
		resp.Delegate = h.delegate()
	}
	if h.config != nil {
		if cfg := h.config(); cfg != nil {
			// Effective 的守卫只有 cfg 非 nil(照 HandleStatus :135-141 既有推导
			// 形态):MeshEffectivelyEnabled 对 nil Customer/Mesh 安全(nil mesh
			// 总开关=默认开),mesh 键缺失而 ssh 开启时真值为 true——若也锁进
			// Mesh!=nil 守卫,这种已生效态会被误报成 false。
			resp.Effective = cfg.MeshEffectivelyEnabled()
			if cfg.Customer != nil && cfg.Customer.Mesh != nil {
				meshCfg := cfg.Customer.Mesh
				resp.Enable = meshCfg.EffectiveEnabled()
				resp.ForwardEnable = meshCfg.EffectiveForwardEnabled()
				resp.PunchEnable = meshCfg.EffectivePunchEnabled()
				resp.AutoPeers = meshCfg.EffectiveAutoPeers()
				resp.StunServers = meshCfg.EffectiveStunServers()
			}
		}
	}
	common.Success(w, resp)
}

// meshIdentityResponse GET /api/mesh/identity 的 data 载荷(全 snake_case)。
// 四源直列(进程 state/identity 常驻文件/beacon 广播 self/roster 本机条目),
// AI 消费方按源取数自行判读;mismatches 是人话判定面(每条一句「哪个源 vs
// 哪个源」不一致)。identity_file_path 本身就是诊断信息:空串=无常驻身份
// 文件(未生成/路径解析失败),不是错误态。device_id 即进程 state 值
// (state 的唯一罗列,不设别名键)。
type meshIdentityResponse struct {
	DeviceID             string `json:"device_id"`
	IdentityFilePath     string `json:"identity_file_path"`
	IdentityFileDeviceID string `json:"identity_file_device_id"`
	BeaconSelfDeviceID   string `json:"beacon_self_device_id"`
	// RosterSelfPresent 如实透传「roster 缓存里有本机条目」。按 PhoneServer
	// 契约恒为 false:GET /api/agent/mesh-roster 回包前会滤掉请求方自身
	// (meshIdentityRoutes.ts,其路由测试钉死「同账号全量(滤 self)」)——
	// 不是故障信号,不进 mismatches;本机身份是否上送成功看 core 日志里
	// endpoint 含 /api/agent/mesh-identity 的 agent_server_call 行。
	RosterSelfPresent bool     `json:"roster_self_present"`
	Mismatches        []string `json:"mismatches"`
}

// HandleIdentity GET /api/mesh/identity 身份一致性比对(读面批 Task 6):
// 四源只读取值,文件源健康时按钉死链式两两比对(state↔identity 文件、
// identity 文件↔beacon self)不一致各产一条人话描述;文件源缺失(路径或
// device_id 任一空)时链比较跳过,补 state↔beacon 直比回退对(I-1(a),
// 脑裂不因文件缺席漏报)。roster 不参与不一致判定(2026-11-24 审计定案):
// 服务端回包本就滤掉请求方自身,「已同步而缺本机条目」是每台健康设备的
// 常态,旧实现把它报成 mismatch 会让 mesh-runbook 的身份脑裂症状族指向
// 健康设备;roster_self_present 仍如实透传,同步态由 /api/mesh/diag 的
// roster_sync 段可见。全只读零副作用:身份文件
// 只读解析、state 只持锁读不生成(身份缺失=空串合法态,绝不走
// CurrentDeviceID 的生成链)。mismatches 恒数组(nil seam/全一致=空数组,
// 不 null)。
func (h *MeshDiagHandler) HandleIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// 同 HandleDiag:直传 405,不走 common.Error(业务码映射会变 500)。
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := meshIdentityResponse{Mismatches: []string{}}
	if h.stateDeviceID != nil {
		resp.DeviceID = h.stateDeviceID()
	}
	if h.identityFile != nil {
		resp.IdentityFilePath, resp.IdentityFileDeviceID = h.identityFile()
	}
	if h.beaconSelfID != nil {
		resp.BeaconSelfDeviceID = h.beaconSelfID()
	}
	// roster:在册回查以进程 state 的 self 为键(self 未知=无从查起,false
	// 合法)。结果只透传不判不一致(见函数注释)。
	rosterPresent := false
	if h.rosterSelfPresent != nil && resp.DeviceID != "" {
		rosterPresent = h.rosterSelfPresent(resp.DeviceID)
	}
	resp.RosterSelfPresent = rosterPresent

	// 链式比对钉死两组(评审拍板 ①②):state↔文件、文件↔beacon。
	// 文件源健康(路径与文件内 device_id 皆非空)时 state↔beacon 不单列——
	// 链式传递已覆盖其不一致信息,重复报会翻倍噪声。文件源缺失(路径或
	// device_id 任一空,I-1(a))时 ①② 跳过,补 state↔beacon 直比回退对——
	// 脑裂不因文件缺席而漏报,且如实交代「文件链不可用」。
	fileHealthy := resp.IdentityFilePath != "" && resp.IdentityFileDeviceID != ""
	if fileHealthy {
		if resp.DeviceID != "" && resp.DeviceID != resp.IdentityFileDeviceID {
			resp.Mismatches = append(resp.Mismatches, fmt.Sprintf(
				"state device_id=%s 与 identity 文件(%s)device_id=%s 不一致——进程内存态与常驻身份文件脱节(双身份脑裂现场,核对本机各进程各自的 identity 路径)",
				resp.DeviceID, resp.IdentityFilePath, resp.IdentityFileDeviceID))
		}
		if resp.BeaconSelfDeviceID != "" && resp.IdentityFileDeviceID != resp.BeaconSelfDeviceID {
			resp.Mismatches = append(resp.Mismatches, fmt.Sprintf(
				"identity 文件(%s)device_id=%s 与 beacon 广播 self=%s 不一致——beacon 用的身份与常驻身份文件脱节(邻居看到的 self 与本机记录不符)",
				resp.IdentityFilePath, resp.IdentityFileDeviceID, resp.BeaconSelfDeviceID))
		}
	} else if resp.DeviceID != "" && resp.BeaconSelfDeviceID != "" && resp.DeviceID != resp.BeaconSelfDeviceID {
		resp.Mismatches = append(resp.Mismatches, fmt.Sprintf(
			"身份文件源缺失,无法比对;进程 state 与 beacon self 不一致:%s vs %s(脑裂特征,文件链不可用)",
			resp.DeviceID, resp.BeaconSelfDeviceID))
	}
	common.Success(w, resp)
}
