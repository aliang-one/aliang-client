package meshproto

import "testing"

// 帧协议线格式用例(Encode/Parse/AppendHop/ClassCarrierAllowed)已随符号
// 退役(SPEC §13.1)删除;本文件只保留语义常量与目标拆解工具的契约测试。

func TestDNSTarget(t *testing.T) {
	if !IsDNSTarget(DNSTarget) {
		t.Fatal("dns target must be recognized")
	}
	if IsDNSTarget("example.com:443") {
		t.Fatal("host target must not be dns")
	}
}

func TestSplitHostPort(t *testing.T) {
	host, port, isDomain, err := SplitTarget("api.aliang.one:443")
	if err != nil || host != "api.aliang.one" || port != "443" || !isDomain {
		t.Fatalf("SplitTarget domain: %v %q %q %v", err, host, port, isDomain)
	}
	host, port, isDomain, err = SplitTarget("10.1.2.3:8080")
	if err != nil || host != "10.1.2.3" || port != "8080" || isDomain {
		t.Fatalf("SplitTarget ip: %v %q %q %v", err, host, port, isDomain)
	}
	host, port, isDomain, err = SplitTarget("10.1.2.3")
	if err != nil || host != "10.1.2.3" || port != "" || isDomain {
		t.Fatalf("SplitTarget bare ip: %v %q %q %v", err, host, port, isDomain)
	}
	host, port, isDomain, err = SplitTarget(DNSTarget)
	if err != nil {
		t.Fatalf("SplitTarget dns: %v", err)
	}
	if host != "" || port != "" || isDomain {
		t.Fatalf("SplitTarget dns must return zero values: %q %q %v", host, port, isDomain)
	}
}
