package main

import (
	"os"

	"github.com/Siddhj2206/pluto/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
