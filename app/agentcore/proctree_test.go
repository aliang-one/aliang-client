package agentcore

import (
	"os/exec"
	"testing"
)

// 编译期断言：两平台 ProcessGroupTree 方法面必须一致（本文件 build-tag-free，
// 两平台都编译）。所列方法面（Attach/KillTree/Release/Ok）在任一侧被删除或
// 改签名，这里即时先红——不会拖到 GOOS 切换的编译现场才炸；单侧**新增**方法
// 不在断言覆盖内（接口列表需同步扩充才钉得住）。
var _ interface {
	Attach(*exec.Cmd) error
	KillTree() error
	Release()
	Ok() bool
} = &ProcessGroupTree{}

// TestProcessGroupTreeMethodSurface 是占位用例：断言本体是上面的 var _（纯
// 编译期检查，无运行期行为）。保留具名入口让 -run 可寻址、也让本文件在
// `go test` 视角下不是空文件。
func TestProcessGroupTreeMethodSurface(t *testing.T) {}
