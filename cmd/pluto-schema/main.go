// Command pluto-schema writes pluto.schema.json, the editor-facing schema for
// .pluto.toml derived from the internal/contract Go types. It is run through
// 'go generate ./...' from the repository root; see the directive in
// internal/contract.
package main

import (
	"fmt"
	"os"

	"github.com/Siddhj2206/pluto/internal/schema"
)

func main() {
	out := "pluto.schema.json"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	data, err := schema.JSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pluto-schema:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "pluto-schema:", err)
		os.Exit(1)
	}
}
