package proto

import "testing"

func TestSSHString(t *testing.T) {
	if got := SSH.String(); got != "ssh" {
		t.Fatalf("SSH.String() = %q, want ssh", got)
	}
	// 枚举追加在尾部,现有协议数值不得变动
	if got := Socks5.String(); got != "socks5" {
		t.Fatalf("Socks5.String() = %q, want socks5", got)
	}
}
