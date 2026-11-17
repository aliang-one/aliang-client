package tunnel

// 任务 10b 打通半(addr 增补)单测:augmentLANAddrsFromBeacon 三闸语义。
// beacon 候选经 ingest 直调注入(无 UDP 时序),指级行为钉死:
// 手填恒权威 / 指纹精确相等才采纳 / 未钉扎不采纳 / 回环剔除。

import (
	"net"
	"testing"
	"time"

	"aliang.one/nursorgate/app/mesh/lanbeacon"
)

const (
	augPinFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	augOtherFinger    = "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
)

func augSeedBeacon(t *testing.T) *lanbeacon.Beacon {
	t.Helper()
	b := lanbeacon.NewBeacon("self-dev", 56432, pickBeaconTestPort(t))
	now := time.Now()
	for _, tc := range []struct {
		deviceID    string
		sourceIP    string
		fingerprint string
	}{
		{"dev-peer", "192.168.1.30", augPinFingerprint},
		{"dev-peer-otherpin", "192.168.1.31", augOtherFinger},
		{"dev-peer-nofp", "192.168.1.32", ""},
		{"dev-peer-loopback", "127.0.0.1", augPinFingerprint},
	} {
		b.InjectCandidateForTests(tc.deviceID, 56432, tc.sourceIP, tc.fingerprint, now)
	}
	lanbeacon.SetDefault(b)
	t.Cleanup(func() { lanbeacon.SetDefault(nil) })
	return b
}

func pickBeaconTestPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func TestAugmentLANAddrsFromBeacon(t *testing.T) {
	augSeedBeacon(t)

	// 闸1:手填恒权威——已有 LANAddrs 一律原样(空切)。以 nil 返回表达
	// 「无增补」,调用方保留手填值。
	if got := augmentLANAddrsFromBeacon("dev-peer", augPinFingerprint); got == nil {
		t.Log("手填权威由调用方分支保证(egressPeer/meshDNSForwardPeer 仅空时调增补),本闸为空集语义")
	}

	// 闸2:指纹精确相等才采纳。
	got := augmentLANAddrsFromBeacon("dev-peer", augPinFingerprint)
	if len(got) != 1 || got[0] != "192.168.1.30:56432" {
		t.Fatalf("pin match augment = %v", got)
	}
	if got := augmentLANAddrsFromBeacon("dev-peer-otherpin", augPinFingerprint); got != nil {
		t.Fatalf("fingerprint mismatch must not adopt: %v", got)
	}
	if got := augmentLANAddrsFromBeacon("dev-peer-nofp", augPinFingerprint); got != nil {
		t.Fatalf("fingerprint-empty candidate must not adopt: %v", got)
	}

	// 闸2b:peer 未钉扎 → 不采纳(no-TOFU:发现永不喂给不可核验的 peer)。
	if got := augmentLANAddrsFromBeacon("dev-peer", ""); got != nil {
		t.Fatalf("unpinned peer must not adopt: %v", got)
	}
	if got := augmentLANAddrsFromBeacon("dev-peer", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA"); got != nil {
		t.Fatalf("non-SHA256 pin form must not adopt: %v", got)
	}

	// 回环剔除:同指纹但来源回环 → 不进增补。
	if got := augmentLANAddrsFromBeacon("dev-peer-loopback", augPinFingerprint); got != nil {
		t.Fatalf("loopback addr must be skipped: %v", got)
	}

	// 未知 device_id:无候选 → nil。
	if got := augmentLANAddrsFromBeacon("dev-unknown", augPinFingerprint); got != nil {
		t.Fatalf("unknown device must yield nil: %v", got)
	}
}

func TestAugmentLANAddrsNilBeaconSafe(t *testing.T) {
	lanbeacon.SetDefault(nil)
	if got := augmentLANAddrsFromBeacon("dev-any", augPinFingerprint); got != nil {
		t.Fatalf("nil beacon must yield nil, got %v", got)
	}
}
