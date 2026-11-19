// mesh_stun_probe_handler.go — POST /api/mesh/stun_probe 显式 STUN 活探测
// (组网诊断读面批 Task 7)。零请求体 POST(照 HandleSSHTest 的 POST-only
// 显式动作语义),现场新建独立短命 socket 逐台探测当前生效 STUN 清单,输出
// 分层结论(逐台结果+truncated+实耗时+默认档语境 note)。
//
// 铁律出处(先读再写的现场):
//   - punch/stun.go 文件头:探测 socket 必须调用方传入,函数内另建=TUN-on
//     下学错映射——本端点场景不同:它就是要新建独立短命 socket
//     (NewBoundUDPConn 出厂即绑物理口),绝不复用在途生产 conn(生产打洞
//     socket 归编排层,碰它=互偷包);
//   - punch/socket.go 绑定铁律:NewBoundUDPConn 失败即 500 上抛,绝不静默
//     回落未绑定 socket;
//   - agent_peer_punch.go:先 Classify 后 listen 顺序(本端点无 listen,天然
//     满足)。
//
// 并发闸:包级独立锁 TryLock(照 agent_peer_exec 的忙即回先例,但刻意不与
// agentPeerExecMu 共锁——两侧时长量级不同,共锁互挡),busy=200 信封结构化
// 返回绝不排队。逐台探测失败是正常结论不是服务端错误(逐台 err 如实透传,
// 照 HandleSSHTest 分层结论口径),只有 socket 绑定失败才 500。
//
// 预算链三层:probe 8s(本端点)→ forward 10s(mesh_forward_handler)→
// tool 14s;截断机制单点在 punch.ClassifyWithBudget(逐台查总预算,判实即
// 停,余台未测以 truncated 如实标注),handler 只传总预算不自己循环。
//
// 路由注册双处(parity 纪律):core routes.go + agentruntime server.go
// (POST /api/mesh/stun_probe,/api/mesh 前缀被 route_parity_test.go 护栏覆盖)。
package handlers

import (
	"net"
	"net/http"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/mesh/punch"
	"aliang.one/nursorgate/processor/config"
)

// meshStunProbeMu 显式活探测的进程级占位锁:同一时刻至多一条探测在跑。
// 刻意独立成锁、不与 agentPeerExecMu(peer-exec 串行槽)共锁——一次探测
// 最长 8s、一次 peer-exec 最长 600s,共锁互挡(探测挡住跨设备 exec,反之
// 亦然),两侧语义本就互不相干。TryLock 原子占位,忙立即回 busy,绝不排队。
var meshStunProbeMu sync.Mutex

// meshStunProbeBudget 单次探测总硬顶。用户配满 4 台全不可达最坏 12s>8s:
// 超出部分由 punch.ClassifyWithBudget 截断(每台启动前查剩余预算,耗尽即停,
// truncated 如实标注),不假装完整。
const meshStunProbeBudget = 8 * time.Second

// meshStunProbeBusyNote busy 态的人话语境:结构化返回,AI 消费方读到后
// 不应立即重试(前一条最长 8s 后自解)。
const meshStunProbeBusyNote = "另一探测在跑,勿立即重试"

// meshStunProbeNote 正常/截断态共用的默认档语境:默认档大量超时是常态
// 非故障(Google 副档国内裸网常不可达);truncated=部分台未测(预算耗尽或
// 对称判实即停),消费方按「结果不完整」读,不假装完整;symmetric 组合解读
// ——判实即停即定论,truncated 只说「余台未测」不削弱已得结论。
const meshStunProbeNote = "默认档大量超时是常态非故障;truncated=部分台未测;symmetric 结论判实即停即定论,truncated 不削弱已得结论"

// MeshStunProbeHandler POST /api/mesh/stun_probe。三缝全注入(测试隔离真
// 网络;生产钉真实现):
//   - servers   当前生效 STUN 清单(EffectiveStunServers;空/缺省回落默认档);
//   - newConn   新建独立短命 socket(出厂绑物理口,绝不复用在途生产 conn);
//   - probe     逐台探测(ClassifyWithBudget 语义,截断单点在 punch 包)。
type MeshStunProbeHandler struct {
	servers func() []string
	newConn func() (net.PacketConn, error)
	probe   func(conn net.PacketConn, servers []string, budget time.Duration) punch.ClassifyResult
}

// NewMeshStunProbeHandler 构造 handler,三缝钉生产实现:servers 走全局配置
// →customer.mesh→EffectiveStunServers(nil receiver 安全,空配置回落默认档
// 拷贝);newConn 走 punch.NewBoundUDPConn(punch.DefaultResolver)(绑定失败
// 即错,绝不静默回落);probe 走 punch.ClassifyWithBudget(net.PacketConn→
// *net.UDPConn 断言:NewBoundUDPConn 恒回 *net.UDPConn,断言失败只可能来自
// 注入错型,如实回逐台错误绝不静默成功)。
func NewMeshStunProbeHandler() *MeshStunProbeHandler {
	return &MeshStunProbeHandler{
		servers: func() []string {
			var mesh *config.MeshConfig
			if cfg := config.GetGlobalConfig(); cfg != nil && cfg.Customer != nil {
				mesh = cfg.Customer.Mesh
			}
			return mesh.EffectiveStunServers()
		},
		newConn: func() (net.PacketConn, error) {
			conn, _, err := punch.NewBoundUDPConn(punch.DefaultResolver)
			if err != nil {
				return nil, err
			}
			return conn, nil
		},
		probe: func(conn net.PacketConn, servers []string, budget time.Duration) punch.ClassifyResult {
			udp, ok := conn.(*net.UDPConn)
			if !ok {
				res := punch.ClassifyResult{Results: make([]punch.StunProbeEntry, 0, len(servers)), Truncated: true}
				for _, server := range servers {
					res.Results = append(res.Results, punch.StunProbeEntry{Server: server, Err: "probe conn 非 *net.UDPConn,无法探测(注入缝错型)"})
				}
				return res
			}
			return punch.ClassifyWithBudget(udp, servers, budget)
		},
	}
}

// NewMeshStunProbeHandlerWithSeams 注入式构造(路由级注册冒烟测试用,照
// NewMeshCoreForwardHandlerWithBase 先例):三缝整体替换,core routes 冒烟
// 经真 RegisterRoutes 断「接线在+方法闸真」而绝不真探 socket。生产接线一律
// 走 NewMeshStunProbeHandler(缝钉真实现)。
func NewMeshStunProbeHandlerWithSeams(
	servers func() []string,
	newConn func() (net.PacketConn, error),
	probe func(conn net.PacketConn, servers []string, budget time.Duration) punch.ClassifyResult,
) *MeshStunProbeHandler {
	return &MeshStunProbeHandler{servers: servers, newConn: newConn, probe: probe}
}

// meshStunProbeBusyData busy 态的 data 载荷(200 信封,结构化返回):
// busy=true+人话 note,AI 消费方据此不排队不重试。
type meshStunProbeBusyData struct {
	Busy bool   `json:"busy"`
	Note string `json:"note"`
}

// meshStunProbeEntry 单台探测结果(snake_case 契约):server/ok/学到的映射
// 端口/判型结论/失败原因。不可达台 ok=false+err 非空+nat_type 空(学不到
// 映射无从判型,空串即诚实)。
type meshStunProbeEntry struct {
	Server     string `json:"server"`
	OK         bool   `json:"ok"`
	MappedPort int    `json:"mapped_port"`
	NatType    string `json:"nat_type"`
	Err        string `json:"err"`
}

// meshStunProbeData 正常/截断态的 data 载荷:results 恒数组(空=一台都
// 未测,不 null);elapsed_ms=探测实耗时(键名刻意不用 budget_ms——装的是
// 实耗时非预算上限,预算由实现常量钉死,AI 消费方按「这次花了多久」读);
// truncated=true=结果不完整(余台未测),note 带默认档语境。
type meshStunProbeData struct {
	Results   []meshStunProbeEntry `json:"results"`
	Truncated bool                 `json:"truncated"`
	ElapsedMs int64                `json:"elapsed_ms"`
	Note      string               `json:"note"`
}

// HandleStunProbe POST /api/mesh/stun_probe 显式活探测。流程:method 闸 →
// TryLock(busy 即回,绝不排队)→ 读生效 STUN 清单 → 新建独立短命 socket
// (失败 500 上抛,绝不静默回落未绑定 socket)→ defer Close(恰一次)→
// 逐台探测(预算截断在 punch 包)→ 分层结论 200。
func (h *MeshStunProbeHandler) HandleStunProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// 同 HandleDiag/HandleLANPeers 惯例:common.Error 收业务码,直传
		// http.StatusMethodNotAllowed 会被映射成 500,此处须真实 405。
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !meshStunProbeMu.TryLock() {
		common.Success(w, meshStunProbeBusyData{Busy: true, Note: meshStunProbeBusyNote})
		return
	}
	defer meshStunProbeMu.Unlock()

	// 装配守卫(零值 handler/接线缺缝=真缺陷,fail loud 不假绿空答)。
	if h.newConn == nil || h.probe == nil {
		common.ErrorInternalServer(w, "stun probe 未装配(newConn/probe 注入缝缺失)", nil)
		return
	}

	servers := []string{}
	if h.servers != nil {
		servers = h.servers()
	}

	// 独立短命 socket:每请求新建,出厂即绑默认物理口;绑定失败即 500,
	// 绝不静默回落未绑定 socket(TUN-on 下裸 socket 学错映射,socket.go 铁律)。
	conn, err := h.newConn()
	if err != nil {
		common.ErrorInternalServer(w, err.Error(), nil)
		return
	}
	defer conn.Close() // 恰一次:新建→探测→关,handler 独占生命周期

	start := time.Now()
	res := h.probe(conn, servers, meshStunProbeBudget)
	elapsedMs := time.Since(start).Milliseconds()

	entries := make([]meshStunProbeEntry, 0, len(res.Results))
	for _, e := range res.Results {
		entries = append(entries, meshStunProbeEntry{
			Server:     e.Server,
			OK:         e.OK,
			MappedPort: e.MappedPort,
			NatType:    e.NatType,
			Err:        e.Err,
		})
	}
	common.Success(w, meshStunProbeData{
		Results:   entries,
		Truncated: res.Truncated,
		ElapsedMs: elapsedMs,
		Note:      meshStunProbeNote,
	})
}
