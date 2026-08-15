package version

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

const Name = "AliangCore"

var (
	// Version can be set at link time by executing
	// the command: `git describe --abbrev=0 --tags HEAD`
	Version = "v1.1.10"

	// GitCommit can be set at link time by executing
	// the command: `git rev-parse --short HEAD`
	GitCommit string

	// BuildMode can be set at link time, for example: prod/dev.
	BuildMode string
)

func String() string {
	return fmt.Sprintf("%s", strings.TrimPrefix(Version, "v"))
}

func BuildString() string {
	return fmt.Sprintf("%s/%s, %s, %s", runtime.GOOS, runtime.GOARCH, runtime.Version(), GitCommit)
}

func EffectiveBuildMode() string {
	if mode := strings.TrimSpace(os.Getenv("ALIANG_BUILD_MODE")); mode != "" {
		return strings.ToLower(mode)
	}
	if mode := strings.TrimSpace(BuildMode); mode != "" {
		return strings.ToLower(mode)
	}
	return "dev"
}

func IsProdBuild() bool {
	return EffectiveBuildMode() == "prod"
}
