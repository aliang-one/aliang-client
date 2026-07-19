package handlers

import (
	"reflect"
	"testing"

	authuser "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

func TestEffectiveStartupAuthSnapshotHidesUserAfterHardInvalid(t *testing.T) {
	status, fetchSuccess, user := effectiveStartupAuthSnapshot(
		runtime.READY,
		true,
		&authuser.UserInfo{Username: "stale-user"},
		authuser.StateHardInvalid,
	)

	if status != runtime.UNCONFIGURED || fetchSuccess || user != nil {
		t.Fatalf("snapshot = status=%v fetch=%t user=%#v, want UNCONFIGURED/false/nil", status, fetchSuccess, user)
	}
}

func TestEffectiveStartupAuthSnapshotKeepsSoftExpiredUser(t *testing.T) {
	wantUser := &authuser.UserInfo{Username: "recovering-user"}
	status, fetchSuccess, user := effectiveStartupAuthSnapshot(
		runtime.READY,
		true,
		wantUser,
		authuser.StateSoftExpired,
	)

	if status != runtime.READY || !fetchSuccess || user != wantUser {
		t.Fatalf("snapshot = status=%v fetch=%t user=%#v, want READY/true/original user", status, fetchSuccess, user)
	}
}

func TestGetSuggestedActions_UsesSessionLoginSemanticsForConfiguring(t *testing.T) {
	actions := getSuggestedActions(runtime.CONFIGURING)
	want := []string{
		"GET /api/auth/session - Retry local session restore",
		"POST /api/auth/login - Login if no local session",
		"GET /api/startup/status - Check authentication progress",
	}

	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("unexpected actions for CONFIGURING:\nwant=%v\ngot=%v", want, actions)
	}
}

func TestGetStatusTransitionInfo_UsesAuthSessionWording(t *testing.T) {
	info := getStatusTransitionInfo(runtime.CONFIGURING)

	description, ok := info["description"].(string)
	if !ok {
		t.Fatalf("description missing or not string: %v", info)
	}
	if description != "Authentication in progress" {
		t.Fatalf("unexpected CONFIGURING description: %q", description)
	}

	transitions, ok := info["possible_transitions"].([]string)
	if !ok {
		t.Fatalf("possible_transitions missing or wrong type: %T %#v", info["possible_transitions"], info["possible_transitions"])
	}

	wantTransitions := []string{
		"→ READY (session restore or login success)",
		"→ UNCONFIGURED (authentication failed, no local session)",
	}
	if !reflect.DeepEqual(transitions, wantTransitions) {
		t.Fatalf("unexpected CONFIGURING transitions:\nwant=%v\ngot=%v", wantTransitions, transitions)
	}
}
