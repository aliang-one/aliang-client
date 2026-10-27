package services

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
)

// testIsolatedUserAgentBaseURL 是测试进程里 user-agent 的默认目标地址。
//
// 选 127.0.0.1:1 是有意的：特权端口，本机不会有任何监听者，误发时立刻
// connection refused —— 既打不到开发者本机真实运行的 agent（默认 56433），
// 也不会让测试阻塞在超时上。
const testIsolatedUserAgentBaseURL = "http://127.0.0.1:1"

// runningUnderGoTest 判断当前进程是否为 go test 编译出的测试二进制。
//
// 2026-10-27 事故：测试包里“模拟登出”的用例通过全局 session 监听器，把一条
// 伪造的 logout 事件真实 POST 到了本机 127.0.0.1:56433，掐断了开发者正在使用
// 的桌面端 AI 任务（在途 run 被取消，手机端结算为 Approval denied）。
// 测试进程必须与真实 agent 隔离。
//
// 优先用 testing 包注册的 -test.v 标志判定（-o 改名也成立），再兜底看可执行
// 文件名（go test 产物形如 xxx.test / xxx.test.exe）。
func runningUnderGoTest() bool {
	if flag.Lookup("test.v") != nil {
		return true
	}
	base := strings.ToLower(filepath.Base(os.Args[0]))
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

// resolveLocalUserAgentBaseURL 返回 user-agent 的出站基地址。
//
// 测试进程默认指向隔离地址；用 httptest 桩并显式覆盖 localUserAgentBaseURL 的
// 测试不受影响，因为覆盖会整体替换这个函数值。拆成纯函数便于分支断言。
func resolveLocalUserAgentBaseURL(underTest bool) string {
	if underTest {
		return testIsolatedUserAgentBaseURL
	}
	return UserAgentBaseURL()
}
