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

// resolvedProject is a project's host-side execution context: the one masker
// every output surface draws from, and the declared secrets to inject into a
// task's environment under their declared names.
type resolvedProject struct {
	mask    *mask.Masker
	secrets map[string]string
}

// resolveProject builds the host-side context for a project: it registers the
// explicit mask prefixes and every declared secret's resolved value with one
// masker, and returns the secrets for injection into a task's exec environment.
//
// A secret backed by an unset host environment variable contributes nothing:
// there is no value to redact or inject. A declared secret file that cannot be
// read is an error, so a run fails closed rather than stream or inject a value
// it cannot mask.
func resolveProject(p *contract.Project, workspace string) (resolvedProject, error) {
	secrets, err := projectSecrets(p, workspace)
	if err != nil {
		return resolvedProject{}, err
	}
	m := mask.New()
	for _, prefix := range p.Mask {
		m.RegisterPrefix(prefix)
	}
	for _, name := range secretNames(p) {
		if value, ok := secrets[name]; ok {
			m.Register(value)
		}
	}
	return resolvedProject{mask: m, secrets: secrets}, nil
}

// projectMasker builds the one host-side masker for a project. Every host-side
// output surface draws from it — a run's retained log and live stream here, and
// attach/watch (#144) next.
func projectMasker(p *contract.Project, workspace string) (*mask.Masker, error) {
	resolved, err := resolveProject(p, workspace)
	if err != nil {
		return nil, err
	}
	return resolved.mask, nil
}

// projectSecrets resolves a project's declared secrets to their values, keyed
// by the declared name. An environment variable that is unset contributes
// nothing. A secret file's trailing newline is a file-ending convention, not
// part of the value, so it is trimmed. A file that cannot be read is an error.
func projectSecrets(p *contract.Project, workspace string) (map[string]string, error) {
	values := make(map[string]string, len(p.Secrets))
	for _, name := range secretNames(p) {
		secret := p.Secrets[name]
		switch {
		case secret.Env != "":
			if value, ok := os.LookupEnv(secret.Env); ok {
				values[name] = value
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
			values[name] = strings.TrimRight(string(data), "\r\n")
		}
	}
	return values, nil
}

// secretNames returns a project's declared secret names in stable order, so
// resolution and registration are deterministic.
func secretNames(p *contract.Project) []string {
	names := make([]string, 0, len(p.Secrets))
	for name := range p.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
