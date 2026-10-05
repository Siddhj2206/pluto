package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Hash returns the fingerprint of this parsed contract, the value a box
// records when it applies the contract at handoff. `pluto status` compares
// the recorded hash with a fresh one to report staleness.
//
// The hash is stable across presentation: it covers a canonical JSON
// encoding of the parsed values, so comments, whitespace, key order, and
// table order never change it. It is not a semantic fingerprint — spelling
// the same value differently ("1h" vs "60m") changes it, as does reordering
// repeated sections such as `[[schedule]]` — but the rule is consistent and
// errs toward reporting: any edit that survives parsing is treated as a
// contract change. A file that parses to the same values hashes the same.
func (c *Contract) Hash() string {
	data, err := json.Marshal(c)
	if err != nil {
		// The contract is plain strings, numbers, slices, and maps; Marshal
		// cannot fail on it. Silence here would mean recording no hash and
		// losing staleness tracking, so fail loudly instead.
		panic(fmt.Sprintf("contract hash: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
