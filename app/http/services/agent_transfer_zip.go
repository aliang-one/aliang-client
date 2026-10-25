package services

// 互传发送链的流式 zip 打包器（D3）：目录边遍历边产出 zip 字节流，包成
// io.Reader 直接喂分块上传，不落临时文件（2GB 临时盘问题不存在）。全仓此前
// 只有解压（wintun_dependency_windows.go，Windows-only），打包是从零写。

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// zipStreamWriter 把 zip.NewWriter 的输出变成 io.Reader。打包 goroutine 的
// 终态经 Wait() 收割：打包中途出错时 pipe 以该错误关闭，下游 Read 返回它，
// 上传循环据此中止；成功时 Wait 返回 nil。
type zipStreamWriter struct {
	pr   *io.PipeReader
	done chan error
}

func (w *zipStreamWriter) Read(p []byte) (int, error) { return w.pr.Read(p) }
func (w *zipStreamWriter) Close() error               { return w.pr.Close() }

// Wait 等打包 goroutine 结束并返回其错误。必须在下游读完（或放弃读）之后调用。
func (w *zipStreamWriter) Wait() error {
	err := <-w.done
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// newZipStreamWriter 开始打包 root 目录。跳过目录的判定复用 shouldSkipAgentProjectDir
// （file.list/project detail 的 23 个噪音目录清单），保持「互传看到的目录」与
// 「平台其他文件视图」一致；符号链接条目跳过（不打包——解压端不收链接，见
// spec §9.2）。
func newZipStreamWriter(root string) (*zipStreamWriter, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("zip pack source is not a directory")
	}
	pr, pw := io.Pipe()
	w := &zipStreamWriter{pr: pr, done: make(chan error, 1)}
	go func() {
		err := packAgentTransferZip(root, pw)
		// CloseWithError(nil) 即 Close：下游 Read 在此后返回 io.EOF。
		_ = pw.CloseWithError(err)
		w.done <- err
		close(w.done)
	}()
	return w, nil
}

func packAgentTransferZip(root string, pw *io.PipeWriter) error {
	zw := zip.NewWriter(pw)
	defer zw.Close()
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// 单个条目不可达（竞态删除/权限）不打包失败，跳过继续——
			// 目录内容在传输期间是活动的。
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			if shouldSkipAgentProjectDir(d.Name()) {
				return filepath.SkipDir
			}
			// 目录条目：名字带尾斜杠、零长度，保结构。
			hdr := &zip.FileHeader{
				Name:     name + "/",
				Method:   zip.Deflate,
				Modified: safeAgentZipModTime(d),
			}
			_, err := zw.CreateHeader(hdr)
			return err
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !info.Mode().IsRegular() {
			// 符号链接/设备文件/socket 一律不打包：解压端不收非普通文件，
			// 打包它们只会留下无法安全还原的条目。
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer src.Close()
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		hdr.Name = name
		hdr.Method = zip.Deflate
		hdr.Modified = safeAgentZipModTime(d)
		// 剥 setuid/setgid/sticky：归档权限位不该携带提权语义。
		hdr.SetMode(info.Mode().Perm())
		dst, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(dst, src)
		return err
	})
}

// safeAgentZipModTime zip 条目时间戳：取 mtime，但钳到 zip 支持的范围
// （1980-01-01 之后），早期时间戳会被 archive/zip 拒绝。
func safeAgentZipModTime(d os.DirEntry) time.Time {
	info, err := d.Info()
	if err != nil {
		return time.Now()
	}
	mt := info.ModTime()
	if mt.Before(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)) {
		return time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return mt
}
