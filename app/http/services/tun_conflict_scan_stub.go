//go:build !windows

package services

func loadWindowsTunInterfaceSnapshotsNative() ([]tunInterfaceSnapshot, error) {
	return nil, nil
}
