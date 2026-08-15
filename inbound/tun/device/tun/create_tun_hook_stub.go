//go:build !windows

package tun

func SetCreateTUNAttemptHook(func(name string, attempt int, maxRetries int, err error)) {}
