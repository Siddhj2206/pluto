package devices

import (
	"fmt"
	"strings"
	"time"
)

// HostOptions is the ssh trust boundary every non-interactive route shares:
// BatchMode never prompts, so a missing key fails instead of hanging; the
// connect timeout bounds a dead host; StrictHostKeyChecking=accept-new accepts
// a host key on first use but a changed key still fails. There is deliberately
// no StrictHostKeyChecking=no and no throw-away known_hosts, which would
// silently accept a swapped host key on every connection. Callers keep their
// own timeout: a probe only gates a warning, while a command's failure is the
// user's.
func HostOptions(connectTimeout time.Duration) []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(connectTimeout/time.Second)),
		"-o", "StrictHostKeyChecking=accept-new",
	}
}

// lastLine returns the last non-empty line of s, so a warning carries ssh's
// actual complaint rather than a banner.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
