package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	auth "aliang.one/nursorgate/processor/auth"
)

func TestUserCenterHandler_ProfileAndUsage_UnauthenticatedEnvelope(t *testing.T) {
	auth.ResetAuthPersistenceForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
	})

	h := NewUserCenterHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/user-center/profile", nil)
	rec := httptest.NewRecorder()
	h.HandleProfile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("profile status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode profile response failed: %v", err)
	}

	data, _ := resp["data"].(map[string]interface{})
	if data["status"] != "unauthenticated" {
		t.Fatalf("expected unauthenticated status, got %#v", data["status"])
	}
	if data["error"] != "session_missing" {
		t.Fatalf("expected session_missing error, got %#v", data["error"])
	}

	usageReq := httptest.NewRequest(http.MethodGet, "/api/user-center/usage/summary", nil)
	usageRec := httptest.NewRecorder()
	h.HandleGetUsageSummary(usageRec, usageReq)
	if usageRec.Code != http.StatusOK {
		t.Fatalf("usage summary status=%d body=%s", usageRec.Code, usageRec.Body.String())
	}

	var usageResp map[string]interface{}
	if err := json.Unmarshal(usageRec.Body.Bytes(), &usageResp); err != nil {
		t.Fatalf("decode usage summary response failed: %v", err)
	}
	usageData, _ := usageResp["data"].(map[string]interface{})
	if usageData["status"] != "unauthenticated" {
		t.Fatalf("expected usage unauthenticated status, got %#v", usageData["status"])
	}
	if usageData["error"] != "session_missing" {
		t.Fatalf("expected usage session_missing error, got %#v", usageData["error"])
	}

	apiKeysReq := httptest.NewRequest(http.MethodGet, "/api/user-center/api-keys", nil)
	apiKeysRec := httptest.NewRecorder()
	h.HandleGetAPIKeys(apiKeysRec, apiKeysReq)
	if apiKeysRec.Code != http.StatusOK {
		t.Fatalf("api keys status=%d body=%s", apiKeysRec.Code, apiKeysRec.Body.String())
	}

	var apiKeysResp map[string]interface{}
	if err := json.Unmarshal(apiKeysRec.Body.Bytes(), &apiKeysResp); err != nil {
		t.Fatalf("decode api keys response failed: %v", err)
	}
	apiKeysData, _ := apiKeysResp["data"].(map[string]interface{})
	if apiKeysData["status"] != "unauthenticated" {
		t.Fatalf("expected api keys unauthenticated status, got %#v", apiKeysData["status"])
	}
	if apiKeysData["error"] != "session_missing" {
		t.Fatalf("expected api keys session_missing error, got %#v", apiKeysData["error"])
	}
}

func TestUserCenterHandler_UpdateAndRedeem_Validation(t *testing.T) {
	auth.ResetAuthPersistenceForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
	})

	h := NewUserCenterHandler()

	updateReq := httptest.NewRequest(http.MethodPut, "/api/user-center/profile", bytes.NewReader([]byte(`{"username":"   "}`)))
	updateRec := httptest.NewRecorder()
	h.HandleProfile(updateRec, updateReq)

	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}

	var updateResp map[string]interface{}
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updateResp); err != nil {
		t.Fatalf("decode update response failed: %v", err)
	}
	updateData, _ := updateResp["data"].(map[string]interface{})
	if updateData["error"] != "username_required" {
		t.Fatalf("expected username_required, got %#v", updateData["error"])
	}

	redeemReq := httptest.NewRequest(http.MethodPost, "/api/user-center/redeem", bytes.NewReader([]byte(`{"code":""}`)))
	redeemRec := httptest.NewRecorder()
	h.HandleRedeemCode(redeemRec, redeemReq)

	if redeemRec.Code != http.StatusOK {
		t.Fatalf("redeem status=%d body=%s", redeemRec.Code, redeemRec.Body.String())
	}

	var redeemResp map[string]interface{}
	if err := json.Unmarshal(redeemRec.Body.Bytes(), &redeemResp); err != nil {
		t.Fatalf("decode redeem response failed: %v", err)
	}
	redeemData, _ := redeemResp["data"].(map[string]interface{})
	if redeemData["error"] != "redeem_code_required" {
		t.Fatalf("expected redeem_code_required, got %#v", redeemData["error"])
	}
}

func TestUserCenterHandler_MethodValidation(t *testing.T) {
	h := NewUserCenterHandler()

	req := httptest.NewRequest(http.MethodDelete, "/api/user-center/profile", nil)
	rec := httptest.NewRecorder()
	h.HandleProfile(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for method not allowed envelope, got %d", rec.Code)
	}
}
