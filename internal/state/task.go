package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	TaskAccepted     = "accepted"
	TaskRunQueued    = "queued"
	TaskRunRunning   = "running"
	TaskRunCompleted = "completed"
	TaskRunFailed    = "failed"
	TaskRunRejected  = "rejected"
	TaskRunBlocked   = "blocked"
)

var ErrTaskNotFound = errors.New("task not found")
var ErrIdempotencyConflict = errors.New("idempotency_key was already used for different work")

// Task is durable requested work with one or more serialized execution runs.
type Task struct {
	ID             string    `json:"id"`
	State          string    `json:"state"`
	Source         string    `json:"source"`
	Project        string    `json:"project,omitempty"`
	Ref            string    `json:"ref,omitempty"`
	BoxID          string    `json:"box_id,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	Runs           []TaskRun `json:"runs"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type TaskRun struct {
	ID             string     `json:"id"`
	QueueID        string     `json:"queue_id"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	Prompt         string     `json:"prompt"`
	Job            string     `json:"job"`
	State          string     `json:"state"`
	Reason         string     `json:"reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	JobID          string     `json:"job_id,omitempty"`
}

type taskRecord struct {
	Schema int    `json:"schema"`
	Tasks  []Task `json:"tasks"`
}

// CreateTask durably creates a task and its first queued run. An idempotency
// key makes client retries return the original task/run identities.
func (s *Store) CreateTask(task Task, run TaskRun, item QueueItem, capacity int, now time.Time) (*Task, error) {
	if task.IdempotencyKey == "" {
		return nil, errors.New("idempotency_key is required")
	}
	if task.BoxID == "" || run.Job == "" || run.Prompt == "" {
		return nil, errors.New("box_id, job, and prompt are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.readTasksLocked()
	if err != nil {
		return nil, err
	}
	for _, old := range r.Tasks {
		if old.IdempotencyKey == task.IdempotencyKey {
			if old.BoxID != task.BoxID || old.Project != task.Project || old.Ref != task.Ref || old.Source != task.Source || len(old.Runs) == 0 || old.Runs[0].Job != run.Job || old.Runs[0].Prompt != run.Prompt {
				return nil, ErrIdempotencyConflict
			}
			if len(old.Runs) == 0 {
				return nil, errors.New("idempotency record has no run")
			}
			queue, err := s.readQueueLocked()
			if err != nil {
				return nil, err
			}
			hydrateTaskFromQueue(&old, queue.Items)
			return &old, nil
		}
	}
	item.Source, item.BoxID, item.Job, item.TaskID, item.RunID, item.Prompt = QueueExplicit, task.BoxID, run.Job, newID(), newID(), run.Prompt
	queued, err := s.enqueueLocked(item, capacity, now)
	if err != nil {
		return nil, err
	}
	task.ID, task.CreatedAt, task.UpdatedAt = item.TaskID, now.UTC(), now.UTC()
	run.ID, run.QueueID, run.State, run.CreatedAt, run.IdempotencyKey = item.RunID, queued.ID, TaskRunQueued, now.UTC(), task.IdempotencyKey
	task.State, task.Runs = TaskRunQueued, []TaskRun{run}
	r.Tasks = append(r.Tasks, task)
	if err := s.writeTasksLocked(r); err != nil {
		return nil, err
	}
	return &task, nil
}

// AppendTaskRun durably queues a serialized follow-up run. Reusing the same
// run idempotency key returns the original run when its inputs match.
func (s *Store) AppendTaskRun(taskID string, run TaskRun, capacity int, now time.Time) (*Task, error) {
	if run.IdempotencyKey == "" || run.Job == "" || run.Prompt == "" {
		return nil, errors.New("idempotency_key, job, and prompt are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.readTasksLocked()
	if err != nil {
		return nil, err
	}
	for i := range r.Tasks {
		task := &r.Tasks[i]
		if task.ID != taskID {
			continue
		}
		for _, old := range task.Runs {
			if old.IdempotencyKey == run.IdempotencyKey {
				if old.Job != run.Job || old.Prompt != run.Prompt {
					return nil, ErrIdempotencyConflict
				}
				queue, err := s.readQueueLocked()
				if err != nil {
					return nil, err
				}
				hydrateTaskFromQueue(task, queue.Items)
				return task, nil
			}
		}
		item := QueueItem{Source: QueueExplicit, BoxID: task.BoxID, Job: run.Job, TaskID: task.ID, RunID: newID(), Prompt: run.Prompt}
		queued, err := s.enqueueLocked(item, capacity, now)
		if err != nil {
			return nil, err
		}
		run.ID, run.QueueID, run.State, run.CreatedAt = item.RunID, queued.ID, TaskRunQueued, now.UTC()
		task.Runs = append(task.Runs, run)
		task.UpdatedAt = now.UTC()
		if err := s.writeTasksLocked(r); err != nil {
			return nil, err
		}
		queue, err := s.readQueueLocked()
		if err != nil {
			return nil, err
		}
		hydrateTaskFromQueue(task, queue.Items)
		return task, nil
	}
	return nil, ErrTaskNotFound
}

func (s *Store) Task(id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.readTasksLocked()
	if err != nil {
		return nil, err
	}
	items, err := s.readQueueLocked()
	if err != nil {
		return nil, err
	}
	for i := range r.Tasks {
		if r.Tasks[i].ID != id {
			continue
		}
		hydrateTaskFromQueue(&r.Tasks[i], items.Items)
		return &r.Tasks[i], nil
	}
	return nil, ErrTaskNotFound
}

func (s *Store) Tasks() ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.readTasksLocked()
	if err != nil {
		return nil, err
	}
	items, err := s.readQueueLocked()
	if err != nil {
		return nil, err
	}
	for i := range r.Tasks {
		hydrateTaskFromQueue(&r.Tasks[i], items.Items)
	}
	return r.Tasks, nil
}

func refreshTaskState(task *Task) {
	if len(task.Runs) == 0 {
		task.State = TaskAccepted
		return
	}
	for _, run := range task.Runs {
		if run.State == TaskRunRunning {
			task.State = TaskRunRunning
			return
		}
	}
	task.State = task.Runs[len(task.Runs)-1].State
}

func hydrateTaskFromQueue(task *Task, items []QueueItem) {
	for j := range task.Runs {
		for _, q := range items {
			if q.ID == task.Runs[j].QueueID {
				updateTaskRun(&task.Runs[j], q)
				break
			}
		}
	}
	refreshTaskState(task)
}

func updateTaskRun(run *TaskRun, q QueueItem) {
	switch q.State {
	case QueuePending, QueueStarting:
		run.State = TaskRunQueued
	case QueueRunning:
		run.State = TaskRunRunning
	case QueueDone:
		run.State = TaskRunCompleted
	case QueueBlocked:
		run.State = TaskRunBlocked
	case QueueRejected:
		run.State = TaskRunRejected
	case QueueFailed:
		run.State = TaskRunFailed
	}
	run.Reason, run.JobID = q.Reason, q.JobID
	if run.State == TaskRunRunning && run.StartedAt == nil {
		at := q.UpdatedAt
		run.StartedAt = &at
	}
	if (run.State == TaskRunCompleted || run.State == TaskRunFailed || run.State == TaskRunBlocked || run.State == TaskRunRejected) && run.FinishedAt == nil {
		at := q.UpdatedAt
		run.FinishedAt = &at
	}
}

func (s *Store) readTasksLocked() (*taskRecord, error) {
	b, err := os.ReadFile(s.tasksPath())
	if errors.Is(err, os.ErrNotExist) {
		return &taskRecord{Schema: 1, Tasks: []Task{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tasks: %w", err)
	}
	var r taskRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("decode tasks: %w", err)
	}
	if r.Schema != 1 {
		return nil, fmt.Errorf("unsupported task schema %d", r.Schema)
	}
	return &r, nil
}

func (s *Store) writeTasksLocked(r *taskRecord) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.tasksPath(), append(b, '\n'), 0o644)
}

func (s *Store) tasksPath() string { return filepath.Join(s.root, "tasks.json") }
