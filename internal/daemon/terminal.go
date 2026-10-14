package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/terminal"
)

// attachShell is the command a box's terminal runs. The daemon owns the PTY
// master; the shell's stdio is the PTY slave.
var attachShell = []string{"/bin/sh", "-i"}

// handleBoxAttach opens or joins a box's shared terminal and streams it as
// newline-delimited JSON. Several clients may be attached at once; any may type
// unless it asked for watch. The daemon replays recent scrollback on join and
// masks declared secrets before any client sees a byte.
//
// An attached client holds the box as shared work, so it runs alongside shared
// runs and makes an exclusive run wait (ADR 0018).
func (s *Server) handleBoxAttach(w http.ResponseWriter, r *http.Request) {
	m, id, ok := s.boxManagerFor(w, r)
	if !ok {
		return
	}
	box, err := s.store.Box(id)
	if err != nil {
		s.writeBoxLookupError(w, err)
		return
	}
	project, err := contract.LoadProject(box.WorkspacePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Resolve the project's secrets before the terminal starts, so an attach
	// that cannot mask its declared values fails closed rather than leaking.
	masker, err := projectMasker(project, box.WorkspacePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := m.Start(r.Context(), id); err != nil {
		s.writeBoxLookupError(w, err)
		return
	}
	release, err := m.AcquireShared(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer release()

	watch := isWatch(r)
	start := func(ctx context.Context, slave io.ReadWriteCloser) (int, error) {
		res, execErr := m.Exec(ctx, id, container.ExecSpec{Argv: attachShell, TTY: true, Stdin: slave}, slave)
		if execErr != nil {
			return 1, execErr
		}
		return res.ExitCode, nil
	}
	replay, sub, err := s.terminals.Attach(id, start, masker, watch)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer sub.Close()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flush, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	emit := func(event api.TerminalEvent) {
		_ = enc.Encode(event)
		if flush != nil {
			flush.Flush()
		}
	}
	if len(replay) > 0 {
		emit(api.TerminalEvent{Type: api.TerminalOutput, Data: replay})
	}
	if !watch {
		go copyTerminalInput(r.Body, sub)
	}
	for {
		select {
		case frame, open := <-sub.Output():
			if !open {
				emit(api.TerminalEvent{Type: api.TerminalExit, ExitCode: sub.ExitCode()})
				return
			}
			emit(api.TerminalEvent{Type: api.TerminalOutput, Data: frame})
		case <-r.Context().Done():
			return
		}
	}
}

// copyTerminalInput relays a client's keystrokes to the terminal until the
// request body ends.
func copyTerminalInput(body io.Reader, sub *terminal.Subscription) {
	buf := make([]byte, 4096)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, writeErr := sub.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// isWatch reports whether an attach asked for the read-only watch mode.
func isWatch(r *http.Request) bool {
	switch r.URL.Query().Get("watch") {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
