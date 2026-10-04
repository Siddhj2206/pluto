package state

import "time"

// SetAutoPause records the idle window the daemon evaluated for a box, so
// `pluto status` can report it without re-reading the contract.
func (s *Store) SetAutoPause(id, setting string) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		box.AutoPause = setting
		return nil
	})
}

// SetIdleSince records when the box was last observed idle; nil clears the
// clock (a client attached or a job started).
func (s *Store) SetIdleSince(id string, since *time.Time) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		box.IdleSince = since
		return nil
	})
}
