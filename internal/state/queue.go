package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Queue source classes, in their default priority order.
const (
	QueueExplicit  = "explicit"
	QueueEvent     = "event"
	QueueScheduled = "scheduled"
)

const (
	QueuePending  = "pending"
	QueueStarting = "starting"
	QueueRunning  = "running"
	QueueDone     = "done"
	QueueFailed   = "failed"
	QueueRejected = "rejected"
	QueueBlocked  = "blocked"
)

var ErrQueueFull = errors.New("queue is full")

// QueueItem is a durable request to wake or run work in a box.
type QueueItem struct {
	ID           string       `json:"id"`
	Source       string       `json:"source"`
	Repo         string       `json:"repo,omitempty"`
	Ref          string       `json:"ref,omitempty"`
	BoxID        string       `json:"box_id"`
	Job          string       `json:"job,omitempty"`
	Argv         []string     `json:"argv,omitempty"`
	Priority     int          `json:"priority"`
	State        string       `json:"state"`
	Reason       string       `json:"reason,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	JobID        string       `json:"job_id,omitempty"`
	EventID      string       `json:"event_id,omitempty"`
	EventSource  string       `json:"event_source,omitempty"`
	ScheduleName string       `json:"schedule_name,omitempty"`
	Event        EventContext `json:"event,omitempty"`
	TaskID       string       `json:"task_id,omitempty"`
	RunID        string       `json:"run_id,omitempty"`
	Prompt       string       `json:"prompt,omitempty"`
}

// EventContext is stable provider metadata passed alongside an event job.
type EventContext struct {
	Kind            string          `json:"kind"`
	Action          string          `json:"action,omitempty"`
	Repo            string          `json:"repo,omitempty"`
	Ref             string          `json:"ref,omitempty"`
	HeadRef         string          `json:"head_ref,omitempty"`
	ObjectID        string          `json:"object_id,omitempty"`
	URL             string          `json:"url,omitempty"`
	Trusted         bool            `json:"trusted,omitempty"`
	CredentialNames []string        `json:"credential_names,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

type queueRecord struct {
	Schema int         `json:"schema"`
	Items  []QueueItem `json:"items"`
}

// Enqueue durably accepts a request. At the bound, automatic requests are
// recorded as rejected with a visible reason; explicit requests fail without
// creating a record so a user can retry when capacity frees.
func (s *Store) Enqueue(item QueueItem, maxPending int, now time.Time) (*QueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueueLocked(item, maxPending, now)
}

// EnqueueScheduled durably materializes a schedule occurrence. An active
// item for the same box and schedule is returned so ticks and restarts
// coalesce missed occurrences instead of growing the queue.
func (s *Store) EnqueueScheduled(item QueueItem, maxPending int, now time.Time) (*QueueItem, error) {
	if item.Source != QueueScheduled || item.ScheduleName == "" {
		return nil, errors.New("scheduled queue item requires a schedule name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.readQueueLocked()
	if err != nil {
		return nil, err
	}
	for i := range r.Items {
		q := r.Items[i]
		if q.BoxID == item.BoxID && q.ScheduleName == item.ScheduleName &&
			(q.State == QueuePending || q.State == QueueStarting || q.State == QueueRunning) {
			return &q, nil
		}
	}
	return s.enqueueRecordLocked(r, item, maxPending, now)
}

func (s *Store) enqueueLocked(item QueueItem, maxPending int, now time.Time) (*QueueItem, error) {
	r, err := s.readQueueLocked()
	if err != nil {
		return nil, err
	}
	return s.enqueueRecordLocked(r, item, maxPending, now)
}

func (s *Store) enqueueRecordLocked(r *queueRecord, item QueueItem, maxPending int, now time.Time) (*QueueItem, error) {
	if item.Source != QueueExplicit && item.Source != QueueEvent && item.Source != QueueScheduled {
		return nil, fmt.Errorf("unknown queue source %q", item.Source)
	}
	if item.BoxID == "" {
		return nil, errors.New("queue item box id is required")
	}
	if maxPending < 1 {
		return nil, errors.New("queue capacity must be positive")
	}
	pending := 0
	for _, old := range r.Items {
		if item.EventID != "" && old.Source == item.Source && old.EventSource == item.EventSource && old.EventID == item.EventID {
			return &old, nil
		}
		if old.State == QueuePending || old.State == QueueStarting || old.State == QueueRunning {
			pending++
		}
	}
	item.ID, item.CreatedAt, item.UpdatedAt = newID(), now.UTC(), now.UTC()
	item.Priority = queuePriority(item.Source)
	item.State = QueuePending
	if pending >= maxPending {
		if item.Source == QueueExplicit {
			return nil, ErrQueueFull
		}
		item.State, item.Reason = QueueRejected, "queue is full"
		r.Items = append(r.Items, item)
		if err := s.writeQueueLocked(r); err != nil {
			return nil, err
		}
		return &item, ErrQueueFull
	}
	r.Items = append(r.Items, item)
	if err := s.writeQueueLocked(r); err != nil {
		return nil, err
	}
	return &item, nil
}

func queuePriority(source string) int {
	switch source {
	case QueueExplicit:
		return 0
	case QueueEvent:
		return 1
	default:
		return 2
	}
}

// Queue returns a snapshot in FIFO insertion order, including terminal records.
func (s *Store) Queue() ([]QueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.readQueueLocked()
	if err != nil {
		return nil, err
	}
	return append([]QueueItem(nil), r.Items...), nil
}

// NextQueueItem atomically claims the best pending request. Aging promotes
// each class one step per aging interval while preserving FIFO ties.
func (s *Store) NextQueueItem(now time.Time, agingInterval time.Duration) (*QueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.readQueueLocked()
	if err != nil {
		return nil, err
	}
	idx := -1
	effective := func(q QueueItem) int {
		p := q.Priority
		if agingInterval > 0 {
			p -= int(now.Sub(q.CreatedAt) / agingInterval)
		}
		if p < 0 {
			p = 0
		}
		return p
	}
	for i := range r.Items {
		q := r.Items[i]
		if q.State != QueuePending {
			continue
		}
		if idx < 0 || effective(q) < effective(r.Items[idx]) || (effective(q) == effective(r.Items[idx]) && q.CreatedAt.Before(r.Items[idx].CreatedAt)) {
			idx = i
		}
	}
	if idx < 0 {
		return nil, nil
	}
	r.Items[idx].State, r.Items[idx].UpdatedAt = QueueStarting, now.UTC()
	if err := s.writeQueueLocked(r); err != nil {
		return nil, err
	}
	item := r.Items[idx]
	return &item, nil
}

// UpdateQueueItem persists a lifecycle update for an accepted request.
func (s *Store) UpdateQueueItem(id, nextState, boxJobID, reason string, now time.Time) (*QueueItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.readQueueLocked()
	if err != nil {
		return nil, err
	}
	for i := range r.Items {
		q := &r.Items[i]
		if q.ID != id {
			continue
		}
		q.State, q.UpdatedAt = nextState, now.UTC()
		q.JobID, q.Reason = boxJobID, reason
		if err := s.writeQueueLocked(r); err != nil {
			return nil, err
		}
		out := *q
		return &out, nil
	}
	return nil, fmt.Errorf("queue item %s not found", id)
}

// RecoverQueue requeues work claimed as starting before a daemon crash and
// marks previously running requests failed because the outcome is unknown.
func (s *Store) RecoverQueue(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.readQueueLocked()
	if err != nil {
		return err
	}
	changed := false
	for i := range r.Items {
		q := &r.Items[i]
		switch q.State {
		case QueueStarting:
			q.State = QueuePending
			q.UpdatedAt = now.UTC()
			changed = true
		case QueueRunning:
			q.State = QueueFailed
			q.Reason = "daemon restarted while request was running; outcome is unknown"
			q.UpdatedAt = now.UTC()
			changed = true
		}
	}
	if changed {
		return s.writeQueueLocked(r)
	}
	return nil
}

func (s *Store) queuePath() string { return filepath.Join(s.root, "queue.json") }
func (s *Store) readQueueLocked() (*queueRecord, error) {
	b, err := os.ReadFile(s.queuePath())
	if errors.Is(err, os.ErrNotExist) {
		return &queueRecord{Schema: 1, Items: []QueueItem{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var r queueRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("read queue: %w", err)
	}
	if r.Schema != 1 {
		return nil, fmt.Errorf("unsupported queue schema %d", r.Schema)
	}
	return &r, nil
}
func (s *Store) writeQueueLocked(r *queueRecord) error {
	// Keep terminal history bounded while retaining all actionable records.
	if len(r.Items) > 1000 {
		sort.SliceStable(r.Items, func(i, j int) bool { return r.Items[i].CreatedAt.Before(r.Items[j].CreatedAt) })
		cut := len(r.Items) - 1000
		kept := make([]QueueItem, 0, len(r.Items))
		for i, q := range r.Items {
			if i >= cut || q.State == QueuePending || q.State == QueueStarting || q.State == QueueRunning {
				kept = append(kept, q)
			}
		}
		r.Items = kept
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.queuePath(), append(b, '\n'), 0o644)
}
