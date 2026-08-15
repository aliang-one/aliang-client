//go:build integration

package test

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	httpServer "aliang.one/nursorgate/app/http"
	"aliang.one/nursorgate/cmd"
	"aliang.one/nursorgate/common/logger"
	authuser "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/runtime"
)

// TestHTTPProxyWithConfig starts HTTP proxy with specified config file
// 使用示例: go test -v -run TestHTTPProxyWithConfig -config=./config.test.json
func TestHTTPProxyWithConfig(t *testing.T) {
	// Get config path from environment variable or flag
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "./config.test.json"
	}

	logger.Info(strings.Repeat("=", 70))
	logger.Info("HTTP Proxy Server Test")
	logger.Info(strings.Repeat("=", 70))
	logger.Info("")
	logger.Info(fmt.Sprintf("Configuration file: %s", configPath))
	logger.Info("")

	// Load configuration if file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		logger.Warn(fmt.Sprintf("Config file not found: %s, using minimal defaults", configPath))
	} else {
		logger.Info(fmt.Sprintf("Loading configuration from: %s", configPath))
		if err := cmd.LoadAndApplyConfig(configPath); err != nil {
			logger.Error(fmt.Sprintf("Failed to load config: %v", err))
			t.Fatalf("Failed to load config: %v", err)
		}
	}

	// Try to load local user info
	logger.Info("")
	logger.Info(strings.Repeat("-", 70))
	logger.Info("Checking local user info...")
	logger.Info(strings.Repeat("-", 70))
	userInfoPath, err := authuser.GetAuthSessionDBPath()
	if err != nil {
		logger.Warn(fmt.Sprintf("Failed to get user info db path: %v", err))
	} else {
		logger.Debug(fmt.Sprintf("User info db path: %s", userInfoPath))
	}

	hasPersisted, checkErr := authuser.HasPersistedUserInfo()
	if checkErr != nil {
		logger.Warn(fmt.Sprintf("Failed to inspect local user info: %v", checkErr))
		hasPersisted = false
	}

	if !hasPersisted {
		logger.Warn(fmt.Sprintf("No local user info found in: %s", userInfoPath))
		logger.Info("You can activate a token later via HTTP API or use --token flag")
	} else {
		runtime.GetStartupState().SetStatus(runtime.READY)
		logger.Info(fmt.Sprintf("Found local user info in: %s", userInfoPath))
		userInfo, loadErr := authuser.LoadUserInfo()
		if loadErr != nil {
			logger.Error(fmt.Sprintf("Failed to load user info: %v", loadErr))
			logger.Info("The file may be corrupted or from an incompatible version")
		} else {
			logger.Info("✓ User info loaded successfully!")
			logger.Info("")
			logger.Info("User Details:")
			logger.Info(fmt.Sprintf("  Username:     %s", userInfo.Username))
			logger.Info(fmt.Sprintf("  Email:        %s", userInfo.Email))
			logger.Info(fmt.Sprintf("  Role:         %s", userInfo.Role))
			logger.Info(fmt.Sprintf("  Status:       %s", userInfo.Status))
			logger.Info(fmt.Sprintf("  Balance:      %.2f", userInfo.Balance))
			logger.Info(fmt.Sprintf("  Concurrency:  %d", userInfo.Concurrency))
			logger.Info(fmt.Sprintf("  Created At:   %s", userInfo.CreatedAt))

			// Token info
			if userInfo.AccessToken != "" {
				logger.Info("  Access Token: ✓ Available")
			} else {
				logger.Warn("  Access Token: ✗ Not available")
			}
			if userInfo.RefreshToken != "" {
				logger.Info("  Refresh Token: ✓ Available")
			} else {
				logger.Warn("  Refresh Token: ✗ Not available")
			}

			logger.Info("")
			logger.Info("Last Updated: " + userInfo.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
	}
	logger.Info(strings.Repeat("-", 70))
	logger.Info("")

	logger.Info("")
	logger.Info(strings.Repeat("=", 70))
	logger.Info("HTTP Proxy Server listening on: http://127.0.0.1:56432")
	logger.Info(strings.Repeat("=", 70))
	logger.Info("")
	logger.Info("Test Commands:")
	logger.Info("")
	logger.Info("1. Test HTTP CONNECT (HTTPS tunneling):")
	logger.Info("   curl -x http://127.0.0.1:56432 https://www.google.com")
	logger.Info("")
	logger.Info("2. Test with verbose output:")
	logger.Info("   curl -x http://127.0.0.1:56432 -v https://www.example.com")
	logger.Info("")
	logger.Info("3. Test HTTP transparent proxy:")
	logger.Info("   curl -x http://127.0.0.1:56432 http://www.example.com")
	logger.Info("")
	logger.Info("4. Test with specific host:")
	logger.Info("   curl -x http://127.0.0.1:56432 https://www.github.com")
	logger.Info("")
	logger.Info(strings.Repeat("=", 70))
	logger.Info("Press Ctrl+C to stop the server")
	logger.Info(strings.Repeat("=", 70))
	logger.Info("")

	// Start HTTP proxy server in goroutine
	go func() {
		httpServer.StartHttpServer()
	}()

	// Wait a moment for server to start
	// time.Sleep(1 * time.Second)
	logger.Info("Server started successfully!")
	logger.Info("")

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Block until we receive a signal
	sig := <-sigChan
	logger.Info("")
	logger.Info(fmt.Sprintf("Received signal: %v", sig))
	logger.Info("Shutting down HTTP proxy server...")
	logger.Info("Test completed!")
}

// TestHTTPProxyDefault starts HTTP proxy with default config
func TestHTTPProxyDefault(t *testing.T) {
	logger.Info(strings.Repeat("=", 70))
	logger.Info("HTTP Proxy Server Test (Default Configuration)")
	logger.Info(strings.Repeat("=", 70))
	logger.Info("")

	// Try to load local user info
	logger.Info(strings.Repeat("-", 70))
	logger.Info("Checking local user info...")
	logger.Info(strings.Repeat("-", 70))
	userInfoPath, err := authuser.GetAuthSessionDBPath()
	if err != nil {
		logger.Warn(fmt.Sprintf("Failed to get user info db path: %v", err))
	} else {
		logger.Debug(fmt.Sprintf("User info db path: %s", userInfoPath))
	}

	hasPersisted, checkErr := authuser.HasPersistedUserInfo()
	if checkErr != nil {
		logger.Warn(fmt.Sprintf("Failed to inspect local user info: %v", checkErr))
		hasPersisted = false
	}

	if !hasPersisted {
		logger.Warn(fmt.Sprintf("No local user info found in: %s", userInfoPath))
		logger.Info("You can activate a token later via HTTP API or use --token flag")
	} else {
		logger.Info(fmt.Sprintf("Found local user info in: %s", userInfoPath))
		userInfo, loadErr := authuser.LoadUserInfo()
		if loadErr != nil {
			logger.Error(fmt.Sprintf("Failed to load user info: %v", loadErr))
			logger.Info("The file may be corrupted or from an incompatible version")
		} else {
			logger.Info("✓ User info loaded successfully!")
			logger.Info("")
			logger.Info("User Details:")
			logger.Info(fmt.Sprintf("  Username:     %s", userInfo.Username))
			logger.Info(fmt.Sprintf("  Email:        %s", userInfo.Email))
			logger.Info(fmt.Sprintf("  Role:         %s", userInfo.Role))
			logger.Info(fmt.Sprintf("  Status:       %s", userInfo.Status))
			logger.Info(fmt.Sprintf("  Balance:      %.2f", userInfo.Balance))
			logger.Info(fmt.Sprintf("  Concurrency:  %d", userInfo.Concurrency))
			logger.Info(fmt.Sprintf("  Created At:   %s", userInfo.CreatedAt))

			// Token info
			if userInfo.AccessToken != "" {
				logger.Info("  Access Token: ✓ Available")
			} else {
				logger.Warn("  Access Token: ✗ Not available")
			}
			if userInfo.RefreshToken != "" {
				logger.Info("  Refresh Token: ✓ Available")
			} else {
				logger.Warn("  Refresh Token: ✗ Not available")
			}

			logger.Info("")
			logger.Info("Last Updated: " + userInfo.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
	}
	logger.Info(strings.Repeat("-", 70))
	logger.Info("")

	logger.Info("HTTP Proxy Server listening on: http://127.0.0.1:56432")
	logger.Info("")
	logger.Info("Test Commands:")
	logger.Info("  curl -x http://127.0.0.1:56432 https://www.example.com")
	logger.Info("")
	logger.Info("Press Ctrl+C to stop the server")
	logger.Info(strings.Repeat("=", 70))
	logger.Info("")

	// Start HTTP proxy server
	go func() {
		httpServer.StartHttpServer()
	}()

	logger.Info("Server started successfully!")
	logger.Info("")

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Block until we receive a signal
	sig := <-sigChan
	logger.Info("")
	logger.Info(fmt.Sprintf("Received signal: %v", sig))
	logger.Info("Shutting down...")
}
