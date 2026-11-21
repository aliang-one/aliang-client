package config

import (
	"encoding/json"
	"testing"
)

// event_listeners 三态:nil=默认开(能力默认在,升级零回退)、true/false 显式。
// 加载白名单与保存白名单(customer_config_service.go)两处都必须放行该键,
// 否则保存 400 / 手改配置加载即拒——开关就成了摆设。同 peer_mcp 的坑位
// (见 peer_mcp_test.go TestPeerMCPKeyAcceptedOnLoad 注释:fixture 必须带合法
// core 段,否则会以「core.api_server is required」无关错误失败误导排查)。
func TestEventListenersKeyAcceptedOnLoad(t *testing.T) {
	raw := `{"core":{"api_server":"https://api.example.com"},"customer":{"event_listeners":{"enable":false}}}`
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("event_listeners 应被加载白名单放行, got: %v", err)
	}
	if c.EffectiveEventListenersEnabled() {
		t.Fatal("显式 false 应关闭")
	}
}
