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

// setupFingerprint is the declared setup that shapes the disk a box
// provisions: the base image, [tools], and [provision]. Everything else a
// contract declares — wake, services, jobs, sessions, schedules, events — runs
// on the provisioned box and must not invalidate a reusable environment layer.
type setupFingerprint struct {
	Image     string `json:"image"`
	Tools     *Tools `json:"tools"`
	Provision *Phase `json:"provision"`
}

// SetupHash fingerprints only the declared setup inputs. Unlike Hash, it is
// stable across unrelated work-item events: two boxes of the same project with
// the same image, [tools], and [provision] share a SetupHash and may reuse each
// other's environment layer, even when their wake, schedules, or jobs differ.
// It changes whenever any declared setup input changes.
func (c *Contract) SetupHash() string {
	data, err := json.Marshal(setupFingerprint{Image: c.Box.Image, Tools: c.Tools, Provision: c.Provision})
	if err != nil {
		// As in Hash: the fields are plain values, so Marshal cannot fail.
		panic(fmt.Sprintf("contract setup hash: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
