// Package devices is the client-side registry of saved SSH destinations: a
// TOML file under the user config directory mapping nicknames to OpenSSH
// connection profiles. os.UserConfigDir puts it at
// $XDG_CONFIG_HOME/pluto/devices.toml (or ~/.config/pluto/devices.toml) on
// unix and %AppData%\pluto\devices.toml on Windows. Nothing here talks to the
// daemon; the file is the whole state.
//
// The profile points at OpenSSH. `destination` is an alias from ~/.ssh/config
// or a raw target, and is passed to ssh as written; port, identity, and
// ProxyJump are optional escape hatches. Pluto never parses ssh config and
// never replaces an alias with a resolved snapshot.
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

// ErrInvalid marks a nickname or profile value the registry will not store.
var ErrInvalid = errors.New("invalid device")

// Defaults for the optional profile fields.
const (
	RemotePathAuto  = "auto"
	PlatformAuto    = "auto"
	PlatformLinux   = "linux"
	AuthAuto        = "auto"
	AuthKey         = "key"
	AuthInteractive = "interactive"
	TrustStrict     = "strict"
	TrustAcceptNew  = "accept-new"
)

// Profile is one saved nickname and the OpenSSH route it names.
type Profile struct {
	Nickname    string
	Destination string
	// Port, IdentityFile, and ProxyJump are optional overrides. Zero values
	// mean "let ~/.ssh/config decide".
	Port         int
	IdentityFile string
	ProxyJump    string
	// RemotePath is "auto" or an absolute path to the host's pluto binary.
	RemotePath string
	// Platform is "auto" or "linux". The daemon is Linux-only.
	Platform string
	// Auth is "auto", "key", or "interactive".
	Auth string
	// Trust is "strict" or "accept-new".
	Trust string
	// ControlMaster is POSIX-only and opt-in.
	ControlMaster bool
}

// Device is the compatibility view of a saved profile: a nickname and its ssh
// destination.
type Device struct {
	Nickname string
	Target   string
}

// Registry is a parsed devices.toml. Mutating methods persist the file.
type Registry struct {
	path     string
	profiles map[string]Profile
}

// file mirrors the on-disk shape. The legacy [device] and [remote] tables stay
// readable so an existing devices.toml keeps working; saves use the richer
// [devices.<name>] tables.
type file struct {
	Version int                    `toml:"version,omitempty"`
	Device  map[string]string      `toml:"device,omitempty"`
	Remote  map[string]string      `toml:"remote,omitempty"`
	Devices map[string]profileFile `toml:"devices,omitempty"`
}

type profileFile struct {
	Destination   string `toml:"destination"`
	Port          int    `toml:"port,omitempty"`
	IdentityFile  string `toml:"identity_file,omitempty"`
	ProxyJump     string `toml:"proxy_jump,omitempty"`
	RemotePath    string `toml:"remote_path,omitempty"`
	Platform      string `toml:"platform,omitempty"`
	Auth          string `toml:"auth,omitempty"`
	Trust         string `toml:"trust,omitempty"`
	ControlMaster bool   `toml:"control_master,omitempty"`
}

// FileName is the registry's file name inside the pluto config directory.
const FileName = "devices.toml"

// Open loads the registry at path. A missing file is an empty registry; a
// malformed one is an error rather than a silent reset.
func Open(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Registry{path: path, profiles: map[string]Profile{}}, nil
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
	profiles := map[string]Profile{}
	for nickname, target := range f.Device {
		p := Profile{Nickname: nickname, Destination: target}
		p.applyDefaults()
		profiles[nickname] = p
	}
	for nickname, pf := range f.Devices {
		p := Profile{
			Nickname:      nickname,
			Destination:   pf.Destination,
			Port:          pf.Port,
			IdentityFile:  pf.IdentityFile,
			ProxyJump:     pf.ProxyJump,
			RemotePath:    pf.RemotePath,
			Platform:      pf.Platform,
			Auth:          pf.Auth,
			Trust:         pf.Trust,
			ControlMaster: pf.ControlMaster,
		}
		p.applyDefaults()
		profiles[nickname] = p
	}
	for nickname, path := range f.Remote {
		p, ok := profiles[nickname]
		if !ok || p.RemotePath != RemotePathAuto {
			continue
		}
		p.RemotePath = path
		profiles[nickname] = p
	}
	return &Registry{path: path, profiles: profiles}, nil
}

// DefaultPath is the devices file under the user config directory:
// $XDG_CONFIG_HOME/pluto/devices.toml (or ~/.config/pluto/devices.toml) on
// unix, %AppData%\pluto\devices.toml on Windows.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find config dir: %w", err)
	}
	return filepath.Join(dir, "pluto", FileName), nil
}

// List returns the saved devices ordered by nickname.
func (r *Registry) List() []Device {
	profiles := r.Profiles()
	list := make([]Device, 0, len(profiles))
	for _, p := range profiles {
		list = append(list, Device{Nickname: p.Nickname, Target: p.Destination})
	}
	return list
}

// Profiles returns every saved profile ordered by nickname.
func (r *Registry) Profiles() []Profile {
	list := make([]Profile, 0, len(r.profiles))
	for _, p := range r.profiles {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Nickname < list[j].Nickname })
	return list
}

// Profile returns the saved profile for nickname.
func (r *Registry) Profile(nickname string) (Profile, bool) {
	p, ok := r.profiles[nickname]
	return p, ok
}

// Resolve returns the destination saved for nickname.
func (r *Registry) Resolve(nickname string) (string, bool) {
	p, ok := r.profiles[nickname]
	return p.Destination, ok
}

// Add saves nickname -> target with default profile values, replacing an
// existing nickname, and writes the registry. A destination change clears the
// learned remote path, which belonged to the previous host.
func (r *Registry) Add(nickname, target string) error {
	return r.AddProfile(Profile{Nickname: nickname, Destination: target})
}

// AddProfile validates and saves a profile, replacing an existing nickname.
// A changed route clears the learned remote path and the cached probe facts.
func (r *Registry) AddProfile(p Profile) error {
	if !validNickname(p.Nickname) {
		return fmt.Errorf("%w nickname %q", ErrInvalid, p.Nickname)
	}
	if !validTarget(p.Destination) {
		return fmt.Errorf("%w ssh destination %q", ErrInvalid, p.Destination)
	}
	p.applyDefaults()
	if err := p.validate(); err != nil {
		return err
	}
	if old, ok := r.profiles[p.Nickname]; ok && old.routeKey() != p.routeKey() {
		p.RemotePath = RemotePathAuto
	}
	r.profiles[p.Nickname] = p
	return r.save()
}

// SetRemotePath records the absolute path of the pluto binary on nickname's
// host.
func (r *Registry) SetRemotePath(nickname, path string) error {
	p, ok := r.profiles[nickname]
	if !ok {
		return fmt.Errorf("no device %q", nickname)
	}
	if !validRemotePath(path) {
		return fmt.Errorf("%w remote path %q", ErrInvalid, path)
	}
	p.RemotePath = path
	r.profiles[nickname] = p
	return r.save()
}

// RemotePath returns the absolute path of pluto on nickname's host. A device
// saved before the path was learned, or one still on "auto", has none.
func (r *Registry) RemotePath(nickname string) (string, bool) {
	p, ok := r.profiles[nickname]
	if !ok || p.RemotePath == "" || p.RemotePath == RemotePathAuto {
		return "", false
	}
	return p.RemotePath, true
}

// Remove deletes nickname and writes the registry.
func (r *Registry) Remove(nickname string) error {
	if _, ok := r.profiles[nickname]; !ok {
		return fmt.Errorf("no device %q", nickname)
	}
	delete(r.profiles, nickname)
	return r.save()
}

func (r *Registry) save() error {
	out := file{Version: 1, Devices: map[string]profileFile{}}
	for nickname, p := range r.profiles {
		p.applyDefaults()
		out.Devices[nickname] = profileFile{
			Destination:   p.Destination,
			Port:          p.Port,
			IdentityFile:  p.IdentityFile,
			ProxyJump:     p.ProxyJump,
			RemotePath:    p.RemotePath,
			Platform:      p.Platform,
			Auth:          p.Auth,
			Trust:         p.Trust,
			ControlMaster: p.ControlMaster,
		}
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(out); err != nil {
		return fmt.Errorf("encode %s: %w", r.path, err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return writeFileAtomic(r.path, buf.Bytes(), 0o644)
}

// applyDefaults fills the optional fields with the documented defaults.
func (p *Profile) applyDefaults() {
	if p.RemotePath == "" {
		p.RemotePath = RemotePathAuto
	}
	if p.Platform == "" {
		p.Platform = PlatformAuto
	}
	if p.Auth == "" {
		p.Auth = AuthAuto
	}
	if p.Trust == "" {
		p.Trust = TrustAcceptNew
	}
}

func (p Profile) validate() error {
	if p.Port < 0 || p.Port > 65535 {
		return fmt.Errorf("%w port %d", ErrInvalid, p.Port)
	}
	if p.IdentityFile != "" && !validPathWord(p.IdentityFile) {
		return fmt.Errorf("%w identity file %q", ErrInvalid, p.IdentityFile)
	}
	if p.ProxyJump != "" && !validTarget(p.ProxyJump) {
		return fmt.Errorf("%w proxy jump %q", ErrInvalid, p.ProxyJump)
	}
	switch p.RemotePath {
	case RemotePathAuto:
	case "":
		return fmt.Errorf("%w remote path %q", ErrInvalid, p.RemotePath)
	default:
		if !validRemotePath(p.RemotePath) {
			return fmt.Errorf("%w remote path %q", ErrInvalid, p.RemotePath)
		}
	}
	switch p.Platform {
	case PlatformAuto, PlatformLinux:
	default:
		return fmt.Errorf("%w platform %q", ErrInvalid, p.Platform)
	}
	switch p.Auth {
	case AuthAuto, AuthKey, AuthInteractive:
	default:
		return fmt.Errorf("%w auth %q", ErrInvalid, p.Auth)
	}
	switch p.Trust {
	case TrustStrict, TrustAcceptNew:
	default:
		return fmt.Errorf("%w trust %q", ErrInvalid, p.Trust)
	}
	return nil
}

// routeKey is the connection identity a learned path belongs to.
func (p Profile) routeKey() string {
	return fmt.Sprintf("%s|%d|%s|%s", p.Destination, p.Port, p.IdentityFile, p.ProxyJump)
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

// validPathWord keeps a path to one ssh argv word: whitespace or control
// characters would change the command.
func validPathWord(path string) bool {
	if path == "" {
		return false
	}
	for _, c := range path {
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

// validRemotePath keeps the learned path to "auto" or a plain absolute path:
// it becomes a remote argv word, so whitespace or control characters would
// change the command.
func validRemotePath(path string) bool {
	if path == RemotePathAuto {
		return true
	}
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for _, c := range path {
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
	return syncDir(dir)
}
