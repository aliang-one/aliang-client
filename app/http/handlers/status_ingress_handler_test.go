// status_ingress_handler_test.go — /api/status/ingress 的行为钉：
// 方法守卫 405（真实 HTTP 码）；本包测试环境无 reconcile 实例 → ok=false
// + 恒非 nil 空数组（前端「未运行」态渲染的数据契约）；kick/block/unblock
// 管理动作的参数校验与持久化闭环。
package handlers

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/processor/config"

	gossh "golang.org/x/crypto/ssh"
)

func TestIngressSessionsMethodGuard(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/status/ingress", nil)
	HandleIngressSessions(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestIngressSessionsNotRunningShape(t *testing.T) {
	// 测试进程未跑 SSH 入口 reconcile → ok=false；两列表恒非 null。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status/ingress", nil)
	HandleIngressSessions(rec, req)

	var envelope struct {
		Code int `json:"code"`
		Data struct {
			OK     bool              `json:"ok"`
			Active []json.RawMessage `json:"active"`
			Closed []json.RawMessage `json:"closed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if envelope.Data.OK {
		t.Fatal("ok must be false when ingress is not running in test process")
	}
	if envelope.Data.Active == nil || envelope.Data.Closed == nil {
		t.Fatal("active/closed must serialize as [] not null")
	}
}

// 测试环境钩子：测试进程无 reconcile 实例且全局配置/DB 需要隔离——blocklist
// 持久化走 UpdateCommittedCustomerConfig 全链（文件+DB+内存），测试里按
// services 包既有范式重置三件套并落临时 HOME。
func resetIngressTestEnv(t *testing.T) {
	t.Helper()
	config.ResetGlobalConfigForTest()
	config.ResetEffectiveConfigCommitCoordinatorForTest()
	storage.ResetSoftwareConfigDBForTest()
	t.Cleanup(func() {
		config.ResetGlobalConfigForTest()
		config.ResetEffectiveConfigCommitCoordinatorForTest()
		storage.ResetSoftwareConfigDBForTest()
	})
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(tempHome, ".aliang-cache"))
}

func TestIngressKickMethodGuard(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status/ingress/kick", nil)
	HandleIngressKick(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestIngressKickRequiresFingerprint(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/status/ingress/kick", strings.NewReader(`{}`))
	HandleIngressKick(rec, req)
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if envelope.Code == 0 {
		t.Fatalf("empty fingerprint must be rejected, body=%s", rec.Body.String())
	}
}

func TestIngressBlockUnblockRoundTrip(t *testing.T) {
	resetIngressTestEnv(t)
	baseCfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			SSHIngress: &config.SSHIngressConfig{
				Enable:         ptrBool(true),
				AuthorizedKeys: []string{ingressTestKeyLine(t)},
			},
		},
	}
	config.SetGlobalConfig(baseCfg)

	// 拉黑（默认同时踢出；无在线会话 kicked=false 不影响 block 结果）。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/status/ingress/block",
		strings.NewReader(`{"fingerprint":"SHA256:abc123"}`))
	HandleIngressBlock(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("block status = %d body=%s", rec.Code, rec.Body.String())
	}

	// 名单已持久化进全局配置（内存提交回调已跑）。
	si := config.GetGlobalConfig().Customer.SSHIngress
	if len(si.BlockedFingerprints) != 1 || si.BlockedFingerprints[0] != "SHA256:abc123" {
		t.Fatalf("blocked_fingerprints = %#v, want [SHA256:abc123]", si.BlockedFingerprints)
	}

	// GET 视图包含该名单。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/status/ingress", nil)
	HandleIngressSessions(rec, req)
	var envelope struct {
		Data struct {
			Blocks struct {
				Fingerprints []string `json:"fingerprints"`
				DeviceIDs    []string `json:"device_ids"`
			} `json:"blocks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode GET: %v body=%s", err, rec.Body.String())
	}
	if len(envelope.Data.Blocks.Fingerprints) != 1 || envelope.Data.Blocks.Fingerprints[0] != "SHA256:abc123" {
		t.Fatalf("GET blocks.fingerprints = %#v", envelope.Data.Blocks.Fingerprints)
	}

	// 解封。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/status/ingress/unblock",
		strings.NewReader(`{"fingerprint":"SHA256:abc123"}`))
	HandleIngressUnblock(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unblock status = %d body=%s", rec.Code, rec.Body.String())
	}
	if n := len(config.GetGlobalConfig().Customer.SSHIngress.BlockedFingerprints); n != 0 {
		t.Fatalf("after unblock blocked_fingerprints = %d, want 0", n)
	}

	// 无效指纹格式被配置校验拒绝（400 而非 500）。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/status/ingress/block",
		strings.NewReader(`{"fingerprint":"garbage"}`))
	HandleIngressBlock(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid fingerprint status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestIngressBlockRequiresTarget(t *testing.T) {
	resetIngressTestEnv(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/status/ingress/block", strings.NewReader(`{}`))
	HandleIngressBlock(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty target status = %d, want 400", rec.Code)
	}
}

func ptrBool(v bool) *bool { return &v }

func ingressTestKeyLine(t *testing.T) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	gosshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}
	_ = priv
	return strings.TrimSpace(string(gossh.MarshalAuthorizedKey(gosshPub)))
}
