// mesh_dns_test.go — mesh-exit dns 代解析面(SPEC §9,自 processor/tcp/
// mesh_inbound_test.go 同源移植,F1 删源侧测试文件后此处为唯一权威):帧往返
// + fake resolver 注入 + 负应答折叠(NXDOMAIN/SERVFAIL)+ 类型矩阵(A/AAAA/
// PTR/MX/SRV/TXT/不支持类型/空 Question)+ 交换窗口超时路径。入口分派
// (`{"t":"dns"}` 分流)由 openFakeMeshDNSChannel 经 handleMeshExitChannel
// 驱动;解析器缝与超时缝用包级 var 覆写+还原,非并行安全(本包测试串行)。
package ssh

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// fakeDNSResolver 是 LocalDNSResolver 测试替身:按方法注行为(缺省零值返回
// 空=合法 NODATA 形态),want 前缀字段记录收到的入参供类型矩阵断言(同源
// processor/tcp 的 fakeLocalDNSResolver 形态;M1:LookupIP 的 network 入参
// 必须记录——AAAA 请求拿到 v4 会被解析面静默过滤,只有入参断言能钉住)。
type fakeDNSResolver struct {
	lookupIP   func(ctx context.Context, network, host string) ([]net.IP, error)
	lookupAddr func(ctx context.Context, addr string) ([]string, error)
	lookupMX   func(ctx context.Context, name string) ([]*net.MX, error)
	lookupSRV  func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	lookupTXT  func(ctx context.Context, name string) ([]string, error)

	// ips/err 是 LookupIP 的快捷注行(等价 lookupIP 闭包,保留给简单用例)。
	ips []net.IP
	err error

	gotIPNetwork string
	gotIPHost    string
	gotAddrArg   string
	gotMXName    string
	gotSRVArgs   [3]string
	gotTXTName   string
}

func (f *fakeDNSResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	f.gotIPNetwork, f.gotIPHost = network, host
	if f.lookupIP != nil {
		return f.lookupIP(ctx, network, host)
	}
	return f.ips, f.err
}
func (f *fakeDNSResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	f.gotAddrArg = addr
	if f.lookupAddr != nil {
		return f.lookupAddr(ctx, addr)
	}
	return nil, nil
}
func (f *fakeDNSResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	f.gotMXName = name
	if f.lookupMX != nil {
		return f.lookupMX(ctx, name)
	}
	return nil, nil
}
func (f *fakeDNSResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	f.gotSRVArgs = [3]string{service, proto, name}
	if f.lookupSRV != nil {
		return f.lookupSRV(ctx, service, proto, name)
	}
	return "", nil, nil
}
func (f *fakeDNSResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	f.gotTXTName = name
	if f.lookupTXT != nil {
		return f.lookupTXT(ctx, name)
	}
	return nil, nil
}

// withFakeDNSResolver 覆写包级解析器缝并在清理时还原。
func withFakeDNSResolver(t *testing.T, r LocalDNSResolver) {
	t.Helper()
	old := meshDNSResolver
	meshDNSResolver = r
	t.Cleanup(func() { meshDNSResolver = old })
}

// withDNSResolveTimeout 缩小交换总窗口缝并在清理时还原(超时路径测试用;
// 覆写+恢复,非并行安全)。
func withDNSResolveTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := meshDNSResolveTimeout
	meshDNSResolveTimeout = d
	t.Cleanup(func() { meshDNSResolveTimeout = old })
}

// openFakeMeshDNSChannel 用替身通道走一遍 handleMeshExitChannel 的 dns 分派:
// gate 开(过门禁)→ Accept → dns 分支 spawn serveMeshDNSExit;返回替身
// 通道的客户端侧 pipe 端(双方按 [2B 长度][报文] 帧读写)。
func openFakeMeshDNSChannel(t *testing.T, s *Server) net.Conn {
	t.Helper()
	nc := &fakeNewChannel{chType: channelMeshExit, extra: []byte(`{"t":"dns"}`)}
	driveServeChannels(t, s, meshTestPerms(), nc)
	if nc.rejected {
		t.Fatalf("dns channel must be accepted, rejected %q", nc.message)
	}
	if !nc.accepted {
		t.Fatal("dns channel must be accepted")
	}
	return nc.acceptEnd
}

// exchangeDNSFrame 在 pipe 两端做一次完整帧交换:写查询帧,读响应帧。
func exchangeDNSFrame(t *testing.T, client net.Conn, query []byte) []byte {
	t.Helper()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))

	frame := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(frame, uint16(len(query)))
	copy(frame[2:], query)
	if _, err := client.Write(frame); err != nil {
		t.Fatalf("write query frame: %v", err)
	}

	var hdr [2]byte
	if _, err := io.ReadFull(client, hdr[:]); err != nil {
		t.Fatalf("read response length: %v", err)
	}
	resp := make([]byte, binary.BigEndian.Uint16(hdr[:]))
	if _, err := io.ReadFull(client, resp); err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp
}

// exchangeDNSMsg 交换一帧并解包响应(类型矩阵用)。
func exchangeDNSMsg(t *testing.T, client net.Conn, query []byte) *dns.Msg {
	t.Helper()
	respWire := exchangeDNSFrame(t, client, query)
	var resp dns.Msg
	if err := resp.Unpack(respWire); err != nil {
		t.Fatalf("unpack response: %v", err)
	}
	return &resp
}

// dnsQueryWire 构造 DNS 查询报文线格式(按需指定 id/查询名/类型)。
func dnsQueryWire(t *testing.T, id uint16, name string, qtype uint16) (*dns.Msg, []byte) {
	t.Helper()
	q := new(dns.Msg)
	q.Id = id
	q.SetQuestion(dns.Fqdn(name), qtype)
	wire, err := q.Pack()
	if err != nil {
		t.Fatalf("pack query: %v", err)
	}
	return q, wire
}

// TestMeshDNSExitRoundTrip 正向往返:{"t":"dns"} 通道 → 读查询帧 → fake
// resolver 返回固定 IP → 合成 A 记录响应帧 → 关通道。
func TestMeshDNSExitRoundTrip(t *testing.T) {
	withFakeDNSResolver(t, &fakeDNSResolver{ips: []net.IP{net.IPv4(9, 9, 9, 9)}})
	s := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")

	q, query := dnsQueryWire(t, 1, "example.org", dns.TypeA)
	client := openFakeMeshDNSChannel(t, s)
	resp := exchangeDNSMsg(t, client, query)

	if resp.Id != q.Id {
		t.Fatalf("response id = %d, want %d", resp.Id, q.Id)
	}
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %s, want NOERROR", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("answers = %d, want 1", len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("answer type = %T, want *dns.A", resp.Answer[0])
	}
	if !a.A.Equal(net.IPv4(9, 9, 9, 9)) {
		t.Fatalf("A = %v, want 9.9.9.9", a.A)
	}
	if a.Hdr.Ttl != meshDNSAnswerTTL {
		t.Fatalf("TTL = %d, want %d (fixed answer TTL)", a.Hdr.Ttl, meshDNSAnswerTTL)
	}
	if a.Hdr.Name != dns.Fqdn("example.org") {
		t.Fatalf("answer name = %q, want example.org. (query name echoed)", a.Hdr.Name)
	}

	// 交换完成即关(单问单答):再读必 EOF。
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("channel must be closed after dns exchange, got %v", err)
	}
}

// TestMeshDNSExitNegativeResponse 钉负应答折叠(dnsRcodeForResolveError):
// IsNotFound(上游 NXDOMAIN)回 RcodeNameError 让客户端负缓存;其余错误
// 折 SERVFAIL。负应答仍带 Question 回显且无 Answer。
func TestMeshDNSExitNegativeResponse(t *testing.T) {
	s := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")

	cases := []struct {
		name      string
		resolver  *fakeDNSResolver
		wantRcode int
	}{
		{"not found folds to nxdomain", &fakeDNSResolver{err: &net.DNSError{Err: "no such host", Name: "example.org", IsNotFound: true}}, dns.RcodeNameError},
		{"transient error folds to servfail", &fakeDNSResolver{err: errors.New("upstream down")}, dns.RcodeServerFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFakeDNSResolver(t, tc.resolver)
			q, query := dnsQueryWire(t, 2, "example.org", dns.TypeA)
			client := openFakeMeshDNSChannel(t, s)
			resp := exchangeDNSMsg(t, client, query)

			if resp.Id != q.Id {
				t.Fatalf("response id = %d, want %d", resp.Id, q.Id)
			}
			if resp.Rcode != tc.wantRcode {
				t.Fatalf("rcode = %s, want %s", dns.RcodeToString[resp.Rcode], dns.RcodeToString[tc.wantRcode])
			}
			if len(resp.Question) != 1 || resp.Question[0].Name != dns.Fqdn("example.org") {
				t.Fatalf("question must be echoed, got %+v", resp.Question)
			}
			if len(resp.Answer) != 0 {
				t.Fatalf("negative response must carry no answers, got %d", len(resp.Answer))
			}
			if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("channel must be closed after response, got %v", err)
			}
		})
	}
}

// TestMeshDNSExitTypeMatrix 类型矩阵(design §6 层1 定案,自 processor/tcp
// 同源移植):A/AAAA→LookupIP(network 按 v4/v6 分流,M1 钉 ip6 入参);
// PTR→LookupAddr(查询名反算点分地址);MX/SRV/TXT→对应 Lookup;SRV 拆
// _service._proto 三元组。
func TestMeshDNSExitTypeMatrix(t *testing.T) {
	s := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")

	t.Run("AAAA walks ip6 LookupIP", func(t *testing.T) {
		fake := &fakeDNSResolver{}
		fake.lookupIP = func(ctx context.Context, network, host string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("2001:db8::1")}, nil
		}
		withFakeDNSResolver(t, fake)
		_, query := dnsQueryWire(t, 7, "v6.example.com", dns.TypeAAAA)
		client := openFakeMeshDNSChannel(t, s)
		resp := exchangeDNSMsg(t, client, query)

		aaaa, ok := resp.Answer[0].(*dns.AAAA)
		if !ok || aaaa.AAAA.String() != "2001:db8::1" {
			t.Fatalf("answer = %v, want AAAA 2001:db8::1", resp.Answer)
		}
		// M1:AAAA 必须带着 ip6 network 进解析面——传 ip4 会被上面的类型
		// 过滤静默吞掉 v6 记录,只有入参断言能钉住分流。
		if fake.gotIPNetwork != "ip6" {
			t.Fatalf("LookupIP network = %q, want ip6", fake.gotIPNetwork)
		}
		if fake.gotIPHost != "v6.example.com." {
			t.Fatalf("LookupIP host = %q, want v6.example.com. (fqdn)", fake.gotIPHost)
		}
	})

	t.Run("PTR reverse-parses in-addr.arpa to dotted addr", func(t *testing.T) {
		fake := &fakeDNSResolver{}
		fake.lookupAddr = func(ctx context.Context, addr string) ([]string, error) {
			return []string{"host.example.com."}, nil
		}
		withFakeDNSResolver(t, fake)
		_, query := dnsQueryWire(t, 8, "4.3.2.1.in-addr.arpa", dns.TypePTR)
		client := openFakeMeshDNSChannel(t, s)
		resp := exchangeDNSMsg(t, client, query)

		ptr, ok := resp.Answer[0].(*dns.PTR)
		if !ok || ptr.Ptr != "host.example.com." {
			t.Fatalf("answer = %v, want PTR host.example.com.", resp.Answer)
		}
		if fake.gotAddrArg != "1.2.3.4" {
			t.Fatalf("LookupAddr arg = %q, want 1.2.3.4 (reverse-parsed dotted addr)", fake.gotAddrArg)
		}
	})

	t.Run("MX synthesizes fixed-TTL answer", func(t *testing.T) {
		fake := &fakeDNSResolver{}
		fake.lookupMX = func(ctx context.Context, name string) ([]*net.MX, error) {
			return []*net.MX{{Pref: 10, Host: "mail.example.com"}}, nil
		}
		withFakeDNSResolver(t, fake)
		_, query := dnsQueryWire(t, 9, "example.com", dns.TypeMX)
		client := openFakeMeshDNSChannel(t, s)
		resp := exchangeDNSMsg(t, client, query)

		mx, ok := resp.Answer[0].(*dns.MX)
		if !ok || mx.Preference != 10 || mx.Mx != "mail.example.com." {
			t.Fatalf("answer = %v, want MX 10 mail.example.com.", resp.Answer)
		}
		if mx.Hdr.Ttl != meshDNSAnswerTTL {
			t.Fatalf("TTL = %d, want %d", mx.Hdr.Ttl, meshDNSAnswerTTL)
		}
		if fake.gotMXName != "example.com." {
			t.Fatalf("LookupMX name = %q, want example.com. (fqdn)", fake.gotMXName)
		}
	})

	t.Run("SRV splits _service._proto prefix", func(t *testing.T) {
		fake := &fakeDNSResolver{}
		fake.lookupSRV = func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
			return "sip.example.com", []*net.SRV{{Priority: 1, Weight: 2, Port: 5060, Target: "sip.example.com"}}, nil
		}
		withFakeDNSResolver(t, fake)
		_, query := dnsQueryWire(t, 10, "_sip._tcp.example.com", dns.TypeSRV)
		client := openFakeMeshDNSChannel(t, s)
		resp := exchangeDNSMsg(t, client, query)

		srv, ok := resp.Answer[0].(*dns.SRV)
		if !ok || srv.Port != 5060 || srv.Target != "sip.example.com." {
			t.Fatalf("answer = %v, want SRV 5060 sip.example.com.", resp.Answer)
		}
		if got := fake.gotSRVArgs; got != [3]string{"sip", "tcp", "example.com"} {
			t.Fatalf("LookupSRV args = %v, want [sip tcp example.com] (triple split)", got)
		}
	})

	t.Run("TXT synthesizes strings answer", func(t *testing.T) {
		fake := &fakeDNSResolver{}
		fake.lookupTXT = func(ctx context.Context, name string) ([]string, error) {
			return []string{"hello mesh"}, nil
		}
		withFakeDNSResolver(t, fake)
		_, query := dnsQueryWire(t, 11, "example.com", dns.TypeTXT)
		client := openFakeMeshDNSChannel(t, s)
		resp := exchangeDNSMsg(t, client, query)

		txt, ok := resp.Answer[0].(*dns.TXT)
		if !ok || len(txt.Txt) != 1 || txt.Txt[0] != "hello mesh" {
			t.Fatalf("answer = %v, want TXT [hello mesh]", resp.Answer)
		}
		if fake.gotTXTName != "example.com." {
			t.Fatalf("LookupTXT name = %q, want example.com. (fqdn)", fake.gotTXTName)
		}
	})
}

// TestMeshDNSExitUnsupportedTypeServfail 其余 qtype 一律 SERVFAIL(design §6
// 层1 定案,同源移植),含空 Question 的畸形查询。
func TestMeshDNSExitUnsupportedTypeServfail(t *testing.T) {
	s := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")
	withFakeDNSResolver(t, &fakeDNSResolver{})

	for name, qtype := range map[string]uint16{
		"CNAME": dns.TypeCNAME,
		"NS":    dns.TypeNS,
		"NULL":  dns.TypeNULL,
	} {
		t.Run(name, func(t *testing.T) {
			_, query := dnsQueryWire(t, 12, "example.com", qtype)
			client := openFakeMeshDNSChannel(t, s)
			resp := exchangeDNSMsg(t, client, query)
			if resp.Rcode != dns.RcodeServerFailure {
				t.Fatalf("rcode = %s, want SERVFAIL (qtype %d unsupported)", dns.RcodeToString[resp.Rcode], qtype)
			}
			if len(resp.Answer) != 0 {
				t.Fatalf("unsupported qtype must carry no answers, got %d", len(resp.Answer))
			}
			if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("channel must be closed after response, got %v", err)
			}
		})
	}

	t.Run("empty question servfail", func(t *testing.T) {
		m := new(dns.Msg)
		m.Id = 13
		query, err := m.Pack()
		if err != nil {
			t.Fatalf("pack empty query: %v", err)
		}
		client := openFakeMeshDNSChannel(t, s)
		resp := exchangeDNSMsg(t, client, query)
		if resp.Rcode != dns.RcodeServerFailure {
			t.Fatalf("rcode = %s, want SERVFAIL (empty question)", dns.RcodeToString[resp.Rcode])
		}
		if resp.Id != 13 {
			t.Fatalf("id = %d, want 13 (servfail still echoes the id)", resp.Id)
		}
	})
}

// TestMeshDNSExitWindowClosesSilentChannel 超时路径 ①(AfterFunc 交换窗口):
// dns 通道打开后不写任何帧 → 读取阻塞 → meshDNSResolveTimeout 到期 AfterFunc
// 关通道 → 客户端侧有限时间内读到 EOF(慢速/静默对端不无限占住 goroutine;
// 无此窗口本用例会撞自己的 2s 读护栏挂死)。断言有界返回而非精确时长。
func TestMeshDNSExitWindowClosesSilentChannel(t *testing.T) {
	withDNSResolveTimeout(t, 50*time.Millisecond)
	withFakeDNSResolver(t, &fakeDNSResolver{})
	s := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")

	client := openFakeMeshDNSChannel(t, s)
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second)) // 防实现漏关时测试永久挂住
	start := time.Now()
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("silent dns channel must be closed by the exchange window")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("window close took %v, want bounded well under the 2s guard (window=50ms)", elapsed)
	}
}

// TestMeshDNSExitWindowCutsBlockedResolve 超时路径 ②(解析侧窗口):fake
// resolver 挂死(阻塞到 ctx 完成)→ 解析窗口(meshDNSResolveTimeout 的 ctx
// 超时)解除阻塞折 SERVFAIL,交换窗口(AfterFunc)几乎同时关通道——客户端
// 侧要么收到 SERVFAIL 响应(写赢在关前)、要么直接 EOF(先关),二者都必须
// 有限时间内返回;不赌竞态落点,只断有界返回。
func TestMeshDNSExitWindowCutsBlockedResolve(t *testing.T) {
	withDNSResolveTimeout(t, 50*time.Millisecond)
	fake := &fakeDNSResolver{}
	fake.lookupIP = func(ctx context.Context, network, host string) ([]net.IP, error) {
		<-ctx.Done() // 挂到解析窗口到期(ctx 超时解除阻塞,不泄漏 goroutine)
		return nil, ctx.Err()
	}
	withFakeDNSResolver(t, fake)
	s := NewServer(RunConfig{ForwardGate: func() bool { return true }}, "")

	_, query := dnsQueryWire(t, 21, "slow.example.org", dns.TypeA)
	client := openFakeMeshDNSChannel(t, s)
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	start := time.Now()

	frame := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(frame, uint16(len(query)))
	copy(frame[2:], query)
	if _, err := client.Write(frame); err != nil {
		t.Fatalf("write query frame: %v", err)
	}

	var hdr [2]byte
	n, rerr := io.ReadFull(client, hdr[:])
	switch {
	case rerr == nil:
		// 帧头已到达(写赢在关前):载荷可能完整(折好的 SERVFAIL),也可能
		// 被窗口在 writeDNSLengthFrame 两段写之间掐断(EOF/UnexpectedEOF)
		// ——后者同样是交换窗口生效的有界返回,不苛求完整响应。
		respWire := make([]byte, binary.BigEndian.Uint16(hdr[:]))
		if _, perr := io.ReadFull(client, respWire); perr == nil {
			var resp dns.Msg
			if err := resp.Unpack(respWire); err != nil {
				t.Fatalf("unpack response: %v", err)
			}
			if resp.Rcode != dns.RcodeServerFailure {
				t.Fatalf("rcode = %s, want SERVFAIL (blocked resolve folds)", dns.RcodeToString[resp.Rcode])
			}
		}
	case n == 0 && (errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrClosedPipe)):
		// 先关后写:EOF 即交换窗口生效。
	default:
		t.Fatalf("unexpected read outcome n=%d err=%v (want response frame or EOF)", n, rerr)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("blocked resolve took %v to surface, want bounded well under the 2s guard (window=50ms)", elapsed)
	}
}
