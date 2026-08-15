package aliang

// New creates a new aliang proxy instance for mTLS connections
// This is an alias for NewAliang for compatibility
func New(config *AliangConfig) (*Aliang, error) {
	return NewAliang(config)
}
