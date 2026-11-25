package cmd

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// auditWiringExactlyOnce 接线审计原语:file×symbol 叉积,剥注释行后精确计数,
// 每格恰 1 次(0=接线孤儿,>1=重复接线)。累积式全报(评审 Minor 1):多文件
// 同丢符号时一次全暴露。手法与 route_parity_test.go 同款。
func auditWiringExactlyOnce(t *testing.T, files, symbols []string) {
	t.Helper()
	var problems []string
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			problems = append(problems, fmt.Sprintf("read %s: %v", file, err))
			continue
		}
		for _, symbol := range symbols {
			n := 0
			for _, line := range strings.Split(string(raw), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				if strings.Contains(trimmed, symbol) {
					n++
				}
			}
			if n != 1 {
				problems = append(problems, fmt.Sprintf("%s 中 %s 出现 %d 次(应恰 1 次;0=接线孤儿,>1=重复接线)", file, symbol, n))
			}
		}
	}
	if len(problems) > 0 {
		t.Fatalf("boot 接线审计失败(与 route_parity 同款一次性全报):\n  %s", strings.Join(problems, "\n  "))
	}
}

// boot 接线审计(接线孤儿治理):源码级断言 mesh 装配族三函数在两个 boot
// 形态(start.go 网关直跑/core.go daemon)各接线一次。运行时看不见没被
// 调用的代码,「本体在、接线断」族(start.go 漏接线=第一例、utun7 吞
// 广播=第二例、StartMeshRosterSync 零调用方=第三例)只能在构建期拦。
//
// 维护契约:新增 boot 形态或装配族函数时必须扩本测试的表,先登记再提交
// ——接线孤儿族靠此表拦截。
func TestBootMeshWiringAudit(t *testing.T) {
	auditWiringExactlyOnce(t,
		[]string{"start.go", "core.go"},
		[]string{
			"tunnel.StartMeshLANAssembly(",
			"tunnel.WireMeshAutoPeerProbe(",
			"services.StartMeshRosterSync(",
			// 出口委托缝(本机节点标识/egress peer 源/TUN DNS 代转发源):
			// 2026-11-24 审计实证 start 形态漏接——liang-dev 这类「aliang --host
			// + 独立 agent」部署里,该进程永远不是 WS 属主,validateConfig 的另一
			// 接线点永不运行,四条缝「永远不接线」:开平台流量代理后平台请求逐
			// 条 fail-loud、AI 流静默留本机,重启后落 SoftExpired、入口代理暂停。
			".WireMeshAssemblySeams(services.GetSharedAgentService().CurrentDeviceID)",
		})
}

// listener 镜像/watcher 接线审计(事件监听器批次 Task 10,specs/005 §2/FR-3):
// 归属 **WS 持有进程 = user-agent runtime**(app/agentruntime/server.go
// StartLocalServer)——cmd 侧 start.go/core.go 刻意不接:其 EnsureRemoteConnection
// 触发是跨进程派发,分裂身份/共享 home 拓扑下双进程共写 listener-tails offset
// 文件会互相踩进度丢事件(评审 C1 定案)。同表同款 exactly-once 拦截:
// server.go 漏接线 = 镜像永不全量重建/watcher 永不启动(0.196 接线孤儿同族)。
// 路径相对 cmd/(go test 工作目录=包目录,与上方 start.go/core.go 同解析方式)。
func TestListenerWiringAudit(t *testing.T) {
	auditWiringExactlyOnce(t,
		[]string{"../app/agentruntime/server.go"},
		[]string{
			"services.LoadListenerMirror(",
			"services.StartEventListenerWatchers(",
		})
	// 顺序契约(先镜像后 watcher,1b8c36b 教训):exactly-once 只拦存在性,
	// 此处补行序——Load 的出现位置必须早于 Start(Task 11 评审加固)。
	raw, err := os.ReadFile("../app/agentruntime/server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	loadIdx, startIdx := listenerSymbolOrder(string(raw), "services.LoadListenerMirror(", "services.StartEventListenerWatchers(")
	if loadIdx < 0 || startIdx < 0 || loadIdx > startIdx {
		t.Fatalf("boot 顺序破损:LoadListenerMirror 出现位 %d 须早于 StartEventListenerWatchers 出现位 %d", loadIdx, startIdx)
	}
}

// listenerSymbolOrder 剥注释行后返回两符号在源中的首现字节位(缺者 -1)。
func listenerSymbolOrder(src, first, second string) (int, int) {
	var code strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteByte('\n')
	}
	return strings.Index(code.String(), first), strings.Index(code.String(), second)
}
