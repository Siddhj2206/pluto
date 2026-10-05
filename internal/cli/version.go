package cli

import "runtime/debug"

// devVersion is what a plain source build reports. A release overrides
// Version at link time; a `go install ...@vX.Y.Z` binary instead carries the
// tag in its build info.
const devVersion = "0.1.0-dev"

// Version is the build version, overridable at link time:
//
//	go build -ldflags "-X github.com/Siddhj2206/pluto/internal/cli.Version=v0.1.0"
var Version = devVersion

// ResolveVersion picks the version to report. A link-time Version (what the
// release workflow injects) wins; otherwise the module version from the Go
// build info — what `go install ...@vX.Y.Z` records — is used. A plain build
// with neither stays on the development default.
func ResolveVersion(linkVersion, buildVersion string) string {
	if linkVersion != "" && linkVersion != devVersion {
		return linkVersion
	}
	if buildVersion != "" && buildVersion != "(devel)" {
		return buildVersion
	}
	return devVersion
}

// resolvedVersion is the value `pluto version` and `pluto --version` print.
func resolvedVersion() string {
	build := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		build = info.Main.Version
	}
	return ResolveVersion(Version, build)
}
