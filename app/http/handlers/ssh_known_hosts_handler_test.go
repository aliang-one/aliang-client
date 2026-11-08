package handlers

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	sshout "aliang.one/nursorgate/outbound/proxy/ssh"
)

func knownHostsTestSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return s
}

// seedKnownHostEntry 走 CallbackFor 首连采集写入——KnownHosts 唯一的生产写入口,
// 与 TOFU 路径同源,测试不直接耦合磁盘 JSON 格式。返回写入条目的指纹与算法。
func seedKnownHostEntry(t *testing.T, path, hostport string) (fingerprint, algorithm string) {
	t.Helper()
	signer := knownHostsTestSigner(t)
	pub := signer.PublicKey()
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		t.Fatalf("split %s: %v", hostport, err)
	}
	if err := sshout.NewKnownHosts(path).CallbackFor(hostport)(host, &net.TCPAddr{}, pub); err != nil {
		t.Fatalf("seed %s: %v", hostport, err)
	}
	return gossh.FingerprintSHA256(pub), pub.Type()
}

// getKnownHostsItems 断言 200 并解出 known_hosts 列表,按 JoinHostPort(host,port)
// 键化(与 DELETE 的键重组口径一致,IPv6 带方括号)。
func getKnownHostsItems(t *testing.T, rec *httptest.ResponseRecorder) map[string]map[string]string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			KnownHosts []map[string]string `json:"known_hosts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode GET body: %v; body=%s", err, rec.Body.String())
	}
	items := map[string]map[string]string{}
	for _, it := range resp.Data.KnownHosts {
		items[net.JoinHostPort(it["host"], it["port"])] = it
	}
	return items
}

func TestSSHKnownHostsHandler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	fp1, alg1 := seedKnownHostEntry(t, path, "203.0.113.10:22")
	seedKnownHostEntry(t, path, "[2001:db8::1]:2200")
	h := NewSSHKnownHostsHandler(path)

	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.HandleKnownHosts(rec, httptest.NewRequest(http.MethodGet, "/api/proxy/ssh/known-hosts", nil))
		return rec
	}
	del := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.HandleKnownHosts(rec, httptest.NewRequest(http.MethodDelete, "/api/proxy/ssh/known-hosts?"+query, nil))
		return rec
	}

	// GET:两条 + 字段齐全(IPv6 拆分后 host 不带方括号)。
	items := getKnownHostsItems(t, get())
	if len(items) != 2 {
		t.Fatalf("known_hosts 条数 = %d, want 2; body=%v", len(items), items)
	}
	first := items["203.0.113.10:22"]
	if first == nil {
		t.Fatalf("缺 203.0.113.10:22 条目; got %v", items)
	}
	if first["fingerprint"] != fp1 || first["algorithm"] != alg1 || first["added_at"] == "" {
		t.Fatalf("字段不全/错值: %+v (want fp=%s alg=%s)", first, fp1, alg1)
	}
	v6 := items["[2001:db8::1]:2200"]
	if v6 == nil {
		t.Fatalf("缺 [2001:db8::1]:2200 条目; got %v", items)
	}

	// DELETE 存在的 → 200;GET 少一条。
	if rec := del("host=203.0.113.10&port=22"); rec.Code != http.StatusOK {
		t.Fatalf("DELETE 存在条目 code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	items = getKnownHostsItems(t, get())
	if len(items) != 1 {
		t.Fatalf("删除后条数 = %d, want 1", len(items))
	}
	if _, ok := items["203.0.113.10:22"]; ok {
		t.Fatal("203.0.113.10:22 删除后仍存在")
	}

	// DELETE IPv6 条目:键重组走 JoinHostPort(host,port),方括号口径对齐。
	if rec := del("host=2001:db8::1&port=2200"); rec.Code != http.StatusOK {
		t.Fatalf("DELETE IPv6 条目 code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec := get(); !strings.Contains(rec.Body.String(), `"known_hosts":[]`) {
		t.Fatalf("全删后 known_hosts 应为空数组而非 null; body=%s", rec.Body.String())
	}

	// DELETE 不存在的 → 404(KnownHosts.Remove 对缺失静默成功,404 语义由 handler 判存补)。
	if rec := del("host=198.51.100.99&port=22"); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE 不存在条目 code = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}

	// DELETE 部分参数(只 host 或只 port)→ 400;全缺=裸删清空,见专项用例。
	for _, q := range []string{"host=1.2.3.4", "port=22"} {
		if rec := del(q); rec.Code != http.StatusBadRequest {
			t.Fatalf("DELETE 部分参数(%q) code = %d, want 400; body=%s", q, rec.Code, rec.Body.String())
		}
	}

	// POST → 405。
	rec := httptest.NewRecorder()
	h.HandleKnownHosts(rec, httptest.NewRequest(http.MethodPost, "/api/proxy/ssh/known-hosts", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST code = %d, want 405; body=%s", rec.Code, rec.Body.String())
	}
}

// 未配置任何条目(库文件尚不存在)→ 200 + 空数组,不是 404。
func TestSSHKnownHostsHandlerEmptyList(t *testing.T) {
	h := NewSSHKnownHostsHandler(filepath.Join(t.TempDir(), "known_hosts"))
	rec := httptest.NewRecorder()
	h.HandleKnownHosts(rec, httptest.NewRequest(http.MethodGet, "/api/proxy/ssh/known-hosts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("空库 GET code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"known_hosts":[]`) {
		t.Fatalf("空库应返回空数组; body=%s", rec.Body.String())
	}
}

// 裸 DELETE(不带任何参数)= 清空全部:删空集合语义,幂等;也是损坏 JSON 库
// 的唯一 API 自愈出口(Clear 不 load 直接重写空库——损坏时 List 恒空、单删
// 恒 404,没有裸删就永不恢复)。部分参数仍 400。
func TestSSHKnownHostsHandlerBareDeleteClearsAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	seedKnownHostEntry(t, path, "203.0.113.10:22")
	seedKnownHostEntry(t, path, "198.51.100.7:2200")
	h := NewSSHKnownHostsHandler(path)

	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.HandleKnownHosts(rec, httptest.NewRequest(http.MethodGet, "/api/proxy/ssh/known-hosts", nil))
		return rec
	}
	bareDelete := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.HandleKnownHosts(rec, httptest.NewRequest(http.MethodDelete, "/api/proxy/ssh/known-hosts", nil))
		return rec
	}

	// 预置两条 → 裸 DELETE 200 → 列表空。
	if rec := bareDelete(); rec.Code != http.StatusOK {
		t.Fatalf("裸 DELETE code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if items := getKnownHostsItems(t, get()); len(items) != 0 {
		t.Fatalf("裸 DELETE 后条数 = %d, want 0", len(items))
	}
	if rec := get(); !strings.Contains(rec.Body.String(), `"known_hosts":[]`) {
		t.Fatalf("裸 DELETE 后应为空数组; body=%s", rec.Body.String())
	}

	// 空库再裸 DELETE → 200(幂等,删空集合)。
	if rec := bareDelete(); rec.Code != http.StatusOK {
		t.Fatalf("空库裸 DELETE code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}
