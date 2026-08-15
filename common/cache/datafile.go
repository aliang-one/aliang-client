package cache

const (
	// UnifiedDataDBFile is the single SQLite data file used for local state.
	UnifiedDataDBFile = "aliang.data"
)

// GetUnifiedDataDBPath returns the canonical path for the shared local SQLite database.
func GetUnifiedDataDBPath() (string, error) {
	return GetCacheFile(UnifiedDataDBFile)
}
