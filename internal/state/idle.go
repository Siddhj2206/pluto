package state

import "time"

// SetIdleSince records when the box was last observed idle; nil clears the
// clock (an attach session or a run started).
func (s *Store) SetIdleSince(id string, since *time.Time) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		box.IdleSince = since
		return nil
	})
}
