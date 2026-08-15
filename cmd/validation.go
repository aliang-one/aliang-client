package cmd

import (
	"errors"
	"net"
	"strings"
)

// validateCoreServerAddr validates the core server address format.
// Empty addresses are allowed (uses default fallback).
// Non-empty addresses must be in host:port format.
func validateCoreServerAddr(addr string) error {
	if addr == "" {
		// Empty is OK, will use default
		return nil
	}

	// Remove URL scheme if present
	normalizedAddr := addr
	if strings.HasPrefix(normalizedAddr, "https://") {
		normalizedAddr = strings.TrimPrefix(normalizedAddr, "https://")
	} else if strings.HasPrefix(normalizedAddr, "http://") {
		normalizedAddr = strings.TrimPrefix(normalizedAddr, "http://")
	}

	// Must be host:port format
	host, port, err := net.SplitHostPort(normalizedAddr)
	if err != nil {
		// Try adding default port
		if !strings.Contains(normalizedAddr, ":") {
			return nil // Will be normalized by normalizeCoreServerAddr
		}
		return err
	}

	if host == "" {
		return errors.New("coreServer host cannot be empty")
	}

	if port == "" {
		return errors.New("coreServer port cannot be empty")
	}

	return nil
}
