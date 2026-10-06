// Package hexid validates hex-encoded identifiers: content-address cache keys,
// git object ids, and ref SHAs. One rule keeps the call sites from drifting.
package hexid

// Valid reports whether s is non-empty hex digits with one of the allowed
// lengths. Both cases are accepted; callers that only ever see lowercase
// encodings are unaffected.
func Valid(s string, lengths ...int) bool {
	if s == "" || len(lengths) == 0 {
		return false
	}
	matched := false
	for _, n := range lengths {
		if len(s) == n {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
