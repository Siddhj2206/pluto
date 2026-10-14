// Package terminal owns a box's host-terminal. The daemon allocates one PTY
// master per box, hands its slave into the container as a process's stdio, keeps
// a bounded scrollback ring (~128 KB), and fans output out to any number of
// subscribers. A subscriber may type (attach) or be read-only (watch); every
// client that joins replays the retained scrollback first (ADR 0016).
//
// The PTY is a seam: production uses a kernel pty (Open), tests drive an
// in-memory pair (PipeOpener), and the box itself is reached through the
// Starter callback, so the whole multiplexer is testable without a container.
package terminal

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/Siddhj2206/pluto/internal/mask"
)

// DefaultScrollback bounds a session's scrollback ring unless overridden.
const DefaultScrollback = 128 << 10

// subscriptionBuffer is how many output frames a client may fall behind before
// its stream is dropped. A lagging reader must never stall the terminal or the
// other clients.
const subscriptionBuffer = 256

// readChunk is the size of one read from the terminal master.
const readChunk = 32 * 1024

// PTY is one host terminal pair the daemon owns. Master is the daemon's side:
// reads are the program's output and writes are keyboard input. Slave is handed
// into the box as the process's stdio.
type PTY interface {
	Master() io.ReadWriteCloser
	Slave() io.ReadWriteCloser
	Close() error
}

// Opener allocates a host terminal pair. It is a seam so tests can drive the
// same verbs with an in-memory pair instead of a kernel pty.
type Opener func() (PTY, error)

// Starter runs the box's terminal process with slave as its stdio, returning
// when the process exits. It is the daemon's seam to the box; the returned code
// is the process's exit code.
type Starter func(ctx context.Context, slave io.ReadWriteCloser) (int, error)

// Hub owns one terminal per box, created on first attach and kept until the box
// stops or the daemon restarts. It is safe for concurrent use.
type Hub struct {
	mu         sync.Mutex
	sessions   map[string]*Session
	open       Opener
	scrollback int
}

// NewHub returns a hub that allocates terminals with open and keeps the default
// scrollback bound.
func NewHub(open Opener) *Hub { return NewHubWithScrollback(open, DefaultScrollback) }

// NewHubWithScrollback is NewHub with an explicit scrollback bound in bytes.
func NewHubWithScrollback(open Opener, scrollback int) *Hub {
	if scrollback <= 0 {
		scrollback = DefaultScrollback
	}
	return &Hub{sessions: make(map[string]*Session), open: open, scrollback: scrollback}
}

// Attach joins boxID's terminal, starting it with start on first use. It
// returns the retained scrollback to replay before live output and a
// subscription for subsequent frames. masker redacts the stream (nil leaves it
// unmasked). A watch subscription may not type.
func (h *Hub) Attach(boxID string, start Starter, masker *mask.Masker, watch bool) (replay []byte, sub *Subscription, err error) {
	h.mu.Lock()
	sess := h.sessions[boxID]
	if sess == nil || sess.Ended() {
		pty, openErr := h.open()
		if openErr != nil {
			h.mu.Unlock()
			return nil, nil, openErr
		}
		sess = newSession(pty, masker, NewScrollback(h.scrollback))
		h.sessions[boxID] = sess
		sess.run(start)
	}
	h.mu.Unlock()

	replay, sub = sess.join(watch)
	return replay, sub, nil
}

// Close ends and forgets a box's terminal. Call it when the box stops or is
// removed; a later Attach starts a fresh terminal.
func (h *Hub) Close(boxID string) {
	h.mu.Lock()
	sess := h.sessions[boxID]
	delete(h.sessions, boxID)
	h.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
}

// CloseAll ends every terminal. Call it when the daemon shuts down.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	sessions := h.sessions
	h.sessions = make(map[string]*Session)
	h.mu.Unlock()
	for _, sess := range sessions {
		sess.Close()
	}
}

// Session is one box's terminal: a PTY master, a bounded scrollback ring, and
// the subscribers reading it.
type Session struct {
	pty  PTY
	mask *mask.Masker
	ring *Scrollback

	cancel      context.CancelFunc
	starterDone chan struct{}
	closeOnce   sync.Once

	writeMu sync.Mutex

	mu      sync.Mutex
	subs    map[*Subscription]struct{}
	ended   bool
	exit    int
	exitErr error
}

func newSession(pty PTY, masker *mask.Masker, ring *Scrollback) *Session {
	return &Session{pty: pty, mask: masker, ring: ring, subs: make(map[*Subscription]struct{}), starterDone: make(chan struct{})}
}

// run starts the terminal process and the output pump. It must be called once.
func (s *Session) run(start Starter) {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() {
		code, err := start(ctx, s.pty.Slave())
		s.mu.Lock()
		s.exit, s.exitErr = code, err
		s.mu.Unlock()
		close(s.starterDone)
		// Closing the PTY unblocks the pump's read, so the stream ends when
		// the process does.
		_ = s.pty.Close()
	}()
	go s.pump(ctx)
}

// pump reads the master's output into the scrollback and every subscriber
// until the terminal ends, then releases any masked tail and ends the stream.
func (s *Session) pump(ctx context.Context) {
	master := s.pty.Master()
	buf := make([]byte, readChunk)
	for {
		n, err := master.Read(buf)
		if n > 0 {
			s.publish(buf[:n])
		}
		if err != nil {
			break
		}
	}
	s.flush()
	select {
	case <-s.starterDone:
	case <-ctx.Done():
	}
	s.closeSubs()
}

// publish masks a chunk of output and delivers the safe prefix.
func (s *Session) publish(p []byte) {
	if s.mask == nil {
		// The read buffer is reused, so the frame must own its bytes.
		s.deliver(append([]byte(nil), p...))
		return
	}
	s.deliver(s.mask.Mask(p))
}

// flush releases the masker's held-back tail when the stream ends, so the final
// bytes are published rather than dropped.
func (s *Session) flush() {
	if s.mask != nil {
		s.deliver(s.mask.Flush())
	}
}

// deliver appends already-safe bytes to the scrollback and fans them out.
func (s *Session) deliver(out []byte) {
	if len(out) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ring.Write(out)
	for sub := range s.subs {
		select {
		case sub.out <- out:
		default:
			s.dropSubLocked(sub)
		}
	}
}

// join registers a subscription and snapshots the scrollback. Snapshot and
// registration are one critical section, so a frame is either in the replay or
// delivered live, never both and never neither.
func (s *Session) join(watch bool) ([]byte, *Subscription) {
	sub := &Subscription{sess: s, watch: watch, out: make(chan []byte, subscriptionBuffer)}
	s.mu.Lock()
	defer s.mu.Unlock()
	replay := s.ring.Bytes()
	if s.ended {
		sub.closeOut()
	} else {
		s.subs[sub] = struct{}{}
	}
	return replay, sub
}

// write sends keyboard input to the terminal.
func (s *Session) write(p []byte) (int, error) {
	s.mu.Lock()
	ended := s.ended
	s.mu.Unlock()
	if ended {
		return 0, io.ErrClosedPipe
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.pty.Master().Write(p)
}

// Ended reports whether the terminal has ended.
func (s *Session) Ended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// removeSub drops a subscription that left before the terminal ended.
func (s *Session) removeSub(sub *Subscription) {
	s.mu.Lock()
	delete(s.subs, sub)
	s.mu.Unlock()
	sub.closeOut()
}

// dropSubLocked is removeSub for a caller already holding s.mu.
func (s *Session) dropSubLocked(sub *Subscription) {
	delete(s.subs, sub)
	sub.closeOut()
}

// closeSubs ends every subscriber's stream exactly once.
func (s *Session) closeSubs() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.ended = true
		subs := s.subs
		s.subs = nil
		s.mu.Unlock()
		for sub := range subs {
			sub.closeOut()
		}
	})
}

// Close ends the terminal now: cancel the process, close the PTY, and end every
// client's stream. Used when the box stops or the daemon shuts down.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	_ = s.pty.Close()
	s.closeSubs()
}

// Subscription is one client's view of a terminal: a channel of output frames,
// the ability to type unless it is a watch, and the terminal's exit code.
type Subscription struct {
	sess  *Session
	watch bool
	out   chan []byte

	closeOnce sync.Once
}

// Output is the client's stream of output frames. It closes when the terminal
// ends or the subscription is closed.
func (sub *Subscription) Output() <-chan []byte { return sub.out }

// Write sends keystrokes to the terminal. A watch subscription is read-only.
func (sub *Subscription) Write(p []byte) (int, error) {
	if sub.watch {
		return 0, errors.New("watch is read-only")
	}
	return sub.sess.write(p)
}

// ExitCode reports the terminal process's exit code once its stream has ended.
func (sub *Subscription) ExitCode() int {
	sub.sess.mu.Lock()
	defer sub.sess.mu.Unlock()
	return sub.sess.exit
}

// Err reports the terminal process's error, if any.
func (sub *Subscription) Err() error {
	sub.sess.mu.Lock()
	defer sub.sess.mu.Unlock()
	return sub.sess.exitErr
}

// Close leaves the terminal. The underlying session keeps running for other
// clients and for a later rejoin.
func (sub *Subscription) Close() { sub.sess.removeSub(sub) }

func (sub *Subscription) closeOut() { sub.closeOnce.Do(func() { close(sub.out) }) }

// Scrollback is a bounded ring of recent terminal output. It is not safe for
// concurrent use; a Session serialises it under its own lock.
type Scrollback struct {
	data []byte
	max  int
}

// NewScrollback returns a ring holding at most max bytes.
func NewScrollback(max int) *Scrollback { return &Scrollback{max: max} }

// Write appends p, dropping the oldest bytes past the bound.
func (b *Scrollback) Write(p []byte) {
	if b.max <= 0 || len(p) == 0 {
		return
	}
	if len(p) >= b.max {
		b.data = append(b.data[:0], p[len(p)-b.max:]...)
		return
	}
	b.data = append(b.data, p...)
	if len(b.data) > b.max {
		b.data = append(b.data[:0], b.data[len(b.data)-b.max:]...)
	}
}

// Bytes returns a copy of the retained output, oldest first.
func (b *Scrollback) Bytes() []byte { return append([]byte(nil), b.data...) }
