package tcp

// mesh UDP 打洞批次 Task 12——tcp 主路径 punch 接线测试(getMeshExitFor
// Execution 工厂层 + dialViaMesh 载体可见性日志):
//   - 工厂三态:开关开+缝接线 → udp 进切片;开关关 → 无 udp(今日行为);
//     开关开+缝未接线 → 哨兵语义(无 udp 不炸);
//   - 翻开关 → CacheKey 变 → 缓存重建(运行期开关生效机制,SPEC §10.4);
//   - [MESH-OUT] 载体日志行:成功知最终载体,udp 失败原因随行(静默回落
//     必须有痕迹,评审定谳)。
//
// 装配缝(egressPeer/meshDNSForwardPeer)的同款三态测试在
// app/tunnel/mesh_assembly_test.go(缝归 app/tunnel 消费)。

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	meshproxy "aliang.one/nursorgate/outbound/proxy/mesh"
	"aliang.one/nursorgate/processor/config"
	gossh "golang.org/x/crypto/ssh"
)

// ---- 测试基建(复用 handler_mesh_test.go 的 newMeshDialTestEnv/真 sshd 夹具)----

// swapMeshPunchDialSource 覆写打洞拨号注入缝并恢复(同 swapMeshTransport
// DialSource 的 swap+Cleanup 模式);非并行安全。
func swapMeshPunchDialSource(t *testing.T, fn func() meshproxy.PunchDialFunc) {
	t.Helper()
	orig, _ := meshPunchDialSource.Load().(func() meshproxy.PunchDialFunc)
	SetMeshPunchDialSource(fn)
	t.Cleanup(func() { SetMeshPunchDialSource(orig) })
}

// setMeshPunchDialConfig 带 punch 开关的配置脚手架(其余口径同
// setMeshDialConfig:真身份私钥+SSH 入口显式开,防依赖降级误吞)。
func setMeshPunchDialConfig(t *testing.T, keyPEM string, peers []config.MeshPeer, punch *bool) {
	t.Helper()
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			Mesh:       &config.MeshConfig{PrivateKey: keyPEM, Peers: peers, PunchEnable: punch},
			SSHIngress: &config.SSHIngressConfig{Enable: boolPtr(true)},
		},
	})
}

// punchDialStub 恒定失败的打洞拨号器(载体可见性用例:udp 尝试失败回落)。
func punchDialStub(errText string) meshproxy.PunchDialFunc {
	return func(_ context.Context, _ string) (net.Conn, error) {
		return nil, fmt.Errorf("%s", errText)
	}
}

// exitTransportNames 工厂产物的载体观测面(ExitClient.TransportNames)。
func exitTransportNames(t *testing.T, client *meshproxy.ExitClient) []string {
	t.Helper()
	if client == nil {
		t.Fatal("ExitClient 为 nil,无法观测载体切片")
	}
	return client.TransportNames()
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// ---- 工厂三态(Task 12 核心改造的观测面)----

// TestGetMeshExitPunchWiringMatrix 工厂 punch 接线矩阵:开关(调用时刻
// 配置)× 缝接线态 → udp 载体是否进 transports 切片。piko 中继缝全程不
// 接线(本矩阵只隔离 punch 一个变量)。
func TestGetMeshExitPunchWiringMatrix(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	peers := []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:56432"}, HostKey: "SHA256:AAAA"}}
	wired := func() func() meshproxy.PunchDialFunc {
		return func() meshproxy.PunchDialFunc { return punchDialStub("stub-unavailable") }
	}

	t.Run("开关开+缝接线→udp进切片", func(t *testing.T) {
		setMeshPunchDialConfig(t, keyPEM, peers, boolPtr(true))
		swapMeshPunchDialSource(t, wired())
		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		client, err := handler.getMeshExitForExecution("dev-b")
		if err != nil {
			t.Fatalf("getMeshExitForExecution: %v", err)
		}
		names := exitTransportNames(t, client)
		if !containsString(names, "udp") {
			t.Fatalf("punch 开+接线时 transports 应含 udp, got %v", names)
		}
	})

	t.Run("开关关(默认nil)→无udp(今日行为)", func(t *testing.T) {
		setMeshPunchDialConfig(t, keyPEM, peers, nil)
		swapMeshPunchDialSource(t, wired())
		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		client, err := handler.getMeshExitForExecution("dev-b")
		if err != nil {
			t.Fatalf("getMeshExitForExecution: %v", err)
		}
		names := exitTransportNames(t, client)
		if containsString(names, "udp") {
			t.Fatalf("punch 默认关时 transports 不应含 udp, got %v", names)
		}
	})

	t.Run("开关显式关+缝接线→无udp", func(t *testing.T) {
		setMeshPunchDialConfig(t, keyPEM, peers, boolPtr(false))
		swapMeshPunchDialSource(t, wired())
		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		client, err := handler.getMeshExitForExecution("dev-b")
		if err != nil {
			t.Fatalf("getMeshExitForExecution: %v", err)
		}
		if containsString(exitTransportNames(t, client), "udp") {
			t.Fatal("punch 显式关时 transports 不应含 udp(关→传 nil 是工厂责任)")
		}
	})

	t.Run("开关开+缝未接线→哨兵:无udp不炸", func(t *testing.T) {
		setMeshPunchDialConfig(t, keyPEM, peers, boolPtr(true))
		swapMeshPunchDialSource(t, nil) // SetMeshPunchDialSource(nil)=未接线哨兵
		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		client, err := handler.getMeshExitForExecution("dev-b")
		if err != nil {
			t.Fatalf("缝未接线是哨兵语义(不进切片),不应报错: %v", err)
		}
		if containsString(exitTransportNames(t, client), "udp") {
			t.Fatal("缝未接线时 transports 不应含 udp")
		}
	})
}

// TestGetMeshExitPunchToggleRebuildsCache 翻 punch 开关 → CacheKey 变 →
// 缓存重建(运行期开关生效机制,评审定谳:该行为必须钉在工厂层)。其余
// spec 字段全程不变,隔离单一键分量。
func TestGetMeshExitPunchToggleRebuildsCache(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	peers := []config.MeshPeer{{DeviceID: "dev-b", LANAddrs: []string{"192.168.1.20:56432"}, HostKey: "SHA256:AAAA"}}
	swapMeshPunchDialSource(t, func() meshproxy.PunchDialFunc {
		return punchDialStub("stub-unavailable")
	})
	handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)

	setMeshPunchDialConfig(t, keyPEM, peers, boolPtr(true))
	onClient, err := handler.getMeshExitForExecution("dev-b")
	if err != nil {
		t.Fatalf("getMeshExitForExecution(punch on): %v", err)
	}
	again, err := handler.getMeshExitForExecution("dev-b")
	if err != nil {
		t.Fatalf("getMeshExitForExecution(punch on, 2nd): %v", err)
	}
	if again != onClient {
		t.Fatal("同配置重复装配应命中缓存(翻开关前缓存语义不回归)")
	}

	setMeshPunchDialConfig(t, keyPEM, peers, boolPtr(false))
	offClient, err := handler.getMeshExitForExecution("dev-b")
	if err != nil {
		t.Fatalf("getMeshExitForExecution(punch off): %v", err)
	}
	if offClient == onClient {
		t.Fatal("翻 punch 开关应重建缓存客户端(CacheKey 的 Punch 位翻变)")
	}
	if containsString(exitTransportNames(t, offClient), "udp") {
		t.Fatal("关向后新客户端不应含 udp 载体")
	}

	setMeshPunchDialConfig(t, keyPEM, peers, boolPtr(true))
	backOn, err := handler.getMeshExitForExecution("dev-b")
	if err != nil {
		t.Fatalf("getMeshExitForExecution(punch back on): %v", err)
	}
	if backOn == offClient {
		t.Fatal("再翻回应脱离关向客户端(键回到开向)")
	}
	// 键翻回开向=命中首次开向的缓存条目(旧条目随键驻留,唯一失效路径是
	// 键不匹配即另立,SPEC §10.4)——实例复用正是翻键重建机制的直接证据。
	if backOn != onClient {
		t.Fatal("翻回开向应命中首次开向的缓存条目(键往返)")
	}
	if !containsString(exitTransportNames(t, backOn), "udp") {
		t.Fatal("开向后客户端应含 udp 载体")
	}
}

// ---- 载体可见性日志(评审定谳:静默回落必须有痕迹)----

// captureMeshCarrierLog 换下载体日志落点收集消息(swap+Cleanup;并发安全
// 由 mutex 兜住——日志可能来自拨号 goroutine)。
func captureMeshCarrierLog(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	orig := meshDialCarrierLog
	meshDialCarrierLog = func(msg string) {
		mu.Lock()
		got = append(got, msg)
		mu.Unlock()
	}
	t.Cleanup(func() { meshDialCarrierLog = orig })
	return &got
}

// TestDialViaMeshCarrierVisibilityLog [MESH-OUT] 载体日志行:真实流量路径
// (dialViaMesh→ExitClient 拨号)上,成功必须留「via carrier=<lan/udp/piko>」
// 痕迹;udp 尝试失败即便回落成功也随行——旧 server+新 agent 的静默丢帧
// 场景正是排查重点。真 sshd 夹具承载 LAN/udp 载体的完整 SSH 握手。
func TestDialViaMeshCarrierVisibilityLog(t *testing.T) {
	keyPEM := newMeshDialTestEnv(t)
	keySigner, err := gossh.ParsePrivateKey([]byte(keyPEM))
	if err != nil {
		t.Fatalf("parse identity: %v", err)
	}
	sshd := newMeshTestSSHD(t, keySigner.PublicKey(), "hang")
	swapMeshPunchDialSource(t, func() meshproxy.PunchDialFunc {
		return punchDialStub("punch signalling rejected (peer firmware too old)")
	})

	t.Run("udp失败回落piko→载体+udp失败原因", func(t *testing.T) {
		// 评审场景本体:lan 失败+udp(打洞)失败 → piko 承载成功。载体日志
		// 行必须同时留「via carrier=piko」与 udp 失败原因——否则静默回落
		// 无从排查。目标 56432 回环 → ai 类别(piko 准入 self/ai)。
		swapMeshTransportDialSource(t, func() meshproxy.PikoDialFunc {
			return func(ctx context.Context, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", sshd.Addr())
			}
		})
		setMeshPunchDialConfig(t, keyPEM, []config.MeshPeer{{
			DeviceID: "dev-b", LANAddrs: []string{closedMeshAddr(t)}, HostKey: sshd.HostKeyPinned(),
		}}, boolPtr(true))
		logs := captureMeshCarrierLog(t)
		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		conn, err := handler.dialViaMesh(context.Background(), meshDialMetadata("toNode:dev-b", "127.0.0.1:56432"))
		if err != nil {
			t.Fatalf("dialViaMesh: %v(piko 载体应承载成功)", err)
		}
		defer conn.Close()
		if len(*logs) != 1 {
			t.Fatalf("成功拨号应打恰好一行载体日志, got %d: %v", len(*logs), *logs)
		}
		for _, want := range []string{"[MESH-OUT]", "via carrier=piko", "udp failed", "peer firmware too old"} {
			if !strings.Contains((*logs)[0], want) {
				t.Fatalf("载体日志行应含 %q, got: %s", want, (*logs)[0])
			}
		}
	})

	t.Run("udp承载成功→via carrier=udp", func(t *testing.T) {
		// 无 LAN 地址 → LAN Admit=false,udp 是唯一准入直连载体;punch 拨号
		// 直拨真 sshd 的 TCP 地址(承载层不区分,SSH 握手照走)。
		setMeshPunchDialConfig(t, keyPEM, []config.MeshPeer{{
			DeviceID: "dev-b", HostKey: sshd.HostKeyPinned(),
		}}, boolPtr(true))
		swapMeshPunchDialSource(t, func() meshproxy.PunchDialFunc {
			return func(ctx context.Context, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", sshd.Addr())
			}
		})
		logs := captureMeshCarrierLog(t)
		handler := NewTCPConnectionHandler(NewDefaultProtocolDetector(), NewDefaultTLSHandler(), nil, nil)
		conn, err := handler.dialViaMesh(context.Background(), meshDialMetadata("toNode:dev-b", "127.0.0.1:9"))
		if err != nil {
			t.Fatalf("dialViaMesh: %v(udp 载体应承载成功)", err)
		}
		defer conn.Close()
		if len(*logs) != 1 {
			t.Fatalf("成功拨号应打恰好一行载体日志, got %d: %v", len(*logs), *logs)
		}
		if !strings.Contains((*logs)[0], "via carrier=udp") {
			t.Fatalf("载体日志行应含 via carrier=udp, got: %s", (*logs)[0])
		}
		if strings.Contains((*logs)[0], "udp failed") {
			t.Fatalf("udp 承载成功时不应有失败注记, got: %s", (*logs)[0])
		}
	})
}
