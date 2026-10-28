package services

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEnsureAIUpstreamReachableNotifiesOnHealedUpstream(t *testing.T) {
	heals := 0
	notifed := 0
	origNotify := aiProxyHealNotifier
	aiProxyHealNotifier = func() { notifed++ }
	defer func() { aiProxyHealNotifier = origNotify }()

	dials := 0
	deps := aiProxyGuardDeps{
		dial: func(ctx context.Context, hostPort string) error {
			dials++
			if dials < 2 { // 首拨失败 → 触发 heal；复拨成功
				return errors.New("refused")
			}
			return nil
		},
		heal:  func() error { heals++; return nil },
		sleep: func(time.Duration) {},
	}
	if err := ensureAIUpstreamReachable(context.Background(), "http://127.0.0.1:56432", deps); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if heals != 1 || notifed != 1 {
		t.Fatalf("heals=%d notified=%d, want 1/1", heals, notifed)
	}
}

func TestEnsureAIUpstreamReachableSilentWhenHealFails(t *testing.T) {
	notifed := 0
	origNotify := aiProxyHealNotifier
	aiProxyHealNotifier = func() { notifed++ }
	defer func() { aiProxyHealNotifier = origNotify }()

	deps := aiProxyGuardDeps{
		dial:  func(ctx context.Context, hostPort string) error { return errors.New("refused") },
		heal:  func() error { return nil },
		sleep: func(time.Duration) {},
	}
	if err := ensureAIUpstreamReachable(context.Background(), "http://127.0.0.1:56432", deps); err == nil {
		t.Fatal("expected error when re-dial still fails")
	}
	if notifed != 0 {
		t.Fatalf("notified=%d on failed heal, want 0", notifed)
	}
}
