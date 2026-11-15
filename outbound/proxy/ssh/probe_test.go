// probe_test.go — SSH 出站链路探测（客户端「测试连接」）的行为钉：
// 成功路径（往返+指纹+延迟）、分层失败（认证/握手/目标不可达）、TOFU
// 首连钉扎+复连，与 sshtest 假服务器全链路。目标不可达走 loopback
// 未监听端口（确定性失败，不出网）。portOf/splitHostPort 复用 ssh_test.go
// 同包既有 helper。
package ssh

import (
	"strings"
	"testing"

	"aliang.one/nursorgate/outbound/proxy/ssh/sshtest"
)

func probeNode(host string, port uint16) NodeConfig {
	return NodeFromAddrPort(host, port, "tester", "pw", "", "")
}

// probeNodeWrongPass 是凭据错误形态（认证必须失败）。
func probeNodeWrongPass(host string, port uint16) NodeConfig {
	return NodeFromAddrPort(host, port, "tester", "WRONG", "", "")
}

func TestProbeSuccessAndFingerprint(t *testing.T) {
	_, addr := sshtest.Start(t, sshtest.WithUsername("tester"), sshtest.WithPassword("pw"))
	echoAddr, closeEcho := sshtest.EchoListener(t)
	defer closeEcho()
	echoHost, echoPort := hostOf(t, echoAddr), portOf(t, echoAddr)

	res := Probe(probeNode("127.0.0.1", portOf(t, addr)), nil, nil, ProbeTarget{Host: echoHost, Port: echoPort})
	if !res.OK {
		t.Fatalf("probe should succeed, got stage=%s err=%s", res.Stage, res.Error)
	}
	if res.LatencyMillis < 0 {
		t.Fatalf("latency must be >= 0, got %d", res.LatencyMillis)
	}
	if !strings.HasPrefix(res.HostKeyFpr, "SHA256:") {
		t.Fatalf("host key fingerprint = %q, want SHA256: prefix", res.HostKeyFpr)
	}
}

func TestProbeRejectsBadPassword(t *testing.T) {
	_, addr := sshtest.Start(t, sshtest.WithUsername("tester"), sshtest.WithPassword("pw"))
	res := Probe(probeNodeWrongPass("127.0.0.1", portOf(t, addr)), nil, nil, ProbeTarget{})
	if res.OK {
		t.Fatal("probe with wrong password must fail")
	}
	if res.Stage != probeStageHandshake {
		t.Fatalf("stage = %s, want handshake (x/crypto 把认证失败归入握手层), got err=%s", res.Stage, res.Error)
	}
}

func TestProbeRejectsBadPrivateKey(t *testing.T) {
	res := Probe(NodeFromAddrPort("127.0.0.1", 1, "u", "", "not-a-pem", ""), nil, nil, ProbeTarget{})
	if res.OK {
		t.Fatal("probe with corrupt key must fail")
	}
	if res.Stage != probeStageAuth {
		t.Fatalf("stage = %s, want auth, err=%s", res.Stage, res.Error)
	}
}

func TestProbeDialRefused(t *testing.T) {
	// loopback 未监听端口：确定性 connection refused，不出网
	res := Probe(probeNode("127.0.0.1", 1), nil, nil, ProbeTarget{})
	if res.OK {
		t.Fatal("probe to closed port must fail")
	}
	if res.Stage != probeStageDial {
		t.Fatalf("stage = %s, want dial, err=%s", res.Stage, res.Error)
	}
}

func TestProbeTOFULearnThenVerify(t *testing.T) {
	// 首测（hosts 为空库）→ TOFU 钉扎；复测 → 指纹一致放行。
	// mismatch 拒连的 fail-closed 行为由 chain_test 的 HostKeyCallback 用例覆盖。
	_, addr := sshtest.Start(t, sshtest.WithUsername("tester"), sshtest.WithPassword("pw"))
	echoAddr, closeEcho := sshtest.EchoListener(t)
	defer closeEcho()
	echoHost, echoPort := hostOf(t, echoAddr), portOf(t, echoAddr)
	hosts := NewKnownHosts(t.TempDir() + "/known_hosts.json")

	res := Probe(probeNode("127.0.0.1", portOf(t, addr)), nil, hosts, ProbeTarget{Host: echoHost, Port: echoPort})
	if !res.OK {
		t.Fatalf("first probe (TOFU learn) should succeed, got %s: %s", res.Stage, res.Error)
	}
	if len(hosts.List()) != 1 {
		t.Fatalf("TOFU must pin exactly one entry after first probe, got %d", len(hosts.List()))
	}
	res2 := Probe(probeNode("127.0.0.1", portOf(t, addr)), nil, hosts, ProbeTarget{Host: echoHost, Port: echoPort})
	if !res2.OK {
		t.Fatalf("second probe (TOFU verify) should succeed, got %s: %s", res2.Stage, res2.Error)
	}
}

func TestProbeTargetUnreachable(t *testing.T) {
	// 服务器在但探测目标端口无人听 → 通道打开被拒（ConnectionFailed），
	// 归 channel 阶段——「通道打不开」与「通道开了数据不通」是两类故障。
	_, addr := sshtest.Start(t, sshtest.WithUsername("tester"), sshtest.WithPassword("pw"))
	res := Probe(probeNode("127.0.0.1", portOf(t, addr)), nil, nil, ProbeTarget{Host: "127.0.0.1", Port: 1})
	if res.OK {
		t.Fatal("probe to unreachable target must fail")
	}
	if res.Stage != probeStageChannel && res.Stage != probeStageRoundTrip {
		t.Fatalf("stage = %s, want channel|roundtrip, err=%s", res.Stage, res.Error)
	}
}
