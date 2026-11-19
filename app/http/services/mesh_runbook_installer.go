package services

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"aliang.one/nursorgate/app"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

// mesh runbook 自愈安装(D-9):内嵌 SKILL.md → ~/.claude/skills/mesh-runbook/。
//
// 对齐 peer MCP 托管注册先例(agent_peer_mcp.go)的 managed 纪律:
//   - managed 范围:只碰本技能名下 mesh-runbook 目录这一个路径,绝不列举、
//     绝不触碰用户自装技能(~/.claude/skills 下其余条目本安装器连读都不读);
//   - 内容寻址:目标已存在且字节相等 → 幂等跳写(不刷 mtime);内容变了 →
//     同目录 tmp + rename 整体替换(无半写窗口);
//   - 尊重 skills_enabled 总闸:关 = 跳过,诚实日志一条(不偷偷装);
//   - 失败只返回结果/记日志,绝不 panic、绝不阻断 boot。
//
// 装好下一 run 即生效(agentcore 技能注入每 run 现读磁盘清单),无需内核
// 重启。归属用户技能域,非 mesh 装配族(boot 接线审计 bootFiles 只含
// start.go/core.go,本文件不在其 scanning 范围,刻意不扩表)。
const meshRunbookSkillDirName = "mesh-runbook"

// meshRunbookHome 与 peerMCPHome 同款的测试注入点:生产走 agentHome()。
var meshRunbookHome = agentHome

// MeshRunbookInstallResult 安装结果(诚实回显,启动位只按此记日志)。
type MeshRunbookInstallResult struct {
	// Installed true=本次新写或替换落盘;false=幂等跳过(内容未变)。
	Installed bool
	// Skipped 非空 = 本次跳过原因("agentcore skills disabled"/"agent home unavailable")。
	Skipped string
	// Error 非空 = 写失败原因(只 Warn 不阻断)。
	Error string
}

// MeshRunbookSkillContent 取 runbook SKILL.md 源真值字节(embed,二进制自带)。
func MeshRunbookSkillContent() ([]byte, error) {
	return app.MeshRunbookSKILL.ReadFile("assets/skills/mesh-runbook/SKILL.md")
}

// EnsureMeshRunbookInstalled 启动自愈(runtime/agent 56433 进程启动位专用):
// 按 embed 源真值对齐 ~/.claude/skills/mesh-runbook/SKILL.md。结果只由调用方
// 记日志,本函数自身不抛、不阻断。
func EnsureMeshRunbookInstalled() MeshRunbookInstallResult {
	content, err := MeshRunbookSkillContent()
	if err != nil {
		res := MeshRunbookInstallResult{Error: fmt.Sprintf("read embedded SKILL.md: %v", err)}
		logger.Warn("mesh-runbook: install failed err=" + res.Error)
		return res
	}
	return ensureMeshRunbookInstalledFor(meshRunbookHome(), content)
}

// ensureMeshRunbookInstalledFor 安装本体(home/content 注入缝,测试直测)。
func ensureMeshRunbookInstalledFor(home string, content []byte) MeshRunbookInstallResult {
	// 总闸先行:skills_enabled=false → 跳过,诚实日志一条(与技能注入同源
	// 三态:nil/缺块=默认开)。关闸期间绝不落盘,开闸后下次安装调用即补齐
	// (每 run 现读磁盘,进程周期语义)。
	if !config.GetGlobalConfig().EffectiveAgentCoreSkillsEnabled() {
		logger.Info("mesh-runbook: install skipped reason=agentcore skills disabled")
		return MeshRunbookInstallResult{Skipped: "agentcore skills disabled"}
	}
	if home == "" {
		// home 缺失时绝不落盘(filepath.Join("",...) 会写进进程 CWD,同
		// peer-mcp 家目录纪律)。
		logger.Info("mesh-runbook: install skipped reason=agent home unavailable")
		return MeshRunbookInstallResult{Skipped: "agent home unavailable"}
	}

	dir := filepath.Join(home, ".claude", "skills", meshRunbookSkillDirName)
	target := filepath.Join(dir, "SKILL.md")
	// 内容寻址幂等:目标已存在且字节相等 → 零写入(不刷 mtime,现场可拿
	// mtime 当「上次变更时间」读)。
	if cur, err := os.ReadFile(target); err == nil && bytes.Equal(cur, content) {
		return MeshRunbookInstallResult{}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		res := MeshRunbookInstallResult{Error: fmt.Sprintf("mkdir %s: %v", dir, err)}
		logger.Warn("mesh-runbook: install failed err=" + res.Error)
		return res
	}
	// 原子写:同目录 tmp + rename,目标永不为半份内容。
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		res := MeshRunbookInstallResult{Error: fmt.Sprintf("write %s: %v", tmp, err)}
		logger.Warn("mesh-runbook: install failed err=" + res.Error)
		return res
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		res := MeshRunbookInstallResult{Error: fmt.Sprintf("rename %s: %v", target, err)}
		logger.Warn("mesh-runbook: install failed err=" + res.Error)
		return res
	}
	logger.Info("mesh-runbook: installed path=" + target)
	return MeshRunbookInstallResult{Installed: true}
}
