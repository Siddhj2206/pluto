// Command pluto-image-builder builds the bootable base image artifact —
// vmlinuz, rootfs.img, and manifest.json — from the pins manifest
// (images/pins.yaml). It is host-only: it needs directory networking for the
// downloads and rootless podman for the rootfs. Run from the repository root:
//
//	go run ./cmd/pluto-image-builder
//
// The artifact lands in images/out unless -out or PLUTO_IMAGE_OUT says
// otherwise, and is consumed by `pluto image import images/out`.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Siddhj2206/pluto/internal/imagebuilder"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "pluto-image-builder: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pluto-image-builder", flag.ContinueOnError)
	pinsPath := fs.String("pins", "images/pins.yaml", "pins manifest")
	out := fs.String("out", envOr("PLUTO_IMAGE_OUT", filepath.Join("images", "out")), "artifact output directory")
	diskMB := fs.Int("disk-mb", imagebuilder.DefaultDiskMB, "rootfs image size in MiB")
	root := fs.String("root", ".", "repository root (built into the guest helpers)")
	images := fs.String("images", "images", "directory holding the Containerfile and files/")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	pins, err := imagebuilder.LoadPins(*pinsPath)
	if err != nil {
		return err
	}
	b := imagebuilder.New(pins, *out, *root, *images)
	b.DiskMB = *diskMB
	if err := b.Build(context.Background()); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "artifact ready: %s\n", filepath.Join(*out, "manifest.json"))
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
