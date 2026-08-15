//go:build !windows

package services

func removeFileElevated(path string) error {
	return nil
}
