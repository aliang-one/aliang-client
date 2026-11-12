// dialer.go 承载 punch A 侧拨号编排:对 peer 候选地址循环拨 QUIC——QUIC
// Initial 本身就是探测包,不再造独立 probe 包;预算内打通即返回会话,
// 预算耗尽返回 ErrPunchTimeout(上层语义=落 piko 中继兜底,非错误态)。
//
// 单 Transport 铁律:整场编排只在传入 conn 上建一个 quic.Transport,各轮
// 各候选的 Dial 全部复用它——quic-go 文档明言「A single net.PacketConn can
// only be handled by one Transport」,多个 Transport 同 conn 会各起读循环
// 互相偷包(与 Task 4 弃用 pion stun.Client 同类事故:残留读者偷走打洞
// 报文);一 conn 多 Dial 是 Transport 的原生形态(按连接 ID 分发,Close
// 之后才不能再拨)。
//
// 拒因三分(上游实锤):B 拒 token=APPLICATION_ERROR code 1 → 专用哨兵
// ErrPunchGateRejected 且不重试(本 token 永久无效,重试只烧预算);传输
// 死亡/超时=可重试,预算内继续轮询;TLS 1.3 拒单时序不对称(Task 5 实锤:
// 握手可能先「成功」随即死)=拨完做存活确认,短宽限窗内会话死亡即失败并
// 按前两类分派,绝不 Dial 返回就当成功。
//
// 分工边界:本文件不做 STUN/Classify(编排层,Task 10)也不做 B 侧接收面
// (listener.go);conn 由调用方绑好传入(socket.go),所有权归调用方
// ——dialer 失败收摊只 Close 自建 Transport(quic-go 语义:不关底层 conn),
// 打通后 Transport 随会话出让,由调用方收摊。
package punch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

// DialBudget 是 DialPeer 的默认总预算:耗尽未打通即返回 ErrPunchTimeout。
// 2.5s≈8 轮重拨(轮间隔 300ms),给「信令候选晚到+前几轮洞未开」留翻盘
// 窗口;再大就要让上层干等更久才落 piko,得不偿失。
const DialBudget = 2500 * time.Millisecond

// 三个节拍参数是包变量而非配置项——仅为同包测试能调小避免真等,生产语义
// 是常量(classifyDiscoverTimeout/punchTokenGateTimeout 既定先例)。
var (
	// punchDialRoundInterval 轮与轮的间隔:给信令送达与 NAT 映射生效留
	// 时间,也把探测发包节奏摊开。
	punchDialRoundInterval = 300 * time.Millisecond
	// punchDialAttemptTimeout 单次 Dial(含握手)围栏:洞未开时 Initial
	// 石沉大海,围栏到点即放弃该候选换道;握手在洞开后 1~2 个 RTT 内完成,
	// 750ms 对常见 mesh RTT 余量充分。
	punchDialAttemptTimeout = 750 * time.Millisecond
	// punchLivenessGrace 拨通后的存活确认宽限窗:TLS 1.3 拒单的
	// CONNECTION_CLOSE 在约 1 个 RTT 内到达,窗内死亡=失败;窗尽未死按活
	// 处理。代价:每次成功拨号多等一窗;RTT 显著超窗的网络里拒单可能迟到
	// (假活),调用方首次读写会撞上会话死亡,由上层兜底——v1 取 250ms
	// (约 2.5 倍常见 mesh RTT 余量)。
	punchLivenessGrace = 250 * time.Millisecond
)

// 拒因哨兵(导出,errors.Is 可判):
var (
	// ErrPunchTimeout 预算耗尽未打通(计划文 errPunchTimeout 的导出名):
	// 上层据此落 piko 中继,不当错误上报。
	ErrPunchTimeout = errors.New("punch dial: 预算耗尽未打通(上层落 piko 中继)")
	// ErrPunchGateRejected B 侧门禁拒单(计划文 errPunchGateRejected 的
	// 导出名):token 被拒=本 token 永久无效,DialPeer 立即收场不重试。
	ErrPunchGateRejected = errors.New("punch dial: B 侧门禁拒单(token 无效,不重试)")
)

// DialOpts 是 DialPeer 的全部输入。Candidates 是「每轮开局快照当前候选」
// 的取数函数(host:port 串,即信令交换来的对端映射地址):设计成函数而非
// 一次性切片,是为了「信令中途送达的候选下一轮立即纳入」——candidates
// 先到/后到都能打通。Budget 零值走 DialBudget。
type DialOpts struct {
	Token      string
	TLS        PunchTLSConfig
	Candidates func() []string
	Budget     time.Duration
	// KeepAlive 置真时拨号带 quic.Config{KeepAlivePeriod: 15s}(经
	// punchDialQUICConfig 组装):只该由 SessionCache 路径置真(DialAndPut
	// 强制置真)——缓存会话要在两次取用的间隙存活等 NAT 映射,保活刷洞;
	// 一次性拨号(不进缓存)保持 false,会话用完即弃无需保活。与空闲回收
	// (5min)双参数协作实现「保活只为在飞(含缓存待取)连接服务」不变式,
	// 全文见 session.go 的 punchKeepAlivePeriod。
	KeepAlive bool
}

// PunchSession 是打通后的出让件:Transport 建在调用方 conn 上,生命周期
// 随会话归调用方——收摊时 Close 它(quic-go 语义:Transport.Close 不关
// 底层 conn,conn 由调用方自管;Close 会 abrupt 终止本会话,正是「会话死
// =连接死」的收场动作)。Conn 用于同会话后续流:B 侧免门契约(listener.
// punchServeConn)保证过门会话从第二条流起直接开流写裸数据。Stream 是
// 首流的 net.Conn 适配(token 行已由 dialer 写毕,流面干净,上层协议从
// 下一字节开始;无 bufio/预读,不吞上层首包)。
type PunchSession struct {
	Transport *quic.Transport
	Conn      *quic.Conn
	Stream    net.Conn
}

// DialPeer 在绑定好的 conn 上对候选循环拨 QUIC,预算内打通即返回会话。
// 返回错误时的收摊契约:dialer 自建的单个 Transport 已 Close(底层 conn
// 不受影响,调用方可复用或自关);返回会话时 Transport 出让,不代收摊。
func DialPeer(conn *net.UDPConn, opts DialOpts) (*PunchSession, error) {
	if conn == nil {
		return nil, errors.New("punch dialer: conn 为 nil")
	}
	if opts.Candidates == nil {
		// 无候选的合法表达是「返回空切片的函数」;传 nil 是装配漏接,
		// fail-loud 不静默当空跑完预算。
		return nil, errors.New("punch dialer: Candidates 为 nil")
	}
	if opts.Token == "" {
		// 与 listener 的空 token 前置拒对齐:空串首流写出去也是被拒,
		// 配置错误在拨号面就炸出来。
		return nil, errors.New("punch dialer: token 为空串——首流无凭据可写,fail-closed")
	}
	budget := opts.Budget
	if budget <= 0 {
		budget = DialBudget
	}
	deadline := time.Now().Add(budget)
	// 单 Transport 铁律(文件头):所有轮次所有候选复用同一个。
	tr := &quic.Transport{Conn: conn}
	tlsConf := ClientTLSConfig(opts.TLS)
	// 缓存路径(KeepAlive=true)带保活配置,其余走 quic-go 全默认:
	// 组装规则与不变式见 session.go 的 punchDialQUICConfig。
	qcfg := punchDialQUICConfig(opts)
	var lastErr error
rounds:
	for round := 0; ; round++ {
		if round > 0 {
			wait := min(punchDialRoundInterval, time.Until(deadline))
			if wait <= 0 {
				break rounds
			}
			time.Sleep(wait)
		}
		// 每轮重新取数+洗牌:信令中途送达的候选本轮即纳入;固定顺序会让
		// 「坏候选恒排第一」锁死整场。防御性拷贝后洗牌,不动调用方切片。
		cands := append([]string(nil), opts.Candidates()...)
		rand.Shuffle(len(cands), func(i, j int) { cands[i], cands[j] = cands[j], cands[i] })
		for _, cand := range cands {
			remain := time.Until(deadline)
			if remain <= 0 {
				break rounds
			}
			sess, err := punchAttemptOnce(tr, tlsConf, qcfg, cand, opts.Token, remain)
			if err == nil {
				// 打通:Transport 随会话出让,收摊归调用方(见 PunchSession)。
				return sess, nil
			}
			lastErr = err
			if errors.Is(err, ErrPunchGateRejected) {
				// token 被拒=本 token 永久无效,重试只是烧预算,立即收场。
				_ = tr.Close()
				return nil, err
			}
			// 其余失败(传输死亡/超时)=可重试:换下一候选,预算内继续。
		}
	}
	// 预算耗尽:收摊自建 Transport(不关底层 conn),捎最后错误给上层排障;
	// 无候选送达的场(never entered inner loop)就是裸哨兵。
	_ = tr.Close()
	if lastErr == nil {
		return nil, ErrPunchTimeout
	}
	return nil, fmt.Errorf("%w(最后错误: %v)", ErrPunchTimeout, lastErr)
}

// punchAttemptOnce 对单候选做一次完整尝试:Dial(QUIC Initial 即探测)→
// 开首流 → 写 token 行 → 存活确认。失败时自行收敛本次连接(CloseWithError
// 幂等,连接已死亦无妨),单次围栏封顶 min(punchDialAttemptTimeout, remain)。
// qcfg 是 Dial 的 quic.Config(缓存路径带保活,其余 nil 走默认)。
func punchAttemptOnce(tr *quic.Transport, tlsConf *tls.Config, qcfg *quic.Config, cand, token string, remain time.Duration) (*PunchSession, error) {
	addr, err := net.ResolveUDPAddr("udp4", cand)
	if err != nil {
		// 候选串坏=候选级失败:换道继续,不是门禁也不是通道死刑(v1 打洞
		// 面是 udp4,与 socket.go 的绑定铁律同界)。
		return nil, fmt.Errorf("候选 %q 解析失败: %w", cand, err)
	}
	attemptCtx, cancel := context.WithTimeout(context.Background(), min(punchDialAttemptTimeout, remain))
	defer cancel()
	// attemptCtx 只管辖握手与开流:quic-go 把连接主体挂在
	// context.WithoutCancel(ctx) 上(transport.go doDial),Dial 成功后
	// 本函数返回时的 cancel 不伤连接。
	qconn, err := tr.Dial(attemptCtx, addr, tlsConf, qcfg)
	if err != nil {
		return nil, punchAttemptErr(cand, err)
	}
	// 首流写序契约(Task 6 实锤):开流后立刻写 token 行——QUIC 空流对端
	// 不可见,不写数据 B 侧 AcceptStream 永不醒;只写 token 行,上层协议
	// 数据归调用方在出让的流上写。禁 bufio/预读同理:会吞上层首包(SSH
	// 版本串)。
	stream, err := qconn.OpenStreamSync(attemptCtx)
	if err != nil {
		_ = qconn.CloseWithError(0, "")
		return nil, punchAttemptErr(cand, err)
	}
	if _, err := stream.Write([]byte(token + "\n")); err != nil {
		_ = qconn.CloseWithError(0, "")
		return nil, punchAttemptErr(cand, err)
	}
	// 存活确认(TLS 1.3 拒单时序不对称):Conn.Context 的 cancel-cause 就是
	// 连接关闭错误(quic-go connection.go run 的 defer ctxCancel(err)),
	// 窗内 Done 关闭即取 cause 分派拒因——全程不碰流的读面,首字节留给上层。
	grace := time.NewTimer(punchLivenessGrace)
	defer grace.Stop()
	select {
	case <-grace.C:
		return &PunchSession{Transport: tr, Conn: qconn, Stream: &punchStreamConn{stream: stream, conn: qconn}}, nil
	case <-qconn.Context().Done():
		cause := context.Cause(qconn.Context())
		_ = qconn.CloseWithError(0, "")
		if punchAsGateErr(cause) {
			return nil, fmt.Errorf("%w(候选 %s): %v", ErrPunchGateRejected, cand, cause)
		}
		return nil, fmt.Errorf("候选 %s 宽限窗内死亡(握手先成随即死?): %w", cand, cause)
	}
}

// punchAttemptErr 把 Dial/开流/写行阶段的失败按拒因三分归档:门禁拒单→
// ErrPunchGateRejected(不可重试;理论上门禁在流层,Dial 阶段到不了,分类
// 在此只是防身),其余→候选级可重试包装(保留错误链供上层排障)。
func punchAttemptErr(cand string, err error) error {
	if punchAsGateErr(err) {
		return fmt.Errorf("%w(候选 %s): %v", ErrPunchGateRejected, cand, err)
	}
	return fmt.Errorf("候选 %s: %w", cand, err)
}

// punchAsGateErr 判 err 链上是否挂着「门禁拒单」的 QUIC 应用层错误
// (code=punchGateErrorCode):B 侧 listener 拒 token 时以该码关整条会话,
// A 侧凭它把「token 永久无效」从「传输死亡」里分出来——前者重试无意义,
// 后者继续轮询。
func punchAsGateErr(err error) bool {
	var appErr *quic.ApplicationError
	return errors.As(err, &appErr) && appErr.ErrorCode == punchGateErrorCode
}
