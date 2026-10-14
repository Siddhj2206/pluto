// Command pluto-schema writes pluto.yml.schema.json, the editor-facing schema
// for pluto.yml, derived from the internal/contract Go types. It is run through
// 'go generate ./...' from the repository root; see the directive in
// internal/contract.
package main

import (
	"fmt"
	"os"

	"github.com/Siddhj2206/pluto/internal/schema"
)

func main() {
	out := "pluto.yml.schema.json"
	if args := os.Args[1:]; len(args) > 0 {
		out = args[0]
	}
	data, err := schema.YAML()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pluto-schema:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "pluto-schema:", err)
		os.Exit(1)
	}
}
