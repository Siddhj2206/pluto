package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Siddhj2206/pluto/internal/container"
)

// Workspace is a repo checkout on a host, identified by its path. A workspace
// owns exactly one durable box (ADR 0015).
type Workspace struct {
	Schema    int       `json:"schema"`
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Repo      string    `json:"repo,omitempty"`
	BoxID     string    `json:"box_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Layer is a content-addressed filesystem tier a box's root is composed from
// (ADR 0017). Kind is the container seam's base or package kind.
type Layer struct {
	Kind container.LayerKind `json:"kind"`
	Key  string              `json:"key"`
}

// Volume is durable per-box storage mounted outside the layer stack: the
// workspace or the user home. Kind is the container seam's volume kind.
type Volume struct {
	Kind container.VolumeKind `json:"kind"`
	Name string               `json:"name"`
}

// EnsureWorkspace creates or returns the workspace record for a checkout path.
// Repeated calls for the same path reuse one record; a newer repo identity
// updates it in place.
func (s *Store) EnsureWorkspace(path, repo string) (*Workspace, error) {
	if path == "" {
		return nil, errors.New("workspace path is required")
	}
	path = filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaces, err := s.listWorkspacesLocked()
	if err != nil {
		return nil, err
	}
	for _, w := range workspaces {
		if w.Path != path {
			continue
		}
		if repo != "" && w.Repo != repo {
			w.Repo = repo
			w.UpdatedAt = time.Now().UTC()
			if err := s.writeWorkspace(w); err != nil {
				return nil, err
			}
		}
		return w, nil
	}
	now := time.Now().UTC()
	w := &Workspace{Schema: RecordSchema, ID: newID(), Path: path, Repo: repo, CreatedAt: now, UpdatedAt: now}
	if err := s.writeWorkspace(w); err != nil {
		return nil, err
	}
	return w, nil
}

// WorkspaceByPath returns the workspace for a checkout path.
func (s *Store) WorkspaceByPath(path string) (*Workspace, error) {
	path = filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	workspaces, err := s.listWorkspacesLocked()
	if err != nil {
		return nil, err
	}
	for _, w := range workspaces {
		if w.Path == path {
			return w, nil
		}
	}
	return nil, ErrNotFound
}

// Workspaces returns all workspace records, sorted by creation time.
func (s *Store) Workspaces() ([]*Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listWorkspacesLocked()
}

// SetWorkspaceBox points a workspace at its one durable box.
func (s *Store) SetWorkspaceBox(id, boxID string) error {
	if !ValidID(boxID) {
		return fmt.Errorf("invalid box id %q", boxID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.readWorkspace(s.workspacePath(id))
	if err != nil {
		return err
	}
	w.BoxID = boxID
	w.UpdatedAt = time.Now().UTC()
	return s.writeWorkspace(w)
}

// CreateContainerBox creates a container box for a workspace path, or returns
// the existing one. A workspace resolves to exactly one box, so create is
// idempotent per workspace path. The record carries no branch or worktree.
func (s *Store) CreateContainerBox(workspacePath, repo, revision string) (*Box, bool, error) {
	if workspacePath == "" {
		return nil, false, errors.New("workspace path is required")
	}
	workspacePath = filepath.Clean(workspacePath)
	s.mu.Lock()
	defer s.mu.Unlock()

	boxes, _, err := s.listLocked()
	if err != nil {
		return nil, false, err
	}
	for _, existing := range boxes {
		if existing.WorkspacePath == workspacePath {
			return existing, false, nil
		}
	}
	now := time.Now().UTC()
	box := &Box{
		Schema:        RecordSchema,
		ID:            newID(),
		WorkspacePath: workspacePath,
		Repo:          repo,
		Revision:      revision,
		State:         StateCreated,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	dir := s.boxDir(box.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, fmt.Errorf("create box dir: %w", err)
	}
	if err := s.writeBox(box); err != nil {
		os.RemoveAll(dir)
		return nil, false, err
	}
	return box, true, nil
}

// SetBoxRuntime records a container box's engine handle and the layers and
// volumes it was composed from.
func (s *Store) SetBoxRuntime(id, engineID string, layers []Layer, volumes []Volume) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		box.EngineID = engineID
		box.Layers = layers
		box.Volumes = volumes
		return nil
	})
}

func (s *Store) workspacesDir() string          { return filepath.Join(s.root, "workspaces") }
func (s *Store) workspacePath(id string) string { return filepath.Join(s.workspacesDir(), id+".json") }

func (s *Store) listWorkspacesLocked() ([]*Workspace, error) {
	entries, err := os.ReadDir(s.workspacesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read workspaces dir: %w", err)
	}
	var workspaces []*Workspace
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		w, err := readWorkspace(filepath.Join(s.workspacesDir(), e.Name()))
		if err != nil {
			continue
		}
		workspaces = append(workspaces, w)
	}
	sort.Slice(workspaces, func(i, j int) bool { return workspaces[i].CreatedAt.Before(workspaces[j].CreatedAt) })
	return workspaces, nil
}

func (s *Store) readWorkspace(path string) (*Workspace, error) {
	w, err := readWorkspace(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return w, err
}

func (s *Store) writeWorkspace(w *Workspace) error {
	if err := os.MkdirAll(s.workspacesDir(), 0o755); err != nil {
		return fmt.Errorf("create workspaces dir: %w", err)
	}
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workspace record: %w", err)
	}
	return writeFileAtomic(s.workspacePath(w.ID), append(data, '\n'), 0o644)
}

// readWorkspace reads and validates a workspace record from an explicit path.
func readWorkspace(path string) (*Workspace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w Workspace
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if w.Schema != RecordSchema {
		return nil, fmt.Errorf("%s: unsupported record schema %d", path, w.Schema)
	}
	if !ValidID(w.ID) {
		return nil, fmt.Errorf("%s: invalid workspace id %q", path, w.ID)
	}
	return &w, nil
}
