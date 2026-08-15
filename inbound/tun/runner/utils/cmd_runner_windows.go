//go:build windows

package utils

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func RunCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
	return cmd.Run()
}

func RunCommandElevated(name string, args ...string) error {
	if name == "" {
		return fmt.Errorf("empty command")
	}

	parameters := ""
	if len(args) > 0 {
		escapedArgs := make([]string, 0, len(args))
		for _, arg := range args {
			escapedArgs = append(escapedArgs, syscall.EscapeArg(arg))
		}
		parameters = strings.Join(escapedArgs, " ")
	}

	exitCode, err := runElevatedHidden(name, parameters)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("elevated command exited with code %d", exitCode)
	}
	return nil
}

func GetRunCommand(name string, args ...string) *exec.Cmd {
	if len(args) > 0 && name == "powershell" && args[0] == "-Command" {
		args[1] = "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new();" + args[1]
	}
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true, // 隐藏命令行窗口
	}
	return cmd
}

// RunCommandAndTrim runs a command and trims the output.
func RunCommandAndTrim(name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := GetRunCommand(name, args...)
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

const (
	seeMaskNoCloseProcess = 0x00000040
	swHide                = 0
	errorCancelled        = 1223
)

type shellExecuteInfo struct {
	CbSize       uint32
	FMask        uint32
	Hwnd         uintptr
	LpVerb       *uint16
	LpFile       *uint16
	LpParameters *uint16
	LpDirectory  *uint16
	NShow        int32
	HInstApp     uintptr
	LpIDList     uintptr
	LpClass      *uint16
	HKeyClass    uintptr
	DwHotKey     uint32
	HIconMonitor uintptr
	HProcess     windows.Handle
}

func runElevatedHidden(executable string, parameters string) (uint32, error) {
	shell32 := windows.NewLazySystemDLL("shell32.dll")
	shellExecuteExW := shell32.NewProc("ShellExecuteExW")

	verbPtr, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}
	filePtr, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return 0, err
	}
	paramsPtr, err := windows.UTF16PtrFromString(parameters)
	if err != nil {
		return 0, err
	}

	info := shellExecuteInfo{
		CbSize:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		FMask:        seeMaskNoCloseProcess,
		LpVerb:       verbPtr,
		LpFile:       filePtr,
		LpParameters: paramsPtr,
		NShow:        swHide,
	}

	r1, _, callErr := shellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			if errno, ok := callErr.(windows.Errno); ok && uint32(errno) == errorCancelled {
				return 0, fmt.Errorf("administrator permission request was cancelled")
			}
			return 0, callErr
		}
		return 0, fmt.Errorf("ShellExecuteExW returned failure")
	}
	if info.HProcess == 0 {
		return 0, fmt.Errorf("ShellExecuteExW did not return a process handle")
	}
	defer windows.CloseHandle(info.HProcess)

	if _, err := windows.WaitForSingleObject(info.HProcess, windows.INFINITE); err != nil {
		return 0, err
	}

	var exitCode uint32
	if err := windows.GetExitCodeProcess(info.HProcess, &exitCode); err != nil {
		return 0, err
	}
	return exitCode, nil
}
