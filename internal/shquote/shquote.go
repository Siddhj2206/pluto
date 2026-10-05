// Package shquote quotes a word for a POSIX shell. It is the one copy of the
// rule wherever pluto hands a word to a shell: ssh's remote command line, a
// systemd ExecStart value, or a generated hook script.
package shquote

import "strings"

// Quote quotes s for /bin/sh when the shell would split or interpret it.
func Quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`;&|<>()*?[]{}~#!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
