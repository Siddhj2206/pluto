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
	return QuoteAlways(s)
}

// QuoteAlways single-quotes s even when it contains no shell metacharacters.
// Use it where one word must stay one word, such as the command string after
// `sh -c`: `sh -c /usr/bin/pluto version` would make version the shell's $0.
func QuoteAlways(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
