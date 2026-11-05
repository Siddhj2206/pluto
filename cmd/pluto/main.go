// Command pluto runs CI on machines you own.
package main

import (
	"os"

	"github.com/Siddhj2206/pluto/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
