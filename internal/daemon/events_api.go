package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/events"
)

// DefaultEventLimit is how many events a read returns when the caller does
// not ask for a bound. MaxEventLimit protects the daemon from an unbounded
// read; a caller that wants more pages by sequence.
const (
	DefaultEventLimit = 100
	MaxEventLimit     = 1000
	// eventHeartbeat is how often an idle event stream writes an SSE comment
	// so a proxy or client can tell the connection is alive.
	eventHeartbeat = 15 * time.Second
)

// handleEventList serves the read-only ledger listing over the unix socket.
// Events are newest first; filters are exact box/trace matches and a kind
// prefix.
func (s *Server) handleEventList(w http.ResponseWriter, r *http.Request) {
	if s.Events == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("event ledger is not enabled"))
		return
	}
	filter, err := s.eventFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	list, err := s.Events.List(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if list == nil {
		list = []events.Event{}
	}
	writeJSON(w, http.StatusOK, api.EventListResponse{SchemaVersion: events.Schema, Events: list})
}

// handleEventStream serves the ledger as server-sent events. With a since
// value it replays matching history first; without one it starts at the
// current head and only sends new events.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if s.Events == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("event ledger is not enabled"))
		return
	}
	filter, err := s.eventFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Subscribe before reading history so an event appended between the two
	// is either replayed or delivered live, never lost.
	live, cancel := s.Events.Subscribe()
	defer cancel()
	head := s.Events.Head()

	lastSeq := uint64(0)
	if r.URL.Query().Get("since") != "" {
		history := filter
		history.Limit = 0
		replayed, err := s.Events.List(history)
		if err != nil {
			return
		}
		for i := len(replayed) - 1; i >= 0; i-- {
			if err := writeSSE(w, flusher, replayed[i]); err != nil {
				return
			}
			lastSeq = replayed[i].Seq
		}
	}
	if head > lastSeq {
		lastSeq = head
	}
	if err := writeSSEComment(w, flusher, "ready"); err != nil {
		return
	}

	heartbeat := time.NewTicker(eventHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-live:
			if !ok {
				return
			}
			if e.Seq <= lastSeq || !filter.Match(e) {
				continue
			}
			if err := writeSSE(w, flusher, e); err != nil {
				return
			}
			lastSeq = e.Seq
		case <-heartbeat.C:
			if err := writeSSEComment(w, flusher, "heartbeat"); err != nil {
				return
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, e events.Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEComment(w http.ResponseWriter, flusher http.Flusher, text string) error {
	if _, err := fmt.Fprintf(w, ": %s\n\n", text); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// eventFilter reads the shared list/stream query: since, limit, box, kind
// (prefix), and trace.
func (s *Server) eventFilter(r *http.Request) (events.Filter, error) {
	q := r.URL.Query()
	filter, err := events.ParseSince(q.Get("since"), s.now())
	if err != nil {
		return events.Filter{}, err
	}
	filter.BoxID = q.Get("box")
	filter.KindPrefix = q.Get("kind")
	filter.TraceID = q.Get("trace")
	filter.Limit = DefaultEventLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return events.Filter{}, fmt.Errorf("limit must be a positive integer")
		}
		if n > MaxEventLimit {
			n = MaxEventLimit
		}
		filter.Limit = n
	}
	return filter, nil
}
