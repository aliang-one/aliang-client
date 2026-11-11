// mesh_dispatch.go — 统一口 mesh 帧分派接线(spec §6.5):56432 嗅探为
// mesh JSON 帧头的连接交给 HandleMeshConn(carrier=lan)。core boot 调用一次;
// mesh 关闭态/转发关由 HandleMeshConn 帧门禁拒收(拒绝行回源),无需在此
// 判断。设备身份每连接现读(闭包)——会话期 device_id 常驻不变,读时求值
// 不在接线时快照。
package tunnel

import (
	"net"
	"strings"

	"aliang.one/nursorgate/app/mesh/relay"
	httpIngress "aliang.one/nursorgate/inbound/http"
	"aliang.one/nursorgate/outbound/meshproto"
	"aliang.one/nursorgate/processor/tcp"
)

// WireUnifiedIngressMeshDispatch 把统一口的 mesh 帧处理器接为
// HandleMeshConn(carrier="lan")。独立 LAN 载体口(56441)退役后,LAN 帧的
// 唯一接收面即本接线;可用性随代理运行态(D4),门禁语义与中继载体完全
// 同一套(HandleMeshConn 内部 AppendHop/ForwardEnabled/PeerAllowed/class×
// carrier 检查零复制)。
func WireUnifiedIngressMeshDispatch(deviceID func() string) {
	httpIngress.MeshConnHandler = func(conn net.Conn) {
		selfID := strings.TrimSpace(deviceID())
		_ = tcp.HandleMeshConn(conn, meshproto.CarrierLAN, relay.ProductionDeps(selfID)())
	}
}
