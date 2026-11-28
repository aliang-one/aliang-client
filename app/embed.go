package app

import (
	"embed"
)

// ⚠️ 必须用 all: 前缀(2026-11-28 托盘面板白屏定谳):go:embed 目录形态
// 默认【排除 _/. 开头的文件】,而 Vite 的 Vue 共享分包恰好命名为
// _plugin-vue_export-helper-*.js——被静默剔除出嵌入 FS,页面 JS 首行
// import 404 → Vue 挂不上 → 仪表盘/面板双双白屏,服务端无任何报错
// (HTML/入口 JS 都是干净的 200,只有深链资产 404)。all: 连 underscore
// 文件一起嵌入,封死这一类回归;app/embed_assets_test.go 兜底扫描。
//
//go:embed all:website/dist
var WebsiteFS embed.FS

// MeshRunbookSKILL 组网诊断 runbook 技能源真值(mesh-runbook 自愈安装 D-9):
// 单文件 embed 二进制自带,现场 ~/.claude/skills/mesh-runbook/SKILL.md 只是
// 派生物(内容寻址对齐,变了才重写)。唯一消费方
// services.EnsureMeshRunbookInstalled。
//
//go:embed assets/skills/mesh-runbook/SKILL.md
var MeshRunbookSKILL embed.FS
