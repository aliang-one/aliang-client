package ssh

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"

	gossh "golang.org/x/crypto/ssh"
)

// meshFrameLine 构造一条帧头行(含换行)——字段与 mesh 分支 meshproto 同源:
// {"from","class","target","via","hops"}。
func meshFrameLine(from, class, target string, via []string, hops int) []byte {
	viaJSON, _ := json.Marshal(via)
	return []byte(fmt.Sprintf(`{"from":%q,"class":%q,"target":%q,"via":%s,"hops":%d}`+"\n",
		from, class, target, viaJSON, hops))
}

// openMeshChannel 用 mesh 凭证连入并打开 direct-tcpip 通道。帧头在 channel
// 数据流上、不在 SSH payload 里(payload 任意,传 nil)。
func openMeshChannel(t *testing.T, addr string, meshSigner gossh.Signer) gossh.Channel {
	t.Helper()
	conn, err := dialIngress(t, addr, gossh.PublicKeys(meshSigner))
	if err != nil {
		t.Fatalf("mesh key must authenticate: %v", err)
	}
	ch, _, err := conn.OpenChannel(channelDirectTCPIP, nil)
	if err != nil {
		t.Fatalf("direct-tcpip open: %v", err)
	}
	return ch
}

// expectMeshReject 断言拒帧语义:先读到一行 JSON 错误 {"err":code},
// 随后通道被服务端关闭(EOF)。
func expectMeshReject(t *testing.T, ch gossh.Channel, code string) {
	t.Helper()
	line, err := bufio.NewReader(ch).ReadString('\n')
	if err != nil {
		t.Fatalf("reject line read: %v", err)
	}
	var got struct {
		Err string `json:"err"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &got); err != nil {
		t.Fatalf("reject line is not JSON: %q: %v", line, err)
	}
	if got.Err != code {
		t.Fatalf("reject err = %q, want %q (raw line %q)", got.Err, code, line)
	}
	if _, err := ch.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("channel must be closed after reject line, got %v", err)
	}
}

func TestMeshFrameParse(t *testing.T) {
	// 合法帧:各字段解析对位。
	r := bufio.NewReader(strings.NewReader(
		`{"from":"node-a","class":"ai","target":"example.org:8443","via":["node-a"],"hops":2}` + "\n"))
	h, err := ParseMeshFrameHeader(r)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.From != "node-a" || h.Class != "ai" || h.Target != "example.org:8443" || h.Hops != 2 {
		t.Fatalf("fields = %+v", h)
	}
	if !reflect.DeepEqual(h.Via, []string{"node-a"}) {
		t.Fatalf("via = %v", h.Via)
	}

	// via 缺省归一为 [From](链头即首跳,与 meshproto 同语义)。
	r = bufio.NewReader(strings.NewReader(
		`{"from":"node-a","class":"self","target":"example.org:443","hops":0}` + "\n"))
	if h, err = ParseMeshFrameHeader(r); err != nil {
		t.Fatalf("parse without via: %v", err)
	} else if !reflect.DeepEqual(h.Via, []string{"node-a"}) {
		t.Fatalf("via default = %v, want [node-a]", h.Via)
	}

	// 超长行(>64KB)拒:合法 JSON 但首行超限同样拒(长度检查先于 JSON)。
	big := `{"from":"` + strings.Repeat("a", 70*1024) + `","class":"self","target":"x:1"}` + "\n"
	if _, err = ParseMeshFrameHeader(bufio.NewReader(strings.NewReader(big))); err == nil {
		t.Fatal("oversized header line must be rejected")
	}
	// 缓冲区大于上限的 reader 下,显式长度检查仍须拒(不依赖 reader 缓冲尺寸)。
	if _, err = ParseMeshFrameHeader(bufio.NewReaderSize(strings.NewReader(big), 256*1024)); err == nil {
		t.Fatal("oversized header line must be rejected regardless of reader buffer size")
	}

	// 非 JSON 拒。
	if _, err = ParseMeshFrameHeader(bufio.NewReader(strings.NewReader("this is not json\n"))); err == nil {
		t.Fatal("non-JSON header must be rejected")
	}
	// 缺必填字段拒(from/class/target 逐项)。
	for _, tc := range []struct {
		name, line string
	}{
		{"missing from", `{"class":"self","target":"x:1","via":[],"hops":0}`},
		{"missing class", `{"from":"node-a","target":"x:1","via":[],"hops":0}`},
		{"missing target", `{"from":"node-a","class":"self","via":[],"hops":0}`},
	} {
		if _, err = ParseMeshFrameHeader(bufio.NewReader(strings.NewReader(tc.line + "\n"))); err == nil {
			t.Fatalf("%s must be rejected", tc.name)
		}
	}
	// 负跳数拒:防伪造 {"hops":-N} 绕过跳数帽(同源 meshproto 加固)。
	if _, err = ParseMeshFrameHeader(bufio.NewReader(strings.NewReader(
		`{"from":"node-a","class":"self","target":"x:1","via":[],"hops":-3}` + "\n"))); err == nil {
		t.Fatal("negative hops must be rejected")
	}
	// 控制字节拒:from/class/target 与 via 条目都会进防环日志,拒绝对端经
	// JSON 转义注入控制字符(同源 meshproto 加固)。
	if _, err = ParseMeshFrameHeader(bufio.NewReader(strings.NewReader(
		`{"from":"node-a\t","class":"self","target":"x:1","via":[],"hops":0}` + "\n"))); err == nil {
		t.Fatal("control byte in from must be rejected")
	}
	if _, err = ParseMeshFrameHeader(bufio.NewReader(strings.NewReader(
		`{"from":"node-a","class":"self","target":"x:1","via":["b\n"],"hops":0}` + "\n"))); err == nil {
		t.Fatal("control byte in via must be rejected")
	}
	// 无换行即 EOF:帧头行不完整拒。
	if _, err = ParseMeshFrameHeader(bufio.NewReader(strings.NewReader(`{"from":"a"}`))); err == nil {
		t.Fatal("header line without newline must be rejected")
	}
}

func TestMeshFormAppendsHopAndInjects(t *testing.T) {
	metaCh := make(chan *M.Metadata, 1)
	frameMetaCh := make(chan *IngressFrameMeta, 1)
	handleDone := make(chan struct{})
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error {
		defer close(handleDone)
		metaCh <- metadata
		fm, ok := ctx.Value(ContextKeyIngressFrameMeta).(*IngressFrameMeta)
		if !ok {
			frameMetaCh <- nil
		} else {
			frameMetaCh <- fm
		}
		_, _ = io.Copy(conn, conn) // 回显:证明注入 conn 双向通
		return nil
	})

	meshPub, meshSigner := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{
		MeshKeys:    []gossh.PublicKey{meshPub},
		LocalNodeID: func() string { return "node-b" },
		// ForwardGate nil=全放行(合并集成缝:本分支凭证列表即 ACL)。
	})
	ch := openMeshChannel(t, addr, meshSigner)

	// 帧头与首段载荷一次写出:钉住「帧头后 bufio 预读字节必须回放进注入
	// conn,不丢首段」(mesh 分支 WrappedConn 同款坑)。
	frame := meshFrameLine("node-a", "self", "example.org:8443", []string{"node-a"}, 1)
	payload := []byte("payload-after-header")
	if _, err := ch.Write(append(frame, payload...)); err != nil {
		t.Fatalf("client write frame: %v", err)
	}

	// 回显只含帧头后字节(帧头已被服务端消费,不得进注入流)。
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(ch, echo); err != nil {
		t.Fatalf("client echo read: %v", err)
	}
	if string(echo) != string(payload) {
		t.Fatalf("echo = %q, want %q", echo, payload)
	}

	// 桩侧断言 Metadata:目标拆解(host:port→HostName/DstPort)、连接层源、
	// ConnID/网络类型。
	var md *M.Metadata
	select {
	case md = <-metaCh:
	case <-time.After(3 * time.Second):
		t.Fatal("mesh frame must reach the pipeline handler")
	}
	if md.HostName != "example.org" {
		t.Fatalf("HostName = %q, want example.org", md.HostName)
	}
	if md.DstPort != 8443 {
		t.Fatalf("DstPort = %d, want 8443", md.DstPort)
	}
	if !md.SrcIP.IsValid() || md.SrcIP != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("SrcIP = %q, want 127.0.0.1 (from ssh conn RemoteAddr)", md.SrcIP)
	}
	if md.SrcPort == 0 {
		t.Fatal("SrcPort must carry the mesh node ephemeral port, got 0")
	}
	if !strings.HasPrefix(md.ConnID, "sshing-") {
		t.Fatalf("ConnID = %q, want prefix sshing-", md.ConnID)
	}
	if md.Network != M.TCP {
		t.Fatalf("Network = %v, want M.TCP", md.Network)
	}
	if md.DstIP.IsValid() {
		t.Fatalf("DstIP must stay unset (bare domain carried unresolved), got %q", md.DstIP)
	}

	// ctx 上的 IngressFrameMeta:AppendHop 后 Via 含本机、Hops+1、Class 透传。
	var fm *IngressFrameMeta
	select {
	case fm = <-frameMetaCh:
	case <-time.After(3 * time.Second):
		t.Fatal("frame meta must be observable on the handler ctx")
	}
	if fm == nil {
		t.Fatal("IngressFrameMeta must be attached via ContextKeyIngressFrameMeta")
	}
	if fm.Class != "self" {
		t.Fatalf("frame Class = %q, want self", fm.Class)
	}
	if want := []string{"node-a", "node-b"}; !reflect.DeepEqual(fm.Via, want) {
		t.Fatalf("frame Via = %v, want %v (AppendHop appends local node id)", fm.Via, want)
	}
	if fm.Hops != 2 {
		t.Fatalf("frame Hops = %d, want 2 (1+1 after AppendHop)", fm.Hops)
	}

	// 收尾同形态①:半关→桩 echo EOF→Handle 返回→服务端关通道。
	if err := ch.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	if _, err := ch.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after pipeline handler closed channel, got %v", err)
	}
	select {
	case <-handleDone:
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline handler must return after channel teardown")
	}
}

func TestMeshFormRejectsLoop(t *testing.T) {
	meshPub, meshSigner := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{
		MeshKeys:    []gossh.PublicKey{meshPub},
		LocalNodeID: func() string { return "node-b" },
	})
	ch := openMeshChannel(t, addr, meshSigner)
	// Via 已含本机 node-b:path vector 防环主防线命中。
	if _, err := ch.Write(meshFrameLine("node-a", "self", "example.org:443", []string{"node-a", "node-b"}, 2)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	expectMeshReject(t, ch, "loop")
}

func TestMeshFormRejectsHopsCap(t *testing.T) {
	meshPub, meshSigner := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{
		MeshKeys:    []gossh.PublicKey{meshPub},
		LocalNodeID: func() string { return "node-b" },
	})
	ch := openMeshChannel(t, addr, meshSigner)
	if _, err := ch.Write(meshFrameLine("node-a", "self", "example.org:443", []string{"node-a"}, MeshHopsHardCap)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	expectMeshReject(t, ch, "hops-exceeded")
}

func TestMeshFormRejectsDNSTarget(t *testing.T) {
	meshPub, meshSigner := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{
		MeshKeys:    []gossh.PublicKey{meshPub},
		LocalNodeID: func() string { return "node-b" },
	})
	ch := openMeshChannel(t, addr, meshSigner)
	// dns: 目标一期拒连:代解析语义归 mesh 分支 Task 8,合并后集成启用。
	if _, err := ch.Write(meshFrameLine("node-a", "custom", "dns:example.org", []string{"node-a"}, 0)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	expectMeshReject(t, ch, "dns-unsupported")
}

// TestMeshFormGateClosedWhenDisabled 钉纵深防御:LocalNodeID 未注入
// (装配未完成)→ 形态②整体禁用(config.go 契约),拒帧;Enable=false 的
// 常规门禁场景在 reconcile 层拦,ForwardGate=false 分支见 TestMeshFormForwardGate。
func TestMeshFormGateClosedWhenDisabled(t *testing.T) {
	meshPub, meshSigner := newTestKey(t)
	_, addr := startTestServer(t, RunConfig{
		MeshKeys: []gossh.PublicKey{meshPub},
		// LocalNodeID 刻意不注入。
	})
	ch := openMeshChannel(t, addr, meshSigner)
	if _, err := ch.Write(meshFrameLine("node-a", "self", "example.org:443", []string{"node-a"}, 0)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	expectMeshReject(t, ch, "forward-disabled")
}

func TestMeshFormForwardGate(t *testing.T) {
	// 全子测共用桩:过闸子测断言注入到达(钩子通路),拒子测不触达。
	metaCh := make(chan *M.Metadata, 1)
	swapTCPHandler(t, func(ctx context.Context, conn net.Conn, metadata *M.Metadata) error {
		metaCh <- metadata
		_, _ = io.Copy(io.Discard, conn)
		return nil
	})
	meshPub, meshSigner := newTestKey(t)
	local := func() string { return "node-b" }

	cases := []struct {
		name       string
		gate       func() (bool, []string)
		wantReject string // 空=应过闸(注入到达)
	}{
		{"gate false rejects", func() (bool, []string) { return false, nil }, "forward-disabled"},
		{"gate true without forwarder rejects", func() (bool, []string) { return true, []string{"node-x"} }, "not-allowed"},
		{"gate true with forwarder passes", func() (bool, []string) { return true, []string{"node-a"} }, ""},
		{"nil gate allows (merge seam)", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, addr := startTestServer(t, RunConfig{
				MeshKeys:    []gossh.PublicKey{meshPub},
				LocalNodeID: local,
				ForwardGate: tc.gate,
			})
			ch := openMeshChannel(t, addr, meshSigner)
			if _, err := ch.Write(meshFrameLine("node-a", "ai", "example.org:443", []string{"node-a"}, 1)); err != nil {
				t.Fatalf("write frame: %v", err)
			}
			if tc.wantReject != "" {
				expectMeshReject(t, ch, tc.wantReject)
				return
			}
			select {
			case md := <-metaCh:
				if md.HostName != "example.org" || md.DstPort != 443 {
					t.Fatalf("metadata = %s:%d, want example.org:443", md.HostName, md.DstPort)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("allowed frame must reach the pipeline handler")
			}
		})
	}
}

func TestMeshFormBadHeaderLineDisconnects(t *testing.T) {
	for _, tc := range []struct {
		name, line string
	}{
		{"non json", "this is not a frame header\n"},
		// 缺 class:帧头解析失败断开不兜底(spec §5)。
		{"missing field", `{"from":"node-a","target":"example.org:443","via":[],"hops":0}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meshPub, meshSigner := newTestKey(t)
			_, addr := startTestServer(t, RunConfig{
				MeshKeys:    []gossh.PublicKey{meshPub},
				LocalNodeID: func() string { return "node-b" },
			})
			ch := openMeshChannel(t, addr, meshSigner)
			if _, err := ch.Write([]byte(tc.line)); err != nil {
				t.Fatalf("write: %v", err)
			}
			// 静默断开:无错误行,零字节即 EOF(节点凭证发不出合法帧=对端
			// 实现错误,不兜底、不回写)。
			n, err := ch.Read(make([]byte, 64))
			if n != 0 || !errors.Is(err, io.EOF) {
				t.Fatalf("bad header must silently disconnect, got n=%d err=%v", n, err)
			}
		})
	}
}
