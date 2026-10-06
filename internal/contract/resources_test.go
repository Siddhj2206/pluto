package contract_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// TestParseMemoryMiB pins the accepted memory spellings. Sizes are binary:
// every suffix is a power of 1024, so "1G" and "1GiB" agree.
func TestParseMemoryMiB(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"512MiB", 512},
		{"8GiB", 8 * 1024},
		{"1GiB", 1024},
		{"2G", 2048},
		{"1024M", 1024},
		{"1TiB", 1024 * 1024},
		{"", 0},
	}
	for _, c := range cases {
		got, err := contract.ParseMemoryMiB(c.in)
		if err != nil {
			t.Errorf("ParseMemoryMiB(%q) error = %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMemoryMiB(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseMemoryMiBRejectsGarbage(t *testing.T) {
	for _, in := range []string{"lots", "8", "8XiB", "-1GiB", "GiB", "1.5GiB", "64KiB", "0MiB"} {
		if got, err := contract.ParseMemoryMiB(in); err == nil {
			t.Errorf("ParseMemoryMiB(%q) = %d, want an error", in, got)
		}
	}
}

// TestParseRejectsBadResources checks that a bad size fails at parse time,
// blaming the resources key, rather than being silently ignored at boot.
func TestParseRejectsBadResources(t *testing.T) {
	_, err := contract.Parse("[box]\nresources = { memory = \"lots\" }\n")
	if err == nil {
		t.Fatal("Parse should reject an unparseable memory size")
	}
	if !errors.Is(err, contract.ErrInvalid) && !strings.Contains(err.Error(), "memory") {
		t.Fatalf("error = %v, want it to blame memory", err)
	}

	_, err = contract.Parse("[box]\nresources = { cpus = -1 }\n")
	if err == nil {
		t.Fatal("Parse should reject a negative cpu count")
	}
}

// TestResourcesDefaults pins the machine size when a contract declares none.
func TestResourcesDefaults(t *testing.T) {
	if contract.DefaultCPUs != 2 || contract.DefaultMemoryMiB != 1024 {
		t.Fatalf("defaults = %d/%d, want 2/1024", contract.DefaultCPUs, contract.DefaultMemoryMiB)
	}
}
