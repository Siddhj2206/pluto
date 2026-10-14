// Command pluto-schema writes pluto.schema.json, the editor-facing schema for
// .pluto.toml, and (with -yaml) pluto.yml.schema.json for pluto.yml, both
// derived from the internal/contract Go types. It is run through
// 'go generate ./...' from the repository root; see the directives in
// internal/contract.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Siddhj2206/pluto/internal/schema"
)

func main() {
	yaml := flag.Bool("yaml", false, "write the pluto.yml schema instead of the .pluto.toml schema")
	flag.Parse()
	out := "pluto.schema.json"
	if *yaml {
		out = "pluto.yml.schema.json"
	}
	if args := flag.Args(); len(args) > 0 {
		out = args[0]
	}
	build := schema.JSON
	if *yaml {
		build = schema.YAML
	}
	data, err := build()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pluto-schema:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "pluto-schema:", err)
		os.Exit(1)
	}
}
