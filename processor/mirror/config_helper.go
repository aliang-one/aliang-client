package mirror

import (
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/config"
)

// loadMirrorConfig returns the effective traffic mirror configuration, or nil.
func loadMirrorConfig() *config.TrafficMirrorConfig {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return nil
	}
	return cfg.EffectiveTrafficMirror()
}

// ShouldMirror reports whether traffic mirroring is configured for metadata's host.
func ShouldMirror(metadata *M.Metadata) bool {
	if metadata == nil || metadata.HostName == "" {
		return false
	}
	cfg := loadMirrorConfig()
	if cfg == nil {
		return false
	}
	return MatchesAnyDomain(metadata.HostName, cfg.Domains)
}
