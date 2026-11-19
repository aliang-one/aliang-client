package lanbeacon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	sshin "aliang.one/nursorgate/inbound/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// 测试基建约定(2026-11-08 macOS 实测定案):UDP 全局广播 255.255.255.255
// 会在本机回环投递到同端口全部 listener;socket 统一经 beaconSocketControl
// 置 SO_REUSEPORT,双实例同绑一端口互见。端口取随机高位并在 bind 失败时
// 重试,避免与其他测试进程/本机服务冲突。

// pickUDPPort 取一个当前空闲的 UDP 端口(TOCTOU 由 startBeaconPair 重试兜底)。
func pickUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatalf("pickUDPPort: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// startBeaconPair 在同一测试端口起 A/B 双实例;tune 在 Start 前调用,
// 供用例缩短 TTL 等时序旋钮(写字段必须发生在 goroutine 起来之前)。
func startBeaconPair(t *testing.T, tune func(a, b *Beacon)) (*Beacon, *Beacon) {
	t.Helper()
	const (
		aID = "dev-a"
		bID = "dev-b"
	)
	for attempt := 0; attempt < 6; attempt++ {
		port := pickUDPPort(t)
		a := NewBeacon(aID, 41001, port)
		b := NewBeacon(bID, 41002, port)
		if tune != nil {
			tune(a, b)
		}
		if err := a.Start(context.Background()); err != nil {
			continue
		}
		if err := b.Start(context.Background()); err != nil {
			a.Stop()
			continue
		}
		t.Cleanup(func() {
			a.Stop()
			b.Stop()
		})
		return a, b
	}
	t.Fatalf("startBeaconPair: 6 次尝试均未能同绑端口")
	return nil, nil
}

// poll 轮询直到 fn 为真或超时(收包异步,固定 sleep 必然 flake)。
func poll(t *testing.T, timeout time.Duration, desc string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("poll 超时(%s): %s", timeout, desc)
}

// sendRawTo 向 beacon 监听口直接注入一条报文(模拟对端广播的到达侧)。
func sendRawTo(t *testing.T, port int, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sendRawBytesTo(t, port, data)
}

func sendRawBytesTo(t *testing.T, port int, data []byte) {
	t.Helper()
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// assertStaysEmpty 断言 dur 内候选恒空(阴性断言:等满时长再查,收包异步
// 只查一次会漏掉迟到写入)。
func assertStaysEmpty(t *testing.T, b *Beacon, dur time.Duration) {
	t.Helper()
	time.Sleep(dur)
	if got := b.Candidates(); len(got) != 0 {
		t.Fatalf("候选应为空, got %+v", got)
	}
}

func hasCandidate(b *Beacon, deviceID string, meshPort int) bool {
	for _, c := range b.Candidates() {
		if c.DeviceID == deviceID && c.MeshPort == meshPort {
			return true
		}
	}
	return false
}

// TestBeaconBroadcastAndReceive 双实例同端口互见:loopback 广播回环投递到
// 同端口全部 listener,A 收到 B 的候选、反之亦然;mesh_port 随报文透传。
func TestBeaconBroadcastAndReceive(t *testing.T) {
	a, b := startBeaconPair(t, nil)

	poll(t, 4*time.Second, "A 应收到 dev-b 候选(mesh_port=41002)", func() bool {
		return hasCandidate(a, "dev-b", 41002)
	})
	poll(t, 4*time.Second, "B 应收到 dev-a 候选(mesh_port=41001)", func() bool {
		return hasCandidate(b, "dev-a", 41001)
	})
}

// TestBeaconDedupesWithinTTL 同 device_id 连续多次广播:候选仅一条,
// last_seen 刷新为最新 ts,端口跟随最新报文。
func TestBeaconDedupesWithinTTL(t *testing.T) {
	port := pickUDPPort(t)
	b := NewBeacon("self-dev", 56441, port)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(b.Stop)

	now := time.Now().Unix()
	sendRawTo(t, port, map[string]any{"device_id": "dev-x", "mesh_port": 41010, "cert_fingerprint": "", "ts": now})
	sendRawTo(t, port, map[string]any{"device_id": "dev-x", "mesh_port": 41010, "cert_fingerprint": "", "ts": now})
	sendRawTo(t, port, map[string]any{"device_id": "dev-x", "mesh_port": 41011, "cert_fingerprint": "", "ts": now + 1})

	poll(t, 3*time.Second, "dev-x 应去重为一条且刷新到最新 ts/端口", func() bool {
		cands := b.Candidates()
		return len(cands) == 1 &&
			cands[0].DeviceID == "dev-x" &&
			cands[0].LastSeenUnix == now+1 &&
			cands[0].MeshPort == 41011
	})
}

// TestBeaconIgnoresForeignPayload 非 JSON / 缺字段 / ts 超龄 → 全部丢弃,
// 不产生候选。
func TestBeaconIgnoresForeignPayload(t *testing.T) {
	port := pickUDPPort(t)
	b := NewBeacon("self-dev", 56441, port)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(b.Stop)

	now := time.Now().Unix()
	cases := map[string]func(){
		"非 JSON":      func() { sendRawBytesTo(t, port, []byte("this-is-not-json")) },
		"缺 device_id": func() { sendRawTo(t, port, map[string]any{"mesh_port": 41010, "ts": now}) },
		"缺 mesh_port": func() { sendRawTo(t, port, map[string]any{"device_id": "dev-x", "ts": now}) },
		"缺 ts":        func() { sendRawTo(t, port, map[string]any{"device_id": "dev-x", "mesh_port": 41010}) },
		"ts 超龄": func() {
			sendRawTo(t, port, map[string]any{"device_id": "dev-x", "mesh_port": 41010, "ts": now - 46})
		},
	}
	for name, send := range cases {
		t.Run(name, func(t *testing.T) {
			send()
			assertStaysEmpty(t, b, 250*time.Millisecond)
		})
	}
}

// TestBeaconIgnoresSelfBroadcast 自身 device_id 的广播不入候选(本机广播
// 回环会送回自己的监听口,靠该过滤防自见)。
func TestBeaconIgnoresSelfBroadcast(t *testing.T) {
	port := pickUDPPort(t)
	b := NewBeacon("self-dev", 56441, port)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(b.Stop)

	sendRawTo(t, port, map[string]any{"device_id": "self-dev", "mesh_port": 56441, "ts": time.Now().Unix()})
	assertStaysEmpty(t, b, 250*time.Millisecond)
}

// TestBeaconStopCeasesBroadcastAndListening Stop 后:对端视角本机候选在
// TTL 内过期消失(不再广播续期);监听口关闭,注入合法报文也不产生候选;
// Stop 幂等。
func TestBeaconStopCeasesBroadcastAndListening(t *testing.T) {
	a, b := startBeaconPair(t, func(a, b *Beacon) {
		// 缩短 B 的去重 TTL:A 停播后其候选应在数百毫秒内消失,
		// 测试不真实等待 15s。
		b.dedupTTL = 400 * time.Millisecond
	})

	poll(t, 4*time.Second, "B 应先看到 dev-a", func() bool {
		return hasCandidate(b, "dev-a", 41001)
	})

	a.Stop()
	a.Stop() // 幂等:重复 Stop 不 panic

	poll(t, 3*time.Second, "A 停播后其候选应过期消失", func() bool {
		return !hasCandidate(b, "dev-a", 41001)
	})

	// A 不再收包:向 A 的监听口注入合法外部报文,不产生新候选。dev-b 是
	// 停播前已收的旧候选,由 A 自身 TTL 懒过期,与收包判定无关,不参与
	// 本断言。
	sendRawTo(t, a.port, map[string]any{"device_id": "dev-z", "mesh_port": 41030, "ts": time.Now().Unix()})
	time.Sleep(300 * time.Millisecond)
	for _, c := range a.Candidates() {
		if c.DeviceID == "dev-z" {
			t.Fatalf("A 停止后仍收到报文并产生候选: %+v", c)
		}
	}
}

// TestCandidatesNilBeaconEmpty nil 接收者(未装配默认实例)返回空数组而非
// nil:handler 在 beacon 未启动时也要给出 peers:[]。
func TestCandidatesNilBeaconEmpty(t *testing.T) {
	var b *Beacon
	got := b.Candidates()
	if got == nil || len(got) != 0 {
		t.Fatalf("nil beacon Candidates() = %#v, want 空数组", got)
	}
}

// TestBeaconCapacityEviction 候选容量上限 64:第 65 个不同 device_id 到达时
// 淘汰最陈旧(首个);直调 ingest,确定性无 UDP 时序。arrival 用递增时钟
// 使「最陈旧」可判定,ts 跟随同一时钟保持 age=0 不触超龄。
func TestBeaconCapacityEviction(t *testing.T) {
	b := NewBeacon("self-dev", 56441, 56440)
	base := time.Now()
	for i := 0; i <= beaconMaxCandidates; i++ {
		now := base.Add(time.Duration(i) * 10 * time.Millisecond)
		data, err := json.Marshal(beaconPayload{
			DeviceID: fmt.Sprintf("dev-%02d", i),
			MeshPort: 41000 + i,
			TS:       now.Unix(),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.ingest(data, now, "192.0.2.50")
	}
	cands := b.Candidates()
	if len(cands) != beaconMaxCandidates {
		t.Fatalf("候选数 = %d, want %d(容量上限)", len(cands), beaconMaxCandidates)
	}
	var hasFirst, hasLast bool
	for _, c := range cands {
		if c.DeviceID == "dev-00" {
			hasFirst = true
		}
		if c.DeviceID == fmt.Sprintf("dev-%02d", beaconMaxCandidates) {
			hasLast = true
		}
	}
	if hasFirst {
		t.Fatal("dev-00(arrival 最陈旧)应被淘汰")
	}
	if !hasLast {
		t.Fatal("最新候选 dev-64 应在列")
	}
}

// TestBeaconRestartResume Stop→再 Start 往返:对端候选随停播 TTL 过期消失,
// 复启(立即首播)后恢复——总开关往复是生产唯一触发路径,Task 14 生命周期
// 接线依赖此契约。
func TestBeaconRestartResume(t *testing.T) {
	a, b := startBeaconPair(t, func(a, b *Beacon) {
		b.dedupTTL = 400 * time.Millisecond
	})
	poll(t, 4*time.Second, "初次互见:B 应看到 dev-a", func() bool {
		return hasCandidate(b, "dev-a", 41001)
	})

	a.Stop()
	poll(t, 3*time.Second, "停播后 dev-a 应过期消失", func() bool {
		return !hasCandidate(b, "dev-a", 41001)
	})

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("复启: %v", err)
	}
	poll(t, 4*time.Second, "复启后 B 应重新看到 dev-a", func() bool {
		return hasCandidate(b, "dev-a", 41001)
	})
}

// TestDefaultSwapAndClear 进程级默认实例的注册/清空(Task 14 生命周期接线
// 的交接缝):SetDefault 后 Default 可取回,置 nil 后候选恒空。
func TestDefaultSwapAndClear(t *testing.T) {
	t.Cleanup(func() { SetDefault(nil) })
	port := pickUDPPort(t)
	b := NewBeacon("self-dev", 56441, port)
	SetDefault(b)
	if Default() != b {
		t.Fatal("Default() 应返回 SetDefault 注册的实例")
	}
	SetDefault(nil)
	if d := Default(); d != nil {
		t.Fatalf("清空后 Default() = %v, want nil", d)
	}
}

// —— 同账号闸(自动组网批次)——

// TestIngestAccountGate 同账号判定四象限:双方有值不等=丢;相等=收;
// 发送方无值(旧版/未登录)=兼容收;本方无值(未登录)=全收兼容。
// 直调 ingest,确定性无 UDP 时序。
func TestIngestAccountGate(t *testing.T) {
	newBeacon := func(own string) *Beacon {
		b := NewBeacon("dev-self", 56441, DefaultBeaconPort)
		if own != "" {
			b.SetAccountHashProvider(func() string { return own })
		}
		return b
	}
	payload := func(id, hash string, ts time.Time) []byte {
		data, err := json.Marshal(beaconPayload{
			DeviceID:    id,
			MeshPort:    41000,
			AccountHash: hash,
			TS:          ts.Unix(),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return data
	}
	base := time.Now()

	t.Run("跨账号丢弃", func(t *testing.T) {
		b := newBeacon("own-hash")
		b.ingest(payload("dev-x", "other-hash", base), base, "192.0.2.10")
		if hasCandidate(b, "dev-x", 41000) {
			t.Fatal("跨账号广播应被丢弃")
		}
	})
	t.Run("同账号收录且哈希透传", func(t *testing.T) {
		b := newBeacon("own-hash")
		b.ingest(payload("dev-y", "own-hash", base), base, "192.0.2.11")
		cands := b.Candidates()
		if !hasCandidate(b, "dev-y", 41000) {
			t.Fatal("同账号广播应收录")
		}
		for _, c := range cands {
			if c.DeviceID == "dev-y" && c.AccountHash != "own-hash" {
				t.Fatalf("候选 AccountHash = %q, want own-hash", c.AccountHash)
			}
		}
	})
	t.Run("发送方无哈希旧版兼容收", func(t *testing.T) {
		b := newBeacon("own-hash")
		b.ingest(payload("dev-legacy", "", base), base, "192.0.2.12")
		if !hasCandidate(b, "dev-legacy", 41000) {
			t.Fatal("无哈希(旧版)广播应兼容收录")
		}
	})
	t.Run("本方无哈希全收兼容", func(t *testing.T) {
		b := newBeacon("") // provider 不注入=未登录态
		b.ingest(payload("dev-any", "other-hash", base), base, "192.0.2.13")
		if !hasCandidate(b, "dev-any", 41000) {
			t.Fatal("本方未登录时应全收兼容")
		}
	})
}

// TestAssemblePayloadCarriesAccountHash 广播载荷携带账号哈希:provider 注入
// 即透传,未注入恒空(空串字段 omitempty 不上线,旧接收方零感知)。
func TestAssemblePayloadCarriesAccountHash(t *testing.T) {
	b := NewBeacon("dev-self", 56441, DefaultBeaconPort)
	if got := b.assemblePayload().AccountHash; got != "" {
		t.Fatalf("未注入 provider 时 AccountHash = %q, want 空", got)
	}
	b.SetAccountHashProvider(func() string { return "own-hash" })
	if got := b.assemblePayload().AccountHash; got != "own-hash" {
		t.Fatalf("AccountHash = %q, want own-hash", got)
	}
}

// TestAccountHashFromID 哈希形态契约:0=空串;非 0=16 hex 小写、稳定、
// 不同 ID 不同哈希。
func TestAccountHashFromID(t *testing.T) {
	if got := AccountHashFromID(0); got != "" {
		t.Fatalf("ID=0 → %q, want 空串(未登录哨兵)", got)
	}
	a := AccountHashFromID(42)
	b := AccountHashFromID(42)
	if a != b {
		t.Fatalf("同 ID 哈希不稳定: %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Fatalf("哈希长度 = %d, want 16 hex 截断", len(a))
	}
	for _, c := range a {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("哈希含非小写 hex 字符: %q", a)
		}
	}
	if AccountHashFromID(43) == a {
		t.Fatal("不同 ID 哈希不应相同")
	}
}

// —— 广播签名(自动组网批次)——

// TestSignedBeaconAutoPromotion 签名广播全链:合法自签名广播进候选且
// 晋升 AutoPeers;同 device_id 无签名伪造不得降级已验签条目;坏签名整包丢弃。
func TestSignedBeaconAutoPromotion(t *testing.T) {
	key, err := sshin.GenerateIngressKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	const (
		selfID  = "dev-self"
		peerID  = "dev-signed"
		ownHash = "own-hash"
		fp      = "SHA256:TestFingerprint"
	)
	signer := func(f BeaconSignFields) string {
		sig, err := SignBeaconFields(key.PrivateKey, f)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return sig
	}
	b := NewBeacon(selfID, 56441, DefaultBeaconPort)
	b.SetAccountHashProvider(func() string { return ownHash })

	// 对端实例自签自的广播(真实形态:对端以自己的 mesh 身份钥签自己的
	// 载荷——本端只换验签视角)。对端改任一签名域都会验签失败,正是
	// 防劫持性质本身。
	peer := NewBeacon(peerID, 56441, DefaultBeaconPort)
	peer.SetAccountHashProvider(func() string { return ownHash })
	peer.SetFingerprintProvider(func() string { return fp })
	peer.SetMeshPubKeyProvider(func() string { return key.PublicKey })
	peer.SetPayloadSigner(signer)
	p := peer.assemblePayload()
	if p.Sig == "" {
		t.Fatal("签名链路应产出 Sig")
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	now := time.Now()
	b.ingest(data, now, "192.0.2.20")
	if !hasCandidate(b, peerID, p.MeshPort) {
		t.Fatal("合法签名广播应收录")
	}
	peers := b.autoPeers()
	if len(peers) != 1 || peers[0].DeviceID != peerID {
		t.Fatalf("AutoPeers 应含签名同账号 peer, got %+v", peers)
	}
	if peers[0].HostKey != fp || peers[0].Addr == "" {
		t.Fatalf("AutoPeer 字段不全: %+v", peers[0])
	}

	// 无签名伪造(同 device_id):不得降级已验签条目。
	forged := p
	forged.Sig = ""
	forged.MeshPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIForgedPayload attacker"
	forgedData, _ := json.Marshal(forged)
	b.ingest(forgedData, now.Add(time.Second), "192.0.2.99")
	peers = b.autoPeers()
	if len(peers) != 1 || peers[0].MeshPubKey != key.PublicKey {
		t.Fatalf("无签名伪造不得降级已验签条目, got %+v", peers)
	}

	// 坏签名(伪 Sig):整包丢弃。
	bad := p
	bad.Sig = "AAAAinvalid"
	badData, _ := json.Marshal(bad)
	b.ingest(badData, now.Add(2*time.Second), "192.0.2.98")
	if !hasCandidate(b, peerID, p.MeshPort) {
		t.Fatal("坏签名应整包丢弃,合法条目应保留")
	}
}

// TestMeshAdmissionForPoisoningResilience 准入查表直测(评审 major#1/#5):
// 真实对端公钥行被复制成「异 device_id+无签名」毒化候选并存时,MeshAdmissionFor
// 必须继续扫描命中合格条目(提前 return 会让准入抛硬币);跨账号候选同公钥
// 在场 → 拒绝。SetDefault 挂本例 beacon,Cleanup 还原。
func TestMeshAdmissionForPoisoningResilience(t *testing.T) {
	key, err := sshin.GenerateIngressKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	const ownHash = "own-hash"
	const fp = "SHA256:TestFP"

	self := NewBeacon("dev-self", 56441, DefaultBeaconPort)
	self.SetAccountHashProvider(func() string { return ownHash })
	SetDefault(self)
	t.Cleanup(func() { SetDefault(nil) })

	// 合格对端:自签自的广播(同账号+指纹+公钥+签名四要素齐)。
	peer := NewBeacon("dev-real", 56441, DefaultBeaconPort)
	peer.SetAccountHashProvider(func() string { return ownHash })
	peer.SetFingerprintProvider(func() string { return fp })
	peer.SetMeshPubKeyProvider(func() string { return key.PublicKey })
	peer.SetPayloadSigner(func(f BeaconSignFields) string {
		sig, err := SignBeaconFields(key.PrivateKey, f)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return sig
	})
	now := time.Now()
	p := peer.assemblePayload()
	pData, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	self.ingest(pData, now, "10.0.0.2")

	// 毒化候选:同公钥行,异 device_id,无签名(评审 major#1 场景)。
	poison := p
	poison.DeviceID = "dev-poison"
	poison.Sig = ""
	poisonData, _ := json.Marshal(poison)
	self.ingest(poisonData, now.Add(time.Second), "10.0.0.66")

	pub, _, _, _, err := gossh.ParseAuthorizedKey([]byte(key.PublicKey))
	if err != nil {
		t.Fatalf("parse pub: %v", err)
	}
	dev, ok := MeshAdmissionFor(pub)
	if !ok || dev != "dev-real" {
		t.Fatalf("毒化候选并存应继续扫描命中合格条目: dev=%q ok=%v", dev, ok)
	}

	// 跨账号变体:本方账号切换(hash 变)后,原同公钥候选(旧账号哈希)
	// 全体不合格 → 拒绝(账号闸在准入层二次判定)。
	self.SetAccountHashProvider(func() string { return "switched-hash" })
	if _, ok := MeshAdmissionFor(pub); ok {
		t.Fatal("账号切换后旧候选应全体不合格")
	}
}

// TestBeaconRunningReflectsStartStop 运行位真值(D2 假绿治理):构造即
// false,Start 成功后 true,Stop 后回到 false。ctx 收摊路径(Broadcast
// watcher 代调 Stop)同样复位——不单测,同一 started 位。
func TestBeaconRunningReflectsStartStop(t *testing.T) {
	b := NewBeacon("self-dev", 56441, pickUDPPort(t))
	if b.Running() {
		t.Fatal("未 Start 时 Running() 应为 false")
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer b.Stop()
	if !b.Running() {
		t.Fatal("Start 成功后 Running() 应为 true")
	}
	b.Stop()
	if b.Running() {
		t.Fatal("Stop 后 Running() 应为 false")
	}
}
