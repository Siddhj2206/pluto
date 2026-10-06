// Package imagebuilder builds the bootable base image artifact — vmlinuz,
// rootfs.img, and manifest.json — from a single pins manifest
// (images/pins.yaml). Rootless podman remains the rootfs mechanism; every
// external command (podman, go, tar, mkfs.ext4) goes through the Shell seam so
// parsing and assembly are unit-testable without podman, network, or KVM.
package imagebuilder

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Pins is the decoded pins manifest: the single source of truth for every
// image input.
type Pins struct {
	Base        BasePins        `json:"base"`
	Apt         AptPins         `json:"apt"`
	Firecracker FirecrackerPins `json:"firecracker"`
	Kernel      KernelPins      `json:"kernel"`
}

// BasePins pins the container base image by tag and digest.
type BasePins struct {
	Image  string `json:"image"`
	Digest string `json:"digest"`
}

// String is the image reference passed to the Containerfile: tag@digest.
func (b BasePins) String() string {
	if b.Digest == "" {
		return b.Image
	}
	return b.Image + "@" + b.Digest
}

// AptPins pins the package set to a dated snapshot.ubuntu.com index.
type AptPins struct {
	Snapshot string   `json:"snapshot"`
	Packages []string `json:"packages"`
}

// Epoch is the snapshot's UTC Unix time. The builder uses it as
// SOURCE_DATE_EPOCH, so every timestamp baked into the artifact derives from a
// pinned input instead of the build clock.
func (a AptPins) Epoch() (int64, error) {
	t, err := time.Parse("20060102T150405Z", a.Snapshot)
	if err != nil {
		return 0, fmt.Errorf("pins: apt.snapshot %q must be a snapshot.ubuntu.com id (YYYYMMDDTHHMMSSZ)", a.Snapshot)
	}
	return t.Unix(), nil
}

// FirecrackerPins pins the Firecracker release tarball.
type FirecrackerPins struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

// KernelPins pins the guest kernel artifact.
type KernelPins struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Name is the kernel's file name, recorded in the manifest.
func (k KernelPins) Name() string {
	if i := strings.LastIndex(k.URL, "/"); i >= 0 {
		return k.URL[i+1:]
	}
	return k.URL
}

// LoadPins reads and parses a pins manifest from disk.
func LoadPins(path string) (Pins, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Pins{}, fmt.Errorf("read pins manifest: %w", err)
	}
	pins, err := ParsePins(data)
	if err != nil {
		return Pins{}, fmt.Errorf("parse pins manifest %s: %w", path, err)
	}
	return pins, nil
}

// ParsePins decodes the fixed pins-manifest shape. YAML was chosen for the
// manifest (research: readable, Renovate-drivable, one reviewable file) but the
// shape is small and fixed, so this parses the YAML subset it uses — nested
// mappings and scalar sequences — rather than taking a dependency on a YAML
// library. Anything outside that subset is an error.
func ParsePins(data []byte) (Pins, error) {
	root, err := parseYAMLSubset(data)
	if err != nil {
		return Pins{}, err
	}
	var pins Pins

	base, err := subMap(root, "base")
	if err != nil {
		return Pins{}, err
	}
	pins.Base.Image = scalar(base, "image")
	pins.Base.Digest = scalar(base, "digest")

	apt, err := subMap(root, "apt")
	if err != nil {
		return Pins{}, err
	}
	pins.Apt.Snapshot = scalar(apt, "snapshot")
	pins.Apt.Packages = stringList(apt, "packages")

	fc, err := subMap(root, "firecracker")
	if err != nil {
		return Pins{}, err
	}
	pins.Firecracker.Version = scalar(fc, "version")
	pins.Firecracker.URL = scalar(fc, "url")
	pins.Firecracker.SHA256 = scalar(fc, "sha256")

	kernel, err := subMap(root, "kernel")
	if err != nil {
		return Pins{}, err
	}
	pins.Kernel.URL = scalar(kernel, "url")
	pins.Kernel.SHA256 = scalar(kernel, "sha256")

	if err := pins.validate(); err != nil {
		return Pins{}, err
	}
	return pins, nil
}

func (p Pins) validate() error {
	if p.Base.Image == "" {
		return fmt.Errorf("pins: base.image is required")
	}
	if !hasPrefixHash(p.Base.Digest, "sha256:") {
		return fmt.Errorf("pins: base.digest must be a sha256 digest, got %q", p.Base.Digest)
	}
	if _, err := p.Apt.Epoch(); err != nil {
		return err
	}
	if len(p.Apt.Packages) == 0 {
		return fmt.Errorf("pins: apt.packages must list at least one package")
	}
	if p.Firecracker.Version == "" {
		return fmt.Errorf("pins: firecracker.version is required")
	}
	if p.Firecracker.URL == "" {
		return fmt.Errorf("pins: firecracker.url is required")
	}
	if !isSHA256(p.Firecracker.SHA256) {
		return fmt.Errorf("pins: firecracker.sha256 must be a sha256 hash, got %q", p.Firecracker.SHA256)
	}
	if p.Kernel.URL == "" {
		return fmt.Errorf("pins: kernel.url is required")
	}
	if !isSHA256(p.Kernel.SHA256) {
		return fmt.Errorf("pins: kernel.sha256 must be a sha256 hash, got %q", p.Kernel.SHA256)
	}
	return nil
}

func hasPrefixHash(v, prefix string) bool {
	return strings.HasPrefix(v, prefix) && isSHA256(strings.TrimPrefix(v, prefix))
}

func isSHA256(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

func subMap(m map[string]any, key string) (map[string]any, error) {
	v, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("pins: %s is required", key)
	}
	child, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("pins: %s must be a mapping", key)
	}
	return child, nil
}

func scalar(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func stringList(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

// yamlLine is one significant line: its indent and trimmed content.
type yamlLine struct {
	number int
	indent int
	text   string
}

// parseYAMLSubset parses the mapping-and-scalar-sequence subset the pins
// manifest uses. The top level must be a mapping.
func parseYAMLSubset(data []byte) (map[string]any, error) {
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	var lines []yamlLine
	for i, raw := range strings.Split(normalized, "\n") {
		text := stripComment(raw)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if strings.ContainsRune(text, '\t') {
			return nil, fmt.Errorf("pins: line %d: tabs are not allowed for indentation", i+1)
		}
		indent := len(text) - len(strings.TrimLeft(text, " "))
		lines = append(lines, yamlLine{number: i + 1, indent: indent, text: strings.TrimSpace(text)})
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("pins: empty manifest")
	}
	if strings.HasPrefix(lines[0].text, "- ") {
		return nil, fmt.Errorf("pins: line %d: top level must be a mapping", lines[0].number)
	}
	p := &yamlParser{lines: lines}
	block, err := p.parseBlock(lines[0].indent)
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.lines) {
		return nil, fmt.Errorf("pins: line %d: unexpected %q", p.lines[p.pos].number, p.lines[p.pos].text)
	}
	root, ok := block.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("pins: top level must be a mapping")
	}
	return root, nil
}

type yamlParser struct {
	lines []yamlLine
	pos   int
}

func (p *yamlParser) parseBlock(indent int) (any, error) {
	if p.pos >= len(p.lines) {
		return nil, nil
	}
	if strings.HasPrefix(p.lines[p.pos].text, "- ") || p.lines[p.pos].text == "-" {
		return p.parseSeq(indent)
	}
	return p.parseMap(indent)
}

func (p *yamlParser) parseMap(indent int) (map[string]any, error) {
	out := map[string]any{}
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			return nil, fmt.Errorf("pins: line %d: unexpected indentation", ln.number)
		}
		if strings.HasPrefix(ln.text, "- ") || ln.text == "-" {
			break
		}
		key, value, ok := splitKey(ln.text)
		if !ok {
			return nil, fmt.Errorf("pins: line %d: expected 'key: value', got %q", ln.number, ln.text)
		}
		p.pos++
		if value != "" {
			out[key] = unquote(value)
			continue
		}
		if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
			child, err := p.parseBlock(p.lines[p.pos].indent)
			if err != nil {
				return nil, err
			}
			out[key] = child
		} else {
			out[key] = ""
		}
	}
	return out, nil
}

func (p *yamlParser) parseSeq(indent int) ([]any, error) {
	var out []any
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			return nil, fmt.Errorf("pins: line %d: unexpected indentation in list", ln.number)
		}
		if !strings.HasPrefix(ln.text, "- ") && ln.text != "-" {
			break
		}
		item := strings.TrimSpace(strings.TrimPrefix(ln.text, "-"))
		if item == "" {
			return nil, fmt.Errorf("pins: line %d: empty list item", ln.number)
		}
		if _, _, ok := splitKey(item); ok {
			return nil, fmt.Errorf("pins: line %d: only scalar list items are supported", ln.number)
		}
		out = append(out, unquote(item))
		p.pos++
	}
	return out, nil
}

// splitKey splits "key: value"; value is empty when the line is "key:".
func splitKey(text string) (key, value string, ok bool) {
	i := strings.Index(text, ":")
	if i < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(text[:i])
	value = strings.TrimSpace(text[i+1:])
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

// stripComment removes a trailing comment that is outside quotes and preceded
// by whitespace (or starts the line).
func stripComment(s string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
				return s[:i]
			}
		}
	}
	return s
}

// unquote removes matching single or double quotes.
func unquote(s string) string {
	if len(s) >= 2 {
		if s[0] == '"' && s[len(s)-1] == '"' {
			if u, err := strconv.Unquote(s); err == nil {
				return u
			}
		}
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			return s[1 : len(s)-1]
		}
	}
	return s
}
