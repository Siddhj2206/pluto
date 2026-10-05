// Package devices is the client-side registry of saved SSH destinations: a
// TOML file under the XDG config directory mapping nicknames to targets.
// Nothing here talks to the daemon; the file is the whole state.
package devices

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// ErrInvalid marks a nickname or destination the registry will not store.
var ErrInvalid = errors.New("invalid device")

// Device is one saved nickname and the ssh destination it resolves to.
type Device struct {
	Nickname string
	Target   string
}

// Registry is a parsed devices.toml. Mutating methods persist the file.
type Registry struct {
	path    string
	targets map[string]string
}

// file mirrors the on-disk shape: a [device] table mapping nicknames to ssh
// destinations.
type file struct {
	Device map[string]string `toml:"device"`
}

// FileName is the registry's file name inside the pluto config directory.
const FileName = "devices.toml"

// Open loads the registry at path. A missing file is an empty registry; a
// malformed one is an error rather than a silent reset.
func Open(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Registry{path: path, targets: map[string]string{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f file
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("%s: unknown keys: %v", path, keys)
	}
	targets := f.Device
	if targets == nil {
		targets = map[string]string{}
	}
	return &Registry{path: path, targets: targets}, nil
}

// DefaultPath is the devices file under the XDG config directory:
// $XDG_CONFIG_HOME/pluto/devices.toml, or ~/.config/pluto/devices.toml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find config dir: %w", err)
	}
	return filepath.Join(dir, "pluto", FileName), nil
}

// List returns the saved devices ordered by nickname.
func (r *Registry) List() []Device {
	list := make([]Device, 0, len(r.targets))
	for nickname, target := range r.targets {
		list = append(list, Device{Nickname: nickname, Target: target})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Nickname < list[j].Nickname })
	return list
}

// Resolve returns the destination saved for nickname.
func (r *Registry) Resolve(nickname string) (string, bool) {
	target, ok := r.targets[nickname]
	return target, ok
}

// Add saves nickname -> target, replacing an existing nickname, and writes
// the registry.
func (r *Registry) Add(nickname, target string) error {
	if !validNickname(nickname) {
		return fmt.Errorf("%w nickname %q", ErrInvalid, nickname)
	}
	if !validTarget(target) {
		return fmt.Errorf("%w ssh destination %q", ErrInvalid, target)
	}
	r.targets[nickname] = target
	return r.save()
}

// Remove deletes nickname and writes the registry.
func (r *Registry) Remove(nickname string) error {
	if _, ok := r.targets[nickname]; !ok {
		return fmt.Errorf("no device %q", nickname)
	}
	delete(r.targets, nickname)
	return r.save()
}

func (r *Registry) save() error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(file{Device: r.targets}); err != nil {
		return fmt.Errorf("encode %s: %w", r.path, err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return writeFileAtomic(r.path, buf.Bytes(), 0o644)
}

// validNickname allows the characters that read back unquoted in TOML.
func validNickname(nickname string) bool {
	if nickname == "" {
		return false
	}
	for _, c := range nickname {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// validTarget rejects destinations ssh would split or mistake for an option.
func validTarget(target string) bool {
	if target == "" || strings.HasPrefix(target, "-") {
		return false
	}
	for _, c := range target {
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

// writeFileAtomic replaces path with data via a same-directory temp file, so
// a crash cannot leave a half-written registry.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s: %w", dir, err)
	}
	defer d.Close()
	return d.Sync()
}
