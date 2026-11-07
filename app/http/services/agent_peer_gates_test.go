package services

// 设备门禁开关读写测试（复用 peerToolsTestServer/peerToolsTestEnv：罐头响应
// = 设备列表，PATCH 的响应体本就被忽略所以同一罐头通吃）。钉五件事：本机按
// deviceId 匹配 / camel 字段兜底 / 请求序列 GET→PATCH→GET / PATCH 路径与
// 载荷 / 空载荷与本机缺席的边界。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPeerGateStatus_FoundAndCamelFallback(t *testing.T) {
	// 本机 device-b 只带 camel 键（服务端字段名演进的兜底路径）；device-a 的
	// snake 键必须被跳过（本机匹配唯一）。
	ts := newPeerToolsTestServer(t, http.StatusOK,
		`[{"id":"device-a","peer_exec_enabled":true},{"id":"device-b","peerExecEnabled":true,"peerFetchEnabled":false}]`)
	svc := peerToolsTestEnv(t, ts)

	status, err := svc.PeerGateStatus(context.Background())
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if status["device_id"] != "device-b" || status["found"] != true {
		t.Fatalf("status = %v", status)
	}
	if status["peer_exec_enabled"] != true || status["peer_fetch_enabled"] != false {
		t.Fatalf("flags = %v, want exec=true fetch=false (camel keys)", status)
	}
}

func TestPeerGateStatus_SelfMissingReportsNotFound(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `[{"id":"device-a","peer_exec_enabled":true}]`)
	svc := peerToolsTestEnv(t, ts)

	status, err := svc.PeerGateStatus(context.Background())
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if status["found"] != false || status["device_id"] != "device-b" {
		t.Fatalf("status = %v, want found=false device-b", status)
	}
}

func TestPeerGateUpdate_PatchThenReread(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK,
		`[{"id":"device-b","peer_exec_enabled":false,"peer_fetch_enabled":false}]`)
	svc := peerToolsTestEnv(t, ts)

	exec, fetch := true, true
	status, err := svc.PeerGateUpdate(context.Background(), &exec, &fetch)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}

	ts.mu.Lock()
	methods := append([]string(nil), ts.methods...)
	paths := append([]string(nil), ts.paths...)
	bodies := append([]string(nil), ts.bodies...)
	ts.mu.Unlock()

	// 请求序列：PATCH settings → GET 回读（写前无读——载荷由前端当前态给出）。
	if len(methods) != 2 || methods[0] != http.MethodPatch || methods[1] != http.MethodGet {
		t.Fatalf("methods = %v, want PATCH,GET", methods)
	}
	if paths[0] != "/api/devices/device-b/settings" {
		t.Fatalf("patch path = %s", paths[0])
	}
	var sent map[string]interface{}
	if err := json.Unmarshal([]byte(bodies[0]), &sent); err != nil {
		t.Fatalf("patch body not json: %s", bodies[0])
	}
	if sent["peer_exec_enabled"] != true || sent["peer_fetch_enabled"] != true {
		t.Fatalf("patch body = %v", sent)
	}
	// 回读结构成立（罐头列表未翻面，旗标仍 false；真值翻转由服务端列表驱动，
	// Status 用例已覆盖）。
	if status["device_id"] != "device-b" || status["found"] != true {
		t.Fatalf("reread = %v", status)
	}
}

func TestPeerGateUpdate_PartialPayloadOnlySendsGivenKey(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `[{"id":"device-b"}]`)
	svc := peerToolsTestEnv(t, ts)

	fetch := false
	if _, err := svc.PeerGateUpdate(context.Background(), nil, &fetch); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	// 请求序列 PATCH→GET,PATCH 在 index 0(末位是回读 GET)。
	ts.mu.Lock()
	body := ts.bodies[0]
	method := ts.methods[0]
	ts.mu.Unlock()
	if method != http.MethodPatch {
		t.Fatalf("method = %s", method)
	}
	if strings.Contains(body, "peer_exec_enabled") {
		t.Fatalf("nil key leaked into body = %s", body)
	}
	if !strings.Contains(body, `"peer_fetch_enabled":false`) {
		t.Fatalf("body = %s", body)
	}
}

func TestPeerGateUpdate_EmptyPayloadRejected(t *testing.T) {
	ts := newPeerToolsTestServer(t, http.StatusOK, `[]`)
	svc := peerToolsTestEnv(t, ts)

	if _, err := svc.PeerGateUpdate(context.Background(), nil, nil); err == nil {
		t.Fatal("empty payload should error")
	} else if !strings.Contains(err.Error(), "no gate flag") {
		t.Fatalf("err = %v", err)
	}
	ts.mu.Lock()
	requests := len(ts.methods)
	ts.mu.Unlock()
	if requests != 0 {
		t.Fatalf("empty payload issued %d requests, want 0", requests)
	}
}

func TestPeerGateStatus_UnregisteredRejected(t *testing.T) {
	// 未注册（无凭据/无设备身份）：doAgentServerJSON 直接拒，零外呼。
	svc := peerToolsTestEnv(t, nil)
	if _, err := svc.PeerGateStatus(context.Background()); err == nil {
		t.Fatal("unregistered should error")
	}
}
