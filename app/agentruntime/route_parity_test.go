package agentruntime

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 双路由表一致性护栏：同一组 /api/agent/*（含 /mcp/aliang/）端点必须同时注册
// 在 core 侧 routes.go（56431）与 user-agent 侧 server.go registerAgentRoutes
// （56433）。漏一边=请求被 core 代理到 56433 后吃 mux 裸 404 并透传回去，本仓
// 已三次踩坑：2026-10-21 数据面 404、2026-10-23 运行状态 404、2026-10-29
// auth-recover 自愈链 404。测试直接从两份源码抽取注册清单做集合比对——
// routes.go 的 register 与 server.go 的 mux.HandleFunc 都无导出的路由表，
// 源码级比对是最轻且不会腐化的方案。
//
// 刻意单边的端点必须在下方例外表登记理由；新增单边端点先登记再提交。
var routeParityExceptions = map[string]string{
	// 仅 56433：调用方 session_event_agent.go 直连 user-agent 进程，
	// 无仪表盘/core 路径依赖。
	"/api/agent/session-event": "user-agent 专用，core 不代理",
}

func TestAgentRouteParityBetweenCoreAndUserAgent(t *testing.T) {
	coreSrc, err := os.ReadFile(filepath.Join("..", "http", "routes", "routes.go"))
	if err != nil {
		t.Fatalf("read routes.go: %v", err)
	}
	agentSrc, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}

	core := extractAgentRoutePaths(t, string(coreSrc),
		regexp.MustCompile(`register\("(?:[A-Za-z]+ )?(/(?:api/agent|mcp/aliang)[^"]*)"`))
	agent := extractAgentRoutePaths(t, string(agentSrc),
		regexp.MustCompile(`mux\.HandleFunc\("(/(?:api/agent|mcp/aliang)[^"]*)"`))

	var drifted []string
	for path := range core {
		if _, ok := routeParityExceptions[path]; ok {
			continue
		}
		if _, ok := agent[path]; !ok {
			drifted = append(drifted, path+" 仅注册在 core(routes.go)，56433 registerAgentRoutes 缺失")
		}
	}
	for path := range agent {
		if _, ok := routeParityExceptions[path]; ok {
			continue
		}
		if _, ok := core[path]; !ok {
			drifted = append(drifted, path+" 仅注册在 user-agent(server.go)，core routes.go 缺失")
		}
	}
	if len(drifted) > 0 {
		sort.Strings(drifted)
		t.Fatalf("双路由表漂移（漏注册=404 透传，2026-10-21/10-23/10-29 三次事故同族）：\n  %s\n修复：把缺的一边补上，或在 routeParityExceptions 登记刻意单边理由。",
			strings.Join(drifted, "\n  "))
	}
}

func extractAgentRoutePaths(t *testing.T, src string, re *regexp.Regexp) map[string]struct{} {
	t.Helper()
	paths := map[string]struct{}{}
	// 剥掉纯注释行：注释里引用的注册样例（文档/事故记录常带
	// mux.HandleFunc("/api/agent/…"）会被误计为已注册，掩盖真实漂移。
	var code strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteByte('\n')
	}
	for _, m := range re.FindAllStringSubmatch(code.String(), -1) {
		paths[m[1]] = struct{}{}
	}
	if len(paths) == 0 {
		t.Fatal("从源码抽取到 0 个 agent 路由——注册写法变了，先修正则再下结论")
	}
	return paths
}
