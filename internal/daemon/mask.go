package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/mask"
)

// projectMasker builds the one host-side masker for a project: its explicit
// mask literals plus every declared secret resolved from the host. Every
// host-side output surface draws from it — a run's retained log and live
// stream here, and attach/watch (#144) next.
//
// A secret backed by an unset host environment variable contributes nothing:
// there is no value to redact. A declared secret file that cannot be read is an
// error, so a run fails closed rather than stream a value it cannot mask.
func projectMasker(p *contract.Project, workspace string) (*mask.Masker, error) {
	m := mask.New(p.Mask...)
	names := make([]string, 0, len(p.Secrets))
	for name := range p.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		secret := p.Secrets[name]
		switch {
		case secret.Env != "":
			if value, ok := os.LookupEnv(secret.Env); ok {
				m.Register(value)
			}
		case secret.File != "":
			path := secret.File
			if !filepath.IsAbs(path) {
				path = filepath.Join(workspace, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("secret %s: %w", name, err)
			}
			// A trailing newline is a file-ending convention, not part of the
			// value, so trim it before registering.
			m.Register(strings.TrimRight(string(data), "\r\n"))
		}
	}
	return m, nil
}
