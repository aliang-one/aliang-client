package cache

import (
	"path/filepath"
	"testing"
)

func TestGetCacheDir_FallsBackToRuntimeDataDirInDaemonMode(t *testing.T) {
	ResetCacheDirForTest()
	t.Cleanup(ResetCacheDirForTest)

	runtimeDir := t.TempDir()
	t.Setenv("HOME", "")
	t.Setenv("ALIANG_DATA_DIR", runtimeDir)
	t.Setenv("ALIANG_CACHE_DIR", "")

	cacheDir, err := GetCacheDir()
	if err != nil {
		t.Fatalf("GetCacheDir() error = %v", err)
	}
	if cacheDir != runtimeDir {
		t.Fatalf("cache dir = %q, want %q", cacheDir, runtimeDir)
	}
}

func TestGetCacheFile_UsesUnifiedRuntimeDir(t *testing.T) {
	ResetCacheDirForTest()
	t.Cleanup(ResetCacheDirForTest)

	runtimeDir := t.TempDir()
	t.Setenv("HOME", "")
	t.Setenv("ALIANG_DATA_DIR", runtimeDir)
	t.Setenv("ALIANG_CACHE_DIR", "")

	path, err := GetCacheFile("aliang.data")
	if err != nil {
		t.Fatalf("GetCacheFile() error = %v", err)
	}

	wantPath := filepath.Join(runtimeDir, "aliang.data")
	if path != wantPath {
		t.Fatalf("cache file path = %q, want %q", path, wantPath)
	}
}
