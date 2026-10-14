package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Ephemeral boxes are the fresh boxes a manual CI trigger runs in (#148). A
// workspace keeps exactly one durable box; an ephemeral box is a separate
// record created per CI run, seeded from the workspace's package layer and
// mounting no durable volumes, so CI can never read or mutate a durable box's
// volumes. It is discarded on success and retained for inspection on failure.

// CreateEphemeralBox creates a fresh ephemeral box for one CI run. Unlike
// CreateContainerBox it never reuses an existing record: each CI run gets its
// own box. The record is not linked to a workspace, which owns only its one
// durable box.
func (s *Store) CreateEphemeralBox(workspacePath, repo, revision string) (*Box, error) {
	if workspacePath == "" {
		return nil, errors.New("workspace path is required")
	}
	workspacePath = filepath.Clean(workspacePath)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	box := &Box{
		Schema:        RecordSchema,
		ID:            newID(),
		WorkspacePath: workspacePath,
		Repo:          repo,
		Revision:      revision,
		Ephemeral:     true,
		State:         StateCreated,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	dir := s.boxDir(box.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create box dir: %w", err)
	}
	if err := s.writeBox(box); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return box, nil
}

// SetRetained marks an ephemeral box retained at at: its failed run finished
// and the box was kept for inspection rather than discarded.
func (s *Store) SetRetained(id string, at time.Time) (*Box, error) {
	when := at.UTC()
	return s.mutate(id, func(box *Box) error {
		if !box.Ephemeral {
			return fmt.Errorf("box %s is not ephemeral", box.ID)
		}
		box.RetainedAt = &when
		return nil
	})
}

// SetPinned exempts a box from retention eviction, or lifts the exemption.
func (s *Store) SetPinned(id string, pinned bool) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		box.Pinned = pinned
		return nil
	})
}

// RemoveWorkspace drops a workspace record whose checkout is gone, so a
// dangling box link does not outlive the box it pointed at.
func (s *Store) RemoveWorkspace(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid workspace id %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.workspacePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove workspace record %s: %w", id, err)
	}
	return nil
}
