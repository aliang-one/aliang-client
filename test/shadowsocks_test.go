//go:build integration

package test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	httpServer "aliang.one/nursorgate/app/http"
	"aliang.one/nursorgate/cmd"
	"aliang.one/nursorgate/common/logger"
)

// TestShadowsocksViaHTTPProxy tests Shadowsocks proxy through HTTP CONNECT tunnel
// Load config from test/config.test.json and send curl request to google.com via Shadowsocks
// 使用示例: go test -v -run TestShadowsocksViaHTTPProxy ./test
func TestShadowsocksViaHTTPProxy(t *testing.T) {
	// Get config path
	configPath := "./config.test.json"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Try alternative path
		configPath = "test/config.test.json"
	}

	logger.Info("")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("SHADOWSOCKS PROXY TEST - via HTTP CONNECT")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("")
	logger.Info(fmt.Sprintf("Configuration file: %s", configPath))
	logger.Info("")

	// Load configuration
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		logger.Warn(fmt.Sprintf("Config file not found: %s", configPath))
		t.Fatalf("Config file not found: %s", configPath)
	} else {
		logger.Info(fmt.Sprintf("Loading configuration from: %s", configPath))
		if err := cmd.LoadAndApplyConfig(configPath); err != nil {
			logger.Error(fmt.Sprintf("Failed to load config: %v", err))
			t.Fatalf("Failed to load config: %v", err)
		}
		logger.Info("✓ Configuration loaded successfully")
	}

	logger.Info("")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("STARTING HTTP PROXY SERVER")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("")

	// Start HTTP proxy server in goroutine
	serverReady := make(chan bool, 1)
	go func() {
		serverReady <- true
		httpServer.StartHttpServer()

	}()

	// Wait for server to start
	<-serverReady
	time.Sleep(2 * time.Second)

	logger.Info("✓ HTTP Proxy Server is listening on: http://127.0.0.1:56432")
	logger.Info("✓ HTTP Proxy Server is listening on: http://127.0.0.1:56431 (management)")
	logger.Info("")

	logger.Info(strings.Repeat("=", 80))
	logger.Info("TEST 1: CURL REQUEST via Shadowsocks (HTTP CONNECT)")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("")
	logger.Info("Testing: curl -x http://127.0.0.1:56432 https://www.google.com")
	logger.Info("")

	// Run curl command with HTTP proxy pointing to our server
	cmd := exec.Command("curl",
		"-x", "http://127.0.0.1:56432",
		"-v",
		"-m", "10", // 10 second timeout
		"https://www.google.com")

	logger.Info("Executing curl command...")
	logger.Info("")

	// Capture output
	output, err := cmd.CombinedOutput()

	logger.Info("")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("CURL OUTPUT")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("")
	logger.Info(string(output))
	logger.Info("")

	if err != nil {
		logger.Warn(fmt.Sprintf("Curl command failed (expected for this test): %v", err))
	}

	logger.Info("")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("TEST 2: CURL REQUEST with HEAD method (lighter)")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("")
	logger.Info("Testing: curl -x http://127.0.0.1:56432 -I https://www.google.com")
	logger.Info("")

	// Run HEAD request
	cmd2 := exec.Command("curl",
		"-x", "http://127.0.0.1:56432",
		"-I", // HEAD request
		"-v",
		"-m", "10",
		"https://www.google.com")

	logger.Info("Executing curl HEAD request...")
	logger.Info("")

	output2, err2 := cmd2.CombinedOutput()

	logger.Info("")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("CURL HEAD OUTPUT")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("")
	logger.Info(string(output2))
	logger.Info("")

	if err2 != nil {
		logger.Warn(fmt.Sprintf("Curl command failed: %v", err2))
	}

	logger.Info("")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("TEST COMPLETED")
	logger.Info(strings.Repeat("=", 80))
	logger.Info("")
	logger.Info("Check the DEBUG logs above for:")
	logger.Info("  - [DEBUG] [Shadowsocks] DEBUG - metadata.DstIP: ...")
	logger.Info("  - [DEBUG] [Shadowsocks] DEBUG - socksAddr bytes: ...")
	logger.Info("  - [DEBUG] [Shadowsocks] DEBUG - Write result: ...")
	logger.Info("")
	logger.Info("These logs will help diagnose the connection issue.")
	logger.Info("")

	// Give it a moment to finish
	time.Sleep(1 * time.Second)
}
