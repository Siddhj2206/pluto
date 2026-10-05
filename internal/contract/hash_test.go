package contract_test

import (
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// The applied-contract hash must ignore formatting: comments, whitespace,
// key order, and table order are presentation, not contract. A box that
// records the hash at handoff must never report staleness for a file that
// only got reformatted.

func TestHashIgnoresCommentsAndFormatting(t *testing.T) {
	verbose := `
# The box contract. Keep comments short.
[box]
image = "ubuntu-24.04"          # pinned
resources = { cpus = 4, memory = "8GiB", disk = "40GiB" }

[wake]
command = "make test"           # the fast path
`
	tight := `
[wake]
command="make test"

[box]
resources={cpus=4, disk="40GiB", memory="8GiB"}
image="ubuntu-24.04"
`
	a, err := contract.Parse(verbose)
	if err != nil {
		t.Fatalf("Parse(verbose): %v", err)
	}
	b, err := contract.Parse(tight)
	if err != nil {
		t.Fatalf("Parse(tight): %v", err)
	}
	if a.Hash() != b.Hash() {
		t.Fatalf("hash changed across formatting:\n  %s\n  %s", a.Hash(), b.Hash())
	}
}

func TestHashChangesWhenTheContractChanges(t *testing.T) {
	before, err := contract.Parse("[wake]\ncommand = \"make test\"\n")
	if err != nil {
		t.Fatalf("Parse(before): %v", err)
	}
	after, err := contract.Parse("[wake]\ncommand = \"make test --short\"\n")
	if err != nil {
		t.Fatalf("Parse(after): %v", err)
	}
	if before.Hash() == after.Hash() {
		t.Fatal("hash survived a command change")
	}
}

func TestHashChangesWhenASectionAppears(t *testing.T) {
	empty, err := contract.Parse("")
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
	}
	declared, err := contract.Parse("[box]\nauto_pause = \"30m\"\n")
	if err != nil {
		t.Fatalf("Parse(declared): %v", err)
	}
	if empty.Hash() == declared.Hash() {
		t.Fatal("hash survived adding a section")
	}
}

// Reordering repeated sections is not formatting: the parsed contract keeps
// schedule order, and the hash reports it. Pinned so the boundary of
// "formatting-only" stays a deliberate choice, not an accident.
func TestHashKeepsScheduleOrderSignificant(t *testing.T) {
	first, err := contract.Parse(`
[[schedule]]
name = "nightly"
cron = "0 2 * * *"

[[schedule]]
name = "hourly"
cron = "0 * * * *"
`)
	if err != nil {
		t.Fatalf("Parse(first): %v", err)
	}
	reordered, err := contract.Parse(`
[[schedule]]
name = "hourly"
cron = "0 * * * *"

[[schedule]]
name = "nightly"
cron = "0 2 * * *"
`)
	if err != nil {
		t.Fatalf("Parse(reordered): %v", err)
	}
	if first.Hash() == reordered.Hash() {
		t.Fatal("hash ignored schedule order; it should report it")
	}
}

// The hash is the box's record across restarts, so the same bytes must hash
// the same on every call.
func TestHashIsStable(t *testing.T) {
	c, err := contract.Parse("[wake]\ncommand = \"true\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Hash() != c.Hash() {
		t.Fatal("hash changed between calls")
	}
	if c.Hash() == "" {
		t.Fatal("hash is empty")
	}
}
