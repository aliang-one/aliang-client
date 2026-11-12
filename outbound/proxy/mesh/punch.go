// punch.go 承担 mesh 出口的 UDP 打洞载体(第三载体,SPEC §7.1 载体表):
// 到对端 sshd 的连接经打洞后的 QUIC/UDP 承载抵达,本 Transport 只做准入
// 判定与注入式拨号——打洞本身(SessionCache lease 复用、信令编排)全在
// 上层 app/mesh/punch + services,经 PunchDialFunc 闭包注入,本包不
// import punch 子树(单向依赖,与 PikoDialFunc 同模式,SPEC §7.3)。
package mesh

import (
	"context"
	"errors"
	"net"
)

// errPunchDialNotWired 是 gate 关变体被误调 DialSSHD 的防御错误:正常路径
// 下零值 UDPTransport 根本进不了 transports 切片(装配门),Admit 恒 false
// 又会先挡一道;走到这里说明两层把关都被绕过,错误而不是 panic。
var errPunchDialNotWired = errors.New("mesh: udp transport dial func not wired")

// PunchDialFunc 拨打洞承载(直连语义,SPEC §4.2 载体三)。签名收裸
// peerID(= peer.DeviceID),与 PikoDialFunc 的拼装责任划分不同——piko 的
// `<deviceID>.mesh` 拼装在 PikoTransport,打洞没有端点后缀概念。生产实现
// 由上层注入(Task 10 编排):SessionCache 已建立则 lease.OpenStream,
// 未建立则现场 DialAndPut。
type PunchDialFunc func(ctx context.Context, peerID string) (net.Conn, error)

// UDPTransport UDP 打洞载体:name "udp"。准入:全类别(self/ai/custom 三类
// 全放——打洞是双方设备点对点直连,不经公共中继,custom 类无「禁公共
// 中继」约束,SPEC §7.1 表 SPEC:560 预留语义,已拍板)。IsRelay=false:
// 直连语义,拨号失败与 LAN 错误同桶(DialContext 分桶)。gate 关闭的
// 构造变体(dial=nil)Admit 恒 false——装配期不进切片是第一层把关
// (NewExitClient,piko 的 relay 门同款),此处防御是第二层,双层把关。
type UDPTransport struct {
	dial PunchDialFunc // nil = gate 关(防御分支:Admit 恒 false)
}

func (UDPTransport) Name() string { return "udp" }

func (t UDPTransport) Admit(_ string, _ PeerSpec) bool {
	return t.dial != nil // gate 开=全类别放行;gate 关=谁也不放(双层把关第二层)
}

func (UDPTransport) IsRelay() bool { return false }

func (t UDPTransport) DialSSHD(ctx context.Context, peer PeerSpec) (net.Conn, error) {
	if t.dial == nil {
		return nil, errPunchDialNotWired
	}
	return t.dial(ctx, peer.DeviceID)
}
