package services

import (
	"encoding/json"
	"errors"
	"testing"

	auth "aliang.one/nursorgate/processor/auth"
)

func resetUserCenterServiceHooksForTest() {
	getUserProfileFn = auth.GetUserProfile
	updateUserProfileFn = auth.UpdateUserProfile
	getUserUsageSummaryFn = auth.GetUserUsageSummary
	getUserUsageProgressFn = auth.GetUserUsageProgress
	redeemCodeFn = auth.RedeemCode
}

func TestUserCenterService_GetProfile_Unauthenticated(t *testing.T) {
	defer resetUserCenterServiceHooksForTest()
	getUserProfileFn = func() (*auth.UserProfile, error) {
		return nil, errors.New("no user session")
	}

	svc := NewUserCenterService()
	result := svc.GetProfile()

	if got := result["status"]; got != "unauthenticated" {
		t.Fatalf("status=%v", got)
	}
	if got := result["error"]; got != "session_missing" {
		t.Fatalf("error=%v", got)
	}
}

func TestUserCenterService_GetProfile_SessionExpired(t *testing.T) {
	defer resetUserCenterServiceHooksForTest()
	getUserProfileFn = func() (*auth.UserProfile, error) {
		return nil, auth.ErrSessionExpired
	}

	svc := NewUserCenterService()
	result := svc.GetProfile()

	if got := result["status"]; got != "unauthenticated" {
		t.Fatalf("status=%v", got)
	}
	if got := result["error"]; got != "session_expired" {
		t.Fatalf("error=%v", got)
	}
}

func TestUserCenterService_UpdateProfile_Validation(t *testing.T) {
	svc := NewUserCenterService()
	result := svc.UpdateProfile("  ")

	if got := result["status"]; got != "failed" {
		t.Fatalf("status=%v", got)
	}
	if got := result["error"]; got != "username_required" {
		t.Fatalf("error=%v", got)
	}
}

func TestUserCenterService_GetUsageSummary_Success(t *testing.T) {
	defer resetUserCenterServiceHooksForTest()
	getUserUsageSummaryFn = func() (*auth.UserUsageSummary, error) {
		return &auth.UserUsageSummary{
			ActiveCount:   2,
			TotalUsedUSD:  9.5,
			Subscriptions: []json.RawMessage{json.RawMessage(`{"id":1}`)},
		}, nil
	}

	svc := NewUserCenterService()
	result := svc.GetUsageSummary()

	if got := result["status"]; got != "success" {
		t.Fatalf("status=%v", got)
	}
}

func TestUserCenterService_GetUsageProgress_Unauthenticated(t *testing.T) {
	defer resetUserCenterServiceHooksForTest()
	getUserUsageProgressFn = func() (*auth.UserUsageProgress, error) {
		return nil, errors.New("missing access token")
	}

	svc := NewUserCenterService()
	result := svc.GetUsageProgress()

	if got := result["status"]; got != "unauthenticated" {
		t.Fatalf("status=%v", got)
	}
	if got := result["error"]; got != "session_missing" {
		t.Fatalf("error=%v", got)
	}
}

func TestUserCenterService_RedeemCode_Success(t *testing.T) {
	defer resetUserCenterServiceHooksForTest()
	redeemCodeFn = func(code string) (*auth.RedeemResult, error) {
		if code != "REDEEM-OK" {
			t.Fatalf("unexpected code: %s", code)
		}
		return &auth.RedeemResult{Data: json.RawMessage(`{"status":"used"}`)}, nil
	}

	svc := NewUserCenterService()
	result := svc.RedeemCode("REDEEM-OK")

	if got := result["status"]; got != "success" {
		t.Fatalf("status=%v", got)
	}
}

func TestUserCenterService_RedeemCode_Unauthenticated(t *testing.T) {
	defer resetUserCenterServiceHooksForTest()
	redeemCodeFn = func(code string) (*auth.RedeemResult, error) {
		return nil, errors.New("no user session")
	}

	svc := NewUserCenterService()
	result := svc.RedeemCode("CODE-1")

	if got := result["status"]; got != "unauthenticated" {
		t.Fatalf("status=%v", got)
	}
	if got := result["error"]; got != "session_missing" {
		t.Fatalf("error=%v", got)
	}
}
