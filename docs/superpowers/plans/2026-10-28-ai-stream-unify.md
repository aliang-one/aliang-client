# AliangCode 聊天链路统一到 agent 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 仪表盘聊天 WS 经 TCP 级透传到 agent 进程跑（journal/清单与手机同源），agent 不可达明确报错绝不回退，AI 自愈拉代理可见，会话期错误必达（含挂死看门狗）。

**Architecture:** core 的 `HandleAIStream` 加 `IsUserAgentRuntime()` 角色分支——core 角色改走新写的 WS 透传（Hijack + 排干 bufio + 双向 io.Copy 到 `UserAgentBaseURL()` 的 56433 同端点），agent 角色保持本地 dispatch；agent 路由表补一行注册（2026-10-21 漏注册事故的守卫测试随行）；自愈通知放共享 `ensureAIUpstreamReachable`（re-dial 成功后触发）；前端加 90s 无帧看门狗与 close-code 错误契约。

**Tech Stack:** Go（gorilla/websocket 已有依赖，Hijack 裸桥）、vitest、Playwright（chromium+chrome）。

**Spec:** `docs/superpowers/specs/2026-10-28-ai-stream-unify-design.md`

**工作区约定（本仓库规则）：**
- 全程在 worktree `/Users/mac/MyProgram/GoProgram/nursor/alianggate/.worktrees/ai-stream-unify`（分支 `feature/ai-stream-unify`，基于 master 4a68214）。
- **任何步骤都不得 `git add`/`git commit`**——全量验证通过后由控制器向用户请示，经同意才提交。
- worktree 无 node_modules：`ln -s /Users/mac/MyProgram/GoProgram/nursor/alianggate/app/website/node_modules app/website/node_modules`（git 已忽略）。Go 测试直接跑。
- vitest/playwright 运行目录 `app/website`；命令 `npm test` / `npx vitest run <file>` / `npx playwright test <file>`。
- gopls 对 worktree 文件的诊断（undefined: X 之类）一律无视，以 `go build`/`go test` 为准。

**关键既有事实：**
- `HandleAIStream`（`app/http/handlers/agent_handler.go:65-87`）当前无条件本地 dispatch；`aiStreamUpgrader`（:50-58，CheckOrigin 仅 localhost/127.0.1）。
- agent 侧路由表 `registerAgentRoutes`（`app/agentruntime/server.go:113-138`）**不含** ai/stream；表内已有 `agentHandler := handlers.NewAgentHandler(services.GetSharedAgentService())`。
- `services.UserAgentBaseURL()`（`agent_service.go:279`）= `"http://" + config.DefaultUserAgentAddr`。
- 手机路径 = 云端 remote-WS 隧道 AI 分支（`agent_remote_ws.go:833-847`），不经 56433 HTTP。
- 自愈共享函数 `ensureAIUpstreamReachable`（`app/http/services/agent_ai_proxy_guard.go:34-56`）：dial 失败 → `deps.heal()` → sleep 500ms → re-dial；两个生产 deps 构造点：`ensureAIUpstreamReachableForRun`（:63-95）与 `agentCoreEnsureUpstream`（`agent_ai_agentcore.go:262-285`）。
- `desktopNotifier` 可注入范式（`auth_runtime_hooks.go:23-24`）；`common/desktop` 包有 `Notify`。
- ChatPage（`app/website/src/components/ChatPage.vue`）：`aiSocketUrl()` :916、socket `onclose` 仅打日志 :953-957、`handleAIEvent` 入口 :1262、确认门 `waitForCreateAck` :678-696（每条消息前重发 create）、`finishRun(conv)` 复位 sending、`local-*` id :1416。
- Go 模块名 `aliang.one/nursorgate`（见 go.mod / 既有 import）。

---

### Task 1: agent 侧路由注册 + 守卫测试

**Files:**
- Modify: `app/agentruntime/server.go:113-138`（registerAgentRoutes）
- Test: `app/agentruntime/server_test.go`（**已存在——追加用例**）

- [ ] **Step 1: 写失败测试**（`app/agentruntime/server_test.go` **已存在——追加**用例，不新建文件；`package agentruntime`）：

```go
// 2026-10-21 liang-dev 漏注册 404 事故的守卫：core 会把 /api/agent/ai/stream
// 透传到本 runtime（2026-10-28 起聊天走 agent），缺注册=仪表盘聊天全挂。
func TestRegisterAgentRoutesIncludesAIStream(t *testing.T) {
	// 固定 runtime 角色 + 独立 HOME（与同文件既有用例同范式）：不设 env 时
	// handler 走透传分支，开发机上若 56433 有活 agent 会握手后永久泵帧、
	// 测试挂到 10 分钟超时。
	t.Setenv("ALIANG_USER_AGENT_RUNTIME", "1")
	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	registerAgentRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/agent/ai/stream")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	// 非 WS 请求会被 upgrader 拒绝（400），但绝不能是 404——404 即漏注册。
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("/api/agent/ai/stream 404：registerAgentRoutes 漏注册")
	}
}
```

（import 与同文件既有用例合并；缺则补 `net/http`、`net/http/httptest`、`testing`。）

- [ ] **Step 2: 跑测试确认失败** — `go test ./app/agentruntime/ -run TestRegisterAgentRoutesIncludesAIStream -v` → FAIL（404）。

- [ ] **Step 3: 注册路由** — `registerAgentRoutes` 内（`/api/agent/ai/approval-hook` 行后）追加：

```go
	// 仪表盘聊天 WS（2026-10-28 统一）：core 把 /api/agent/ai/stream 透传到本
	// runtime，由本地 AI 管理器跑（journal/清单与手机同源）。缺注册=404 透传
	// （2026-10-21 liang-dev 双注册教训）。
	mux.HandleFunc("/api/agent/ai/stream", agentHandler.HandleAIStream)
```

并把 :114 的 `logger.Info("[AGENT-BOOT] local_server registering_routes routes=...")` 清单补上 `,/api/agent/ai/stream`。

- [ ] **Step 4: 跑测试确认通过** — 同命令 → PASS；再 `go build ./app/agentruntime/ && go vet ./app/agentruntime/` → 静默。

---

### Task 2: WS 透传器 `proxyAIStream`（TDD）

**Files:**
- Create: `app/http/handlers/ai_stream_proxy.go`
- Test: `app/http/handlers/ai_stream_proxy_test.go`

- [ ] **Step 1: 写失败测试**（`package handlers`，同包可覆写目标 host）：

```go
package handlers

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 起一个回声 agent：连上后把收到的二进制帧原样发回。
func startEchoAgent(t *testing.T) *httptest.Server {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
}

// drainBuffered 把 gorilla Upgrade 缓冲里滞留的早期帧字节写给 dst，
// 再交回裸 io.Copy——不排干会静默吞帧（回环下真实发生过）。
func TestDrainBufferedWritesResidualBytes(t *testing.T) {
	var out bytes.Buffer
	rw := bufio.NewReadWriter(bufio.NewReader(strings.NewReader("residual-bytes")), bufio.NewWriter(&out))
	if err := drainBuffered(&out, rw.Reader); err != nil {
		t.Fatalf("drainBuffered: %v", err)
	}
	if out.String() != "residual-bytes" {
		t.Fatalf("got %q", out.String())
	}
	// 排干后再次调用是 no-op
	if err := drainBuffered(&out, rw.Reader); err != nil {
		t.Fatalf("second drain: %v", err)
	}
}

func TestProxyAIStreamPassthroughEcho(t *testing.T) {
	agentSrv := startEchoAgent(t)
	defer agentSrv.Close()

	origTarget := aiStreamTargetHost
	aiStreamTargetHost = func() string { return strings.TrimPrefix(agentSrv.URL, "http://") }
	defer func() { aiStreamTargetHost = origTarget }()

	coreSrv := httptest.NewServer(http.HandlerFunc(proxyAIStream))
	defer coreSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(coreSrv.URL, "http") + "/api/agent/ai/stream"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial through proxy: %v", err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(data) != "ping" {
		t.Fatalf("echo = %q, want ping", data)
	}
}

func TestProxyAIStreamAgentOfflineCloses1011(t *testing.T) {
	origTarget := aiStreamTargetHost
	aiStreamTargetHost = func() string { return "127.0.0.1:1" } // 必然拒绝
	defer func() { aiStreamTargetHost = origTarget }()

	coreSrv := httptest.NewServer(http.HandlerFunc(proxyAIStream))
	defer coreSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(coreSrv.URL, "http") + "/api/agent/ai/stream"

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		// 升级失败也算可达性错误可见，但本实现承诺先 Upgrade 再发 close 帧；
		// 走到 err 分支即说明实现用了裸 4xx/5xx，不合格。
		t.Fatalf("expected upgrade+close(1011), got dial error: %v (resp=%v)", err, resp)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage() // gorilla 把 close 帧转成 *websocket.CloseError 返回
	if err != nil {
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			if ce.Code != websocket.CloseInternalServerErr {
				t.Fatalf("close code = %d, want 1011", ce.Code)
			}
			if !strings.Contains(ce.Text, "device agent offline") {
				t.Fatalf("close reason = %q", ce.Text)
			}
			return
		}
		t.Fatalf("read close: %v", err)
	}
	t.Fatal("expected close frame, got normal message")
}
```

注意：`TestProxyAIStreamAgentOfflineCloses1011` 里 gorilla client 对 `ReadMessage` 收到 close 帧会返回 `*websocket.CloseError`——以该分支断言为准。

- [ ] **Step 2: 跑测试确认失败** — `go test ./app/http/handlers/ -run "TestProxyAIStream|TestDrainBuffered" -v` → FAIL（未定义）。

- [ ] **Step 3: 写实现**（`app/http/handlers/ai_stream_proxy.go`）。**要点：成功路径必须手写浏览器侧 101 握手**（`aiStreamUpgrader.Upgrade` 内部自 Hijack、不暴露 bufio，无法与排干/裸泵共存；gorilla client 收不到 `101 + Sec-WebSocket-Accept` 会在 Dial 即失败）：

```go
package handlers

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"aliang.one/nursorgate/app/http/services"
)

// aiStreamProxyDialTimeout 是拨向 agent 进程（56433）的握手期限。
const aiStreamProxyDialTimeout = 5 * time.Second

// websocketAcceptSuffix 是 RFC6455 固定的魔术串（Accept = b64(sha1(key+suffix))）。
const websocketAcceptSuffix = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// aiStreamTargetHost 可注入的透传目标（测试覆写）。生产取
// services.UserAgentBaseURL() 的 host——与 proxyToUserAgentClient 同源地址解析。
var aiStreamTargetHost = func() string {
	u, err := url.Parse(services.UserAgentBaseURL())
	if err != nil {
		return ""
	}
	return u.Host
}

var errAgentNotWebsocket = errors.New("agent did not upgrade to websocket")

// proxyAIStream 把仪表盘聊天 WS 以 TCP 级透传到 agent 进程（56433）的同一端点。
// 仪表盘会话必须跑在 agent 的 AI 管理器里（journal/统一清单与手机同源），
// core 绝不本地兜底（spec 2026-10-28-ai-stream-unify）。
// 失败语义：完成浏览器侧 Upgrade 后发 close(1011, "device agent offline")——
// 裸 Close 不发 close 帧，浏览器只会看到 1006，前端无法区分「设备不在线」。
func proxyAIStream(w http.ResponseWriter, r *http.Request) {
	// 与 aiStreamUpgrader 同源的 host 策略（CheckOrigin 检查的是 r.Host）：
	// 透传成功路径是手写 101、不经 Upgrader，必须显式复检，否则经非 localhost
	// 主机名（如 172.16.222.1:56431）访问的仪表盘会从今天的 403 变成能聊——
	// 与离线路径（走 rejectBrowserWS→Upgrade，仍 403）语义也不一致。
	if !aiStreamHostAllowed(r) {
		rejectBrowserWS(w, r, "origin not allowed")
		return
	}
	// 顺序铁律：先与 agent 握手成功，再 Hijack 浏览器——Hijack 之后就再也无法
	// 用 aiStreamUpgrader.Upgrade 发「101+close(1011)」了（裸 conn 发不出合法
	// close 帧），离线语义就会退化成 1006。
	host := aiStreamTargetHost()
	agentConn, err := net.DialTimeout("tcp", host, aiStreamProxyDialTimeout)
	if err == nil {
		var agentReader io.Reader
		agentReader, err = handshakeAgent(agentConn, host, r.URL.RequestURI())
		if err == nil {
			hj, ok := w.(http.Hijacker)
			if !ok {
				_ = agentConn.Close()
				http.Error(w, "websocket upgrade unsupported", http.StatusInternalServerError)
				return
			}
			var browserConn net.Conn
			var browserRW *bufio.ReadWriter
			browserConn, browserRW, err = hj.Hijack()
			if err == nil {
				// 手写浏览器侧 101（aiStreamUpgrader.Upgrade 会二次 Hijack
				// 且不暴露 bufio，无法与裸泵共存；bufio.Writer 必须 Flush）。
				err = completeBrowserHandshake(browserRW, r)
				if err == nil {
					// 浏览器侧读经 browserRW.Reader（排干防御性保留）；agent 侧
					// 早期帧已并入 agentReader。
					if err = drainBuffered(agentConn, browserRW.Reader); err == nil {
						done := make(chan struct{}, 2)
						go func() { _, _ = io.Copy(agentConn, browserRW.Reader); done <- struct{}{} }()
						go func() { _, _ = io.Copy(browserConn, agentReader); done <- struct{}{} }()
						<-done
					}
				}
				_ = browserConn.Close()
			}
		}
	}
	if agentConn != nil {
		_ = agentConn.Close() // 拨号失败时是 nil，裸调 Close 会 panic
	}
	if err != nil {
		// 尚未 Hijack，ResponseWriter 仍可用：正常 Upgrade + close(1011)，
		// 前端据此显示「设备 agent 不在线」。
		rejectBrowserWS(w, r, "device agent offline")
	}
}

// aiStreamHostAllowed 与 aiStreamUpgrader.CheckOrigin 同源同语义（r.Host 的
// host 段须为 localhost/127.0.0.1）。抽成函数供两处共用，防再漂移。
func aiStreamHostAllowed(r *http.Request) bool {
	host := strings.Split(r.Host, ":")[0]
	return host == "localhost" || host == "127.0.0.1"
}
```

同时把 `aiStreamUpgrader.CheckOrigin`（`agent_handler.go:52-56`）改为委托同一函数：

```go
var aiStreamUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     aiStreamHostAllowed,
}

// completeBrowserHandshake 手写 RFC6455 101 响应（key 来自浏览器握手请求）。
func completeBrowserHandshake(bw *bufio.ReadWriter, r *http.Request) error {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return errors.New("missing Sec-WebSocket-Key")
	}
	h := sha1.New()
	_, _ = h.Write([]byte(key + websocketAcceptSuffix))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := bw.WriteString(resp); err != nil {
		return err
	}
	return bw.Flush()
}

// handshakeAgent 与 agent 侧独立完成 WS 握手（自造新 key；浏览器↔core 的握手
// 由 completeBrowserHandshake 负责，core 只在 TCP 层架桥）。返回 agent 侧读取
// 源——MultiReader 前置 bufio，握手期间已到的早期帧不丢。
func handshakeAgent(conn net.Conn, host, requestURI string) (io.Reader, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	req := "GET " + requestURI + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return nil, err
	}
	rw := bufio.NewReader(conn)
	status, err := rw.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.Contains(status, "101") {
		return nil, errAgentNotWebsocket
	}
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			return io.MultiReader(rw, conn), nil
		}
	}
}

// drainBuffered 把 bufio.Reader 里已缓冲的字节先写给 dst（不排干会静默吞帧）。
func drainBuffered(dst io.Writer, src *bufio.Reader) error {
	if n := src.Buffered(); n > 0 {
		if _, err := io.CopyN(dst, src, int64(n)); err != nil {
			return err
		}
	}
	return nil
}

// rejectBrowserWS 完成浏览器侧 Upgrade 后发 close(1011, reason)。
func rejectBrowserWS(w http.ResponseWriter, r *http.Request, reason string) {
	conn, err := aiStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseInternalServerErr, reason),
		time.Now().Add(2*time.Second))
	_ = conn.Close()
}
```

（失败路径统一收敛：host 检查失败与 Hijack 之前任一步失败 → `rejectBrowserWS`；Hijack 之后失败（手写 101 写失败、drainBuffered 的 agent 侧写失败）连接已残，直接清理，浏览器侧看到 1006——可接受，此时连 101 都没发出去。绝不半桥。）

- [ ] **Step 4: 跑测试确认通过** — 同 Step 2 命令 → 3 个测试全 PASS（离线用例断言 close 1011 + reason；回声用例断言往返）。

- [ ] **Step 5: 编译 + 全量 handler 包回归** — `go build ./app/http/... && go test ./app/http/handlers/ 2>&1 | tail -3` → 全绿。

---

### Task 3: `HandleAIStream` 角色分支 + DispatchLocalAI 标弃

**Files:**
- Modify: `app/http/handlers/agent_handler.go:65-87`

- [ ] **Step 1: 角色分支** — `HandleAIStream` 开头插入：

```go
	// 2026-10-28 统一：仪表盘聊天必须在 agent 进程跑（journal/统一清单与手机
	// 同源）。core 角色一律 TCP 透传到 56433，绝不本地兜底——本地跑的会话进不
	// 了任何记录（2026-10-27 liang-dev 事故）。
	if !services.IsUserAgentRuntime() {
		proxyAIStream(w, r)
		return
	}
```

（`services` 已在该文件 import；若无需新增。）

- [ ] **Step 2: 修正 DispatchLocalAI 注释**（**不标 Deprecated**——任务 1 注册后 agent 角色端点仍经它跑本地 dispatch，它是活路径；对 spec §4.2 的有意偏离，理由：core 角色分支移除后唯一剩余调用方就是 agent 端点）：

```go
// 2026-10-28 起 core 角色的仪表盘聊天改走 WS 透传（见 handlers.proxyAIStream），
// 本函数现仅由 agent 进程的 /api/agent/ai/stream 端点（56433）调用。
```

（先 `grep -rn "DispatchLocalAI" app --include="*.go"` 确认调用方仅 `agent_handler.go:84`，若发现其他调用方，**停止并报告**。）

- [ ] **Step 3: 验证** — `go build ./app/... && go test ./app/http/handlers/ ./app/agentruntime/ 2>&1 | tail -3` → 全绿；`go vet ./app/http/handlers/` → 静默。

---

### Task 4: 自愈可观察性（共享函数 + 注入通知）

**Files:**
- Modify: `app/http/services/agent_ai_proxy_guard.go`
- Test: `app/http/services/agent_ai_proxy_guard_test.go`（**新建**；`package services`，按需补 `context`/`errors`/`testing`/`time` import）

- [ ] **Step 1: 写失败测试**（追加）：

```go
func TestEnsureAIUpstreamReachableNotifiesOnHealedUpstream(t *testing.T) {
	heals := 0
	notifed := 0
	origNotify := aiProxyHealNotifier
	aiProxyHealNotifier = func() { notifed++ }
	defer func() { aiProxyHealNotifier = origNotify }()

	dials := 0
	deps := aiProxyGuardDeps{
		dial: func(ctx context.Context, hostPort string) error {
			dials++
			if dials < 2 { // 首拨失败 → 触发 heal；复拨成功
				return errors.New("refused")
			}
			return nil
		},
		heal:  func() error { heals++; return nil },
		sleep: func(time.Duration) {},
	}
	if err := ensureAIUpstreamReachable(context.Background(), "http://127.0.0.1:56432", deps); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if heals != 1 || notifed != 1 {
		t.Fatalf("heals=%d notified=%d, want 1/1", heals, notifed)
	}
}

func TestEnsureAIUpstreamReachableSilentWhenHealFails(t *testing.T) {
	notifed := 0
	origNotify := aiProxyHealNotifier
	aiProxyHealNotifier = func() { notifed++ }
	defer func() { aiProxyHealNotifier = origNotify }()

	deps := aiProxyGuardDeps{
		dial: func(ctx context.Context, hostPort string) error { return errors.New("refused") },
		heal: func() error { return nil },
		sleep: func(time.Duration) {},
	}
	if err := ensureAIUpstreamReachable(context.Background(), "http://127.0.0.1:56432", deps); err == nil {
		t.Fatal("expected error when re-dial still fails")
	}
	if notifed != 0 {
		t.Fatalf("notified=%d on failed heal, want 0", notifed)
	}
}
```

（若执行时发现该文件已被前置任务创建，则按既有风格合并用例，不重复。）

- [ ] **Step 2: 跑测试确认失败** — `go test ./app/http/services/ -run TestEnsureAIUpstreamReachable -v` → FAIL（`aiProxyHealNotifier` 未定义）。

- [ ] **Step 3: 实现** — `agent_ai_proxy_guard.go`：

包级注入位（仿 `auth_runtime_hooks.go` 的 `desktopNotifier`）：

```go
// aiProxyHealNotifier 在自愈真正拉起代理后发出用户可见提示（桌面通知）。
// 可注入替身；默认系统通知。re-dial 成功才触发——非 root 403 时 POST「成功」
// 但代理没起来，不能误报。
var aiProxyHealNotifier = func() {
	desktop.Notify("aliang-gateway", "检测到 AI 代理未运行，已自动拉起")
}
```

`ensureAIUpstreamReachable` 的 re-dial 成功分支（现 `if err := deps.dial(ctx, u.Host); err == nil { return nil }`）改为：

```go
		if err := deps.dial(ctx, u.Host); err == nil {
			logger.Info("AI 上游不可达，已自动拉起本机代理(" + u.Host + ")")
			aiProxyHealNotifier()
			return nil
		}
```

import 区补 `aliang.one/nursorgate/common/desktop` 与 `aliang.one/nursorgate/common/logger`（若缺）。

- [ ] **Step 4: 跑测试确认通过** — 同 Step 2 → 全 PASS；`go build ./app/http/services/ && go vet ./app/http/services/` → 静默；`go test ./app/http/services/ 2>&1 | tail -3` → 全绿（该包测试多，若命中预存 flake 按「单包复跑」惯例处理并如实记录）。

---

### Task 5: 前端错误契约（看门狗 + close 语义 + i18n）

**Files:**
- Modify: `app/website/src/components/ChatPage.vue`
- Modify: `app/website/src/i18n/zh.js` / `en.js`（`chat_` 键区末尾追加）

- [ ] **Step 1: i18n 追加**（zh）：

```js
  chat_agentOffline: '设备 agent 不在线，请检查设备后再试。',
  chat_connInterrupted: '与设备的连接已中断，请重试。',
  chat_idleTimeout: 'AI 长时间无响应，请重试。',
```

（en）：

```js
  chat_agentOffline: 'Device agent is offline. Check the device and retry.',
  chat_connInterrupted: 'Connection to the device was interrupted. Please retry.',
  chat_idleTimeout: 'AI did not respond for a long time. Please retry.',
```

- [ ] **Step 2: 看门狗常量与状态**（设置条状态区附近追加）：

```js
// 无活动看门狗：ai.message 发出即武装，连续 CHAT_IDLE_TIMEOUT_MS 无任何帧
// （喂狗点在 handleAIEvent 入口，delta/thinking/status/progress/usage 一律算
// 活动）→ 合成气泡错误。E2E 经 window.__CHAT_IDLE_TIMEOUT_MS__ 注入短值。
const CHAT_IDLE_TIMEOUT_MS = Number(globalThis.__CHAT_IDLE_TIMEOUT_MS__) || 90000;
let idleWatchdogTimer = null;

function armIdleWatchdog(conv) {
  disarmIdleWatchdog();
  idleWatchdogTimer = setTimeout(() => {
    idleWatchdogTimer = null;
    const asst = findRunningAssistant(conv);
    if (!asst) return;
    asst.status = 'error';
    if (!asst.content) asst.content = t('chat_idleTimeout');
    finishRun(conv);
    saveToStorage();
  }, CHAT_IDLE_TIMEOUT_MS);
}

function disarmIdleWatchdog() {
  if (idleWatchdogTimer) {
    clearTimeout(idleWatchdogTimer);
    idleWatchdogTimer = null;
  }
}
```

- [ ] **Step 3: 武装/喂狗/解除接线**：
  - `sendMessage`：确认门通过、`sendAI({type:'ai.message',...})` 之前 → `armIdleWatchdog(conv);`；catch 分支与早退分支 → `disarmIdleWatchdog();`
  - `handleAIEvent` 入口（`let persist = false;` 前）→ `if (idleWatchdogTimer) armIdleWatchdog(conv);`（重置计时；任何事件帧都算活动）
  - `case 'ai.done'` / `case 'ai.error'` → `disarmIdleWatchdog();`
  - socket `onclose`（:953-957，现仅打日志）→ 追加：

```js
      disarmIdleWatchdog();
      const conv = activeConversation.value;
      if (conv && (sending.value || findRunningAssistant(conv))) {
        const asst = findRunningAssistant(conv);
        if (asst) {
          asst.status = 'error';
          if (!asst.content) {
            asst.content = e.code === 1011 ? t('chat_agentOffline') : t('chat_connInterrupted');
          }
        }
        conv.aiSessionCreated = false; // 下条消息经确认门重发 create 重试
        // 离线语义下同步压住设置条的 serviceUnavailable（先设 createError，
        // 再结算确认门——早退分支的 `!createError.value` 兜底就不触发了）
        if (conv.id === activeId.value && !createError.value) {
          createError.value = e.code === 1011 ? t('chat_agentOffline') : t('chat_connInterrupted');
        }
        const pending = conv.aiSessionId ? pendingCreates.get(conv.aiSessionId) : undefined;
        if (pending) pending('error'); // 别让确认门空等 5s 超时
        finishRun(conv);
        saveToStorage();
      }
```

（1011=设备 agent 不在线（透传器语义），其余 code=连接中断；结算确认门避免「气泡已报错、设置条 5s 后又弹 serviceUnavailable」的双重报错。）

**对 spec 的两处有意偏离（评审已认可）：** 看门狗喂狗点在 `handleAIEvent` 的 `let persist` 前（:1306）而非字面「入口」——slash 补全等非运行帧不喂狗，运行相关帧（delta/thinking/status/usage/done/error）全覆盖；自愈通知用包级 `aiProxyHealNotifier` 注入位而非 `deps` 字段——一处覆盖两个生产调用点。

- [ ] **Step 4: 验证** — `cd app/website && npm test 2>&1 | tail -3`（121+ 全绿）与 `npm run build 2>&1 | tail -2 && node ../../scripts/check-dist.mjs | tail -1`。

---

### Task 6: E2E（设备离线 + 挂死看门狗）

**Files:**
- Create: `app/website/tests/e2e/chat-unify.spec.js`

- [ ] **Step 1: 写 spec**（复用 `chat-settings.spec.js` 的 openChat/认证 stub 范式；FakeWebSocket 扩展两种模式）：

```js
import { expect, test } from '@playwright/test';

// mode=offline：socket 打开后立即被服务端 close(1011,'device agent offline')
// mode=idle：create 正常回执，但 ai.message 永不回帧 → 看门狗（短超时注入）
const WS_STUB = (mode) => `
  window.__aiFrames = [];
  window.__mode = ${JSON.stringify(mode)};
  window.EventSource = class { constructor() { setTimeout(() => this.onopen?.(), 0); } close() {} };
  class FakeWebSocket {
    static OPEN = 1; static CONNECTING = 0; static CLOSING = 2; static CLOSED = 3;
    constructor(url) {
      this.url = url; this.readyState = 0;
      setTimeout(() => { this.readyState = 1; this.onopen?.(); }, 0);
    }
    send(data) {
      const frame = JSON.parse(data);
      window.__aiFrames.push(frame);
      // offline：不回任何回执，立即由服务端语义 close(1011)——必须死在 create
      // 回执之前，否则确认门放行 ai.message、「不回退」断言必失败。
      if (window.__mode === 'offline') {
        setTimeout(() => this.onclose?.({ code: 1011, reason: 'device agent offline' }), 0);
        return;
      }
      // idle：create 正常回执（否则确认门 5s 超时走早退分支、看门狗根本没机会
      // 武装——挂死必须发生在「已武装」之后），ai.message 起的一切静默。
      if (frame.type === 'ai.session.create') {
        setTimeout(() => this.onmessage?.({ data: JSON.stringify({
          type: 'ai.session.created', session_id: frame.session_id, mode: frame.mode,
          project_path: frame.project_path || '/home/e2e', provider: frame.provider,
          model: frame.model || '', effort: frame.effort || '', state: 'idle',
        })}), 0);
        return;
      }
      if (window.__mode === 'idle') return;
      if (frame.type === 'ai.message') {
        setTimeout(() => this.onmessage?.({ data: JSON.stringify({ type: 'ai.done', session_id: frame.session_id }) }), 0);
      }
    }
    close() {}
  }
  window.WebSocket = FakeWebSocket;
`;

async function openChat(page) {
  // 与 chat-settings.spec.js 相同：认证 stub(/api/auth/session active+user)、
  // onboarding v2 completed、其余 /api/** 兜底空 data。进入聊天并新建会话，
  // 断言 chat-settings-directory 可见。
}

test('agent 离线：close(1011) 转成可见错误，不静默', async ({ page }) => {
  await openChat(page, { wsStub: WS_STUB('offline') });
  await page.getByPlaceholder(/输入消息|Type a message/).fill('你好');
  await page.keyboard.press('Enter');
  await expect(page.locator('div', { hasText: '设备 agent 不在线' }).first()).toBeVisible();
  // 不回退本地跑：确认门未过、ai.message 永不发出
  expect(await page.evaluate(() => window.__aiFrames.some(f => f.type === 'ai.message'))).toBe(false);
});

test('挂死看门狗：无帧超时转成可见错误', async ({ page }) => {
  await page.addInitScript(() => { window.__CHAT_IDLE_TIMEOUT_MS__ = 300; }); // 必须 app 代码运行前
  await openChat(page, { wsStub: WS_STUB('idle') });
  await page.getByPlaceholder(/输入消息|Type a message/).fill('你好');
  await page.keyboard.press('Enter');
  await expect(page.locator('div', { hasText: 'AI 长时间无响应' }).first()).toBeVisible({ timeout: 5000 });
});
```

（`openChat` 按 chat-settings.spec.js 实现补全；看门狗短超时注入必须经**独立 addInitScript**（app 代码运行前生效）：`page.addInitScript(() => { window.__CHAT_IDLE_TIMEOUT_MS__ = 300; })`，不走 openChat 的 wsStub 参数。断言选择器按实际 DOM 调整——错误内容在助手气泡内，定位放宽为文本包含即可；不得弱化「不回退」断言。）

- [ ] **Step 2: 运行** — `npx playwright test tests/e2e/chat-unify.spec.js` → 2 passed。选择器失配时按文本定位迭代，不得改断言语义。

---

### Task 7: 全量验证 + 汇总（提交须用户同意）

- [ ] **Step 1: 全量** — `go build ./app/... && go vet ./app/http/handlers/ ./app/http/services/ ./app/agentruntime/`；`go test ./app/http/handlers/ ./app/agentruntime/ ./app/http/services/ 2>&1 | tail -5`；`cd app/website && npm test && npm run build && npx playwright test 2>&1 | tail -5`。全绿（services 包命中预存 flake 时单包复跑并如实记录）。
- [ ] **Step 2: 汇总变更清单与提交建议，请示用户**（未获明确同意不 commit）。建议信息：`新增:仪表盘聊天 WS 透传到 agent 进程统一记录,错误必达看门狗,AI 自愈拉代理可见`。
