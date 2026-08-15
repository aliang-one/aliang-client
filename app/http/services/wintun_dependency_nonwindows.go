//go:build !windows

package services

import "runtime"

type noopWintunDependencyController struct{}

func newWintunDependencyController() wintunDependencyController {
	return noopWintunDependencyController{}
}

func (noopWintunDependencyController) Status() WintunDependencyStatus {
	return WintunDependencyStatus{
		Supported:    false,
		Required:     false,
		Available:    true,
		Installing:   false,
		State:        "not_applicable",
		Progress:     100,
		ErrorCode:    "",
		Message:      "Wintun dependency is only required on Windows.",
		Architecture: runtime.GOARCH,
		DownloadURL:  wintunDownloadURL,
	}
}

func (c noopWintunDependencyController) Refresh() WintunDependencyStatus {
	return c.Status()
}

func (c noopWintunDependencyController) StartInstall() WintunDependencyStatus {
	return c.Status()
}
