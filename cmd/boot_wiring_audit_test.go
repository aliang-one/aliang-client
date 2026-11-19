package cmd

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// boot 接线审计(接线孤儿治理):源码级断言 mesh 装配族三函数在两个 boot
// 形态(start.go 网关直跑/core.go daemon)各接线一次。运行时看不见没被
// 调用的代码,「本体在、接线断」族(start.go 漏接线=第一例、utun7 吞
// 广播=第二例、StartMeshRosterSync 零调用方=第三例)只能在构建期拦。
// 手法与 route_parity_test.go 同款:剥注释行后精确计数。
//
// 维护契约:新增 boot 形态或装配族函数时必须扩本测试的 bootFiles/symbols
// 表,先登记再提交——接线孤儿族(start.go 漏接线/utun7 吞广播/
// StartMeshRosterSync 零调用方)靠此表拦截。
func TestBootMeshWiringAudit(t *testing.T) {
	bootFiles := []string{"start.go", "core.go"}
	symbols := []string{
		"tunnel.StartMeshLANAssembly(",
		"tunnel.WireMeshAutoPeerProbe(",
		"services.StartMeshRosterSync(",
	}
	// 叉积遍历(评审 Minor 2):新装配函数只改 symbols 一处即自动覆盖全部
	// boot 形态,结构性防止「加了 start.go 忘了 core.go」的静默缩窄。
	var problems []string
	for _, file := range bootFiles {
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
	// 累积式全报(评审 Minor 1):两 boot 文件同丢符号时一次全暴露。
	if len(problems) > 0 {
		t.Fatalf("boot 接线审计失败(与 route_parity 同款一次性全报):\n  %s", strings.Join(problems, "\n  "))
	}
}
