package handlers

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
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
	hijacked := false
	agentConn, err := net.DialTimeout("tcp", host, aiStreamProxyDialTimeout)
	if err != nil {
		logger.Warn(fmt.Sprintf("ai stream proxy: dial agent %q failed: %v", host, err))
	}
	if err == nil {
		var agentReader io.Reader
		agentReader, err = handshakeAgent(agentConn, host, r.URL.RequestURI())
		if err != nil {
			logger.Warn(fmt.Sprintf("ai stream proxy: agent %q websocket handshake failed: %v", host, err))
		}
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
				hijacked = true
				// 手写浏览器侧 101（aiStreamUpgrader.Upgrade 会二次 Hijack
				// 且不暴露 bufio，无法与裸泵共存；bufio.Writer 必须 Flush）。
				err = completeBrowserHandshake(browserRW, r)
				if err != nil {
					logger.Warn(fmt.Sprintf("ai stream proxy: browser 101 handshake write failed: %v", err))
				}
				if err == nil {
					// 浏览器侧读经 browserRW.Reader（排干防御性保留）；agent 侧
					// 早期帧已并入 agentReader。
					if err = drainBuffered(agentConn, browserRW.Reader); err != nil {
						logger.Warn(fmt.Sprintf("ai stream proxy: drain buffered browser bytes failed: %v", err))
					}
					if err == nil {
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
		if hijacked {
			// Hijack 之后失败：浏览器侧要么已收到 101、要么连接压根没建立，
			// 此时 ResponseWriter 已不可用，裸 conn 上也发不出合法 close 帧，
			// 浏览器只会观察到 1006——可接受；本函数承诺的 1011 离线语义
			// 只覆盖 Hijack 之前的失败。
			return
		}
		// 尚未 Hijack，ResponseWriter 仍可用：正常 Upgrade + close(1011)，
		// 前端据此显示「设备 agent 不在线」。
		rejectBrowserWS(w, r, "device agent offline")
	}
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
	// net.DialTimeout 只兜 connect：wedged agent 也能靠内核 backlog 完成 TCP
	// 握手，之后响应永远不来——读死线防止下面的 ReadString 无限阻塞，把
	// 浏览器卡在 CONNECTING（无 1011、无日志的僵尸 agent 形态）。
	if err := conn.SetReadDeadline(time.Now().Add(aiStreamProxyDialTimeout)); err != nil {
		return nil, err
	}
	rw := bufio.NewReader(conn)
	status, err := rw.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(status, "HTTP/1.1 101") {
		return nil, fmt.Errorf("%w: status %q", errAgentNotWebsocket, status)
	}
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			// 读死线用完即弃：透传会话可能长时间空闲（用户不发言），
			// 不清掉的话空闲会话会在 5s 处被硬掐。
			if err := conn.SetReadDeadline(time.Time{}); err != nil {
				return nil, err
			}
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
