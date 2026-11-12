// mesh_delegate 出口链路(2026-11-12 用户拍板,替代离线兜底)的 tcp 侧
// 落点:开关开(config.MeshDelegateNextHop)且链守卫通过(via 不含自己、
// 未达经停上限)时,toAliang 类明文流量(AI 56432 + 非 TLS toAliang 命中)
// 与平台自身出口(processor/egress)改道交给下一跳——触发为常驻语义,
// 不按本机能否直连平台做限制,离线信号缝已随旧门控一并移除。
package tcp

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync/atomic"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/config"
)

// meshSelfIDSource 本机节点标识注入缝(装配层 = Manager.deviceID,与
// inbound/ssh LocalNodeID 同源同值):via 防环链的追加与判定都要本机
// device_id。未接线(空)时委托能力不成立(fail-closed:没有身份就无法
// 防环,宁可不上链)。
var meshSelfIDSource atomic.Value // func() string

// SetMeshSelfIDSource 注入本机 device_id 提供器;nil = 清除(空 = 委托
// 不成立)。并发安全(生产写方为装配接线路径,读方为每次委托判定)。
func SetMeshSelfIDSource(fn func() string) {
	if fn == nil {
		fn = func() string { return "" }
	}
	meshSelfIDSource.Store(fn)
}

func meshSelfID() string {
	fn, _ := meshSelfIDSource.Load().(func() string)
	if fn == nil {
		return ""
	}
	return strings.TrimSpace(fn())
}

// MeshSelfIDForTest 读回本机节点标识缝的当前值(生产读方走 meshSelfID):
// 导出仅供 app/tunnel 装配断言测试钉「selfID 已接线且等于配置 DeviceID」。
func MeshSelfIDForTest() string { return meshSelfID() }

// MeshDelegateActive 出口链路委托是否激活的跨包查询(探针降级等消费方
// 入口):与数据面判定同源同真假——即 config.MeshDelegateNextHop 的布尔
// 投影,不存在第二份判定逻辑。
func MeshDelegateActive() bool {
	_, ok := config.GetGlobalConfig().MeshDelegateNextHop()
	return ok
}

// meshDelegateGuard 链守卫(防环+防放大):via 已含本机 id → 回环(此
// 连接曾经过本机,再上链只会打转);via 长度达 max_hops → 放大上限。
// 两态都返回 false(调用方就地处理,不报错——链语义是「继续发给下一跳
// 直到没有下一跳」,守卫命中即本机是链的终点)。本机 id 未接线同样拒绝
// (fail-closed)。返回的第二值在 ok 时是下一跳 device_id,否则是拒绝原因。
func meshDelegateGuard(md *M.Metadata) (bool, string) {
	self := meshSelfID()
	if self == "" {
		return false, "self id unavailable"
	}
	cfg := config.GetGlobalConfig()
	peer, ok := cfg.MeshDelegateNextHop()
	if !ok {
		return false, "delegate toggle off or next_hop invalid"
	}
	for _, via := range md.MeshVia {
		if strings.TrimSpace(via) == self {
			return false, "via loop (own id already in chain)"
		}
	}
	if len(md.MeshVia) >= cfg.Customer.Mesh.EffectiveMaxHops() {
		return false, "via chain at max hops"
	}
	return true, strings.TrimSpace(peer.DeviceID)
}

// meshDelegateEligible 委托资格判定:守卫通过即可上链。注入连接
// (MeshInjected)允许再上链——这正是链式转发的推进方式;回环/超限由
// 守卫拒掉,拒掉后调用方就地处理(链的终止语义)。
func (h *TCPConnectionHandler) meshDelegateEligible(md *M.Metadata) bool {
	if md == nil {
		return false
	}
	ok, _ := meshDelegateGuard(md)
	return ok
}

// dialViaMeshDelegate 委托拨出:下一跳即 peers 中已钉扎的节点(保存期
// Validate 强制),复用 getMeshExitForExecution 的 ExitClient 装配(LAN/
// Piko 载体、钉扎、身份与规则路径同一套)——不再有独立的 fallback.addr
// 通道。拨出前把本机 id 追加进 via(接收端据其判回环),payload 由
// ExitClient 从 metadata 构造(md.MeshVia → payload.via)。
func (h *TCPConnectionHandler) dialViaMeshDelegate(ctx context.Context, metadata *M.Metadata) (net.Conn, error) {
	ok, nextID := meshDelegateGuard(metadata)
	if !ok {
		return nil, fmt.Errorf("mesh: delegate guard rejected (%s)", nextID)
	}
	client, err := h.getMeshExitForExecution(nextID)
	if err != nil {
		return nil, fmt.Errorf("mesh: delegate exit client for %s: %w", nextID, err)
	}
	metadata.MeshVia = append(slices.Clone(metadata.MeshVia), meshSelfID())
	conn, err := client.DialContext(ctx, metadata)
	if err != nil {
		return nil, fmt.Errorf("mesh: delegate dial via %s: %w", nextID, err)
	}
	metadata.Route = "RouteMeshDelegate"
	return conn, nil
}
