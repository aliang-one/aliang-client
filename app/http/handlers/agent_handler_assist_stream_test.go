package handlers

// 远程协助 P1 Task 7:观看 WS(GET /api/agent/assist/stream)的门前矩阵与
// 生命周期钉测(spec §7.1「照 HandleWebTerminalStream 闸门模板」)。
//
// 测试形态分工(拍板:轻路径):handlers 包拿不到 active 态的驱动缝
// (OnRemoteFrame 在未导出类型上),active 态的回放续屏/live 帧顺序在
// services 同包测试钉(TestAssistReplayThenLiveOrderingUnderOneMutex,
// 以 handler 完全相同的注册/持锁回放形态驱动);handler 层钉:
//   ①双闸矩阵(401/loopback 豁免)  ②透传腿 Origin 复检(ai_stream_proxy.go
//     :47/:56 历史坑:跨站拒+同源真转发)  ③会话闸(idle/ended→1011 带
//     state 原因)  ④只读结构性(伪 terminal.input→零写/连接保持/快照
//     不变)  ⑥会话终态→观看端收 assist.end 后被服务端关闭  ⑦SetReadLimit
//     64KiB。
// 等待模式(requested 态先连上等审批)在 ③/④/⑥ 用例里顺带钉住:申请后
// 即可拨号、无 ring 优雅跳过、撤销即收终帧。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
)

// ——— 核心腿门前矩阵(core 角色:方法闸+双闸;透传腿细节走真服务器用例)———

func TestHandleAssistStreamCoreGateMatrix(t *testing.T) {
	h := NewAgentHandler(nil)

	// 方法闸最前(先于鉴权,同四端点约定):POST → 405。
	rec := httptest.NewRecorder()
	h.HandleAssistStream(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/stream", "", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 打 stream 应 405, got %d", rec.Code)
	}

	// ① 无 cookie 非 loopback → 401。
	rec = httptest.NewRecorder()
	h.HandleAssistStream(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/stream", "", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无登录态 WS 握手应 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	// 伪造 cookie → 401。
	forged := &http.Cookie{Name: "dashboard_session", Value: url.QueryEscape("forged-token")}
	rec = httptest.NewRecorder()
	h.HandleAssistStream(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/stream", "", forged))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("伪造 cookie 应 401, got %d", rec.Code)
	}

	// ② loopback 豁免腿放行:非 401 即证明(过闸后进透传腿;recorder 无
	// Hijacker,proxyAIStream 只能落 500/无写入,均为非门级结局)。
	req := webTerminalGateRequest(http.MethodGet, "/api/agent/assist/stream", "", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	rec = httptest.NewRecorder()
	h.HandleAssistStream(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("loopback 豁免不应 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleAssistStreamProxyLegRechecks 钉透传腿两条历史坑断言
// (ai_stream_proxy.go:47 Cookie 捕获/:56 Origin 复检):core 角色 + 真服务器,
// 拨号目标钉死死端口(127.0.0.1:1,aiStreamTargetHost 测试覆写)保证 hermetic。
//   - 跨站 Origin(loopback 来源过双闸豁免)→ 透传腿复检拒:rejectBrowserWS
//     的 Upgrade 同样过不了 CheckOrigin → 客户端拨号失败;
//   - 同源 → 过复检、真走拨号路径(Origin+Cookie 捕获后转发)→ 死端口 →
//     close 1011 "device agent offline"(区别于跨站的直接拒,两条路径语义
//     不分叉——这正是 :47/:56 复检要钉的历史坑)。
func TestHandleAssistStreamProxyLegRechecks(t *testing.T) {
	h := NewAgentHandler(nil)
	old := aiStreamTargetHost
	aiStreamTargetHost = func() string { return "127.0.0.1:1" }
	t.Cleanup(func() { aiStreamTargetHost = old })
	srv := httptest.NewServer(http.HandlerFunc(h.HandleAssistStream))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agent/assist/stream"

	// 跨站 Origin:拨号必须失败(同源闸在透传腿复检处拦下)。
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": []string{"http://evil.example:8080"}})
	if err == nil {
		conn.Close()
		t.Fatal("跨站 Origin 应在透传腿被拒(拨号失败),不应完成握手")
	}

	// 同源:拨号成功(过复检),但拨号目标死端口 → 1011 close。
	conn, _, err = websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": []string{srv.URL}})
	if err != nil {
		t.Fatalf("同源拨号应成功(透传腿放行): %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("透传腿拨死端口应 close 1011,不应收到业务帧")
	}
	var ce *websocket.CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("应收到 close 帧(1011), got %v", err)
	}
	if ce.Code != websocket.CloseInternalServerErr {
		t.Fatalf("close code = %d, want 1011 (text=%q)", ce.Code, ce.Text)
	}
	if !strings.Contains(ce.Text, "device agent offline") {
		t.Fatalf("close 语义应指向设备离线(证明同源请求真被转发到拨号路径): %q", ce.Text)
	}
}

// ——— runtime 腿(真身 service 夹具)———

// newAssistStreamRuntimeServer runtime 角色夹具(复用 Task 6 的
// newAssistSessionHandler 隔离形态)+ 总开关开 + WS 服务器。
func newAssistStreamRuntimeServer(t *testing.T) (*AgentHandler, *httptest.Server) {
	t.Helper()
	h, _ := newAssistSessionHandler(t)
	h.service.SetRemoteAssistEnabled(true)
	srv := httptest.NewServer(http.HandlerFunc(h.HandleAssistStream))
	t.Cleanup(srv.Close)
	return h, srv
}

// dialAssistStream 拨观看 WS(Origin=服务器同源,CheckOrigin 精确 host:port)。
func dialAssistStream(t *testing.T, srv *httptest.Server, sid string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agent/assist/stream?session_id=" + url.QueryEscape(sid)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": []string{srv.URL}})
	if err != nil {
		t.Fatalf("dial assist stream: %v", err)
	}
	return conn
}

// assistStreamRequestSession 经 HTTP 申请会话,返回 session_id。
func assistStreamRequestSession(t *testing.T, h *AgentHandler, cookie *http.Cookie) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.HandleAssistRequest(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/request", `{"message":"watch"}`, cookie))
	if rec.Code != 200 {
		t.Fatalf("request code = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	sid, _ := assistSessionData(t, rec)["session_id"].(string)
	if sid == "" {
		t.Fatalf("request 未回 session_id: %s", rec.Body.String())
	}
	return sid
}

// assistStreamSnapshot 经 HTTP 读会话快照(伪输入前后对照用)。
func assistStreamSnapshot(t *testing.T, h *AgentHandler, cookie *http.Cookie) map[string]interface{} {
	t.Helper()
	rec := httptest.NewRecorder()
	h.HandleAssistSession(rec, webTerminalGateRequest(http.MethodGet, "/api/agent/assist/session", "", cookie))
	if rec.Code != 200 {
		t.Fatalf("session code = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	return assistSessionData(t, rec)
}

// isReadTimeout 读超时判定(net.Error.Timeout 或 ErrDeadlineExceeded)。
func isReadTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	return false
}

// assertNoFrameSoon 短窗内必须零帧且连接保持(读超时=连接仍开;任何帧/close
// 都是失败——无 ring 的 requested 态优雅跳过回放、入站帧不得引发任何写)。
func assertNoFrameSoon(t *testing.T, conn *websocket.Conn, window time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(window))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("窗口内不应收到任何帧(无回放/无错误风暴)")
	}
	if !isReadTimeout(err) {
		t.Fatalf("连接应保持打开(读超时), got 非 timeout 错误: %v", err)
	}
}

// assertClose1011 断言服务端 close 1011 且语义包含给定子串。
func assertClose1011(t *testing.T, conn *websocket.Conn, wantText string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatalf("应被 close 1011(%s),却收到业务帧", wantText)
	}
	var ce *websocket.CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("应收到 close 帧(1011 %s), got %v", wantText, err)
	}
	if ce.Code != websocket.CloseInternalServerErr {
		t.Fatalf("close code = %d, want 1011 (text=%q)", ce.Code, ce.Text)
	}
	if !strings.Contains(ce.Text, wantText) {
		t.Fatalf("close 语义应包含 %q: %q", wantText, ce.Text)
	}
}

func TestHandleAssistStreamRuntimeGateDisabled403(t *testing.T) {
	// 总开关缺省 false(零值纪律)→ runtime 门 403:升级前 HTTP 403,
	// 拨号失败。gates 端点常开翻转回来(同四端点纪律)。
	h, _ := newAssistSessionHandler(t)
	srv := httptest.NewServer(http.HandlerFunc(h.HandleAssistStream))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agent/assist/stream?session_id=assist-x"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": []string{srv.URL}})
	if err == nil {
		t.Fatal("开关关应 403 拒升级")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		if resp != nil {
			t.Fatalf("upgrade HTTP = %d, want 403", resp.StatusCode)
		}
		t.Fatalf("拨号失败但无 HTTP 响应: %v", err)
	}
}

func TestHandleAssistStreamRuntimeOriginRejected(t *testing.T) {
	// runtime 腿同源闸(aiStreamUpgrader.CheckOrigin=aiStreamHostAllowed):
	// 跨站 Origin → Upgrade 拒 → 拨号失败。
	_, srv := newAssistStreamRuntimeServer(t)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/agent/assist/stream?session_id=assist-x"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": []string{"http://evil.example:8080"}})
	if err == nil {
		conn.Close()
		t.Fatal("runtime 腿跨站 Origin 应在 Upgrade 处被拒")
	}
}

func TestHandleAssistStreamSessionGateIdleAndEnded(t *testing.T) {
	// 会话闸:requested/active 放行(requested=等待模式,见 wait-mode 用例);
	// idle/ended → 1011 带 state 原因。缺 session_id 参数/未知 id 同 idle 处置。
	h, srv := newAssistStreamRuntimeServer(t)
	cookie := mintDashboardCookie(t)

	for _, sid := range []string{"", "assist-unknown"} {
		conn := dialAssistStream(t, srv, sid)
		assertClose1011(t, conn, "state=idle")
		conn.Close()
	}

	// ended:申请→撤销→重拨同一 sid → 1011 state=ended。
	sid := assistStreamRequestSession(t, h, cookie)
	rec := httptest.NewRecorder()
	h.HandleAssistCancel(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/cancel", "", cookie))
	if rec.Code != 200 {
		t.Fatalf("cancel code = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	conn := dialAssistStream(t, srv, sid)
	assertClose1011(t, conn, "state=ended")
	conn.Close()
}

func TestHandleAssistStreamWaitModeLifecycle(t *testing.T) {
	// 等待模式全链:requested 态先连上等审批(会话闸放行)→ 无 ring 优雅跳过
	// 回放(短窗零帧)→ 伪 terminal.input 全部丢弃(连接保持、零写、快照
	// 不变)→ 撤销终态 → 观看端收 assist.end{reason} 后被服务端关闭。
	//
	// 夹具注:gorilla 客户端的读死线超时会毒化连接(超时后再读同连接拿不到
	// 服务端后续帧,即便重置死线——实证见 TestDbgGorillaReadDeadlinePoison
	// 形态的本地实验),故「短窗零帧」断言独立用一条连接完成,终态收帧走
	// 另一条全新连接(服务端语义不变:任一在册观看端都会被通知)。
	h, srv := newAssistStreamRuntimeServer(t)
	cookie := mintDashboardCookie(t)
	sid := assistStreamRequestSession(t, h, cookie)

	snapBefore := assistStreamSnapshot(t, h, cookie)

	// 第一条连接:短窗零帧(requested 态无终端,无 ring 可回放→优雅跳过)。
	conn := dialAssistStream(t, srv, sid)
	assertNoFrameSoon(t, conn, 400*time.Millisecond)
	conn.Close()

	// 第二条连接:只读结构性 + 终态生命周期。
	conn = dialAssistStream(t, srv, sid)
	defer conn.Close()

	// 伪 terminal.input ×3 → 服务端读循环丢弃,连接保持,无错误帧,状态
	// 零变化(入站帧没有任何去处)。
	for i := 0; i < 3; i++ {
		_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		if err := conn.WriteJSON(map[string]interface{}{
			"type":       "terminal.input",
			"session_id": sid,
			"data":       fmt.Sprintf("should-go-nowhere-%d", i),
		}); err != nil {
			t.Fatalf("写伪输入帧 %d: %v", i, err)
		}
	}
	// 伪输入后的静默窗:不等帧,纯 sleep(读死线会毒化连接,改用时间窗;
	// 服务端若有错误风暴,下方的快照与终态收帧断言都会翻红)。
	time.Sleep(300 * time.Millisecond)

	snapAfter := assistStreamSnapshot(t, h, cookie)
	for _, key := range []string{"session_id", "state", "message", "requested_at"} {
		if snapBefore[key] != snapAfter[key] {
			t.Fatalf("伪输入不得改变快照字段 %q: before=%v after=%v", key, snapBefore[key], snapAfter[key])
		}
	}

	// 终态:撤销 → 观看端收 assist.end(唯一生命周期终态信标)后被关闭。
	rec := httptest.NewRecorder()
	h.HandleAssistCancel(rec, webTerminalGateRequest(http.MethodPost, "/api/agent/assist/cancel", "", cookie))
	if rec.Code != 200 {
		t.Fatalf("cancel code = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("终态应收到 assist.end 帧: %v", err)
	}
	var endFrame map[string]interface{}
	if err := json.Unmarshal(msg, &endFrame); err != nil {
		t.Fatalf("解析 assist.end: %v (raw=%s)", err, msg)
	}
	if endFrame["type"] != models.AgentEventAssistEnd {
		t.Fatalf("终帧 type = %v, want %s (frame=%v)", endFrame["type"], models.AgentEventAssistEnd, endFrame)
	}
	if endFrame["session_id"] != sid || endFrame["reason"] != string(services.AssistEndClient) {
		t.Fatalf("assist.end 帧不符: %v", endFrame)
	}
	// 帧送达后由服务端关闭连接(计划⑥):fan 闭包在 assist.end 写达后
	// 发 close 帧再关——客户端必须读到**真 close 帧**(*websocket.CloseError
	// 1000),不是靠客户端读死线超时假绿(评审实证:修前连接在终帧后
	// 挂满 3s 无 close 帧)。
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("assist.end 之后服务端应关闭连接")
	}
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("应是服务端 close 帧(*websocket.CloseError),got %v(读死线超时=假绿,连接半开)", err)
	}
	if closeErr.Code != websocket.CloseNormalClosure {
		t.Fatalf("close code = %d, want 1000 (text=%q)", closeErr.Code, closeErr.Text)
	}
}

func TestHandleAssistStreamReadLimit64KiB(t *testing.T) {
	// 入站读限 64KiB(入站只有 ping/垃圾帧):超限帧 → 服务端断连。
	h, srv := newAssistStreamRuntimeServer(t)
	cookie := mintDashboardCookie(t)
	sid := assistStreamRequestSession(t, h, cookie)
	conn := dialAssistStream(t, srv, sid)
	defer conn.Close()

	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	big := `{"type":"terminal.input","data":"` + strings.Repeat("x", 64*1024+256) + `"}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(big)); err != nil {
		t.Fatalf("写超限帧: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("超限入站帧应让服务端断连(读限生效)")
	}
}

func TestHandleAssistStreamViewRegistryCleanupOnExit(t *testing.T) {
	// Fix 2 回归钉:连接退出(读循环任何错误)必须注销 view sink——注册表
	// 不积累死桶(RegisterViewSink 文档纪律)。驱动:requested 态拨号观看
	// → 客户端主动断开 → 读循环 ReadMessage 错误退出 → defer 注销。断言经
	// 服务缝观察桶状态:先注册探针 sink 占住桶(证明桶在),客户端断开后
	// 服务端 defer 注销自己的 viewID;探针注销后桶必须整体消失(若服务端
	// 的死 view 残留,桶会剩一个成员而不消失)。
	h, srv := newAssistStreamRuntimeServer(t)
	cookie := mintDashboardCookie(t)
	sid := assistStreamRequestSession(t, h, cookie)

	probe := &assistStreamProbeSink{}
	h.service.AssistRegisterViewSink(sid, "probe", probe.fan)
	conn := dialAssistStream(t, srv, sid)
	// 给服务端 handler goroutine 时间完成注册+进入读循环。
	time.Sleep(150 * time.Millisecond)
	_ = conn.Close()
	// 等读循环感知断开并执行 defer 注销(有界轮询桶成员数)。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.service.AssistUnregisterViewSink(sid, "probe") // 探针出桶
		if h.service.AssistViewBucketEmpty(sid) {
			break // 服务端 viewID 也已注销,桶清空
		}
		// 探针重新入桶继续等(下一轮再试)。
		h.service.AssistRegisterViewSink(sid, "probe", probe.fan)
		time.Sleep(20 * time.Millisecond)
	}
	h.service.AssistUnregisterViewSink(sid, "probe")
	if !h.service.AssistViewBucketEmpty(sid) {
		t.Fatal("连接退出后服务端必须注销其 view sink(桶残留=死 view 泄漏)")
	}
}

// assistStreamProbeSink 空探针(桶占位用)。
type assistStreamProbeSink struct{}

func (p *assistStreamProbeSink) fan(frame map[string]interface{}) error { return nil }

// ——— Task 11:服务端 ping 保活(等待模式零流量缺口)———

// TestHandleAssistStreamPingLoopStartStop 结构性钉测:assistViewPingLoop 按
// 周期发 ping、done 关闭即停。直驱循环本体(不拉 handler 全链;pong handler
// 注册与保活闭环的行为面由 TestHandleAssistStreamWaitModePingKeepalive 覆盖
// ——若 pong handler 未注册/未续读窗,那条用例会在缩短读窗处翻红)。
// 客户端以计数 ping handler 观测(gorilla 在读泵内同步回调,时序即真实
// 到达序);全程单次长读(读死线超时毒化连接的房规,见本文件头注记)。
func TestHandleAssistStreamPingLoopStartStop(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		done := make(chan struct{})
		go assistViewPingLoop(c, 50*time.Millisecond, done)
		time.AfterFunc(250*time.Millisecond, func() { close(done) })
		// 占位读循环:handler 活到客户端读窗结束(否则升级连接被提前回收)。
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	client, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/", nil)
	if err != nil {
		t.Fatalf("dial ping loop fixture: %v", err)
	}
	defer client.Close()

	var mu sync.Mutex
	var pings []time.Time
	client.SetPingHandler(func(string) error {
		mu.Lock()
		pings = append(pings, time.Now())
		mu.Unlock()
		return nil
	})
	// 读到超时=连接存活=ping 全程在送;读到 close/其他错误=夹具异常。
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = client.ReadMessage()
	if err != nil && !isReadTimeout(err) {
		t.Fatalf("观测窗内连接应存活(只该读到读死线超时): %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pings) < 3 {
		t.Fatalf("250ms 窗/50ms 周期应送达 ≥3 个 ping, got %d", len(pings))
	}
	// done 在 250ms 关闭;若循环不随 done 收摊,ping 会持续到读窗结束
	// (last 距今 <200ms)。留 50ms 周期余量:stopped ⇒ last ≥300ms 前。
	if stale := time.Since(pings[len(pings)-1]); stale < 300*time.Millisecond {
		t.Fatalf("done 关闭后循环必须停止发 ping(last ping 距今 %v ⇒ 未停)", stale)
	}
}

// TestHandleAssistStreamWaitModePingKeepalive 行为钉测(Task 11 缺口本体):
// requested 等待模式服务端→观看端零流量——无 ping 时缩短后的读窗(500ms)
// 到期即被服务端静默断连;有了 ping 循环,gorilla 客户端自动 pong(默认
// ping handler 发 pong,浏览器同 RFC6455 自动 pong),服务端 pong handler
// 续读窗,观看端跨 ≥2 个读窗存活。全程单次长读(读死线毒化房规):读到
// 超时=存活;读到 close/其他错误=保活失效。
func TestHandleAssistStreamWaitModePingKeepalive(t *testing.T) {
	oldInterval, oldWindow := assistViewPingInterval, assistViewReadWindow
	assistViewPingInterval = 100 * time.Millisecond
	assistViewReadWindow = 500 * time.Millisecond
	t.Cleanup(func() { assistViewPingInterval, assistViewReadWindow = oldInterval, oldWindow })

	h, srv := newAssistStreamRuntimeServer(t)
	cookie := mintDashboardCookie(t)
	sid := assistStreamRequestSession(t, h, cookie)
	conn := dialAssistStream(t, srv, sid)
	defer conn.Close()

	// 1.2s > 2×读窗:保活失效时 500ms 处即被服务端关闭(断言中途翻红)。
	_ = conn.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
	_, _, err := conn.ReadMessage()
	if err != nil && !isReadTimeout(err) {
		t.Fatalf("保活期内观看连接被服务端关闭(读窗无人续期=ping 循环失效): %v", err)
	}

	// 收摊确定性屏障:返回前先关连接并等服务端 handler 退出(读循环错误
	// 退出→defer close(done)+注销→桶空),否则 t.Cleanup 回写包级 var 会与
	// 仍在飞行 pong 上读 var 的 pong handler 构成数据竞态(-race 实证)。
	// 桶空 ⇒ close(done) 已执行 ⇒ 保活循环已收信号(该循环只读 const 写
	// 期限,不读本 var);读循环已退出 ⇒ pong handler 不会再被调用。
	_ = conn.Close()
	drain := time.Now().Add(3 * time.Second)
	for time.Now().Before(drain) {
		if h.service.AssistViewBucketEmpty(sid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("观测窗结束后服务端 handler 未收摊(view 桶未清)")
}
