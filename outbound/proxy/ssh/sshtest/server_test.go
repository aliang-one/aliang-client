package sshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// openDirect 是测试侧打开 direct-tcpip 通道的公共路径。
func openDirect(t *testing.T, client *gossh.Client, target string) gossh.Channel {
	t.Helper()
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("split target: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	ch, _, err := client.OpenChannel("direct-tcpip", gossh.Marshal(directTCPIPMsg{
		Addr: host, Port: uint32(port), OriginAddr: "127.0.0.1", OriginPort: 0,
	}))
	if err != nil {
		t.Fatalf("open direct-tcpip: %v", err)
	}
	return ch
}

// readN 带超时读满 n 字节,防 pipe 死锁挂死整个测试。
func readN(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, n)
		got := 0
		for got < n {
			m, err := r.Read(buf[got:])
			got += m
			if err != nil {
				done <- result{buf[:got], err}
				return
			}
		}
		done <- result{buf, nil}
	}()
	select {
	case res := <-done:
		if got := len(res.data); got != n {
			t.Fatalf("读回显不足: got %d want %d (err=%v)", got, n, res.err)
		}
		return res.data
	case <-time.After(5 * time.Second):
		t.Fatal("读回显超时:pipe 疑似死锁")
		return nil
	}
}

// assertDrainsToEOF 断言半关后对端数据排空并 EOF,不挂死。
func assertDrainsToEOF(t *testing.T, r io.Reader) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(r)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("半关后读 EOF: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("半关后未 EOF:pipe 半关语义挂死")
	}
}

// assertNoGoroutineLeak 等待 goroutine 数回落到 baseline+slack 以内,
// 给「装置 Close 后无泄漏」以硬证据(失败重试容忍异步收尾)。
func assertNoGoroutineLeak(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		now := runtime.NumGoroutine()
		if now <= baseline+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine 泄漏: 收尾后 %d 个 > 基线 %d+2", now, baseline)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func dialSSH(t *testing.T, addr, user string, auth gossh.AuthMethod) *gossh.Client {
	t.Helper()
	c, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            user,
		Auth:            []gossh.AuthMethod{auth},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return c
}

// TestSmokePasswordEcho 装置冒烟:密码认证 → direct-tcpip → 大包回显(验证
// 流控窗口下的 pipe 搬运)→ 半关后排空 EOF(验证半关语义不挂死)→ 无泄漏。
func TestSmokePasswordEcho(t *testing.T) {
	baseline := runtime.NumGoroutine()

	srv, addr := Start(t, WithPassword("secret"), WithUsername("tester"))

	// 错密码必须被拒。
	if bad, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            "tester",
		Auth:            []gossh.AuthMethod{gossh.Password("wrong")},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}); err == nil {
		bad.Close()
		t.Fatal("错密码竟然连上了")
	}

	client := dialSSH(t, addr, "tester", gossh.Password("secret"))

	echoAddr, closeEcho := EchoListener(t)

	ch := openDirect(t, client, echoAddr)

	// 300KB 超出单窗口默认值,覆盖流控路径。
	payload := make([]byte, 300_000)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("gen payload: %v", err)
	}
	go func() {
		_, _ = ch.Write(payload) // 写失败由读侧超时/比对暴露,这里不用 t
		_ = ch.CloseWrite()
	}()

	got := readN(t, ch, len(payload))
	if !bytes.Equal(got, payload) {
		t.Fatal("回显内容不一致")
	}
	assertDrainsToEOF(t, ch)

	// 显式清场(与 t.Cleanup 幂等叠加),然后取泄漏证据。
	_ = ch.Close()
	_ = client.Close()
	closeEcho()
	srv.Close()
	assertNoGoroutineLeak(t, baseline)
}

// TestSmokeCascadePublicKey 验证公钥认证与 NextDial 两跳级联:
// client → hop1(sshd) → NextDial → hop2(sshd, 公钥认证) → direct-tcpip → echo。
func TestSmokeCascadePublicKey(t *testing.T) {
	baseline := runtime.NumGoroutine()

	_, pub, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("client key gen: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(pub)
	if err != nil {
		t.Fatalf("client key signer: %v", err)
	}

	_, wrongPub, _ := ed25519.GenerateKey(rand.Reader)
	wrongSigner, _ := gossh.NewSignerFromKey(wrongPub)

	echoAddr, closeEcho := EchoListener(t)

	hop2, hop2Addr := Start(t, WithAuthorized(signer.PublicKey()))

	// 错公钥必须被拒(同类型不同 key)。
	if bad, err := gossh.Dial("tcp", hop2Addr, &gossh.ClientConfig{
		User:            "tester",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(wrongSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}); err == nil {
		bad.Close()
		t.Fatal("错公钥竟然连上了")
	}

	hop2Client := dialSSH(t, hop2Addr, "tester", gossh.PublicKeys(signer))

	hop1, hop1Addr := Start(t, WithPassword("hop1"), WithNextDial(func(target string) (net.Conn, error) {
		host, portStr, err := net.SplitHostPort(target)
		if err != nil {
			return nil, fmt.Errorf("split next target: %w", err)
		}
		port, _ := strconv.Atoi(portStr)
		ch, _, err := hop2Client.OpenChannel("direct-tcpip", gossh.Marshal(directTCPIPMsg{
			Addr: host, Port: uint32(port), OriginAddr: "127.0.0.1", OriginPort: 0,
		}))
		if err != nil {
			return nil, fmt.Errorf("open next-hop channel: %w", err)
		}
		return WrapChannel(ch), nil
	}))

	client := dialSSH(t, hop1Addr, "tester", gossh.Password("hop1"))

	ch := openDirect(t, client, echoAddr)

	payload := []byte("cascade through two sshd hops")
	go func() {
		_, _ = ch.Write(payload)
		_ = ch.CloseWrite()
	}()

	got := readN(t, ch, len(payload))
	if !bytes.Equal(got, payload) {
		t.Fatal("级联回显内容不一致")
	}
	assertDrainsToEOF(t, ch)

	// 显式清场(与 t.Cleanup 幂等叠加),然后取泄漏证据。
	_ = ch.Close()
	_ = client.Close()
	_ = hop2Client.Close()
	hop1.Close()
	hop2.Close()
	closeEcho()
	assertNoGoroutineLeak(t, baseline)
}
