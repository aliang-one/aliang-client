package services

import (
	"errors"
	"fmt"
	"testing"

	inboundhttp "aliang.one/nursorgate/inbound/http"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/storage"
)

// 适配器是路由层(Task 3)与 registry(Task 2)之间的形状翻译缝。这里钉死:
// 五返回值拆解 / ModelOwner→map 翻译 / 三哨兵→路由层哨兵映射 / raw error 透传 /
// nil 降级。路由层行为本身由 inbound/http 的集成测试覆盖。

func TestAIEndpointSelectorAdapterSelectSplitsRow(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, &config.LLMEndpoint{
		ID:                "byo-a",
		Name:              "row-a",
		BaseURL:           "http://127.0.0.1:9/a",
		APIKey:            "key-a",
		ProtocolAnthropic: true,
		AutoGroup:         true,
		Enabled:           true,
		Priority:          5,
		Models:            "[]",
	})

	adapter := NewAIEndpointSelectorAdapter(svc)
	if adapter == nil {
		t.Fatal("adapter must not be nil for a live service")
	}

	id, baseURL, apiKey, builtin, err := adapter.Select("anthropic", "", "", "")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if id != "byo-a" || baseURL != "http://127.0.0.1:9/a" || apiKey != "key-a" || builtin {
		t.Fatalf("select split = (%q %q %q %v)", id, baseURL, apiKey, builtin)
	}
}

// final-review 必改 2:adapter 的 excludeID 必须透传到 service Select——重试
// attempt 排除上一失败行,命中下一候选(真切换缝)。
func TestAIEndpointSelectorAdapterExcludeIDPassesThrough(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, &config.LLMEndpoint{
		ID:                "byo-1",
		Name:              "row-1",
		BaseURL:           "http://127.0.0.1:9/1",
		APIKey:            "key-1",
		ProtocolAnthropic: true,
		AutoGroup:         true,
		Enabled:           true,
		Priority:          5,
		Models:            "[]",
	})
	createEndpoint(t, store, &config.LLMEndpoint{
		ID:                "byo-2",
		Name:              "row-2",
		BaseURL:           "http://127.0.0.1:9/2",
		APIKey:            "key-2",
		ProtocolAnthropic: true,
		AutoGroup:         true,
		Enabled:           true,
		Priority:          6,
		Models:            "[]",
	})

	adapter := NewAIEndpointSelectorAdapter(svc)
	id, _, _, _, err := adapter.Select("anthropic", "", "", "")
	if err != nil || id != "byo-1" {
		t.Fatalf("首次选择 = (%q, %v), want byo-1", id, err)
	}
	// 排除 byo-1 → byo-2。
	id, _, _, _, err = adapter.Select("anthropic", "", "", "byo-1")
	if err != nil || id != "byo-2" {
		t.Fatalf("排除 byo-1 后 = (%q, %v), want byo-2(真切换)", id, err)
	}
}

func TestAIEndpointSelectorAdapterModelsUnionShape(t *testing.T) {
	svc, store := newEndpointTestHarness(t)
	createEndpoint(t, store, &config.LLMEndpoint{
		ID:             "ep-m",
		Name:           "models-row",
		BaseURL:        "http://127.0.0.1:9/m",
		APIKey:         "k",
		ProtocolOpenAI: true,
		AutoGroup:      true,
		Enabled:        true,
		Priority:       1,
		Models:         `["gpt-x","glm-4.6"]`,
	})

	adapter := NewAIEndpointSelectorAdapter(svc)
	items := adapter.ModelsUnion()
	if len(items) != 2 {
		t.Fatalf("models union = %+v, want 2 entries", items)
	}
	if items[0]["id"] != "glm-4.6" || items[0]["owner"] != "ep-m" {
		t.Fatalf("items[0] = %+v", items[0])
	}
	if items[1]["id"] != "gpt-x" {
		t.Fatalf("items[1] = %+v", items[1])
	}
}

func TestAIEndpointSelectorAdapterTranslatesSentinels(t *testing.T) {
	// 翻译函数直测(三哨兵 + raw 透传)。
	if got := translateAISelectorError(fmt.Errorf("wrap: %w", ErrNoEndpoint)); !errors.Is(got, inboundhttp.ErrAINoEndpoint) {
		t.Fatalf("ErrNoEndpoint → %v", got)
	}
	if got := translateAISelectorError(fmt.Errorf("wrap: %w", ErrEndpointNotAvailable)); !errors.Is(got, inboundhttp.ErrAIEndpointNotAvailable) {
		t.Fatalf("ErrEndpointNotAvailable → %v", got)
	}
	if got := translateAISelectorError(fmt.Errorf("wrap: %w", ErrQueueTimeout)); !errors.Is(got, inboundhttp.ErrAIQueueTimeout) {
		t.Fatalf("ErrQueueTimeout → %v", got)
	}
	raw := errors.New("db gone")
	if got := translateAISelectorError(raw); !errors.Is(got, raw) {
		t.Fatalf("raw err must pass through, got %v", got)
	}

	// 真实 service 路径:显式点名不存在 → 404 哨兵;空注册表 → 503 哨兵。
	svc, _ := newEndpointTestHarness(t)
	adapter := NewAIEndpointSelectorAdapter(svc)
	_, _, _, _, err := adapter.Select("anthropic", "", "missing-row", "")
	if !errors.Is(err, inboundhttp.ErrAIEndpointNotAvailable) {
		t.Fatalf("explicit miss err = %v, want ErrAIEndpointNotAvailable", err)
	}
	// 空自动池 → 回落内置行(不报错,五返回值还原 builtin 行):这是
	// 「空候选 + builtin 健康」的降级主路径,顺带钉死。
	id, baseURL, _, builtin, err := adapter.Select("openai", "", "", "")
	if err != nil {
		t.Fatalf("builtin fallback err = %v", err)
	}
	if id != storage.BuiltinEndpointID || baseURL != "" || !builtin {
		t.Fatalf("fallback = (%q %q %v)", id, baseURL, builtin)
	}
}

func TestAIEndpointSelectorAdapterNilServiceYieldsNil(t *testing.T) {
	if got := NewAIEndpointSelectorAdapter(nil); got != nil {
		t.Fatalf("adapter for nil service = %v, want nil (degrade-safe)", got)
	}
}

// 编译期哨兵可见性:storage 侧确保测试 harness 存在(防御性 import 检查)。
var _ = storage.BuiltinEndpointID
