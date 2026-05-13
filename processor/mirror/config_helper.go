package mirror

import "aliang.one/nursorgate/processor/config"

// loadMirrorConfig returns the effective traffic mirror configuration, or nil.
func loadMirrorConfig() *config.TrafficMirrorConfig {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		return nil
	}
	return cfg.EffectiveTrafficMirror()
}
