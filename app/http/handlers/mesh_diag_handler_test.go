package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	meshexit "aliang.one/nursorgate/outbound/proxy/mesh"
	egress "aliang.one/nursorgate/processor/egress"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/app/mesh/lanbeacon"
	"aliang.one/nursorgate/app/mesh/punch"
	"aliang.one/nursorgate/processor/config"
)

// ---------------------------------------------------------------------------
// 组网诊断读面批 Task 5:GET /api/mesh/diag 聚合端点测试。
// 三断言组:①注入透传(各 seam 注入已知值→JSON 各段透传);②nil seam
// 零值安全(&MeshDiagHandler{} → 200 + 恒数组不 null);③snake_case 全场
// (递归遍历响应 JSON,任何键含大写字母即失败)。
// ---------------------------------------------------------------------------

// meshDiagEnvelope 是 diag 响应顶层解码面(整包 map,snake_case 遍历用)。
func meshDiagRequest(t *testing.T, h *MeshDiagHandler, method string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/mesh/diag", nil)
	rec := httptest.NewRecorder()
	h.HandleDiag(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode diag body: %v (body=%s)", err, rec.Body.String())
	}
	return rec.Code, envelope
}

// assertKeysSnakeCase 递归遍历 JSON 树,任一对象键含大写字母即失败
// (diag 契约是全 snake_case;Go 结构体 tag 漏写会让 camelCase 混进消费方)。
func assertKeysSnakeCase(t *testing.T, node any, path string) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			for _, r := range k {
				if unicode.IsUpper(r) {
					t.Fatalf("非 snake_case 键 %s: %q", path, k)
				}
			}
			assertKeysSnakeCase(t, child, path+"."+k)
		}
	case []any:
		for i, child := range v {
			assertKeysSnakeCase(t, child, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

// TestMeshDiagHandlerInjectsPassthrough 断言组①:全部 seam 注入已知值,
// JSON 各段逐一透传(四重闸判定组/roster 三态/beacon 段/carriers 五桶/
// delegate 三元组/punch classify+sessions 段/stun_servers 现值)。
func TestMeshDiagHandlerInjectsPassthrough(t *testing.T) {
	meshOn, forwardOn, punchOn, autoOn, sshOn := true, true, true, true, true
	h := &MeshDiagHandler{
		config: func() *config.Config {
			return &config.Config{Customer: &config.CustomerConfig{
				SSHIngress: &config.SSHIngressConfig{Enable: &sshOn},
				Mesh: &config.MeshConfig{
					Enable:        &meshOn,
					ForwardEnable: &forwardOn,
					PunchEnable:   &punchOn,
					AutoPeers:     &autoOn,
					StunServers:   []string{"stun.custom:3478"},
				},
			}}
		},
		// 未启动真实 beacon(不绑口):透传零计数快照+空候选,Stats/Candidates
		// 的非零路径由 lanbeacon 包自测钉,此处钉的是 seam 接线。
		beacon: func() *lanbeacon.Beacon { return lanbeacon.NewBeacon("diag-self", 56441, 0) },
		rosterSync: func() services.MeshRosterSyncSnapshot {
			return services.MeshRosterSyncSnapshot{
				Synced:     true,
				LastSyncAt: "2026-11-20T08:00:00Z",
				LastError:  "roster unreachable",
			}
		},
		punchClassify: func() services.MeshPunchClassifySnapshot {
			return services.MeshPunchClassifySnapshot{
				SideA: []services.ClassifyOutcome{{PeerID: "peer-b", Outcome: "ok", At: "2026-11-20T08:00:01Z"}},
				SideB: []services.ClassifyOutcome{{PeerID: "diag-self", Outcome: "symmetric", At: "2026-11-20T08:00:02Z"}},
			}
		},
		punchSessions: func() services.MeshPunchSessionsSnapshot {
			return services.MeshPunchSessionsSnapshot{
				Cache:    []punch.SessionSnapshotEntry{{Key: "peer-b", Refs: 2, InFlight: true, IdleSeconds: 4, AgeSeconds: 12, Dead: false}},
				Sockets:  []services.MeshPunchSocketEntry{{Key: "transfer:peer-b", PeerID: "peer-b", Purpose: "transfer"}},
				Counters: services.MeshPunchSessionsCounters{Hit: 7, Miss: 3, MemoSkip: 1, SendFail: 2, Symmetric: 4, NoCands: 5, DialFail: 6, Success: 8},
				Memo: services.MeshPunchSessionsMemo{
					SideA: []punch.SkipMemoEntry{{PeerID: "peer-c", RemainingSeconds: 10}},
					SideB: []punch.SkipMemoEntry{{PeerID: "diag-self", RemainingSeconds: 5, Expired: true}},
				},
				SideB: services.MeshPunchBSideSnapshot{
					ActiveCount: 1,
					Sessions:    []services.MeshPunchBSideSession{{SessionID: "sess-1", AgeSeconds: 3, RemainingSeconds: 87, Done: false}},
				},
			}
		},
		carriers: func() meshexit.CarrierBucketSnapshot {
			return meshexit.CarrierBucketSnapshot{LanOK: 3, UDPOK: 2, PikoOK: 1, UDPErr: 4, DialFail: 5}
		},
		delegate: func() egress.DelegateDiagnostics {
			return egress.DelegateDiagnostics{
				Active:         true,
				LastFailureAt:  "2026-11-20T08:00:03Z",
				LastFailureErr: "egress: delegate dial via mesh failed (target=example.test:443): boom",
			}
		},
		// 运行态真值双缝:注入 true 钉透传(false 态由 nil seam 用例钉)。
		beaconRunning: func() bool { return true },
		lanRunning:    func() bool { return true },
	}

	code, envelope := meshDiagRequest(t, h, http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	if envelope["code"].(float64) != 0 || envelope["msg"].(string) != "success" {
		t.Fatalf("信封 code/msg = %v/%v", envelope["code"], envelope["msg"])
	}
	data := envelope["data"].(map[string]any)

	// 四重闸判定组:配置推导 + roster 三态(阶段 0 复用)。
	if data["enable"] != true || data["effective"] != true ||
		data["forward_enable"] != true || data["punch_enable"] != true || data["auto_peers"] != true {
		t.Fatalf("闸门组不符: enable=%v effective=%v forward=%v punch=%v auto_peers=%v",
			data["enable"], data["effective"], data["forward_enable"], data["punch_enable"], data["auto_peers"])
	}
	roster := data["roster_sync"].(map[string]any)
	if roster["synced"] != true || roster["last_sync_at"] != "2026-11-20T08:00:00Z" || roster["last_error"] != "roster unreachable" {
		t.Fatalf("roster_sync 透传不符: %v", roster)
	}
	// 运行态真值双布尔透传(阶段 0 灭假绿)。
	if data["lan_running"] != true {
		t.Fatalf("lan_running = %v, want true", data["lan_running"])
	}

	beacon := data["beacon"].(map[string]any)
	if beacon["beacon_running"] != true {
		t.Fatalf("beacon_running = %v, want true", beacon["beacon_running"])
	}
	stats := beacon["stats"].(map[string]any)
	if stats["started_at"] != "" || stats["bcast_sent"].(float64) != 0 {
		t.Fatalf("未启动 beacon stats 应零值: %v", stats)
	}
	lanPeers, ok := beacon["lan_peers"].([]any)
	if !ok || len(lanPeers) != 0 {
		t.Fatalf("lan_peers = %#v, want 空数组", beacon["lan_peers"])
	}

	carriers := data["carriers"].(map[string]any)
	if carriers["lan_ok"].(float64) != 3 || carriers["udp_ok"].(float64) != 2 ||
		carriers["piko_ok"].(float64) != 1 || carriers["udp_err"].(float64) != 4 || carriers["dial_fail"].(float64) != 5 {
		t.Fatalf("carriers 透传不符: %v", carriers)
	}

	delegate := data["delegate"].(map[string]any)
	if delegate["active"] != true ||
		delegate["last_failure_at"] != "2026-11-20T08:00:03Z" ||
		delegate["last_failure_err"] != "egress: delegate dial via mesh failed (target=example.test:443): boom" {
		t.Fatalf("delegate 透传不符: %v", delegate)
	}

	punch := data["punch"].(map[string]any)
	classify := punch["classify"].(map[string]any)
	sideA := classify["side_a"].([]any)
	sideB := classify["side_b"].([]any)
	if len(sideA) != 1 || sideA[0].(map[string]any)["peer_id"] != "peer-b" ||
		sideA[0].(map[string]any)["outcome"] != "ok" || sideA[0].(map[string]any)["at"] != "2026-11-20T08:00:01Z" {
		t.Fatalf("classify.side_a 透传不符: %v", classify["side_a"])
	}
	if len(sideB) != 1 || sideB[0].(map[string]any)["outcome"] != "symmetric" {
		t.Fatalf("classify.side_b 透传不符: %v", classify["side_b"])
	}
	sessions := punch["sessions"].(map[string]any)
	cache := sessions["cache"].([]any)
	if len(cache) != 1 || cache[0].(map[string]any)["key"] != "peer-b" ||
		cache[0].(map[string]any)["refs"].(float64) != 2 || cache[0].(map[string]any)["in_flight"] != true ||
		cache[0].(map[string]any)["idle_seconds"].(float64) != 4 || cache[0].(map[string]any)["age_seconds"].(float64) != 12 ||
		cache[0].(map[string]any)["dead"] != false {
		t.Fatalf("sessions.cache 透传不符: %v", sessions["cache"])
	}
	sockets := sessions["sockets"].([]any)
	if len(sockets) != 1 || sockets[0].(map[string]any)["key"] != "transfer:peer-b" ||
		sockets[0].(map[string]any)["purpose"] != "transfer" || sockets[0].(map[string]any)["peer_id"] != "peer-b" {
		t.Fatalf("sessions.sockets 透传不符: %v", sessions["sockets"])
	}
	counters := sessions["counters"].(map[string]any)
	if counters["hit"].(float64) != 7 || counters["memo_skip"].(float64) != 1 || counters["success"].(float64) != 8 {
		t.Fatalf("sessions.counters 透传不符: %v", counters)
	}
	memo := sessions["memo"].(map[string]any)
	memoA := memo["side_a"].([]any)
	memoB := memo["side_b"].([]any)
	if len(memoA) != 1 || memoA[0].(map[string]any)["peer_id"] != "peer-c" || memoA[0].(map[string]any)["remaining_seconds"].(float64) != 10 {
		t.Fatalf("sessions.memo.side_a 透传不符: %v", memo["side_a"])
	}
	if len(memoB) != 1 || memoB[0].(map[string]any)["expired"] != true {
		t.Fatalf("sessions.memo.side_b 透传不符: %v", memo["side_b"])
	}
	bSide := sessions["side_b"].(map[string]any)
	bSessions := bSide["sessions"].([]any)
	if bSide["active_count"].(float64) != 1 || len(bSessions) != 1 ||
		bSessions[0].(map[string]any)["session_id"] != "sess-1" ||
		bSessions[0].(map[string]any)["age_seconds"].(float64) != 3 ||
		bSessions[0].(map[string]any)["remaining_seconds"].(float64) != 87 ||
		bSessions[0].(map[string]any)["done"] != false {
		t.Fatalf("sessions.side_b 透传不符: %v", bSide)
	}

	stun := data["stun_servers"].([]any)
	if len(stun) != 1 || stun[0] != "stun.custom:3478" {
		t.Fatalf("stun_servers 透传不符: %v", data["stun_servers"])
	}
}

// TestMeshDiagHandlerNilSeamsZeroSafe 断言组②:&MeshDiagHandler{}(全部
// seam nil)→ 200 且恒数组不 null:beacon.lan_peers/classify.side_a/
// classify.side_b/sessions.cache/sockets/memo.side_a/memo.side_b/
// sessions.side_b.sessions/stun_servers;标量段零值合法(enable 默认开,
// 其余默认关/零)。
func TestMeshDiagHandlerNilSeamsZeroSafe(t *testing.T) {
	h := &MeshDiagHandler{}
	code, envelope := meshDiagRequest(t, h, http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	data := envelope["data"].(map[string]any)

	if data["enable"] != true || data["effective"] != false ||
		data["forward_enable"] != false || data["punch_enable"] != false || data["auto_peers"] != false {
		t.Fatalf("nil seam 默认闸门不符: %v", data)
	}
	roster := data["roster_sync"].(map[string]any)
	if roster["synced"] != false || roster["last_sync_at"] != "" || roster["last_error"] != "" {
		t.Fatalf("nil seam roster_sync 应零值: %v", roster)
	}
	// 运行态真值双布尔 nil seam=false(未注入缝即未装配,不 panic)。
	if data["lan_running"] != false {
		t.Fatalf("nil seam lan_running = %v, want false", data["lan_running"])
	}
	if data["beacon"].(map[string]any)["beacon_running"] != false {
		t.Fatalf("nil seam beacon_running = %v, want false", data["beacon"].(map[string]any)["beacon_running"])
	}

	// 恒数组清单(null 会逼消费方判空):每项都必须是 [] 而非 null。
	arrays := map[string]any{
		"beacon.lan_peers":         data["beacon"].(map[string]any)["lan_peers"],
		"classify.side_a":          data["punch"].(map[string]any)["classify"].(map[string]any)["side_a"],
		"classify.side_b":          data["punch"].(map[string]any)["classify"].(map[string]any)["side_b"],
		"sessions.cache":           data["punch"].(map[string]any)["sessions"].(map[string]any)["cache"],
		"sessions.sockets":         data["punch"].(map[string]any)["sessions"].(map[string]any)["sockets"],
		"sessions.memo.side_a":     data["punch"].(map[string]any)["sessions"].(map[string]any)["memo"].(map[string]any)["side_a"],
		"sessions.memo.side_b":     data["punch"].(map[string]any)["sessions"].(map[string]any)["memo"].(map[string]any)["side_b"],
		"sessions.side_b.sessions": data["punch"].(map[string]any)["sessions"].(map[string]any)["side_b"].(map[string]any)["sessions"],
		"stun_servers":             data["stun_servers"],
	}
	for name, v := range arrays {
		arr, ok := v.([]any)
		if !ok || arr == nil {
			t.Fatalf("%s = %#v, want 空数组非 null", name, v)
		}
		if len(arr) != 0 {
			t.Fatalf("%s = %d 条, want 空", name, len(arr))
		}
	}

	carriers := data["carriers"].(map[string]any)
	for _, k := range []string{"lan_ok", "udp_ok", "piko_ok", "udp_err", "dial_fail"} {
		if carriers[k].(float64) != 0 {
			t.Fatalf("nil seam carriers.%s = %v, want 0", k, carriers[k])
		}
	}
	if data["delegate"].(map[string]any)["active"] != false {
		t.Fatalf("nil seam delegate.active 应 false")
	}
	stats := data["beacon"].(map[string]any)["stats"].(map[string]any)
	if stats["bcast_sent"].(float64) != 0 || stats["started_at"] != "" {
		t.Fatalf("nil seam beacon.stats 应零值: %v", stats)
	}
}

// TestMeshDiagHandlerSnakeCaseKeys 断言组③:响应 JSON 全树递归,任一键含
// 大写即失败;另钉各段必含键(形状契约:消费方按这些键取数)。
func TestMeshDiagHandlerSnakeCaseKeys(t *testing.T) {
	h := &MeshDiagHandler{}
	code, envelope := meshDiagRequest(t, h, http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	assertKeysSnakeCase(t, envelope, "$")

	data := envelope["data"].(map[string]any)
	for _, k := range []string{
		"enable", "effective", "forward_enable", "punch_enable", "auto_peers",
		"roster_sync", "lan_running", "beacon", "carriers", "delegate", "punch", "stun_servers",
	} {
		if _, ok := data[k]; !ok {
			t.Fatalf("data 缺键 %q", k)
		}
	}
	beacon := data["beacon"].(map[string]any)
	for _, k := range []string{"stats", "lan_peers", "beacon_running"} {
		if _, ok := beacon[k]; !ok {
			t.Fatalf("beacon 缺键 %q", k)
		}
	}
	// 13/13 全键钉死(rx_* 六键+cand_* 三键是归因判别力最强的键——缺任一
	// 即丢「utun7 吞广播 vs 回环健康 vs 签名拒收」的判别位)。
	for _, k := range []string{
		"started_at", "bcast_sent", "bcast_send_err", "rx_packets", "rx_malformed",
		"rx_self", "rx_stale", "rx_acct_rejected", "rx_sig_rejected", "rx_degrade_blocked",
		"cand_accepted", "cand_refreshed", "cand_evicted",
	} {
		if _, ok := beacon["stats"].(map[string]any)[k]; !ok {
			t.Fatalf("beacon.stats 缺键 %q", k)
		}
	}
	carriers := data["carriers"].(map[string]any)
	for _, k := range []string{"lan_ok", "udp_ok", "piko_ok", "udp_err", "dial_fail"} {
		if _, ok := carriers[k]; !ok {
			t.Fatalf("carriers 缺键 %q", k)
		}
	}
	delegate := data["delegate"].(map[string]any)
	for _, k := range []string{"active", "last_failure_at", "last_failure_err"} {
		if _, ok := delegate[k]; !ok {
			t.Fatalf("delegate 缺键 %q", k)
		}
	}
	punch := data["punch"].(map[string]any)
	if _, ok := punch["classify"]; !ok {
		t.Fatal("punch 缺键 classify")
	}
	sessions := punch["sessions"].(map[string]any)
	for _, k := range []string{"cache", "sockets", "counters", "memo", "side_b"} {
		if _, ok := sessions[k]; !ok {
			t.Fatalf("punch.sessions 缺键 %q", k)
		}
	}
	for _, k := range []string{"side_a", "side_b"} {
		if _, ok := punch["classify"].(map[string]any)[k]; !ok {
			t.Fatalf("punch.classify 缺键 %q", k)
		}
	}
	counters := sessions["counters"].(map[string]any)
	for _, k := range []string{"hit", "miss", "memo_skip", "send_session_fail", "symmetric", "no_candidates", "dial_fail", "success"} {
		if _, ok := counters[k]; !ok {
			t.Fatalf("punch.sessions.counters 缺键 %q", k)
		}
	}
}

// TestMeshDiagHandlerMethodNotAllowed 非 GET 405(HandleLANPeers 同款
// http.Error 惯例——common.Error 收业务码会把 405 映射成 500)。
func TestMeshDiagHandlerMethodNotAllowed(t *testing.T) {
	h := &MeshDiagHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/mesh/diag", nil)
	rec := httptest.NewRecorder()
	h.HandleDiag(rec, req)
	if rec.Code != 405 {
		t.Fatalf("POST code = %d, want 405", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// 组网诊断读面批 Task 6:GET /api/mesh/identity 身份一致性端点测试。
// 四源缝注入(state/identity 文件/beacon self/roster 在册)断言 mismatches
// 判定:①state≠identity 文件 ②identity 文件≠beacon self ③roster 缺 self
// 各产一条;四源全一致→mismatches 空;nil seam 零值安全不 panic。
// mismatches 判定逻辑全部经 seam 注入测试,不依赖真实文件系统。
// ---------------------------------------------------------------------------

// meshIdentityRequestEnvelope identity 端点请求 helper:返回 (code, 整包
// envelope);非 200 时 envelope 为 nil(405 用例直断 code)。
func meshIdentityRequestEnvelope(t *testing.T, h *MeshDiagHandler, method string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/mesh/identity", nil)
	rec := httptest.NewRecorder()
	h.HandleIdentity(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode identity body: %v (body=%s)", err, rec.Body.String())
	}
	return rec.Code, envelope
}

// meshIdentityRequest identity 端点请求 helper:返回 (code, data 载荷);
// 非 200 时 data 为 nil(405 用例直断 code)。
func meshIdentityRequest(t *testing.T, h *MeshDiagHandler, method string) (int, map[string]any) {
	t.Helper()
	code, envelope := meshIdentityRequestEnvelope(t, h, method)
	if envelope == nil {
		return code, nil
	}
	return code, envelope["data"].(map[string]any)
}

// meshIdentitySeams 全一致四源缝基座:state=identity 文件=beacon self=
// dev-1,roster 已同步且在册。各用例按需覆写单缝,精确触发单一判定组
// (链式比对 state↔文件、文件↔beacon;其余源保持一致=恰好一条)。
func meshIdentitySeams() *MeshDiagHandler {
	return &MeshDiagHandler{
		stateDeviceID:     func() string { return "dev-1" },
		identityFile:      func() (string, string) { return "/cache/agent/device_identity.json", "dev-1" },
		beaconSelfID:      func() string { return "dev-1" },
		rosterSelfPresent: func(string) bool { return true },
		rosterSync:        func() services.MeshRosterSyncSnapshot { return services.MeshRosterSyncSnapshot{Synced: true} },
	}
}

// assertMeshIdentityMismatch 恰好一条 mismatch 且同时含两个源值(AI 消费方
// 直读:人话描述必须点名「哪个源 vs 哪个源」的两侧取值)。
func assertMeshIdentityMismatch(t *testing.T, data map[string]any, wantBoth ...string) {
	t.Helper()
	raw, ok := data["mismatches"].([]any)
	if !ok || raw == nil {
		t.Fatalf("mismatches = %#v, want 数组非 null", data["mismatches"])
	}
	if len(raw) != 1 {
		t.Fatalf("mismatches = %d 条, want 恰好 1 条: %v", len(raw), raw)
	}
	msg, _ := raw[0].(string)
	for _, want := range wantBoth {
		if !strings.Contains(msg, want) {
			t.Fatalf("mismatch 未含 %q: %s", want, msg)
		}
	}
}

// TestMeshIdentityHandlerAllConsistent 四源全一致→mismatches 空(恒数组
// 非 null)+各源字段原样透传+roster_self_present=true。
func TestMeshIdentityHandlerAllConsistent(t *testing.T) {
	code, data := meshIdentityRequest(t, meshIdentitySeams(), http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	if data["device_id"] != "dev-1" ||
		data["identity_file_path"] != "/cache/agent/device_identity.json" ||
		data["identity_file_device_id"] != "dev-1" ||
		data["beacon_self_device_id"] != "dev-1" {
		t.Fatalf("四源透传不符: %v", data)
	}
	if data["roster_self_present"] != true {
		t.Fatalf("roster_self_present = %v, want true", data["roster_self_present"])
	}
	mismatches, ok := data["mismatches"].([]any)
	if !ok || mismatches == nil || len(mismatches) != 0 {
		t.Fatalf("mismatches = %#v, want 空数组非 null", data["mismatches"])
	}
}

// TestMeshIdentityHandlerStateVsFileMismatch 断言①:state≠identity 文件
// 恰产一条(beacon 与文件保持一致,排除链式第二跳干扰)。
func TestMeshIdentityHandlerStateVsFileMismatch(t *testing.T) {
	h := meshIdentitySeams()
	h.stateDeviceID = func() string { return "dev-new" }
	code, data := meshIdentityRequest(t, h, http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	if data["device_id"] != "dev-new" || data["identity_file_device_id"] != "dev-1" {
		t.Fatalf("两源透传不符: %v", data)
	}
	assertMeshIdentityMismatch(t, data, "dev-new", "dev-1")
}

// TestMeshIdentityHandlerFileVsBeaconMismatch 断言②:identity 文件≠beacon
// self 恰产一条(state 与文件保持一致)。
func TestMeshIdentityHandlerFileVsBeaconMismatch(t *testing.T) {
	h := meshIdentitySeams()
	h.beaconSelfID = func() string { return "dev-beacon" }
	code, data := meshIdentityRequest(t, h, http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	if data["identity_file_device_id"] != "dev-1" || data["beacon_self_device_id"] != "dev-beacon" {
		t.Fatalf("两源透传不符: %v", data)
	}
	assertMeshIdentityMismatch(t, data, "dev-1", "dev-beacon")
}

// TestMeshIdentityHandlerFileMissingFallbackPair 断言 I-1(a) 回退对:文件源
// 缺失(路径与 device_id 皆空)时 ①② 链比较跳过,补 state↔beacon 直比——
// 恰一条、含两值、如实交代文件链不可用(脑裂不因文件缺席漏报)。
func TestMeshIdentityHandlerFileMissingFallbackPair(t *testing.T) {
	h := meshIdentitySeams()
	h.identityFile = func() (string, string) { return "", "" }
	h.beaconSelfID = func() string { return "dev-beacon" }
	code, data := meshIdentityRequest(t, h, http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	if data["identity_file_path"] != "" || data["identity_file_device_id"] != "" {
		t.Fatalf("文件缺失态透传不符: %v", data)
	}
	assertMeshIdentityMismatch(t, data, "dev-1", "dev-beacon", "文件源缺失")
}

// TestMeshIdentityHandlerFileMissingAgreeNoFallback 回退对的守门面:文件源
// 缺失但 state==beacon(两可观测源一致)→ 不产任何 mismatch(回退对只在
// 真不一致时产,不把「文件缺席」单独当 mismatch 报)。
func TestMeshIdentityHandlerFileMissingAgreeNoFallback(t *testing.T) {
	h := meshIdentitySeams()
	h.identityFile = func() (string, string) { return "", "" }
	code, data := meshIdentityRequest(t, h, http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	mismatches := data["mismatches"].([]any)
	if len(mismatches) != 0 {
		t.Fatalf("state==beacon 且文件缺失 mismatches = %v, want 空", mismatches)
	}
}

// TestMeshIdentityHandlerRosterMissingSelfIsNotMismatch roster 已同步而缓存
// 里没有本机条目,不是身份不一致(2026-11-24 审计定案):PhoneServer 的
// GET /api/agent/mesh-roster 回包前会把请求方自己的条目滤掉(其路由测试钉死
// 「同账号全量(滤 self)」),所以任何同步成功的设备 roster_self_present 恒为
// false——旧实现在每台健康设备上都报「缺本机条目,自动组网将 fail-closed」,
// 把 mesh-runbook 的身份脑裂症状族指向了健康设备。本用例钉:不产 mismatch;
// roster_self_present 仍如实透传 false;回查键仍=进程 state 的 self
// (resp.DeviceID,非 beacon self)。
func TestMeshIdentityHandlerRosterMissingSelfIsNotMismatch(t *testing.T) {
	h := meshIdentitySeams()
	var lookupKey string
	h.rosterSelfPresent = func(selfDeviceID string) bool {
		lookupKey = selfDeviceID
		return false
	}
	code, data := meshIdentityRequest(t, h, http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	if lookupKey != "dev-1" {
		t.Fatalf("roster 回查键 = %q, want 进程 state 的 self dev-1(非 beacon self)", lookupKey)
	}
	if data["roster_self_present"] != false {
		t.Fatalf("roster_self_present = %v, want false", data["roster_self_present"])
	}
	if mismatches := data["mismatches"].([]any); len(mismatches) != 0 {
		t.Fatalf("roster 滤掉请求方自身是服务端契约,不得报不一致:mismatches = %v", mismatches)
	}
}

// TestMeshIdentityHandlerRosterUnsyncedNotMismatch 诚实闸:roster 从未同步
// (synced=false)时缓存空是合法态,缺 self 不产 mismatch(未同步≠不一致,
// 误报会污染 AI 判读;同步态由 roster_sync 段 diag 端点另行可见)。回查键
// 同样参数捕获钉死(M3)。
func TestMeshIdentityHandlerRosterUnsyncedNotMismatch(t *testing.T) {
	h := meshIdentitySeams()
	h.rosterSync = func() services.MeshRosterSyncSnapshot {
		return services.MeshRosterSyncSnapshot{LastError: "roster unreachable"}
	}
	var lookupKey string
	h.rosterSelfPresent = func(selfDeviceID string) bool {
		lookupKey = selfDeviceID
		return false
	}
	code, data := meshIdentityRequest(t, h, http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	if lookupKey != "dev-1" {
		t.Fatalf("roster 回查键 = %q, want 进程 state 的 self dev-1(非 beacon self)", lookupKey)
	}
	mismatches := data["mismatches"].([]any)
	if len(mismatches) != 0 {
		t.Fatalf("未同步态 mismatches = %v, want 空", mismatches)
	}
}

// TestMeshIdentityHandlerNilSeamsZeroSafe nil seam 零值安全:&MeshDiagHandler{}
// →200 不 panic,各源恒零值,mismatches 恒空数组非 null(未装配=空态合法)。
func TestMeshIdentityHandlerNilSeamsZeroSafe(t *testing.T) {
	code, data := meshIdentityRequest(t, &MeshDiagHandler{}, http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	if data["device_id"] != "" || data["identity_file_path"] != "" ||
		data["identity_file_device_id"] != "" || data["beacon_self_device_id"] != "" ||
		data["roster_self_present"] != false {
		t.Fatalf("nil seam 各源应零值: %v", data)
	}
	mismatches, ok := data["mismatches"].([]any)
	if !ok || mismatches == nil || len(mismatches) != 0 {
		t.Fatalf("nil seam mismatches = %#v, want 空数组非 null", data["mismatches"])
	}
}

// TestMeshIdentityHandlerSnakeCaseKeys 全树 snake_case + 必含键形状契约。
func TestMeshIdentityHandlerSnakeCaseKeys(t *testing.T) {
	code, envelope := meshIdentityRequestEnvelope(t, meshIdentitySeams(), http.MethodGet)
	if code != 200 {
		t.Fatalf("GET code = %d, want 200", code)
	}
	assertKeysSnakeCase(t, envelope, "$")
	data := envelope["data"].(map[string]any)
	for _, k := range []string{
		"device_id", "identity_file_path", "identity_file_device_id",
		"beacon_self_device_id", "roster_self_present", "mismatches",
	} {
		if _, ok := data[k]; !ok {
			t.Fatalf("data 缺键 %q", k)
		}
	}
}

// TestMeshIdentityHandlerMethodNotAllowed 非 GET 405(HandleDiag 同款
// http.Error 惯例)。
func TestMeshIdentityHandlerMethodNotAllowed(t *testing.T) {
	h := meshIdentitySeams()
	req := httptest.NewRequest(http.MethodPost, "/api/mesh/identity", nil)
	rec := httptest.NewRecorder()
	h.HandleIdentity(rec, req)
	if rec.Code != 405 {
		t.Fatalf("POST code = %d, want 405", rec.Code)
	}
}
