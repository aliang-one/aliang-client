// status_ssh_test_handler_test.go —「测试连接」API 的行为钉：全链路走
// sshtest 假服务器（正确凭据=ok+指纹；错误凭据=handshake 失败结构）；
// 参数完整性 400；方法守卫 405。
package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"aliang.one/nursorgate/outbound/proxy/ssh/sshtest"
)

func sshTestPayload(server string, port int, user, pass string) string {
	return `{"server_host":"` + server + `","server_port":` + strconv.Itoa(port) + `,"username":"` + user + `","password":"` + pass + `"}`
}

func TestSSHTestHandlerSuccess(t *testing.T) {
	_, addr := sshtest.Start(t, sshtest.WithUsername("tester"), sshtest.WithPassword("pw"))
	host, port, _ := strings.Cut(addr, ":")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/ssh/test",
		bytes.NewBufferString(sshTestPayload(host, mustAtoi(t, port), "tester", "pw")))
	HandleSSHTest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			OK            bool   `json:"ok"`
			Stage         string `json:"stage"`
			LatencyMillis int64  `json:"latency_millis"`
			HostKeyFpr    string `json:"host_key_fingerprint"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !envelope.Data.OK {
		t.Fatalf("ok=true expected, got %+v", envelope.Data)
	}
	if !strings.HasPrefix(envelope.Data.HostKeyFpr, "SHA256:") {
		t.Fatalf("fingerprint = %q, want SHA256: prefix", envelope.Data.HostKeyFpr)
	}
}

func TestSSHTestHandlerWrongCredentials(t *testing.T) {
	_, addr := sshtest.Start(t, sshtest.WithUsername("tester"), sshtest.WithPassword("pw"))
	host, port, _ := strings.Cut(addr, ":")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/ssh/test",
		bytes.NewBufferString(sshTestPayload(host, mustAtoi(t, port), "tester", "WRONG")))
	HandleSSHTest(rec, req)

	var envelope struct {
		Code int `json:"code"`
		Data struct {
			OK    bool   `json:"ok"`
			Stage string `json:"stage"`
			Error string `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Data.OK || envelope.Data.Stage == "" || envelope.Data.Error == "" {
		t.Fatalf("failure must carry ok=false + stage + error, got %+v", envelope.Data)
	}
}

func TestSSHTestHandlerValidation(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"missing server", `{"server_port":22,"username":"u","password":"p"}`},
		{"missing port", `{"server_host":"h","username":"u","password":"p"}`},
		{"missing credentials", `{"server_host":"h","server_port":22,"username":"u"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/proxy/ssh/test", bytes.NewBufferString(tc.payload))
			HandleSSHTest(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestSSHTestHandlerMethodGuard(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/proxy/ssh/test", nil)
	HandleSSHTest(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("atoi %s: %v", s, err)
	}
	return n
}
