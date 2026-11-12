// listener.go 承载 punch B 侧常驻接收面:在调用方绑定好的 UDPConn 上起
// quic.Transport 的 QUIC listener,TTL 到点整体收摊;每条完成握手的会话
// (双向指纹钉扎在 quic-go 握手面先行裁决,见 tls.go ServerTLSConfig),
// 其首条流先过 TokenGate,过门后的每条流(含首条流内剩余字节)以
// net.Conn 语义交注入 Handler——B 侧既有入口 sshin.HandleIngressConn 吃
// 的就是 net.Conn(app/tunnel/manager.go:423 的 mesh relay 同款喂法)。
//
// 分工边界:本文件不做 STUN、不做物理接口绑定(conn 由服务编排层经
// NewBoundUDPConn 绑好传入,Task 10),也不做「首会话后收摊」——TTL 是
// v1 唯一收摊条件。取舍依据(Task 5 实锤):QUIC 空流不可见,对端连上但
// 不开流时 AcceptStream 永不醒,流级围栏无从挂起,「连上不开流」的占用
// 只能靠 TTL 收口;多会话各自过门天然支持(A 侧断线重拨=新会话再过门),
// 提前收摊没有收益只有编排复杂度。

package punch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

// punchListenerDefaultTTL 是 listener 整体寿命默认值:一次打洞协商配一个
// listener,A 侧 dialer(Task 7)在洞打通后立即拨;90s 覆盖协商尾差+A 侧
// 重拨余量,超出即判定本次打洞失败或被弃,收摊省资源。这也是防「连上不
// 开流」静默占用的唯一围栏(见文件头注释)。
const punchListenerDefaultTTL = 90 * time.Second

// punchGateErrorCode 是门禁拒单的 QUIC 应用层错误码。刻意不用 0(NO_ERROR):
// A 侧 dialer/编排层凭错误码把「token 被拒」从「B 侧正常收摊」里分辨出来,
// 排障时对得上因果。
const punchGateErrorCode quic.ApplicationErrorCode = 1

// ListenerOpts 是 ServeListener 的全部输入:TTL 到点整体收摊(零值走
// punchListenerDefaultTTL);Token 是首流门禁凭据(转发给 TokenGate,空串
// 在此 fail-closed);TLS 收拢两侧证书与钉扎指纹;Handler 收每条过门后的
// 流——每条流=一条「到 sshd 的连接」,listener 在独立 goroutine 里调用,
// 慢 Handler(如 HandleIngressConn 阻塞至会话结束)不阻塞 accept 面。
type ListenerOpts struct {
	TTL     time.Duration
	Token   string
	TLS     PunchTLSConfig
	Handler func(net.Conn)
	// Transport 调用方在传入 conn 上预建的 quic.Transport(单 Transport
	// 铁律的出让面,Task 10):B 侧编排要向 A 候选发探测 Dial——同一 conn
	// 上另建 Transport 会各起读循环互相偷包(dialer.go 文件头,Task 7 实锤),
	// 探测必须复用 listener 的同一实例,故由调用方预建并经此注入。nil=内部
	// 自建(既有调用方兼容)。收摊契约:无论谁建,Transport 都随收摊被
	// Close(与 conn 同寿命),调用方在 done 之后只关 conn,不得复用本实例。
	Transport *quic.Transport
}

// ServeListener 在绑定好的 conn 上起 B 侧 punch listener,立即返回本地
// 端口与 done 通道,接收循环在后台跑:TTL 到点(或 Transport 意外死亡)
// 时关 Transport、done 关闭。已受理的流随会话死亡自然收敛——收摊不通知
// Handler,Handler 的读写会以连接错误收场(这正是「流=连接、会话死=连接
// 死」的语义)。
//
// 所有权契约:conn 归调用方——ServeListener 不关它(quic-go 对非自建
// conn 的 Transport.Close 只停读循环不关底层 conn,transport.go:465),
// 调用方在 done 之后自行 Close 或复用;错 token 的会话被 CloseWithError
// 拒死,不影响其他会话与 TTL。
func ServeListener(conn *net.UDPConn, opts ListenerOpts) (localPort int, done <-chan struct{}, err error) {
	if conn == nil {
		return 0, nil, errors.New("punch listener: conn 为 nil")
	}
	if opts.Handler == nil {
		return 0, nil, errors.New("punch listener: Handler 为 nil")
	}
	// 空串 token 前置拒:TokenGate 对空期望也会 fail-closed(tls.go:152),
	// 但那要等到首条流进来才炸——配置错误在起听面就炸出来,不给「起了个
	// 每条流都拒的假 listener」留时间窗。
	if opts.Token == "" {
		return 0, nil, errors.New("punch listener: token 为空串——门禁不能形同虚设,fail-closed")
	}
	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		// *net.UDPConn.LocalAddr 恒返回 *net.UDPAddr,此分支理论不可达;
		// 留着只为 fail-loud,不做静默断言panic。
		return 0, nil, fmt.Errorf("punch listener: 本地地址非 UDP: %T", conn.LocalAddr())
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = punchListenerDefaultTTL
	}

	// 单 Transport 铁律:注入实例优先(见 ListenerOpts.Transport),否则
	// 内部自建——两种来源的收摊归宿一致(punchAcceptLoop 统一 Close)。
	tr := opts.Transport
	if tr == nil {
		tr = &quic.Transport{Conn: conn}
	}
	ln, err := tr.Listen(ServerTLSConfig(opts.TLS), nil)
	if err != nil {
		return 0, nil, fmt.Errorf("punch listener: 起 QUIC listener: %w", err)
	}
	doneCh := make(chan struct{})
	go punchAcceptLoop(tr, ln, ttl, opts.Token, opts.Handler, doneCh)
	return udpAddr.Port, doneCh, nil
}

// punchAcceptLoop 是收摊调度:accept 会话的循环跑在子 goroutine,本
// goroutine 只等两件事之一——TTL 到点(正常收摊)或 accept 报错(现实中
// 只有 conn 被调用方关闭/Transport 死亡才会走到,同样收摊,v1 不自愈)。
// 收摊动作=关 Transport:listener 与全部已建会话 abrupt 终止(quic-go
// Transport.Close 语义),已交 Handler 的流随会话死亡收敛。
func punchAcceptLoop(tr *quic.Transport, ln *quic.Listener, ttl time.Duration, token string, handler func(net.Conn), done chan struct{}) {
	defer close(done)
	timer := time.NewTimer(ttl)
	defer timer.Stop()

	// acceptErr 用 close 信号语义:accept goroutine 退出时 close,错误值
	// 本身只是随发的一次性线索(收摊原因上层自有见证)。join 侧因此在对称
	// 两条路径上都必然返回——TTL 先至:Transport 关闭唤醒 Accept 报错,发
	// 送+close 依次落地;accept 报错先至:select 已把值收走也无妨,通道
	// close 后的接收立即返回零值,不存在「唯一的值被消费、二次收永久挂死」。
	acceptErr := make(chan error, 1)
	go func() {
		defer close(acceptErr)
		for {
			qconn, err := ln.Accept(context.Background())
			if err != nil {
				acceptErr <- err // 缓冲 1 且生命周期内唯一一次发送,必不阻塞
				return
			}
			go punchServeConn(qconn, token, handler)
		}
	}()

	select {
	case <-timer.C:
	case <-acceptErr:
	}
	_ = tr.Close()
	<-acceptErr // join accept goroutine:close 语义下两分支都必然返回
}

// punchServeConn 服务单条已过握手的会话:首条 AcceptStream 到的流先过
// TokenGate——门禁只消费 token 行,流内剩余字节(SSH 版本串等)与后续
// 流全归 Handler;拒单=CloseWithError 关整条会话(token 是本会话唯一
// 凭据,首条不过门,后续流没有放行依据),客户端侧观察到连接死亡。
//
// 门禁在本 goroutine 内联跑:半截 token 行吊着时,本会话的后续流最多
// 陪绑到 punchTokenGateTimeout(5s 读围栏,tls.go:35),不碰其他会话与
// 主 accept 面。会话死亡(TTL 收摊/对端关闭)时 AcceptStream 报错,静默
// 退出——死亡原因上层自有见证(done 通道/客户端侧),无遗言可留。
func punchServeConn(qconn *quic.Conn, token string, handler func(net.Conn)) {
	gated := false
	for {
		stream, err := qconn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		if !gated {
			if err := TokenGate(stream, token); err != nil {
				// 分工铁律(tls.go:140):TokenGate 只判定,关门归本层。
				_ = qconn.CloseWithError(punchGateErrorCode, "punch token gate rejected")
				return
			}
			gated = true
		}
		s := stream
		go func() {
			defer func() {
				// panic 隔离:Handler 是注入件,炸了只赔本流不赔接收面。
				// 留一行日志而非静默吞——fail-loud 至少要在日志面现形。
				if r := recover(); r != nil {
					log.Printf("[PUNCH-LISTENER] handler panic 已隔离(stream=%d): %v", s.StreamID(), r)
				}
			}()
			defer s.Close() // Handler 返回后兜底关本流(不关会话)
			handler(&punchStreamConn{stream: s, conn: qconn})
		}()
	}
}

// punchStreamConn 把 quic.Stream 适配成 net.Conn:Handler 一族(sshin.
// HandleIngressConn)拿到标准连接语义。Close 只关本流不关会话(quic-go
// Stream.Close 只关发送方向,恰与「每条流=一条独立连接」的生死语义一致);
// LocalAddr/RemoteAddr 取自所属会话——quic.Stream 自身不携带地址。
type punchStreamConn struct {
	stream *quic.Stream
	conn   *quic.Conn
}

// 编译期契约:punchStreamConn 必须是完整 net.Conn,缺方法在此现形。
var _ net.Conn = (*punchStreamConn)(nil)

func (c *punchStreamConn) Read(p []byte) (int, error)  { return c.stream.Read(p) }
func (c *punchStreamConn) Write(p []byte) (int, error) { return c.stream.Write(p) }
func (c *punchStreamConn) Close() error                { return c.stream.Close() }
func (c *punchStreamConn) LocalAddr() net.Addr         { return c.conn.LocalAddr() }
func (c *punchStreamConn) RemoteAddr() net.Addr        { return c.conn.RemoteAddr() }

func (c *punchStreamConn) SetDeadline(t time.Time) error      { return c.stream.SetDeadline(t) }
func (c *punchStreamConn) SetReadDeadline(t time.Time) error  { return c.stream.SetReadDeadline(t) }
func (c *punchStreamConn) SetWriteDeadline(t time.Time) error { return c.stream.SetWriteDeadline(t) }
