package app

import (
	"embed"
)

//go:embed website/dist
var WebsiteFS embed.FS

// MeshRunbookSKILL 组网诊断 runbook 技能源真值(mesh-runbook 自愈安装 D-9):
// 单文件 embed 二进制自带,现场 ~/.claude/skills/mesh-runbook/SKILL.md 只是
// 派生物(内容寻址对齐,变了才重写)。唯一消费方
// services.EnsureMeshRunbookInstalled。
//
//go:embed assets/skills/mesh-runbook/SKILL.md
var MeshRunbookSKILL embed.FS
