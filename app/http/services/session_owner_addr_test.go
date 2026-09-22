package services

import (
	"testing"

	"aliang.one/nursorgate/processor/config"
)

// TestDefaultSessionOwnerAddr 锁定 owner 默认地址的计算：跟随 --host 配置的
// 管理监听地址（默认 127.0.0.1:56431）。owner 进程注入
// （agentruntime.ownerBaseURL 的默认分支）与 agent 进程自身兜底
// （cmd.ensureUserAgentEnvironment）共用该函数，两端答案必须一致。
func TestDefaultSessionOwnerAddr(t *testing.T) {
	if err := config.SetServiceBindHost(config.DefaultServiceBindHost); err != nil {
		t.Fatalf("reset ServiceBindHost to default failed: %v", err)
	}
	t.Cleanup(func() {
		_ = config.SetServiceBindHost(config.DefaultServiceBindHost)
	})

	if got, want := DefaultSessionOwnerAddr(), "http://127.0.0.1:56431"; got != want {
		t.Fatalf("DefaultSessionOwnerAddr() = %q, want %q", got, want)
	}

	if err := config.SetServiceBindHost("0.0.0.0"); err != nil {
		t.Fatalf("SetServiceBindHost(0.0.0.0) failed: %v", err)
	}
	if got, want := DefaultSessionOwnerAddr(), "http://0.0.0.0:56431"; got != want {
		t.Fatalf("DefaultSessionOwnerAddr() after --host 0.0.0.0 = %q, want %q", got, want)
	}
}
