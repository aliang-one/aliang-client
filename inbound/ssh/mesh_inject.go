// mesh_inject.go — 形态②(mesh 帧外壳)注入路径:帧头在 channel 数据流
// 首行(不在 direct-tcpip payload 里——A 侧可在一条 SSH 连接上为多条转发
// 连接复用多 channel,帧头按 channel 计,spec §2),其后原始字节双向转发。
package ssh

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	tcphandler "aliang.one/nursorgate/processor/tcp"

	gossh "golang.org/x/crypto/ssh"
)

// mesh 拒帧原因短语(spec §6.4 G5 收口:SSH 载体与 TCP 载体统一纯文本
// 行,短语对齐 HandleMeshConn 的拒绝文案)。窄且稳定,不回写内部细节
// ——对端实现/节点身份不泄漏。语义与 mesh 分支 B 侧「错误回源」一致:
// 一行错误行后关流。
const (
	meshRejectDNSUnsupported  = "dns target not supported on ssh carrier"
	meshRejectForwardDisabled = "forward disabled (customer.mesh.forward_enable)"
	meshRejectNotAllowedFmt   = "forwarder %q not allowed"
	meshRejectLoopFmt         = "meshproto: loop detected via %s"
	meshRejectHopsFmt         = "meshproto: hop limit %d reached"
)

// meshFrameDefaultPort 是帧目标不带端口时的兜底端口(裸域名/裸 IP 直转
// 语义,443;与 mesh 分支 meshproto.SplitTarget 的调用侧口径一致)。
const meshFrameDefaultPort = 443

// IngressFrameMeta 是形态② 帧元数据经 context 携带的接缝载体
// (spec §2:master 的 Metadata 与 ProxyRoute 枚举无 Route/Via/Hops 字段——
// 它们随 mesh 分支落地,本分支不得自行扩;携带机制=本包导出的 context
// 载体)。本分支只负责挂载与透传;消费侧(mesh 分支 Task 7 的 dialByRoute
// 续传)是合并后集成契约,测试断言的是 context 值挂载正确,非 Metadata 字段。
type IngressFrameMeta struct {
	Class string   // 流量类别:self|ai|custom
	Via   []string // AppendHop 后的 path vector(含本机)
	Hops  int      // AppendHop 后的跳数
}

// ingressFrameMetaKey 是 ContextKeyIngressFrameMeta 的具名键类型:键相等性
// 由类型唯一性保证,不受字符串字面量碰撞影响(net/http contextKey 同款)。
type ingressFrameMetaKey struct{}

// ContextKeyIngressFrameMeta 是从 context 读写 *IngressFrameMeta 的键
// (context.WithValue / ctx.Value)。以导出 var 而非字符串 const 承载:
// context 键的推荐形态是具名类型(spec「导出常量」的意图=导出键名)。
//
// 集成期注记:合并后 dialByRoute(processor/tcp 侧)回读本键时,Go 禁止
// 反向 import inbound/ssh(环)——集成批次需把 IngressFrameMeta 与本键迁至
// 共享低层包(或 processor/tcp 内;这不违反「不给 Metadata 加字段/扩枚举」
// 的分支禁令,见 spec §2 集成期注记)。
var ContextKeyIngressFrameMeta = ingressFrameMetaKey{}

// injectMeshFrame 是形态② 注入路径(每 channel 一帧):
//
//	解析帧头(失败=断开不兜底)→ dns: 拒 → ForwardGate 门禁(nil=全放行,
//	合并集成缝)→ LocalNodeID 未注入防御 → 防环(Via 含本机即拒)→
//	跳数帽(hops>=8 拒)→ AppendHop(本机 ID)→ IngressFrameMeta 挂 ctx →
//	构 Metadata(裸域名保留不解析)→ 喂 processor 统一管线。
//
// 阻塞语义与 injectBareStream 同:独立 goroutine 驱动(不入 WaitGroup),
// 在途 Handle 靠连接级 ctx 取消+连接拆除有界退出。
func injectMeshFrame(ctx context.Context, ch gossh.Channel, connRemote net.Addr, rc RunConfig) {
	defer ch.Close()

	reader := bufio.NewReaderSize(ch, meshFrameHeaderLimit)
	hdr, err := ParseMeshFrameHeader(reader)
	if err != nil {
		// 帧头解析失败:静默断开,不写错误行(spec §5)。
		logger.Debug(fmt.Sprintf("SSH ingress mesh frame header rejected: %v", err))
		return
	}

	// dns: 目标一期拒连(spec §2/§5):代解析语义归 mesh 分支 Task 8,
	// 合并后集成批次统一启用,本分支不做半套。
	if strings.HasPrefix(hdr.Target, "dns:") {
		logger.Debug(fmt.Sprintf("SSH ingress mesh frame from=%s rejected: dns target not supported on ssh carrier", hdr.From))
		writeMeshRejectLine(ch, meshRejectDNSUnsupported)
		return
	}

	// ForwardGate 门禁(design §8,文案区分):nil=全放行(合并集成缝:
	// 本分支 mesh 凭证列表即 ACL,防御已含);非 nil:总开关关→
	// forward-disabled;来源 device_id 不在允许列表→not-allowed。
	if rc.ForwardGate != nil {
		enabled, allowed := rc.ForwardGate()
		if !enabled {
			logger.Debug(fmt.Sprintf("SSH ingress mesh frame from=%s rejected: forward disabled", hdr.From))
			writeMeshRejectLine(ch, meshRejectForwardDisabled)
			return
		}
		if !forwarderAllowed(allowed, hdr.From) {
			logger.Debug(fmt.Sprintf("SSH ingress mesh frame from=%s rejected: not an allowed forwarder", hdr.From))
			writeMeshRejectLine(ch, fmt.Sprintf(meshRejectNotAllowedFmt, hdr.From))
			return
		}
	}

	// 纵深防御:LocalNodeID 未注入(装配未完成)或返回空→形态②整体禁用
	// (config.go 契约)。错误码并入 forward-disabled:本节点 mesh 转发面
	// 不可用,与总开关关同观感。
	localID := ""
	if rc.LocalNodeID != nil {
		localID = rc.LocalNodeID()
	}
	if localID == "" {
		logger.Debug("SSH ingress mesh frame rejected: local node id unconfigured (form mesh disabled)")
		writeMeshRejectLine(ch, meshRejectForwardDisabled)
		return
	}

	// 防环层 1 主防线(design §7):帧头 Via 已含本机=环路,立即拒。
	for _, id := range hdr.Via {
		if id == localID {
			logger.Debug(fmt.Sprintf("SSH ingress mesh frame from=%s rejected: loop via %s", hdr.From, localID))
			writeMeshRejectLine(ch, fmt.Sprintf(meshRejectLoopFmt, localID))
			return
		}
	}
	// 防环层 2 跳数硬顶:路径字段异常时的数值保险。
	if hdr.Hops >= MeshHopsHardCap {
		logger.Debug(fmt.Sprintf("SSH ingress mesh frame from=%s rejected: hops %d reached cap %d", hdr.From, hdr.Hops, MeshHopsHardCap))
		writeMeshRejectLine(ch, fmt.Sprintf(meshRejectHopsFmt, MeshHopsHardCap))
		return
	}
	hdr.AppendHop(localID)

	// 目标拆解:host:port→HostName/DstPort;裸域名(无端口)保留不解析
	// (末跳代解析归 mesh 集成批次),按 443 兜底;DstIP 不设——与形态① 及
	// inbound/http CONNECT 对不可解析域名同口径,管线隧道期重试解析。
	host, portStr, splitErr := net.SplitHostPort(hdr.Target)
	if splitErr != nil {
		host, portStr = hdr.Target, strconv.Itoa(meshFrameDefaultPort)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || port == 0 {
		// 畸形端口按坏帧处理:静默断开(同帧头解析失败口径)。
		logger.Debug(fmt.Sprintf("SSH ingress mesh frame from=%s rejected: bad target %q", hdr.From, hdr.Target))
		return
	}

	// 源地址取 SSH 连接层 RemoteAddr(与形态① 同语义):mesh 节点连入的
	// 记账依据,帧头 From 是对端自报的节点身份,二者用途不同、各自保留。
	metadata := &M.Metadata{
		Network:  M.TCP,
		ConnID:   fmt.Sprintf("sshing-%d", atomic.AddUint64(&sshIngressConnCounter, 1)),
		HostName: host,
		DstPort:  uint16(port),
	}
	metadata.SrcIP, metadata.SrcPort = netAddrToIPPort(connRemote)

	// 帧元数据挂 ctx(AppendHop 后的 Via/Hops),接缝契约见 IngressFrameMeta。
	framedCtx := context.WithValue(ctx, ContextKeyIngressFrameMeta, &IngressFrameMeta{
		Class: hdr.Class,
		Via:   hdr.Via,
		Hops:  hdr.Hops,
	})

	conn := newMeshChannelConn(ch, connRemote, host, uint16(port), reader)
	logger.Debug(fmt.Sprintf("SSH ingress mesh frame injected conn_id=%s from=%s class=%s target=%s:%d via=%v hops=%d",
		metadata.ConnID, hdr.From, hdr.Class, host, port, hdr.Via, hdr.Hops))
	if err := tcphandler.GetHandler().Handle(framedCtx, conn, metadata); err != nil {
		logger.Debug(fmt.Sprintf("SSH ingress mesh frame conn_id=%s handle: %v", metadata.ConnID, err))
	}
}

// writeMeshRejectLine 向 channel 回写一行纯文本拒绝(reason 已格式化),
// 经 channelConn 适配后委托 processor/tcp.WriteMeshRejectLine——与 TCP 载体
// 同一实现(G5 收口):行格式/单行转义/写窗口完全同源,对端按同一套解析。
// 写入尽力而为:gossh.Channel 无 deadline 语义(channelConn 的 SetWriteDeadline
// 为 no-op),错误行单条极小,失联场景由连接拆除/连接级 ctx 取消兜底,不因
// 回写卡死拒帧路径。
func writeMeshRejectLine(ch gossh.Channel, reason string) {
	conn := &channelConn{Channel: ch, local: &net.TCPAddr{}, remote: &net.TCPAddr{}}
	tcphandler.WriteMeshRejectLine(conn, reason)
}

// forwarderAllowed 报告来源 device_id 是否在 ForwardGate 允许列表。
func forwarderAllowed(allowed []string, from string) bool {
	for _, id := range allowed {
		if id == from {
			return true
		}
	}
	return false
}

// meshChannelConn 在 channelConn 之上回放帧头后的 bufio 预读字节:帧头与
// 首段载荷可能一次写出,ParseMeshFrameHeader 的 reader 已把载荷部分读进
// 缓冲,注入管线必须先读回放字节再读 channel,否则丢首段(mesh 分支
// WrappedConn 同款坑,TestMeshFormAppendsHopAndInjects 钉住)。
type meshChannelConn struct {
	*channelConn
	preread []byte
}

// newMeshChannelConn 构造形态② 注入 conn。LocalAddr=帧头目标、RemoteAddr=
// SSH 连接层源(与形态① 取 payload 四元组不同:mesh 帧的 direct-tcpip
// payload 是任意值,地址语义由帧头与连接层承载)。
func newMeshChannelConn(ch gossh.Channel, connRemote net.Addr, host string, port uint16, reader *bufio.Reader) *meshChannelConn {
	var preread []byte
	if reader.Buffered() > 0 {
		// n==Buffered() 时 Peek 不会失败(请求不超过已缓冲字节数),忽略 err。
		leftover, _ := reader.Peek(reader.Buffered())
		preread = append([]byte(nil), leftover...)
	}
	return &meshChannelConn{
		channelConn: &channelConn{
			Channel: ch,
			local:   &net.TCPAddr{IP: net.ParseIP(host), Port: int(port)},
			remote:  connRemote,
		},
		preread: preread,
	}
}

func (c *meshChannelConn) Read(p []byte) (int, error) {
	if len(c.preread) > 0 {
		n := copy(p, c.preread)
		c.preread = c.preread[n:]
		return n, nil
	}
	return c.Channel.Read(p)
}
