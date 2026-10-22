// Package events owns pluto's durable activity ledger: an append-only JSONL
// record of what the host daemon decided, where, when, and why.
//
// One event is one JSON object per line. A single writer (the daemon) assigns
// the monotonic sequence, so seq — not the timestamp — is the ordering key.
// The ledger lives under the state directory in rotating segments and is
// bounded by age and event count. It is read only over the daemon's unix
// socket; there is no network listener.
//
// Redaction is by construction: Event has no field for a payload body, an
// environment value, a token, or a secret. Callers record small facts in
// Attrs and never pass secret material.
package events

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Schema is the event record schema version.
const Schema = 1

// Ledger defaults. They are deliberately modest: the ledger is a debugging
// record, not a metrics store.
const (
	// DefaultMaxAge keeps roughly a month of activity.
	DefaultMaxAge = 30 * 24 * time.Hour
	// DefaultMaxEvents bounds the ledger by count as well as age.
	DefaultMaxEvents = 1_000_000
	// DefaultSegmentBytes rotates a segment at 64 MiB.
	DefaultSegmentBytes int64 = 64 << 20
	// RetentionEnv overrides the defaults: comma-separated "age=<duration>"
	// and "count=<n>", for example PLUTO_EVENT_RETENTION=age=720h,count=1000000.
	// An empty value keeps the defaults; "age=0" or "count=0" disables that
	// bound.
	RetentionEnv = "PLUTO_EVENT_RETENTION"
	// subscriberBuffer is how many live events one stream may fall behind.
	subscriberBuffer = 512
)

// Levels classify an event's importance. Human logs stay the detailed
// evidence; the level is only a filter for the ledger.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Event kinds (schema 1). Later tickets extend this vocabulary; the CLI and
// the daemon filter by prefix, so an unknown kind stays readable.
const (
	KindDaemonStarted  = "daemon.started"
	KindDaemonStopped  = "daemon.stopped"
	KindDaemonReloaded = "daemon.reloaded"
	KindDaemonError    = "daemon.error"

	KindBoxCreated      = "box.created"
	KindBoxWakeStarted  = "box.wake.started"
	KindBoxWakeDone     = "box.wake.done"
	KindBoxPauseStarted = "box.pause.started"
	KindBoxPauseDone    = "box.pause.done"
	KindBoxFailed       = "box.failed"
	KindBoxDestroyed    = "box.destroyed"

	KindPhaseProvisionStarted = "phase.provision.started"
	KindPhaseProvisionDone    = "phase.provision.done"
	KindPhaseProvisionFailed  = "phase.provision.failed"
	KindPhaseWakeStarted      = "phase.wake.started"
	KindPhaseWakeDone         = "phase.wake.done"
	KindPhaseWakeFailed       = "phase.wake.failed"

	KindSessionStarted  = "session.started"
	KindSessionAttached = "session.attached"
	KindSessionDetached = "session.detached"
	KindSessionStopped  = "session.stopped"

	KindJobQueued    = "job.queued"
	KindJobStarted   = "job.started"
	KindJobDone      = "job.done"
	KindJobFailed    = "job.failed"
	KindJobCancelled = "job.cancelled"

	KindQueueAccepted   = "queue.accepted"
	KindQueueRejected   = "queue.rejected"
	KindQueueDispatched = "queue.dispatched"
	KindQueueBlocked    = "queue.blocked"
	KindQueueRecovered  = "queue.recovered"

	KindTaskCreated    = "task.created"
	KindTaskRunQueued  = "task.run.queued"
	KindTaskRunStarted = "task.run.started"
	KindTaskRunDone    = "task.run.done"
	KindTaskRunFailed  = "task.run.failed"
	KindTaskRunBlocked = "task.run.blocked"

	KindScheduleArmed    = "schedule.armed"
	KindScheduleFired    = "schedule.fired"
	KindScheduleSkipped  = "schedule.skipped"
	KindScheduleFailed   = "schedule.failed"
	KindScheduleCaughtUp = "schedule.caught_up"

	KindEventReceived = "event.received"
	KindEventVerified = "event.verified"
	KindEventRejected = "event.rejected"
	KindEventAdmitted = "event.admitted"
	KindEventDeduped  = "event.deduped"
	KindEventIgnored  = "event.ignored"

	KindWebhookReceived = "webhook.received"
	KindWebhookVerified = "webhook.verified"
	KindWebhookRejected = "webhook.rejected"
	KindWebhookDeduped  = "webhook.deduped"
	KindWebhookIgnored  = "webhook.ignored"
	KindWebhookAdmitted = "webhook.admitted"

	KindContractApplied  = "contract.applied"
	KindContractStale    = "contract.stale"
	KindContractApproved = "contract.approved"
	KindContractRejected = "contract.rejected"

	KindImageImported   = "image.imported"
	KindProviderChanged = "provider.changed"
)

// Actor names who caused an event. Type is "cli", "daemon", "schedule",
// "github", or a later trigger class. Client is best effort and never used for
// authorization; Host is the host the daemon ran on.
type Actor struct {
	Type   string `json:"type"`
	ID     string `json:"id,omitempty"`
	Client string `json:"client,omitempty"`
	Host   string `json:"host,omitempty"`
}

// Where is the flat location of an event. Empty fields mean unknown. It is a
// flat object so a reader can filter without a schema join.
type Where struct {
	Host        string `json:"host,omitempty"`
	BoxID       string `json:"box_id,omitempty"`
	Project     string `json:"project,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Worktree    string `json:"worktree,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	QueueID     string `json:"queue_id,omitempty"`
	Schedule    string `json:"schedule,omitempty"`
	EventSource string `json:"event_source,omitempty"`
	DeliveryID  string `json:"delivery_id,omitempty"`
}

// Event is one durable decision record.
type Event struct {
	Schema     int               `json:"schema"`
	Seq        uint64            `json:"seq"`
	ID         string            `json:"id"`
	At         time.Time         `json:"at"`
	Kind       string            `json:"kind"`
	Level      string            `json:"level"`
	TraceID    string            `json:"trace_id,omitempty"`
	ParentID   string            `json:"parent_id,omitempty"`
	Actor      Actor             `json:"actor"`
	Where      Where             `json:"where"`
	Summary    string            `json:"summary"`
	DurationMS int64             `json:"duration_ms,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Attrs      map[string]string `json:"attrs,omitempty"`
}

// Fact is a lifecycle observation a component (the box runner today) reports
// to the daemon. The reporter names what happened; the daemon adds the actor,
// trace, and where from the job and box records. A Fact is never written to
// the ledger directly.
type Fact struct {
	Kind     string
	BoxID    string
	JobID    string
	Summary  string
	Reason   string
	Level    string
	Duration time.Duration
	ExitCode int
	Attrs    map[string]string

	// The job's correlation fields, copied by the reporter so the daemon does
	// not need a second store read to place the fact in its trace.
	TraceID      string
	TaskID       string
	RunID        string
	QueueID      string
	ScheduleName string
	EventSource  string
	EventID      string
}

// Filter selects events. Zero values mean "no bound". Limit applies after
// filtering, newest first.
type Filter struct {
	// AfterSeq keeps events with a sequence greater than this value.
	AfterSeq uint64
	// Since keeps events at or after this instant when non-zero.
	Since time.Time
	// BoxID, KindPrefix, and TraceID are exact/prefix matches.
	BoxID      string
	KindPrefix string
	TraceID    string
	Limit      int
}

// Match reports whether e passes the filter's predicates (not the limit).
func (f Filter) Match(e Event) bool {
	if f.AfterSeq != 0 && e.Seq <= f.AfterSeq {
		return false
	}
	if !f.Since.IsZero() && e.At.Before(f.Since) {
		return false
	}
	if f.BoxID != "" && e.Where.BoxID != f.BoxID {
		return false
	}
	if f.KindPrefix != "" && !strings.HasPrefix(e.Kind, f.KindPrefix) {
		return false
	}
	if f.TraceID != "" && e.TraceID != f.TraceID {
		return false
	}
	return true
}

// Options configures a Store. Zero values take the documented defaults; a
// negative MaxAge or MaxEvents disables that retention bound. Tests use the
// zero-value defaults and a small SegmentBytes.
type Options struct {
	MaxAge       time.Duration
	MaxEvents    int
	SegmentBytes int64
	// Now is the store's clock; nil means time.Now.
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.MaxAge == 0 {
		o.MaxAge = DefaultMaxAge
	}
	if o.MaxEvents == 0 {
		o.MaxEvents = DefaultMaxEvents
	}
	if o.SegmentBytes <= 0 {
		o.SegmentBytes = DefaultSegmentBytes
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

type segment struct {
	Name  string    `json:"name"`
	Count int       `json:"count"`
	First time.Time `json:"first,omitempty"`
	Last  time.Time `json:"last,omitempty"`
}

type indexFile struct {
	Schema   int       `json:"schema"`
	Seq      uint64    `json:"seq"`
	Segments []segment `json:"segments"`
}

// Store is one ledger directory. It is safe for concurrent use: appends and
// retention are serialized, reads scan the immutable segment files.
type Store struct {
	dir  string
	opts Options

	mu        sync.Mutex
	file      *os.File
	size      int64
	seq       uint64
	nextSeg   int
	segments  []segment
	subs      map[chan Event]struct{}
	lastPrune time.Time
	closed    bool
}

// Open opens (or creates) the ledger at dir using the retention environment
// override when present.
func Open(dir string) (*Store, error) {
	opts, err := OptionsFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	return OpenWithOptions(dir, opts)
}

// OptionsFromEnv parses PLUTO_EVENT_RETENTION. It accepts a comma-separated
// list of age=<duration> and count=<n>; unknown keys are errors so a typo does
// not silently keep everything.
func OptionsFromEnv(getenv func(string) string) (Options, error) {
	raw := strings.TrimSpace(getenv(RetentionEnv))
	if raw == "" {
		return Options{}, nil
	}
	var opts Options
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return Options{}, fmt.Errorf("%s: expected key=value, got %q", RetentionEnv, part)
		}
		switch strings.TrimSpace(key) {
		case "age":
			d, err := time.ParseDuration(strings.TrimSpace(value))
			if err != nil {
				return Options{}, fmt.Errorf("%s: age: %w", RetentionEnv, err)
			}
			if d <= 0 {
				opts.MaxAge = -1
			} else {
				opts.MaxAge = d
			}
		case "count":
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return Options{}, fmt.Errorf("%s: count must be a non-negative integer", RetentionEnv)
			}
			if n == 0 {
				opts.MaxEvents = -1
			} else {
				opts.MaxEvents = n
			}
		default:
			return Options{}, fmt.Errorf("%s: unknown key %q (want age or count)", RetentionEnv, key)
		}
	}
	return opts, nil
}

// OpenWithOptions opens (or creates) the ledger at dir with explicit options.
// An existing ledger keeps its sequence: the newest segment is scanned on
// open, so a restart continues where the last writer stopped.
func OpenWithOptions(dir string, opts Options) (*Store, error) {
	opts = opts.withDefaults()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create event dir: %w", err)
	}
	s := &Store{dir: dir, opts: opts, subs: make(map[chan Event]struct{})}
	names, err := segmentNames(dir)
	if err != nil {
		return nil, err
	}
	index, indexErr := readIndex(dir)
	trusted := indexErr == nil && sameSegments(index.Segments, names)

	if len(names) == 0 {
		// Keep the sequence monotonic even if an operator removed the segment
		// files by hand; the index still remembers the last number written.
		if indexErr == nil {
			s.seq = index.Seq
		}
		s.nextSeg = 1
		seg := segment{Name: segmentName(1)}
		if err := s.openSegment(seg); err != nil {
			return nil, err
		}
		s.segments = []segment{seg}
		if err := s.writeIndexLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}

	// The last file is the active segment and may hold appends made after the
	// index was written. Earlier files are immutable, so the index is
	// authoritative for them when it matches the directory.
	if trusted {
		s.segments = append([]segment(nil), index.Segments...)
		s.seq = index.Seq
	} else {
		for _, name := range names {
			seg, maxSeq, err := scanSegment(dir, name)
			if err != nil {
				return nil, err
			}
			s.segments = append(s.segments, seg)
			if maxSeq > s.seq {
				s.seq = maxSeq
			}
		}
	}
	for _, name := range names {
		if n := segmentNumber(name); n > s.nextSeg {
			s.nextSeg = n
		}
	}
	active := s.segments[len(s.segments)-1]
	scanned, maxSeq, err := scanSegment(dir, active.Name)
	if err != nil {
		return nil, err
	}
	active.Count, active.First, active.Last = scanned.Count, scanned.First, scanned.Last
	s.segments[len(s.segments)-1] = active
	if maxSeq > s.seq {
		s.seq = maxSeq
	}
	if err := s.openSegment(active); err != nil {
		return nil, err
	}
	s.pruneLocked(opts.Now())
	if err := s.writeIndexLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Dir is the ledger directory.
func (s *Store) Dir() string { return s.dir }

// Head is the newest assigned sequence, or zero for an empty ledger.
func (s *Store) Head() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// Append writes one event and returns it with schema, sequence, id, and time
// filled in. Sequence is assigned here, by the ledger's single writer.
func (s *Store) Append(e Event) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Event{}, errors.New("events: ledger is closed")
	}
	if s.file == nil {
		return Event{}, errors.New("events: ledger has no open segment")
	}
	now := s.opts.Now().UTC()
	if e.Schema == 0 {
		e.Schema = Schema
	}
	if e.At.IsZero() {
		e.At = now
	} else {
		e.At = e.At.UTC()
	}
	if e.ID == "" {
		e.ID = NewEventID()
	}
	if e.Level == "" {
		e.Level = LevelInfo
	}
	s.seq++
	e.Seq = s.seq

	line, err := json.Marshal(e)
	if err != nil {
		s.seq--
		return Event{}, fmt.Errorf("encode event: %w", err)
	}
	n, err := s.file.Write(append(line, '\n'))
	if err != nil {
		s.seq--
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	s.size += int64(n)
	active := &s.segments[len(s.segments)-1]
	active.Count++
	if active.First.IsZero() {
		active.First = e.At
	}
	active.Last = e.At
	s.notifyLocked(e)

	if s.size >= s.opts.SegmentBytes {
		if err := s.rotateLocked(); err != nil {
			return e, err
		}
		// A rotation is the natural moment to enforce retention: the closed
		// segment can be deleted whole.
		s.pruneLocked(now)
	} else if now.Sub(s.lastPrune) >= time.Minute {
		s.pruneLocked(now)
	}
	return e, nil
}

// List returns matching events newest first. It scans immutable segment files
// from newest to oldest and stops once Limit is reached.
func (s *Store) List(f Filter) ([]Event, error) {
	s.mu.Lock()
	segments := append([]segment(nil), s.segments...)
	s.mu.Unlock()

	out := make([]Event, 0, 32)
	for i := len(segments) - 1; i >= 0; i-- {
		events, err := readSegment(s.dir, segments[i].Name)
		if errors.Is(err, os.ErrNotExist) {
			// Retention removed the segment between the snapshot and the read;
			// the remaining history is still readable.
			continue
		}
		if err != nil {
			return nil, err
		}
		for j := len(events) - 1; j >= 0; j-- {
			if !f.Match(events[j]) {
				continue
			}
			out = append(out, events[j])
			if f.Limit > 0 && len(out) >= f.Limit {
				return out, nil
			}
		}
	}
	return out, nil
}

// Subscribe returns a channel of newly appended events and a cancel function.
// A subscriber that falls subscriberBuffer events behind misses the overflow;
// it can catch up with List.
func (s *Store) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
	return ch, cancel
}

// Close flushes the index and closes the active segment.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for ch := range s.subs {
		delete(s.subs, ch)
		close(ch)
	}
	var errs []error
	if s.file != nil {
		if err := s.file.Sync(); err != nil {
			errs = append(errs, err)
		}
		if err := s.file.Close(); err != nil {
			errs = append(errs, err)
		}
		s.file = nil
	}
	if err := s.writeIndexLocked(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Store) notifyLocked(e Event) {
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
			// The subscriber is too far behind; it catches up with List.
		}
	}
}

func (s *Store) openSegment(seg segment) error {
	path := filepath.Join(s.dir, seg.Name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open event segment: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat event segment: %w", err)
	}
	if s.file != nil {
		s.file.Close()
	}
	s.file = f
	s.size = info.Size()
	return nil
}

// rotateLocked closes the active segment and starts the next one. Sequence
// continues across segments; the index is written so a restart does not need
// to rescan closed segments.
func (s *Store) rotateLocked() error {
	if s.file != nil {
		_ = s.file.Sync()
		_ = s.file.Close()
		s.file = nil
	}
	s.nextSeg++
	next := segment{Name: segmentName(s.nextSeg)}
	if err := s.openSegment(next); err != nil {
		return err
	}
	s.segments = append(s.segments, next)
	return s.writeIndexLocked()
}

// pruneLocked enforces the age and count bounds at segment granularity: a
// whole segment is deleted, never a line inside one, and the active segment is
// never deleted. The ledger can therefore exceed the count bound by at most
// the events in its newest segment.
func (s *Store) pruneLocked(now time.Time) {
	s.lastPrune = now
	changed := false
	if s.opts.MaxAge > 0 {
		cutoff := now.Add(-s.opts.MaxAge)
		active := &s.segments[len(s.segments)-1]
		if active.Count > 0 && !active.Last.IsZero() && active.Last.Before(cutoff) {
			if err := s.rotateLocked(); err == nil {
				changed = true
			}
		}
		for len(s.segments) > 1 {
			oldest := s.segments[0]
			if oldest.Count > 0 && !oldest.Last.IsZero() && !oldest.Last.Before(cutoff) {
				break
			}
			s.removeOldestLocked()
			changed = true
		}
	}
	if s.opts.MaxEvents > 0 {
		total := 0
		for _, seg := range s.segments {
			total += seg.Count
		}
		for len(s.segments) > 1 && total-s.segments[0].Count >= s.opts.MaxEvents {
			total -= s.segments[0].Count
			s.removeOldestLocked()
			changed = true
		}
	}
	if changed {
		_ = s.writeIndexLocked()
	}
}

func (s *Store) removeOldestLocked() {
	oldest := s.segments[0]
	_ = os.Remove(filepath.Join(s.dir, oldest.Name))
	s.segments = s.segments[1:]
}

func (s *Store) writeIndexLocked() error {
	idx := indexFile{Schema: Schema, Seq: s.seq, Segments: s.segments}
	data, err := json.Marshal(idx)
	if err != nil {
		return fmt.Errorf("encode event index: %w", err)
	}
	tmp := filepath.Join(s.dir, "index.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write event index: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, "index.json")); err != nil {
		return fmt.Errorf("replace event index: %w", err)
	}
	return nil
}

func readIndex(dir string) (indexFile, error) {
	var idx indexFile
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return idx, err
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return idx, err
	}
	if idx.Schema != Schema {
		return idx, fmt.Errorf("event index schema %d is not %d", idx.Schema, Schema)
	}
	return idx, nil
}

func sameSegments(indexed []segment, names []string) bool {
	if len(indexed) != len(names) {
		return false
	}
	for i := range indexed {
		if indexed[i].Name != names[i] {
			return false
		}
	}
	return true
}

func segmentNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read event dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "events-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func segmentName(n int) string {
	return fmt.Sprintf("events-%06d.jsonl", n)
}

// segmentNumber parses the numeric suffix of a segment name; an unrecognized
// name yields zero.
func segmentNumber(name string) int {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(name, "events-"), ".jsonl")
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0
	}
	return n
}

// scanSegment reads one segment, truncating a trailing partial line left by a
// crash so the next append starts on a fresh line. It returns the segment's
// metadata and the highest sequence it contains.
func scanSegment(dir, name string) (segment, uint64, error) {
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return segment{}, 0, fmt.Errorf("read event segment: %w", err)
	}
	complete := len(data)
	if complete > 0 && data[complete-1] != '\n' {
		if idx := strings.LastIndexByte(string(data), '\n'); idx >= 0 {
			complete = idx + 1
		} else {
			complete = 0
		}
		if err := os.Truncate(path, int64(complete)); err != nil {
			return segment{}, 0, fmt.Errorf("truncate partial event line: %w", err)
		}
	}
	seg := segment{Name: name}
	var maxSeq uint64
	scanner := bufio.NewScanner(strings.NewReader(string(data[:complete])))
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// A corrupt line is skipped rather than failing the daemon: the
			// rest of the ledger is still readable.
			continue
		}
		seg.Count++
		if seg.First.IsZero() || e.At.Before(seg.First) {
			seg.First = e.At
		}
		if e.At.After(seg.Last) {
			seg.Last = e.At
		}
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
	}
	return seg, maxSeq, scanner.Err()
}

func readSegment(dir, name string) ([]Event, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("read event segment: %w", err)
	}
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		if idx := strings.LastIndexByte(text, '\n'); idx >= 0 {
			text = text[:idx+1]
		} else {
			text = ""
		}
	}
	var out []Event
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, scanner.Err()
}

// ParseSince turns a CLI/API "since" value into a Filter. It accepts a decimal
// sequence ("42" means events after 42), a duration ("1h" means the last
// hour), or an RFC3339 timestamp.
func ParseSince(raw string, now time.Time) (Filter, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Filter{}, nil
	}
	if seq, err := strconv.ParseUint(raw, 10, 64); err == nil {
		return Filter{AfterSeq: seq}, nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			return Filter{}, fmt.Errorf("since must not be negative")
		}
		return Filter{Since: now.Add(-d)}, nil
	}
	if at, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return Filter{Since: at}, nil
	}
	if at, err := time.Parse(time.RFC3339, raw); err == nil {
		return Filter{Since: at}, nil
	}
	return Filter{}, fmt.Errorf("since %q is not a sequence number, duration, or RFC3339 time", raw)
}

// NewEventID returns a fresh stable event id.
func NewEventID() string { return "evt_" + randomHex(8) }

// NewTraceID returns a fresh trace id, created at a cause (a manual command, a
// schedule occurrence, or an inbound event).
func NewTraceID() string { return "trc_" + randomHex(8) }

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b)
}
