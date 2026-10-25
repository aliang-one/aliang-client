package services

// 互传接收链的 zip 解压（D10）。全仓唯一的既有 extractZipArchive 是
// //go:build windows，darwin/linux 编译不到，且自身有四处缺陷（RemoveAll
// 目标目录、不拒符号链接条目、照抄归档 mode、io.Copy 无上限）——必须重写。
//
// 防护矩阵：
//   - 路径穿越：拒绝 `..` 段、绝对路径（含 Windows 盘符形态 `C:/`）、反斜杠、
//     符号链接条目；Clean 后做前缀围栏（照旧实现的前缀检查手法）。
//   - zip 炸弹：条目数 / 路径深度 / 单条目解压量 / 解压总量四道绝对上限，
//     另设 累计解压÷zip体积 比值闸（默认放宽到 64×，源码目录 deflate 常见
//     4-6×，不再误杀；对高可压内容仍兜底）。
//   - 权限：一律 0755/0644，绝不采信归档 mode（防 setuid/可执行位投毒）。

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	agentTransferZipMaxEntries     = 20000
	agentTransferZipMaxDepth       = 32
	agentTransferZipMaxEntryBytes  = int64(2) << 30 // 单条目 2GiB
	agentTransferZipMaxTotalBytes  = int64(8) << 30 // 总量 8GiB
	agentTransferZipRatioLimit     = 64
)

var agentTransferZipDrivePrefix = regexp.MustCompile(`^[A-Za-z]:`)

// errAgentTransferZipUnsafe 统一的「条目不安全」错误；调用方按
// fence_escape 上报。
var errAgentTransferZipUnsafe = errors.New("zip entry rejected by safety fence")

// extractAgentTransferZip 把 archivePath 解到 destination（必须已存在且为
// 目录，由调用方做重名规避）。任何不安全条目整体失败并返回错误——不做
// 部分解压的「尽力而为」，半拉子目录比失败更难清理。
func extractAgentTransferZip(ctx context.Context, archivePath, destination string) error {
	info, err := os.Stat(archivePath)
	if err != nil {
		return err
	}
	archiveSize := info.Size()
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open archive failed: %w", err)
	}
	defer reader.Close()

	destRoot := filepath.Clean(destination)
	destPrefix := destRoot + string(os.PathSeparator)
	var total int64
	entries := 0
	for _, f := range reader.File {
		entries++
		if ctx.Err() != nil {
			// 取消感知：几 GB 的解压不接 ctx 会在用户取消后照跑到底
			// （P2 复核项）。半成品由调用方 extractAgentTransferArchive 清理。
			return ctx.Err()
		}
		if entries > agentTransferZipMaxEntries {
			return fmt.Errorf("%w: too many entries (> %d)", errAgentTransferZipUnsafe, agentTransferZipMaxEntries)
		}
		targetPath, err := agentTransferZipEntryPath(destRoot, destPrefix, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		n, err := copyAgentTransferZipEntry(targetPath, f)
		if err != nil {
			return err
		}
		total += n
		if total > agentTransferZipMaxTotalBytes {
			return fmt.Errorf("%w: extracted total exceeds %d bytes", errAgentTransferZipUnsafe, agentTransferZipMaxTotalBytes)
		}
		if archiveSize > 0 && total > archiveSize*agentTransferZipRatioLimit {
			return fmt.Errorf("%w: extracted/size ratio exceeds %dx", errAgentTransferZipUnsafe, agentTransferZipRatioLimit)
		}
	}
	if entries == 0 {
		return errors.New("archive contains no entries")
	}
	return nil
}

// agentTransferZipEntryPath 校验单条目名并给出目标路径。穿越类一律拒绝，
// 不做静默改名（改名会把穿越企图变成一个看似无害的新文件）。
func agentTransferZipEntryPath(destRoot, destPrefix, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty entry name", errAgentTransferZipUnsafe)
	}
	if strings.Contains(name, "\\") {
		return "", fmt.Errorf("%w: backslash in entry %q", errAgentTransferZipUnsafe, name)
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) || agentTransferZipDrivePrefix.MatchString(name) {
		return "", fmt.Errorf("%w: absolute entry %q", errAgentTransferZipUnsafe, name)
	}
	cleaned := filepath.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: traversal in entry %q", errAgentTransferZipUnsafe, name)
	}
	if depth := strings.Count(cleaned, string(os.PathSeparator)) + 1; depth > agentTransferZipMaxDepth {
		return "", fmt.Errorf("%w: entry deeper than %d levels", errAgentTransferZipUnsafe, agentTransferZipMaxDepth)
	}
	// 逐段 D9 闸：Windows 保留设备名与尾点/尾空格在任何平台都拒收——
	// 落到 Windows 设备时会被系统吞掉结尾或变设备引用（P2 复核项）。
	for _, seg := range strings.Split(cleaned, string(os.PathSeparator)) {
		if seg == "" || seg == "." {
			continue
		}
		if isWindowsReservedTransferName(seg) || seg != strings.TrimRight(seg, ". ") {
			return "", fmt.Errorf("%w: unsafe entry name %q", errAgentTransferZipUnsafe, name)
		}
	}
	target := filepath.Join(destRoot, cleaned)
	if target != destRoot && !strings.HasPrefix(target, destPrefix) {
		return "", fmt.Errorf("%w: entry escapes destination %q", errAgentTransferZipUnsafe, name)
	}
	return target, nil
}

// copyAgentTransferZipEntry 落单个文件：固定 0644（不采信归档 mode）、
// 单条目量闸、流式拷贝。
func copyAgentTransferZipEntry(targetPath string, f *zip.File) (int64, error) {
	mode := f.Mode()
	if mode&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("%w: symlink entry %q", errAgentTransferZipUnsafe, f.Name)
	}
	src, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer src.Close()
	dst, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(dst, io.LimitReader(src, agentTransferZipMaxEntryBytes+1))
	closeErr := dst.Close()
	if err == nil && n > agentTransferZipMaxEntryBytes {
		_ = os.Remove(targetPath)
		return n, fmt.Errorf("%w: entry exceeds %d bytes", errAgentTransferZipUnsafe, agentTransferZipMaxEntryBytes)
	}
	if err != nil {
		_ = os.Remove(targetPath)
		return n, err
	}
	return n, closeErr
}
