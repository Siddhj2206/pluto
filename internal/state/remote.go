package state

import "strings"

// Remote is one git remote mirrored from the host worktree into a box: its
// name, fetch URL, and any push URLs that differ from the fetch URL. It is
// the durable record of the box's relationship to its repository's remotes;
// an empty list means the worktree had no remotes and the box is local-only.
type Remote struct {
	Name  string   `json:"name"`
	Fetch string   `json:"fetch"`
	Push  []string `json:"push,omitempty"`
}

// IsSSH reports whether the remote's fetch URL is SSH-shaped: an ssh:// URL or
// an scp-like git@host:path. SSH remotes are mirrored but pushing over them is
// out of scope until M4 (ADR 0008); the sanctioned private path is an HTTPS
// remote with a token from [env].
func (r Remote) IsSSH() bool {
	return isSSHURL(r.Fetch)
}

func isSSHURL(url string) bool {
	if strings.HasPrefix(url, "ssh://") {
		return true
	}
	// scp-like syntax: user@host:path, with no scheme separator.
	return strings.Contains(url, "@") &&
		strings.Contains(url, ":") &&
		!strings.Contains(url, "://")
}

// TrackedRemote picks the remote the checked-out branch should track: origin
// when it exists, otherwise the sole remote. ok is false when the choice is
// ambiguous — several remotes and none named origin — or when there are no
// remotes at all. In the ambiguous case the branch is left untracked.
func TrackedRemote(remotes []Remote) (Remote, bool) {
	if len(remotes) == 0 {
		return Remote{}, false
	}
	for _, r := range remotes {
		if r.Name == "origin" {
			return r, true
		}
	}
	if len(remotes) == 1 {
		return remotes[0], true
	}
	return Remote{}, false
}

// SetRemotes records the host worktree's remotes as mirrored into the box, so
// `pluto status` can report them while the box is paused (the agent mirrors at
// first sync, but status must not need a live box). An empty list is valid and
// means local-only.
func (s *Store) SetRemotes(id string, remotes []Remote) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		box.Remotes = remotes
		return nil
	})
}
