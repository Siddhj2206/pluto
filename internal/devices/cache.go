package devices

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// CacheSchemaVersion is the shape of the per-device cache JSON.
const CacheSchemaVersion = 1

// Device status values shown by `device ls` and the connection card.
const (
	StatusConnected  = "connected"
	StatusUnverified = "unverified"
	StatusOffline    = "offline"
)

// Resolved is the route OpenSSH reported for a profile. It is display and
// diagnostic metadata only; the profile keeps pointing at the alias.
type Resolved struct {
	Destination  string `json:"destination"`
	Host         string `json:"host"`
	User         string `json:"user"`
	Port         int    `json:"port"`
	ProxyJump    string `json:"proxy_jump,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
}

// Remote describes the host the probe found.
type Remote struct {
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Shell        string   `json:"shell"`
	Path         string   `json:"path,omitempty"`
	PathSource   string   `json:"path_source,omitempty"`
	Version      string   `json:"version,omitempty"`
	Protocol     int      `json:"protocol,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Legacy       bool     `json:"legacy,omitempty"`
}

// Daemon records the last daemon reachability check.
type Daemon struct {
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

// Cache is the disposable per-device record. It holds no secrets: no
// passwords, keys, tokens, or environment values.
type Cache struct {
	SchemaVersion int        `json:"schema_version"`
	Nickname      string     `json:"nickname"`
	CheckedAt     time.Time  `json:"checked_at"`
	Status        string     `json:"status"`
	Resolved      Resolved   `json:"resolved"`
	Remote        Remote     `json:"remote"`
	Daemon        Daemon     `json:"daemon"`
	LastOK        *time.Time `json:"last_ok,omitempty"`
	LastError     *string    `json:"last_error,omitempty"`
}

// CacheDir is where per-device cache files live:
// $XDG_STATE_HOME/pluto/devices on unix and %LocalAppData%\pluto\devices on
// Windows. PLUTO_DEVICE_CACHE_DIR overrides it (used by tests and unusual
// layouts).
func CacheDir() (string, error) {
	if dir := os.Getenv("PLUTO_DEVICE_CACHE_DIR"); dir != "" {
		return dir, nil
	}
	if runtime.GOOS == "windows" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("find local app data: %w", err)
		}
		return filepath.Join(dir, "pluto", "devices"), nil
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "pluto", "devices"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "pluto", "devices"), nil
}

// CachePath is the JSON cache file for nickname under dir.
func CachePath(dir, nickname string) string {
	return filepath.Join(dir, nickname+".json")
}

// LoadCache reads nickname's cache. A missing file is an empty cache; a
// malformed one is an error so a corrupt cache is never mistaken for a fresh
// device.
func LoadCache(dir, nickname string) (Cache, error) {
	data, err := os.ReadFile(CachePath(dir, nickname))
	if os.IsNotExist(err) {
		return Cache{SchemaVersion: CacheSchemaVersion, Nickname: nickname, Status: StatusUnverified}, nil
	}
	if err != nil {
		return Cache{}, fmt.Errorf("read device cache: %w", err)
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}, fmt.Errorf("decode device cache %s: %w", CachePath(dir, nickname), err)
	}
	if c.Nickname == "" {
		c.Nickname = nickname
	}
	if c.Status == "" {
		c.Status = StatusUnverified
	}
	return c, nil
}

// Save writes nickname's cache atomically.
func (c Cache) Save(dir string) error {
	c.SchemaVersion = CacheSchemaVersion
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode device cache: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create device cache dir: %w", err)
	}
	return writeFileAtomic(CachePath(dir, c.Nickname), data, 0o644)
}

// RemoveCache deletes nickname's cache, ignoring a missing file.
func RemoveCache(dir, nickname string) error {
	err := os.Remove(CachePath(dir, nickname))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove device cache: %w", err)
	}
	return nil
}
