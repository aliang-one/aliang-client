package services

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// agentGitStatusTimeout bounds `git status` so a slow/hung repo can't stall a
// directory listing request.
const agentGitStatusTimeout = 5 * time.Second

// loadGitStatusMap runs `git status --porcelain` rooted at targetPath and
// returns map[absolutePath]→'added'|'modified'|'deleted' for changed files
// UNDER targetPath. Keys are filepath.Join(targetPath, rel) — the same space the
// listing's itemPath uses — so lookups match regardless of symlink resolution.
// Non-git dir / git missing / any error → empty map (everything reads 'clean').
func loadGitStatusMap(targetPath string) map[string]string {
	out := make(map[string]string)
	porcelain, err := agentRunGit(targetPath, "status", "--porcelain=v1", "-unormal")
	if err != nil {
		return out
	}
	parentPrefix := ".." + string(filepath.Separator)
	for _, line := range strings.Split(porcelain, "\n") {
		if len(line) < 4 {
			continue
		}
		code := line[:2]
		rel := agentUnquoteGitPath(strings.TrimSpace(line[3:]))
		if rel == "" || strings.HasPrefix(rel, parentPrefix) {
			continue // skip changes outside targetPath's subtree
		}
		out[filepath.Join(targetPath, rel)] = classifyGitStatusCode(code)
	}
	return out
}

// classifyGitStatusCode maps a 2-char `git status --porcelain` code (XY: index +
// worktree status) onto the phone's status vocabulary.
//   - '??' → added (untracked)
//   - A → added
//   - D → deleted
//   - M/R/C → modified
//   - any other change indicator → modified
func classifyGitStatusCode(code string) string {
	if len(code) < 2 {
		return "modified"
	}
	x, y := code[0], code[1]
	if x == '?' && y == '?' {
		return "added"
	}
	if x == 'A' || y == 'A' {
		return "added"
	}
	if x == 'D' || y == 'D' {
		return "deleted"
	}
	if x == 'M' || y == 'M' || x == 'R' || y == 'R' || x == 'C' || y == 'C' {
		return "modified"
	}
	return "modified"
}

// gitDirectoryStatus returns 'modified' if any changed file lives under dirAbs
// (at any depth), else 'clean'. Drives the "folder glows if it contains changes"
// aggregation in the file browser.
// agentDirectoryGitStatus 是目录行状态的总入口:porcelain 的未跟踪目录
// `?? dir/` 经 filepath.Join 清洗后键不带尾斜杠,gitDirectoryStatus 的
// 前缀聚合匹配不到它——先查目录自身键(直接命中 = added/modified),查不到
// 再退回「目录下有改动文件」聚合。2026-10-28 审计 #3 配套。
func agentDirectoryGitStatus(dirAbs string, changes map[string]string) string {
	if s, ok := changes[dirAbs]; ok && s != "clean" {
		return s
	}
	return gitDirectoryStatus(dirAbs, changes)
}

func gitDirectoryStatus(dirAbs string, changes map[string]string) string {
	prefix := strings.TrimSuffix(dirAbs, string(filepath.Separator)) + string(filepath.Separator)
	for p := range changes {
		if strings.HasPrefix(p, prefix) {
			return "modified"
		}
	}
	return "clean"
}

func agentRunGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentGitStatusTimeout)
	defer cancel()
	// -c core.quotepath=false:porcelain 对非 ASCII 路径默认输出八进制转义
	// ("src/\346\226\207..."),与 ReadDir 的真实 UTF-8 名对不上 → 中文文件/
	// 目录的 M/A 徽章与状态过滤全丢(2026-10-28 审计 #3)。关掉引号转义后
	// 输出即原始 UTF-8;agentUnquoteGitPath 只兜重命名引号形态。
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.quotepath=false"}, args...)...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// Discard stderr so a non-git dir doesn't spam the agent log.
	cmd.Stderr = nil
	// NOTE: run git FIRST, then read the buffer — Go evaluates return args
	// left-to-right, so `return stdout.String(), cmd.Run()` would read the
	// (still-empty) buffer before git actually ran.
	err := cmd.Run()
	return stdout.String(), err
}

// agentUnquoteGitPath strips the surrounding quotes git adds to paths with
// special chars and takes the destination side of a rename ("old -> new").
// (Porcelain octal-escapes non-ASCII bytes; full unescaping is a refinement.)
func agentUnquoteGitPath(raw string) string {
	s := raw
	if i := strings.Index(s, " -> "); i >= 0 {
		s = s[i+4:]
	}
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return s
}
