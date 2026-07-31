package test

import (
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/cmd"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/runtime"
)

func activateSessionForStartupTest(t *testing.T) {
	t.Helper()
	auth.ResetSessionAuthorityForTest().NotifyLoggedIn(&auth.UserInfo{ID: 1, Username: "startup-test"})
	runtime.GetStartupState().SetStatus(runtime.UNCONFIGURED)
	t.Cleanup(func() {
		auth.ResetSessionAuthorityForTest()
		runtime.ResetGlobalStartupStateForTest()
	})
}

// TestRunServiceWithDefaultConfig 测试当使用默认配置时，RunService.StartService() 返回错误
func TestRunServiceWithDefaultConfig(t *testing.T) {
	activateSessionForStartupTest(t)
	// 步骤1: 准备 - 创建 RunService 实例
	runService := services.NewRunService()
	t.Log("✓ Step 1: RunService instance created")

	// 步骤2: 模拟直接启动场景 - 标记为使用默认配置
	config.SetUsingDefaultConfig(true)
	t.Log("✓ Step 2: Marked as using default config")

	// 步骤3: 尝试调用 StartService()
	result := runService.StartService()
	t.Log("✓ Step 3: StartService() called")

	// 步骤4: 验证返回结果
	if result == nil {
		t.Fatal("Result should not be nil")
	}

	// 验证错误类型
	if errorType, ok := result["error"]; !ok {
		t.Fatal("Result should contain 'error' field")
	} else if errorType != "activation_required" {
		t.Fatalf("Error type should be 'activation_required', got '%v'", errorType)
	}
	t.Log("✓ Step 4: Error type verified - 'activation_required'")

	// 验证状态
	if status, ok := result["status"]; !ok {
		t.Fatal("Result should contain 'status' field")
	} else if status != "failed" {
		t.Fatalf("Status should be 'failed', got '%v'", status)
	}
	t.Log("✓ Step 5: Status verified - 'failed'")

	// 验证消息
	if msg, ok := result["msg"]; !ok {
		t.Fatal("Result should contain 'msg' field")
	} else if !strings.Contains(msg.(string), "登录或配置恢复") {
		t.Fatalf("Message mismatch: got '%v'", msg)
	}
	t.Log("✓ Step 6: Error message verified - login/session-restore guidance")

	// 打印完整响应
	t.Logf("\nFull API response when using default config:\n%+v\n", result)

	// 清理
	config.SetUsingDefaultConfig(false)
	t.Log("✓ Cleanup completed")
	t.Log("\n✓ API activation check test PASSED - Default config prevents proxy startup!")
}

// TestRunServiceWithoutDefaultConfig 测试当不使用默认配置时，StartService() 可以继续执行
func TestRunServiceWithoutDefaultConfig(t *testing.T) {
	activateSessionForStartupTest(t)
	// 步骤1: 确保不使用默认配置
	config.SetUsingDefaultConfig(false)
	t.Log("✓ Step 1: Confirmed not using default config")

	// 步骤2: 创建 RunService 实例
	runService := services.NewRunService()
	t.Log("✓ Step 2: RunService instance created")

	// 步骤3: 尝试调用 StartService()
	result := runService.StartService()
	t.Log("✓ Step 3: StartService() called")

	// 步骤4: 验证返回结果
	if result == nil {
		t.Fatal("Result should not be nil")
	}

	if errorType, ok := result["error"]; !ok || errorType != "activation_required" {
		t.Fatalf("Should get 'activation_required' error when not ready: %+v", result)
	}
	t.Log("✓ Step 4: 'activation_required' error verified (not ready)")

	t.Logf("\nAPI response when not using default config:\n%+v\n", result)
	t.Log("✓ Non-default config startup test PASSED!")
}

// TestCompleteStartupFlow 测试完整的启动流程
func TestCompleteStartupFlow(t *testing.T) {
	activateSessionForStartupTest(t)
	t.Log("=== Complete Startup Flow Test ===\n")

	// 场景 1: 直接启动（模拟 ./nursorgate-darwin-arm64）
	t.Log("Scenario 1: Direct binary launch (no parameters)")
	t.Log("-" + string([]byte{45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45}))

	// 步骤 1.1: 加载默认配置
	configBytes := cmd.GetDefaultConfigBytes()
	_, err := cmd.LoadConfigFromBytes(configBytes)
	if err != nil {
		t.Fatalf("Failed to load default config: %v", err)
	}
	t.Log("✓ 1.1: Default config loaded")

	// 步骤 1.2: 标记使用默认配置
	config.SetUsingDefaultConfig(true)
	t.Log("✓ 1.2: Marked as using default config")

	// 步骤 1.3: 验证 HTTP 服务初始化（仅验证 RunService）
	runService := services.NewRunService()
	t.Log("✓ 1.3: HTTP server services initialized")

	// 步骤 1.4: 用户尝试调用 /api/run/start
	result := runService.StartService()
	if errorType, ok := result["error"]; !ok || errorType != "activation_required" {
		t.Fatalf("Expected 'activation_required' error, got: %+v", result)
	}
	t.Log("✓ 1.4: /api/run/start returns 'activation_required' error")
	t.Logf("   Response: %+v\n", result)

	// 清理
	config.SetUsingDefaultConfig(false)

	// 场景 2: 使用配置文件启动（模拟 ./nursorgate-darwin-arm64 --config ./config.json）
	t.Log("Scenario 2: Launch with config file (--config parameter)")
	t.Log("-" + string([]byte{45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45}))

	// 步骤 2.1: 不标记默认配置
	if config.IsUsingDefaultConfig() {
		t.Fatal("Should not be using default config")
	}
	t.Log("✓ 2.1: Not using default config (real config provided)")

	// 步骤 2.2: 创建新的 RunService
	runService2 := services.NewRunService()
	t.Log("✓ 2.2: RunService instance created")

	// 步骤 2.3: 用户尝试调用 /api/run/start
	result2 := runService2.StartService()
	if errorType, ok := result2["error"]; !ok || errorType != "activation_required" {
		t.Fatalf("Expected 'activation_required' when not ready even with real config: %+v", result2)
	}
	t.Log("✓ 2.3: /api/run/start returns 'activation_required' when not ready")
	t.Logf("   Response: %+v\n", result2)

	t.Log("\n=== All Startup Scenarios Tested Successfully ===")
	t.Log("✓ Default config blocks proxy startup via API when not ready")
	t.Log("✓ Real config also requires ready/login state before startup")
}
