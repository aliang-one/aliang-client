// mesh_inject.go — 形态②(mesh-exit channel)注入路径:payload 走 SSH
// channel ExtraData(SPEC §4,无长度歧义、无首段预读坑——旧帧协议的
// meshChannelConn/preread 回放机制随帧协议删除),身份由公钥承载
// (Permissions.Extensions),注入本机入站管线(Route=RouteMesh 溯源 +
// MeshInjected=true 防环,SPEC §6.2)。
package ssh

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	tcphandler "aliang.one/nursorgate/processor/tcp"

	gossh "golang.org/x/crypto/ssh"
)

// channelMeshExit 是 mesh 形态唯一放行的 channel 名(与出口侧
// meshproxy.ChannelMeshExit 同值在线协议,两处字面量由集成测试互锁)。
const channelMeshExit = "mesh-exit"

// meshExitMaxExtra 是 mesh-exit ExtraData 上限(与出口侧同值,测试互锁)。
const meshExitMaxExtra = 512

// meshExitTarget 是 mesh-exit ExtraData 的解码形态(t=tcp 带 h/p;t=dns 无
// 其它字段,SPEC §4.1)。
type meshExitTarget struct {
	T   string   `json:"t"`
	H   string   `json:"h"`
	P   uint16   `json:"p"`
	Via []string `json:"v,omitempty"` // 出口链路经停链(与出口侧 exitMeta.v 同形)
}

// handleMeshExitChannel 是 mesh form 的 channel 分派:ExtraData cap →
// payload 解析 → ForwardGate fail-closed 门禁(SPEC §10.1 决策 #14)全部
// 在 Accept 前完成(Reject reason 回给 A 侧 OpenChannel);Accept 后按 t
// 分流(dns→代解析不进管线;tcp→注入管线)。
func handleMeshExitChannel(ctx context.Context, newCh gossh.NewChannel, extra []byte, rc RunConfig, meshDeviceID string, connRemote net.Addr) {
	reject := func(msg string) {
		logger.Debug(fmt.Sprintf("SSH ingress mesh-exit rejected (%s) from %s", msg, meshDeviceID))
		_ = newCh.Reject(gossh.Prohibited, msg)
	}
	if len(extra) > meshExitMaxExtra {
		reject("payload too large")
		return
	}
	var tgt meshExitTarget
	if err := json.Unmarshal(extra, &tgt); err != nil {
		reject("bad payload")
		return
	}
	switch tgt.T {
	case "tcp":
		if tgt.H == "" || tgt.P == 0 {
			reject("bad payload: h/p required for tcp target")
			return
		}
	case "dns":
		// 无其它字段(SPEC §4.1:域名只在帧内,B 侧自己解问)
	default:
		reject("unknown payload type")
		return
	}
	// ForwardGate:fail-closed(SPEC §10.1)——nil=装配层未接线=拒绝,
	// 而非旧「全放行」。
	if rc.ForwardGate == nil || !rc.ForwardGate() {
		reject("forward disabled (customer.mesh.forward_enable)")
		return
	}

	ch, chReqs, err := newCh.Accept()
	if err != nil {
		logger.Debug(fmt.Sprintf("SSH ingress mesh-exit accept failed: %v", err))
		return
	}
	go gossh.DiscardRequests(chReqs)

	if tgt.T == "dns" {
		// 缝值(解析器/交换窗口)在分派点(与调用方同 goroutine,同步段)
		// 快照传入:注入协程内不触碰包级 var,与测试的覆写/还原严格免竞态。
		go serveMeshDNSExit(ch, meshDNSResolveTimeout, meshDNSResolver)
		return
	}
	go injectMeshExitStream(ctx, ch, connRemote, tgt, meshDeviceID)
}

// injectMeshExitStream 把 mesh-exit TCP 流注入本机入站管线(SPEC §4.1 注入
// 落点:直接复用 channelConn 字段字面量,无适配器;源地址取连接层)。
func injectMeshExitStream(ctx context.Context, ch gossh.Channel, connRemote net.Addr, tgt meshExitTarget, meshDeviceID string) {
	defer ch.Close()

	md := &M.Metadata{
		Network:      M.TCP,
		ConnID:       fmt.Sprintf("sshing-%d", atomic.AddUint64(&sshIngressConnCounter, 1)),
		HostName:     tgt.H, // ExtraData 里的 h(已拆端口)
		DstPort:      tgt.P,
		Route:        tcphandler.RouteMesh, // 决策前统计口径(SPEC §4.1 附注)
		MeshInjected: true,                 // §6.2 注入溯源,不被决策覆写
		MeshVia:      tgt.Via,              // 出口链路经停链(委托再上链时防环判定读它)
	}
	md.SrcIP, md.SrcPort = netAddrToIPPort(connRemote)

	conn := &channelConn{
		Channel: ch,
		local:   &net.TCPAddr{IP: net.ParseIP(tgt.H), Port: int(tgt.P)}, // 域名→IP nil,仅日志展示
		remote:  connRemote,
	}
	logger.Debug(fmt.Sprintf("SSH ingress mesh-exit injected conn_id=%s from=%s target=%s:%d",
		md.ConnID, meshDeviceID, tgt.H, tgt.P))
	if err := tcphandler.GetHandler().Handle(ctx, conn, md); err != nil {
		logger.Debug(fmt.Sprintf("SSH ingress mesh-exit conn_id=%s handle: %v", md.ConnID, err))
	}
}
