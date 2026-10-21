package remotecli

import "runtime/debug"

// devVersion is what a plain source build reports. A release overrides
// Version at link time. The Windows client binary links this package and not
// internal/cli, so the release workflow stamps both Version variables.
const devVersion = "0.1.5-dev"

// Version is the build version, overridable at link time:
//
//	go build -ldflags "-X github.com/Siddhj2206/pluto/internal/remotecli.Version=v0.1.5"
var Version = devVersion

// resolveVersion picks the version to report: a link-time Version wins,
// otherwise the module version from the Go build info, otherwise the
// development default. It mirrors internal/cli/version.go; the two are kept
// apart because the Windows client must not link the Linux-only CLI.
func resolveVersion() string {
	build := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		build = info.Main.Version
	}
	if Version != "" && Version != devVersion {
		return Version
	}
	if build != "" && build != "(devel)" {
		return build
	}
	return devVersion
}
