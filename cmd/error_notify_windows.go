//go:build windows

package cmd

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func notifyExecuteError(err error) {
	if err == nil {
		return
	}

	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBoxW := user32.NewProc("MessageBoxW")
	title, _ := windows.UTF16PtrFromString("Aliang Startup Failed")
	body, _ := windows.UTF16PtrFromString(fmt.Sprintf("Aliang failed to start:\n\n%v", err))
	const mbIconError = 0x00000010
	const mbOK = 0x00000000
	messageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(body)),
		uintptr(unsafe.Pointer(title)),
		uintptr(mbOK|mbIconError),
	)
}
