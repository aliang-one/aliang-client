package handlers

// 日志流 WS 的同源闸(2026-11-24 审计):旧 CheckOrigin 只看 r.Host 是否为
// localhost/127.0.0.1,对 Origin 头看都不看——用户浏览器里任何网页都能开
// ws://127.0.0.1:56431/api/logs/stream 读实时日志(部分脱敏),这条向量不依赖
// ssh_ingress。规则与聊天 WS(aiStreamHostAllowed,2026-11-19 重写)同源:带
// Origin 必须真同源;不带 Origin(脚本直连)仍要求回环 Host。

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogStreamCheckOriginSameOriginOnly(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"同源:回环仪表盘", "127.0.0.1:56431", "http://127.0.0.1:56431", true},
		{"同源:LAN 仪表盘(--host 部署远程浏览)", "172.16.222.1:56431", "http://172.16.222.1:56431", true},
		{"同源:https 反代", "aliang.liang.home", "https://aliang.liang.home", true},
		{"跨站页面被拒", "127.0.0.1:56431", "https://evil.example", false},
		{"跨站页面即使 Host 是 localhost 也被拒", "localhost:56431", "https://evil.example", false},
		{"Origin 为 null(file:// 页)被拒", "127.0.0.1:56431", "null", false},
		{"Origin 畸形被拒", "127.0.0.1:56431", "://", false},
		{"无 Origin + 回环 Host(脚本直连)放行", "127.0.0.1:56431", "", true},
		{"无 Origin + 非回环 Host 被拒", "172.16.222.1:56431", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/logs/stream", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := logUpgrader.CheckOrigin(r); got != tc.want {
				t.Fatalf("CheckOrigin(host=%q origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}
