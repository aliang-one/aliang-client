// status_ssh_test_handler_test.go —「测试连接」API 的行为钉：全链路走
// sshtest 假服务器（正确凭据=ok+指纹；错误凭据=handshake 失败结构）；
// 参数完整性 400；方法守卫 405。
package handlers

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	sshout "aliang.one/nursorgate/outbound/proxy/ssh"
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
		// 混合态(password+私钥并存)空用户名拒绝:密码会以兜底身份先发到远端
		{"mixed credentials without username", `{"server_host":"h","server_port":22,"password":"p","private_key":"K"}`},
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

func TestSSHTestHandlerKeyAuthWithoutUsername(t *testing.T) {
	// 私钥+空用户名端到端:handler 放行(与 SSHProxyConfig.Validate 同口径)→
	// NodeFromAddrPort 兜底本机名 → 公钥认证通过。UI 私钥面把用户名置空后,
	// 「测试连接」与真实拨号走同一条兜底链。
	keyPEM, signer, err := testIdentityKeyPEM(t)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	// 假服务端按「用户名+公钥」双因子准入:用户名须等于拨号侧兜底名
	// (NodeFromAddrPort 对私钥+空用户名填入的本机名)。
	_, addr := sshtest.Start(t, sshtest.WithUsername(sshout.FallbackUsername()), sshtest.WithAuthorized(signer.PublicKey()))
	host, port, _ := strings.Cut(addr, ":")

	payload := `{"server_host":"` + host + `","server_port":` + strconv.Itoa(mustAtoi(t, port)) +
		`,"username":"","private_key":` + strconv.Quote(keyPEM) + `}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proxy/ssh/test", bytes.NewBufferString(payload))
	HandleSSHTest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			OK    bool   `json:"ok"`
			Stage string `json:"stage"`
			Error string `json:"error"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if !envelope.Data.OK {
		t.Fatalf("ok=true expected (key auth with fallback username), got %+v", envelope.Data)
	}
}

func testIdentityKeyPEM(t *testing.T) (string, gossh.Signer, error) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return "", nil, err
	}
	block, err := gossh.MarshalPrivateKey(priv, "ssh-test-identity")
	if err != nil {
		return "", nil, err
	}
	return string(pem.EncodeToMemory(block)), signer, nil
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
