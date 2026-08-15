package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aliang.one/nursorgate/processor/config"
)

func TestAtomicApplySuccess_ConfigHandlerUpdatesVersionHash(t *testing.T) {
	config.ResetRoutingApplyStoreForTest()

	h := NewConfigHandler()
	payload := []byte(`{
		"version": 1,
		"ingress": {"mode": "tun"},
		"egress": {
			"direct": {"enabled": true},
			"toAliang": {"enabled": true},
			"toSocks": {"enabled": true, "upstream": {"type": "socks"}}
		},
		"routing": {
			"default_egress": "direct",
			"rules": []
		}
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/config/routing", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	h.HandleRoutingConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("response data missing: %s", rec.Body.String())
	}

	version, ok := data["version"].(float64)
	if !ok || uint64(version) != 1 {
		t.Fatalf("response version=%v, want 1", data["version"])
	}
	hash, ok := data["hash"].(string)
	if !ok || hash == "" {
		t.Fatalf("response hash=%v, want non-empty string", data["hash"])
	}

	_, activeVersion, activeHash := config.GetRoutingApplyStore().ActiveSnapshotVersionHash()
	if activeVersion != 1 {
		t.Fatalf("active version=%d, want 1", activeVersion)
	}
	if activeHash != hash {
		t.Fatalf("active hash=%q, want %q", activeHash, hash)
	}
}

func TestAtomicApplyRollback_ConfigHandlerKeepsLKG(t *testing.T) {
	config.ResetRoutingApplyStoreForTest()

	h := NewConfigHandler()
	validPayload := []byte(`{
		"version": 1,
		"ingress": {"mode": "tun"},
		"egress": {
			"direct": {"enabled": true},
			"toAliang": {"enabled": true},
			"toSocks": {"enabled": true, "upstream": {"type": "socks"}}
		},
		"routing": {
			"default_egress": "direct",
			"rules": []
		}
	}`)

	seedReq := httptest.NewRequest(http.MethodPost, "/api/config/routing", bytes.NewReader(validPayload))
	seedRec := httptest.NewRecorder()
	h.HandleRoutingConfig(seedRec, seedReq)
	if seedRec.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", seedRec.Code, seedRec.Body.String())
	}

	seedSnapshot, seedVersion, seedHash := config.GetRoutingApplyStore().ActiveSnapshotVersionHash()
	if seedSnapshot == nil || seedHash == "" || seedVersion == 0 {
		t.Fatalf("seed active state invalid: snapshot=%v version=%d hash=%q", seedSnapshot, seedVersion, seedHash)
	}

	invalidPayload := []byte(`{
		"version": 1,
		"ingress": {"mode": "tun"},
		"egress": {
			"direct": {"enabled": true},
			"toAliang": {"enabled": false},
			"toSocks": {"enabled": true, "upstream": {"type": "socks"}}
		},
		"routing": {
			"default_egress": "direct",
			"rules": [
				{"id":"r1","type":"domain","condition":"example.com","enabled":true,"target":"toAliang"}
			]
		}
	}`)

	badReq := httptest.NewRequest(http.MethodPost, "/api/config/routing", bytes.NewReader(invalidPayload))
	badRec := httptest.NewRecorder()
	h.HandleRoutingConfig(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", badRec.Code, badRec.Body.String())
	}

	finalSnapshot, finalVersion, finalHash := config.GetRoutingApplyStore().ActiveSnapshotVersionHash()
	if finalVersion != seedVersion {
		t.Fatalf("final version=%d, want %d", finalVersion, seedVersion)
	}
	if finalHash != seedHash {
		t.Fatalf("final hash=%q, want %q", finalHash, seedHash)
	}
	if finalSnapshot != seedSnapshot {
		t.Fatal("active snapshot changed after failed apply")
	}
}
