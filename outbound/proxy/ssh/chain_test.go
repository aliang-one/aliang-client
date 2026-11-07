package ssh

import (
	"context"
	"net"
	"testing"

	"aliang.one/nursorgate/outbound/proxy/ssh/sshtest"
	gossh "golang.org/x/crypto/ssh"
)

func TestDialChainZeroHopPassword(t *testing.T) {
	srv, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))
	kh := newTestKnownHosts(t)
	d := newChainDialer(finalNode(addr, "u", "p", "", ""), nil, kh)

	client, err := d.dialChain(context.Background())
	if err != nil {
		t.Fatalf("dialChain: %v", err)
	}
	defer client.Close()

	// TOFU 写侧闭环:真实握手路径必须把 sshd 的 host key 指纹写进库
	//(known_hosts 单元测试只验证过回调本身,这里验证 dialChain 真接线)。
	entries := kh.List()
	want := gossh.FingerprintSHA256(srv.HostKey.PublicKey())
	if len(entries) != 1 {
		t.Fatalf("known_hosts entries = %d, want 1", len(entries))
	}
	if entries[0].Fingerprint != want {
		t.Fatalf("known_hosts fingerprint = %s, want %s", entries[0].Fingerprint, want)
	}
}

func TestDialChainWrongPassword(t *testing.T) {
	_, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))
	d := newChainDialer(finalNode(addr, "u", "WRONG", "", ""), nil, newTestKnownHosts(t))

	if _, err := d.dialChain(context.Background()); err == nil {
		t.Fatal("wrong password must fail")
	}
}

func TestDialChainHostKeyMismatch(t *testing.T) {
	_, addr := sshtest.Start(t, sshtest.WithUsername("u"), sshtest.WithPassword("p"))
	kh := newTestKnownHosts(t)
	// 模拟「服务器换 key 后复连」:known_hosts 已记录一个非本 sshd 的指纹,
	// 复连时服务器呈现真实 key,必须拒连。(计划骨架的「两台 Start 模拟」
	// 不成立:两次 Start 各占随机端口,known_hosts 按 host:port 分键,
	// 第二台只是首连 TOFU,不会冲突;预写指纹才是等价且真实的场景。)
	other := testSigner(t)
	if err := kh.CallbackFor(addr)("h", &net.TCPAddr{}, other.PublicKey()); err != nil {
		t.Fatalf("seed known_hosts: %v", err)
	}
	d := newChainDialer(finalNode(addr, "u", "p", "", ""), nil, kh)
	if _, err := d.dialChain(context.Background()); err == nil {
		t.Fatal("host key mismatch must be rejected")
	}
}
