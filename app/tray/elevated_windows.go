//go:build windows

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"aliang.one/nursorgate/processor/setup"
	"golang.org/x/sys/windows"
)

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

func startServiceWithElevation(name string) error {
	if err := setup.StartService(name, true); err == nil {
		return nil
	}

	exitCode, err := runElevatedHidden(resolveSystemExecutable("sc.exe"), fmt.Sprintf("start %s", name))
	if err != nil {
		return err
	}
	if !isAcceptableWindowsServiceStartExitCode(exitCode) {
		return fmt.Errorf("elevated service start exited with code %d", exitCode)
	}
	return nil
}

func stopServiceWithElevation(name string) error {
	if err := setup.StopService(name, true); err == nil {
		return nil
	}

	exitCode, err := runElevatedHidden(resolveSystemExecutable("sc.exe"), fmt.Sprintf("stop %s", name))
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("elevated service stop exited with code %d", exitCode)
	}
	return nil
}

func resolveSystemExecutable(name string) string {
	windir := os.Getenv("WINDIR")
	if windir == "" {
		windir = os.Getenv("SystemRoot")
	}
	if windir == "" {
		windir = `C:\Windows`
	}
	return filepath.Join(windir, "System32", name)
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
