//go:build !windows

package agentcore

import (
	"errors"
	"os/exec"
)

// ProcessGroupTree 是非 Windows 平台的空壳：组杀语义由 exec_unix.go 的
// Setpgid + kill(-pgid) 在 cmd.Cancel 内完成，无需独立句柄。存在它是为了让
// newToolCommandContext 在两平台返回统一签名（cmd, tree），调用方无需分叉。
type ProcessGroupTree struct{}

func NewProcessGroupTree() *ProcessGroupTree { return &ProcessGroupTree{} }

func (t *ProcessGroupTree) Attach(*exec.Cmd) error { return nil }

func (t *ProcessGroupTree) KillTree() error {
	return errors.New("process group tree is unix-callback based")
}

func (t *ProcessGroupTree) Release() {}

func (t *ProcessGroupTree) Ok() bool { return false }
