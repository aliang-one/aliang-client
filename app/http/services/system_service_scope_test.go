package services

import (
	"errors"
	"testing"

	"aliang.one/nursorgate/processor/setup"
)

func TestChooseReportedScope(t *testing.T) {
	cases := []struct {
		name            string
		systemInstalled bool
		userInstalled   bool
		want            string
	}{
		{"neither registered", false, false, serviceScopeNone},
		{"system only", true, false, serviceScopeSystem},
		{"user only", false, true, serviceScopeUser},
		{"both registered prefers system", true, true, serviceScopeSystem},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chooseReportedScope(tc.systemInstalled, tc.userInstalled); got != tc.want {
				t.Fatalf("chooseReportedScope(%v, %v) = %q, want %q", tc.systemInstalled, tc.userInstalled, got, tc.want)
			}
		})
	}
}

func TestChooseUninstallScope(t *testing.T) {
	cases := []struct {
		name          string
		system        bool
		user          bool
		hasPrivileges bool
		wantSystem    bool
		wantErr       error
	}{
		{"system with privileges uninstalls system", true, false, true, true, nil},
		{"system without privileges stays forbidden", true, false, false, true, setup.ErrNotRoot},
		{"system preferred over user", true, true, true, true, nil},
		{"user only uninstalls without privileges", false, true, false, false, nil},
		{"nothing registered is not installed", false, false, true, false, setup.ErrServiceNotInstalled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useSystem, err := chooseUninstallScope(tc.system, tc.user, tc.hasPrivileges)
			if useSystem != tc.wantSystem {
				t.Fatalf("chooseUninstallScope(%v, %v, %v) useSystem = %v, want %v", tc.system, tc.user, tc.hasPrivileges, useSystem, tc.wantSystem)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("chooseUninstallScope(%v, %v, %v) err = %v, want %v", tc.system, tc.user, tc.hasPrivileges, err, tc.wantErr)
			}
		})
	}
}

func TestDefaultInstallScope(t *testing.T) {
	svc := &SystemServiceService{}
	if got := svc.defaultInstallScope(true); got != serviceScopeSystem {
		t.Fatalf("privileged install scope = %q, want %q", got, serviceScopeSystem)
	}
	// 无特权:平台支持用户级注册时降级为 user 级,否则保持系统级。
	wantWithoutPrivileges := serviceScopeSystem
	if userScopeServiceSupported() {
		wantWithoutPrivileges = serviceScopeUser
	}
	if got := svc.defaultInstallScope(false); got != wantWithoutPrivileges {
		t.Fatalf("unprivileged install scope = %q, want %q", got, wantWithoutPrivileges)
	}
}
