// Package version names this build and what its host API can do.
package version

// Version is the build version.
const Version = "0.1.0-dev"

// Capabilities are the host API features a client may rely on. Clients check
// these instead of comparing version numbers.
var Capabilities = []string{"ci.github", "runs", "logs", "enter", "tasks", "schedules", "boxes", "secrets", "schedule.run", "box.enter"}
