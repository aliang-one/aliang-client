package handlers

// 远程协助 P1 Task 7:观看 WS(GET /api/agent/assist/stream,spec §7.1
// 「照 HandleWebTerminalStream 闸门模板」)。观看页 xterm 只读镜像的推送面:
// 纯服务端单向推送 + 回放续屏;入站帧**任何形式都不进任何通道**(结构性
// 只读——观看着对目标设备零交互通道,spec §10「只读结构性保证」的服务端半场)。
//
// 段序(模板 HandleWebTerminalStream 四段制):
//   ①方法闸+双闸(guardWebTerminalGate)
//   ②core 腿:同源闸(assistSameOriginGuard)+ 真源标记 + proxyAIStream
//     TCP 透传(透传腿 aiStreamHostAllowed 复检由 proxyAIStream 自带,
//     ai_stream_proxy.go:56 历史坑钉在其测试里)
//   ③runtime 腿:runtime 门(总开关;gates 常开翻回)+ Upgrade
//     (CheckOrigin=aiStreamHostAllowed)
//   ④会话闸 + 注册 + 回放 + 只读循环
//
// 会话闸拍板:requested OR active 放行(requested=等待模式——观看页在
// 审批等待期就连上,approved 后无缝转 live);idle/ended 立即 close 1011
// 带 state 原因。注册后复检一次 state(register-vs-end 竞态兜底):已终态
// 则给本连接补发 assist.end + 注销 + 关闭。终态送达的常态路径在 fan 闭包:
// assist.end 扇达本连接即写 close 帧并关连接(计划⑥——观看页不得挂着
// 死连接,读循环 90s 窗内无人收尸是不可接受的半开连接)。
//
// 回放-续拍顺序铁律(Task 7 拍板+实现期锁序修正):先注册 sink,再发 ring
// 回放块,然后进 live——序列化由 sendReplay 的 outputGate 承担(快照+逐帧
// 横跨持锁;live 扇出在 copy 循环里同样于 gate 内),全局锁序恒为
// outputGate → view.mu,无环、零缝隙、零乱序。fanFn(=view.write)与回放
// writeJSON 共用同一把 view.mu,并发写同连接仍被串行化;fanOneView 的 10s
// 写期限防卡死观看端拖住 PTY 复制循环。requested 态无终端时
// AssistTerminalReplayTo 优雅 no-op。

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
)

// assistViewReadLimit 入站读限:观看面入站只有 ping/垃圾帧(业务上零入站),
// 64KiB 上限让超限帧立即断连,防对端把内存任意挥霍给本只读面。
const assistViewReadLimit = 64 * 1024

// assistViewReadWindow / assistViewWriteDeadline 读活窗与写期限
// (agentRemoteReadWindow/agentRemoteWriteTimeout 同族语义):读窗每帧刷新
// (静默对端超窗断连,不挂死 socket);每帧写期限 10s 防半死观看端把
// 扇出腿无限期拖住(远端 writer 同款惯例)。
const (
	assistViewReadWindow    = 90 * time.Second
	assistViewWriteDeadline = 10 * time.Second
)

// assistViewConn 观看连接的写出口:writeMu 与回放共用同一把(见文件头
// 顺序铁律)——live 扇出、回放块、终帧全部经它串行化。
type assistViewConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *assistViewConn) write(payload interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(assistViewWriteDeadline))
	return s.conn.WriteJSON(payload)
}

// HandleAssistStream GET /api/agent/assist/stream?session_id=。
func (h *AgentHandler) HandleAssistStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !guardWebTerminalGate(w, r) {
		return
	}
	// 同源闸仅 core 腿(透传腿 r.Host 已改写,照搬会误杀 LAN;语义同
	// assistSameOriginGuard 的四端点注记)。runtime 直连腿的 Origin 复检
	// 由 aiStreamUpgrader.CheckOrigin 承担。
	if !assistSameOriginGuard(w, r) {
		return
	}
	markWebTerminalLoopbackSource(r)
	if !services.IsUserAgentRuntime() {
		proxyAIStream(w, r)
		return
	}
	// runtime 腿:总开关门(403 先于 Upgrade,gates 端点常开翻回;nil
	// service 防御同 webTerminalFSRuntimeGate 形态——rejectBrowserWS
	// 兑现 1011)。
	if h.service == nil {
		rejectBrowserWS(w, r, "remote assist unavailable")
		return
	}
	if !h.service.RemoteAssistEnabled() {
		common.ErrorForbidden(w, "Remote assist is disabled")
		return
	}

	// 会话闸:requested/active 放行;idle/ended 拒(1011 带 state)。
	// 快照懒扫已收敛时间驱动的终态(pending 过期/时长上限),这里读到的
	// 即真值。缺 session_id 与未知 id 同 idle 处置(双闸+开关门已过,
	// state 字面无泄露面——观看着本就持门票)。
	sid := r.URL.Query().Get("session_id")
	snap := h.service.AssistSnapshot()
	if sid == "" || snap.SessionID != sid || (snap.State != "requested" && snap.State != "active") {
		state := snap.State
		if sid == "" || snap.SessionID != sid {
			state = "idle" // 缺参/未知 id:按 idle 口径汇报
		}
		rejectBrowserWS(w, r, "assist session is not active (state="+string(state)+")")
		return
	}

	conn, err := aiStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(assistViewReadLimit)

	// 观看连接写出口(与回放共用 writeMu,见文件头顺序铁律)。
	view := &assistViewConn{conn: conn}
	// 注册 sink(会话闸已确认 requested/active——先于注册的纪律见
	// RegisterViewSink 文档)。连接退出(任何 return 路径)统一注销,防桶
	// 泄漏(注册表纪律:UnregisterViewSink 文档)。fan 内嵌终帧关连接语义:
	// assist.end 送达即写 close 帧+关连接(计划⑥「会话 ended→连接收
	// assist.end 后被服务端关闭」)——notifyAndClearViews 是「先删桶再扇」,
	// 终帧之后本 viewID 不会再有任何扇出,close 无并发写者;读循环的
	// ReadMessage 随即错误退出 → defer 注销+关连接收尾。
	viewID := "view-" + viewConnCounter()
	h.service.AssistRegisterViewSink(sid, viewID, func(frame map[string]interface{}) error {
		err := view.write(frame)
		if frameType, _ := frame["type"].(string); err == nil && frameType == models.AgentEventAssistEnd {
			view.mu.Lock()
			_ = view.conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "assist session ended"),
				time.Now().Add(2*time.Second))
			view.mu.Unlock()
			_ = view.conn.Close()
		}
		return err
	})
	defer h.service.AssistUnregisterViewSink(sid, viewID)
	// 注册后复检一次 state(belt-and-braces,register-vs-end 竞态):注册
	// 前落终态的 notifyAndClearViews 是「先删桶再扇」——晚到注册会成孤儿
	// 桶;此处复检兜住窗口:已终态则给本连接补发 assist.end + 注销(defer
	// 已兜)+ 关闭。
	snap = h.service.AssistSnapshot()
	if sid == "" || snap.SessionID != sid || (snap.State != "requested" && snap.State != "active") {
		_ = view.write(services.AssistViewEndFrame(sid, string(snap.EndReason)))
		return
	}

	// 回放-续屏(顺序铁律,锁序修正版):sink 已注册,此处**不持 view.mu**
	// 调 AssistTerminalReplayTo——sendReplay 自带 outputGate(快照+逐帧横跨),
	// live 帧的扇出在 copy 循环里同样于 outputGate 内发生(agent_terminal.go
	// copyTerminalOutput:push+writeJSON 同 gate),故全局锁序恒为
	// outputGate → view.mu:回放块持 outputGate 期间 live 帧整段挡在 gate
	// 外,replay* 先于 live 到达,零缝隙零乱序;**若在此持 view.mu 再进
	// gate 则与 copy 循环的 gate→fan→view.mu 构成锁环**(fan 写有 10s 期限
	// 兜底但每帧白等 10s,不可取)。requested 态无终端:优雅 no-op(等待
	// 模式静默进入 live 等待)。
	h.service.AssistTerminalReplayTo(sid, view.write)

	// 只读读循环:读并丢弃一切入站帧(结构性只读——入站帧没有任何去处,
	// 永远)。读窗每帧刷新(含 pong):静默对端超窗断连。任何读错误(对端
	// 断开/超限/超窗)→ 退出 → defer 注销 sink + 关连接。
	resetReadDeadline := func() {
		_ = conn.SetReadDeadline(time.Now().Add(assistViewReadWindow))
	}
	resetReadDeadline()
	conn.SetPongHandler(func(string) error {
		resetReadDeadline()
		return nil
	})
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			return
		}
		resetReadDeadline()
	}
}

// viewConnCounter 观看连接 id 计数器(连接身份;重连=新 id)。
var assistViewConnSeq struct {
	mu sync.Mutex
	n  uint64
}

func viewConnCounter() string {
	assistViewConnSeq.mu.Lock()
	defer assistViewConnSeq.mu.Unlock()
	assistViewConnSeq.n++
	return "v" + time.Now().Format("150405") + "-" +
		string(rune('0'+assistViewConnSeq.n%10))
}
