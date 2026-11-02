//go:build !darwin

package services

import "errors"

// 非 macOS 没有系统设置里的隐私面板可开，也没有 TCC。
const (
	FileAccessPaneFilesAndFolders = "files"
	FileAccessPaneAllFiles        = "allfiles"
)

// OpenFileAccessSystemSettings 在非 macOS 上是空实现：返回错误让调用方把
// 「当前平台不支持」如实告诉用户，而不是静默假装成功。
func OpenFileAccessSystemSettings(pane string) error {
	return errors.New("当前平台不支持 macOS 隐私授权设置")
}
