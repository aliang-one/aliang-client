package tls

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"aliang.one/nursorgate/processor/config"
)

func modelMappingBoolPtr(value bool) *bool {
	return &value
}

func mustParseHTTP1TestURL(t *testing.T, rawPath string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(rawPath)
	if err != nil {
		t.Fatalf("parse test URL: %v", err)
	}
	return parsed
}

func resetModelMappingConfigForTest(t *testing.T, cfg *config.ModelMappingConfig) {
	t.Helper()
	config.ResetGlobalConfigForTest()
	config.SetGlobalConfig(&config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{
			ModelMapping: cfg,
		},
	})
	t.Cleanup(config.ResetGlobalConfigForTest)
}

func TestRewriteHTTP1AIModelField(t *testing.T) {
	rules := map[string]string{"gpt-4": "gpt-4o", "claude-3": "claude-3-5"}
	cases := []struct {
		name      string
		body      string
		wantOK    bool
		wantModel string
		wantIn    string // substring that must be present in output when ok
	}{
		{name: "hit", body: `{"model":"gpt-4","messages":[]}`, wantOK: true, wantModel: "gpt-4o"},
		{name: "miss", body: `{"model":"other","messages":[]}`, wantOK: false},
		{name: "model not string", body: `{"model":123}`, wantOK: false},
		{name: "no model field", body: `{"foo":"bar"}`, wantOK: false},
		{name: "invalid json", body: `not json`, wantOK: false},
		{name: "trailing data", body: `{"model":"gpt-4"}{}`, wantOK: false},
		{name: "preserves numbers", body: `{"model":"claude-3","n":1.5}`, wantOK: true, wantModel: "claude-3-5", wantIn: `"n":1.5`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := rewriteHTTP1AIModelField([]byte(tc.body), rules)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (out=%q)", ok, tc.wantOK, out)
			}
			if !ok {
				return
			}
			var obj map[string]any
			if err := json.Unmarshal(out, &obj); err != nil {
				t.Fatalf("rewritten body is not JSON: %v (out=%q)", err, out)
			}
			if got := obj["model"]; got != tc.wantModel {
				t.Fatalf("model = %v, want %q", got, tc.wantModel)
			}
			if tc.wantIn != "" && !strings.Contains(string(out), tc.wantIn) {
				t.Fatalf("output %q missing %q", out, tc.wantIn)
			}
		})
	}
}

func TestApplyHTTP1AIModelRewrite_RewritesAndNormalizesFraming(t *testing.T) {
	resetModelMappingConfigForTest(t, &config.ModelMappingConfig{
		Enabled: modelMappingBoolPtr(true),
		Rules:   map[string]string{"src-model": "dst-model"},
	})

	body := []byte(`{"model":"src-model","x":1}`)
	req := &http.Request{
		Method:           "POST",
		URL:              mustParseHTTP1TestURL(t, "/v1/chat/completions"),
		Proto:            "HTTP/1.1",
		ProtoMajor:       1,
		ProtoMinor:       1,
		Host:             "api.openai.com",
		Header:           http.Header{},
		Body:             io.NopCloser(bytes.NewReader(body)),
		ContentLength:    int64(len(body)),
		TransferEncoding: []string{"chunked"},
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Transfer-Encoding", "chunked")

	applyHTTP1AIModelRewrite(req)

	out, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read rewritten body: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("rewritten body not JSON: %v (out=%q)", err, out)
	}
	if obj["model"] != "dst-model" {
		t.Fatalf("model = %v, want dst-model", obj["model"])
	}
	if obj["x"].(float64) != 1 {
		t.Fatalf("x = %v, want 1", obj["x"])
	}
	if got := req.Header.Get("Transfer-Encoding"); got != "" {
		t.Fatalf("Transfer-Encoding = %q, want removed", got)
	}
	if got := req.Header.Get("Content-Length"); got != strconv.Itoa(len(out)) {
		t.Fatalf("Content-Length header = %q, want %d", got, len(out))
	}
	if req.ContentLength != int64(len(out)) {
		t.Fatalf("req.ContentLength = %d, want %d", req.ContentLength, len(out))
	}
	if len(req.TransferEncoding) != 0 {
		t.Fatalf("req.TransferEncoding = %v, want cleared", req.TransferEncoding)
	}

	req.Body = io.NopCloser(bytes.NewReader(out))
	var wire bytes.Buffer
	if err := req.Write(&wire); err != nil {
		t.Fatalf("serialize rewritten request: %v", err)
	}
	if strings.Contains(wire.String(), "Transfer-Encoding:") {
		t.Fatalf("wire request still uses Transfer-Encoding:\n%s", wire.String())
	}
	if !strings.Contains(wire.String(), "Content-Length: "+strconv.Itoa(len(out))+"\r\n") {
		t.Fatalf("wire request missing normalized Content-Length %d:\n%s", len(out), wire.String())
	}
}

func TestApplyHTTP1AIModelRewrite_NoopCases(t *testing.T) {
	t.Run("disabled passes through", func(t *testing.T) {
		resetModelMappingConfigForTest(t, &config.ModelMappingConfig{
			Enabled: modelMappingBoolPtr(false),
			Rules:   map[string]string{"src": "dst"},
		})
		body := []byte(`{"model":"src"}`)
		req := &http.Request{Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
		req.Header.Set("Content-Type", "application/json")
		applyHTTP1AIModelRewrite(req)
		out, _ := io.ReadAll(req.Body)
		if string(out) != `{"model":"src"}` {
			t.Fatalf("disabled rewrite changed body to %q", out)
		}
	})

	t.Run("non-json passes through", func(t *testing.T) {
		resetModelMappingConfigForTest(t, &config.ModelMappingConfig{
			Enabled: modelMappingBoolPtr(true),
			Rules:   map[string]string{"src": "dst"},
		})
		body := []byte(`{"model":"src"}`)
		req := &http.Request{Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
		req.Header.Set("Content-Type", "text/plain")
		applyHTTP1AIModelRewrite(req)
		out, _ := io.ReadAll(req.Body)
		if string(out) != `{"model":"src"}` {
			t.Fatalf("non-json rewrite changed body to %q", out)
		}
	})

	t.Run("unmapped model passes through", func(t *testing.T) {
		resetModelMappingConfigForTest(t, &config.ModelMappingConfig{
			Enabled: modelMappingBoolPtr(true),
			Rules:   map[string]string{"src": "dst"},
		})
		body := []byte(`{"model":"untouched"}`)
		req := &http.Request{Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
		req.Header.Set("Content-Type", "application/json")
		applyHTTP1AIModelRewrite(req)
		out, _ := io.ReadAll(req.Body)
		if string(out) != `{"model":"untouched"}` {
			t.Fatalf("unmapped model changed body to %q", out)
		}
	})
}

func TestApplyHTTP1AIModelRewrite_OversizedPassesThroughStream(t *testing.T) {
	resetModelMappingConfigForTest(t, &config.ModelMappingConfig{
		Enabled: modelMappingBoolPtr(true),
		Rules:   map[string]string{"src": "dst"},
	})
	// Build a JSON body larger than the buffer cap, with a model that would be
	// mapped if buffered. It must be forwarded verbatim.
	pad := bytes.Repeat([]byte(" "), http1ModelRewriteMaxBodyBytes+64)
	body := append([]byte(`{"model":"src","pad":"`), append(pad, []byte(`"}`)...)...)
	req := &http.Request{Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
	req.Header.Set("Content-Type", "application/json")

	applyHTTP1AIModelRewrite(req)

	out, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read oversize body: %v", err)
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("oversize body modified: got len %d, want %d", len(out), len(body))
	}
}

// relaySingleHTTP1ModelRequest sends one client request through RelayHTTP1 and
// returns the request (as parsed at the remote) along with its body and the
// response body the client received.
func relaySingleHTTP1ModelRequest(t *testing.T, clientReq, remoteResp string) (*http.Request, []byte, []byte) {
	t.Helper()

	clientConn, proxyClientConn := net.Pipe()
	defer clientConn.Close()
	defer proxyClientConn.Close()
	proxyRemoteConn, remoteConn := net.Pipe()
	defer proxyRemoteConn.Close()
	defer remoteConn.Close()

	relayDone := make(chan error, 1)
	go func() {
		_, err := RelayHTTP1(t.Context(), proxyClientConn, proxyRemoteConn)
		relayDone <- err
	}()

	type remoteResult struct {
		req  *http.Request
		body []byte
		err  string
	}
	remoteCh := make(chan remoteResult, 1)
	go func() {
		req, err := http.ReadRequest(bufio.NewReader(remoteConn))
		if err != nil {
			remoteCh <- remoteResult{err: "read request: " + err.Error()}
			return
		}
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			remoteCh <- remoteResult{err: "read body: " + err.Error()}
			return
		}
		if _, werr := io.WriteString(remoteConn, remoteResp); werr != nil {
			remoteCh <- remoteResult{err: "write resp: " + werr.Error()}
			return
		}
		remoteCh <- remoteResult{req: req, body: body}
	}()

	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(clientConn, clientReq)
		writeDone <- err
	}()

	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatalf("read client response: %v", err)
	}
	clientBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	res := <-remoteCh
	if res.err != "" {
		t.Fatalf("remote side error: %s", res.err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("client write: %v", err)
	}
	if err := <-relayDone; err != nil {
		t.Fatalf("RelayHTTP1: %v", err)
	}
	return res.req, res.body, clientBody
}

func TestRelayHTTP1_RewritesMappedAIModel(t *testing.T) {
	resetModelMappingConfigForTest(t, &config.ModelMappingConfig{
		Enabled: modelMappingBoolPtr(true),
		Rules:   map[string]string{"gpt-3.5-turbo": "gpt-4o"},
	})

	body := `{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"hi"}]}`
	reqText := "POST /v1/chat/completions HTTP/1.1\r\n" +
		"Host: api.openai.com\r\n" +
		"Content-Type: application/json\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(body)) +
		"Connection: close\r\n" +
		"\r\n" + body
	respText := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nOK"

	remoteReq, remoteBody, clientBody := relaySingleHTTP1ModelRequest(t, reqText, respText)

	var got map[string]any
	if err := json.Unmarshal(remoteBody, &got); err != nil {
		t.Fatalf("remote body not JSON: %v (body=%q)", err, remoteBody)
	}
	if got["model"] != "gpt-4o" {
		t.Fatalf("remote model = %v, want gpt-4o", got["model"])
	}
	messages, _ := got["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages not preserved: %v", got["messages"])
	}
	if cl := remoteReq.Header.Get("Content-Length"); cl != strconv.Itoa(len(remoteBody)) {
		t.Fatalf("remote Content-Length = %q, want %d", cl, len(remoteBody))
	}
	if string(clientBody) != "OK" {
		t.Fatalf("client response body = %q, want OK", clientBody)
	}
}

func TestRelayHTTP1_PassesThroughUnmappedAIModel(t *testing.T) {
	resetModelMappingConfigForTest(t, &config.ModelMappingConfig{
		Enabled: modelMappingBoolPtr(true),
		Rules:   map[string]string{"gpt-4o": "gpt-4o-mini"},
	})

	body := `{"model":"claude-3-opus","prompt":"hi"}`
	reqText := "POST /v1/messages HTTP/1.1\r\n" +
		"Host: api.anthropic.com\r\n" +
		"Content-Type: application/json\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(body)) +
		"Connection: close\r\n" +
		"\r\n" + body
	respText := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nOK"

	_, remoteBody, _ := relaySingleHTTP1ModelRequest(t, reqText, respText)

	var got map[string]any
	if err := json.Unmarshal(remoteBody, &got); err != nil {
		t.Fatalf("remote body not JSON: %v (body=%q)", err, remoteBody)
	}
	if got["model"] != "claude-3-opus" {
		t.Fatalf("remote model = %v, want claude-3-opus (unmapped passthrough)", got["model"])
	}
}
