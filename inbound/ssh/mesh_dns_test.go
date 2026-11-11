// mesh_dns_test.go — mesh-exit dns 代解析面(SPEC §9,自 processor/tcp/
// mesh_inbound.go 搬运):[2B 长度][DNS 报文] 帧往返 + fake resolver 注入 +
// 负应答折叠(NXDOMAIN/SERVFAIL)。入口分派(`{"t":"dns"}` 分流)由
// openFakeMeshDNSChannel 经 handleMeshExitChannel 驱动;解析器缝用包级 var
// 覆写+还原,非并行安全(本包测试串行)。
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

// fakeDNSResolver 是 LocalDNSResolver 测试替身:LookupIP 注入固定解析结果,
// 其余类型未实现(替身不发真查询)。
type fakeDNSResolver struct {
	ips []net.IP
	err error
}

func (f *fakeDNSResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	return f.ips, f.err
}
func (f *fakeDNSResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeDNSResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeDNSResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	return "", nil, errors.New("not implemented")
}
func (f *fakeDNSResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return nil, errors.New("not implemented")
}

// withFakeDNSResolver 覆写包级解析器缝并在清理时还原。
func withFakeDNSResolver(t *testing.T, r LocalDNSResolver) {
	t.Helper()
	old := meshDNSResolver
	meshDNSResolver = r
	t.Cleanup(func() { meshDNSResolver = old })
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

func dnsAQueryWire(t *testing.T, name string) (*dns.Msg, []byte) {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), dns.TypeA)
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

	q, query := dnsAQueryWire(t, "example.org")
	client := openFakeMeshDNSChannel(t, s)
	respWire := exchangeDNSFrame(t, client, query)

	var resp dns.Msg
	if err := resp.Unpack(respWire); err != nil {
		t.Fatalf("unpack response: %v", err)
	}
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
	q, query := dnsAQueryWire(t, "example.org")

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
			client := openFakeMeshDNSChannel(t, s)
			respWire := exchangeDNSFrame(t, client, query)

			var resp dns.Msg
			if err := resp.Unpack(respWire); err != nil {
				t.Fatalf("unpack response: %v", err)
			}
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
