// Package testisolate keeps test binaries from touching the developer's real
// Aliang user-state directory (~/.aliang). Test binaries that trigger the file
// logger — directly or through any service they construct — would otherwise
// append test noise (state_load dumps, dial 127.0.0.1:0 errors, ...) to the
// production aliang_core.log under ~/.aliang/logs, which poisoned real incident
// diagnostics once (2026-09-20 outage investigation).
//
// Usage from a package's TestMain, before m.Run():
//
//	cleanup := testisolate.RedirectUserStateDir()
//	code := m.Run()
//	cleanup()
//	os.Exit(code)
package testisolate

import (
	"fmt"
	"os"

	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
)

// RedirectUserStateDir points ALIANG_DATA_DIR at a fresh temp directory and
// re-resolves the package-level singletons that already captured the real
// user-state path before TestMain ran:
//
//   - cache.GetCacheDir is a sync.Once resolved during logger package init
//     (globalLogConfig = DefaultLogConfig()), pinning ~/.aliang;
//   - logger.globalLogConfig baked FileLogPath=~/.aliang/logs/aliang_core.log
//     at the same time.
//
// ALIANG_DATA_DIR (not ALIANG_CACHE_DIR) is deliberate: the repo's per-test
// isolation convention is `t.Setenv("ALIANG_DATA_DIR", t.TempDir())`, so a
// TestMain-level value of the same variable composes with it — a test's
// t.Setenv overrides this one for its duration and is restored afterwards.
// ALIANG_CACHE_DIR would instead outrank every per-test ALIANG_DATA_DIR (it
// wins in runtimepath.ResolveStateDir) and silently defeat their isolation.
//
// Side effect: a set ALIANG_DATA_DIR makes runtimepath.DetectMode() return
// ModeDaemon for the whole test binary — part of how this scheme takes
// effect, and it incidentally redirects runtimepath.CoreDataDir() writes
// into the temp dir as well. Packages that assert interactive-mode behavior
// should take note before adopting this helper.
//
// It returns a cleanup func that removes the temp directory. If the temp dir
// cannot be created the function degrades to a no-op so an environment hiccup
// cannot fail the suite (tests then keep the legacy, polluting behavior).
func RedirectUserStateDir() func() {
	dir, err := os.MkdirTemp("", "aliang-test-state-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "testisolate: redirect failed, tests may write user state dir: %v\n", err)
		return func() {}
	}
	_ = os.Setenv("ALIANG_DATA_DIR", dir)
	cache.ResetCacheDirForTest()
	// Recompute the default log config against the temp state dir and
	// propagate it. The main logger opens its file lazily: the path is only
	// resolved by initLoggers on the FIRST log write, so tests that log
	// after this point write to the temp dir. SetLogConfig re-points an
	// instance created earlier only if it has not written a log yet — once
	// initLoggers has run and the fileSink is open, the instance stays
	// bound to the old path. Caveat: this helper must be called before any
	// test in the package writes a log (the earliest point of TestMain); a
	// package-level var that logs during init silently defeats it.
	logger.SetLogConfig(logger.DefaultLogConfig())
	return func() { _ = os.RemoveAll(dir) }
}
