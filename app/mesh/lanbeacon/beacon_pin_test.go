package lanbeacon

// 任务 10b 单测:指纹源注入(广播前调用、nil=恒空)与来源 IP 捕获
// (UDP 来源地址 + 报文 mesh_port = 可拨 addr)。广播/收听整体路径的
// 三测试(TestBeaconBroadcastAndReceive/StopCeases/RestartResume)在本机
// 活 agent 环境下预存红(macOS 广播路由敏感,见 2026-11-08 批记忆),
// 本文件只用 sendRawTo 注入 + broadcastOnce 直调,不依赖广播可达。

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestIngestCapturesSenderAddr(t *testing.T) {
	port := pickUDPPort(t)
	b := NewBeacon("self-dev", 56441, port)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(b.Stop)

	now := time.Now().Unix()
	sendRawTo(t, port, map[string]any{"device_id": "dev-addr", "mesh_port": 41020, "ts": now})

	// 来源 IP=127.0.0.1(sendRawTo 的拨号端),拨号端口恒取报文 mesh_port。
	cand := pollCandidate(t, b, "dev-addr", func(c LanPeerCandidate) bool { return c.Addr != "" })
	if cand.Addr != "127.0.0.1:41020" {
		t.Fatalf("addr = %q, want 127.0.0.1:41020(来源 IP+报文端口)", cand.Addr)
	}
}

func TestFingerprintProviderWiredIntoBroadcast(t *testing.T) {
	port := pickUDPPort(t)
	b := NewBeacon("self-dev", 56441, port)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(b.Stop)

	// 广播 sendto 在本环境可能失败(broadcast 路由敏感),但指纹读取发生在
	// marshal 之前、sendto 之前——spy 计数即可钉「provider 已接线」。
	var calls int32
	b.SetFingerprintProvider(func() string {
		atomic.AddInt32(&calls, 1)
		return "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	})
	b.broadcastOnce()
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("fingerprint provider must be invoked per broadcast")
	}

	// nil provider = 恒空(诚实降级):空串进 payload。
	b.SetFingerprintProvider(nil)
	payload := b.payloadForTest()
	if payload.CertFingerprint != "" {
		t.Fatalf("nil provider fingerprint = %q, want empty", payload.CertFingerprint)
	}
	b.SetFingerprintProvider(func() string { return "SHA256:BBBB" })
	payload = b.payloadForTest()
	if payload.CertFingerprint != "SHA256:BBBB" {
		t.Fatalf("provider fingerprint = %q, want SHA256:BBBB", payload.CertFingerprint)
	}
}

// payloadForTest 广播载荷装配的测试缝:与 broadcastOnce 共用 assemblePayload
// 同一取值路径,供指纹接线断言不经 UDP。
func (b *Beacon) payloadForTest() beaconPayload {
	return b.assemblePayload()
}

func pollCandidate(t *testing.T, b *Beacon, deviceID string, ok func(LanPeerCandidate) bool) LanPeerCandidate {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range b.Candidates() {
			if c.DeviceID == deviceID && ok(c) {
				return c
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("candidate %s not matched within 3s", deviceID)
	return LanPeerCandidate{}
}

// 阴性:provider 未注入(默认 nil)时,直调 ingest 侧收到的候选指纹跟随
// 报文(报文带什么是什么)——本断言钉 ingest 不自产指纹。
func TestIngestFingerprintFollowsPayload(t *testing.T) {
	b := NewBeacon("self-dev", 56441, pickUDPPort(t))
	now := time.Now()
	data, err := json.Marshal(beaconPayload{
		DeviceID: "dev-fp", MeshPort: 41030, CertFingerprint: "SHA256:CCCC", TS: now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	b.ingest(data, now, "192.0.2.77")
	cands := b.Candidates()
	if len(cands) != 1 || cands[0].CertFingerprint != "SHA256:CCCC" || cands[0].Addr != "192.0.2.77:41030" {
		t.Fatalf("candidates = %+v", cands)
	}
}
