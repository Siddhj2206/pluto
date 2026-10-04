package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Siddhj2206/pluto/internal/fsutil"
	"github.com/Siddhj2206/pluto/internal/state"
)

// RunJob runs a bounded command in the box, ensuring it is up first. Output
// chunks are teed to the job's host-side log and to emit as they arrive. A
// command that fails is a failed job, not an error; an error means the job
// never started (a concurrent run, a store or log failure).
func (r *Runner) RunJob(ctx context.Context, box *state.Box, argv []string, emit func([]byte)) (*state.Box, *state.Job, error) {
	if len(argv) == 0 {
		return nil, nil, fmt.Errorf("no command given")
	}
	box, err := r.Store.Box(box.ID)
	if err != nil {
		return nil, nil, err
	}
	boxID := box.ID
	job := state.StartJob(state.NewID(), argv)
	job.Log = filepath.Join("jobs", job.ID+".log")
	box, err = r.Store.BeginJob(boxID, job)
	if err != nil {
		return nil, nil, err
	}
	job = *box.Job

	running, err := r.Up(ctx, box)
	if err != nil {
		return r.finishJob(boxID, job, err)
	}

	logFile, err := openJobLog(r.jobLogPath(boxID, job.ID))
	if err != nil {
		return r.finishJob(boxID, job, err)
	}
	defer logFile.Close()

	final, err := r.NewAgent(vsockPath(r.boxDir(boxID))).Run(job.ID, argv, boxWorktreePath(running), func(data []byte) {
		_, _ = logFile.Write(data)
		if emit != nil {
			emit(data)
		}
	})
	if err != nil {
		// The stream broke; the job may still be running in the box. The
		// agent's durable record is the recovery path: the next status
		// refresh adopts the real outcome.
		return r.finishJob(boxID, job, err)
	}
	final.Log = job.Log
	updated, err := r.Store.SetJob(boxID, *final)
	if err != nil {
		return nil, final, err
	}
	return updated, final, nil
}

// JobLog returns the tail of a job's recorded output. While the box is up
// the agent's copy is authoritative — it is complete even when the daemon
// lost the stream — and the host copy covers a paused box.
func (r *Runner) JobLog(box *state.Box, jobID string, lines int) (string, error) {
	if !state.ValidID(jobID) {
		return "", fmt.Errorf("invalid job id %q", jobID)
	}
	if box.Job == nil || box.Job.ID != jobID {
		return "", fmt.Errorf("box %s has no job %s", shortID(box.ID), shortID(jobID))
	}
	if box.State == state.StateRunning {
		client := r.NewAgent(vsockPath(r.boxDir(box.ID)))
		if log, err := client.JobLog(jobID, lines); err == nil {
			return log, nil
		}
	}
	return fsutil.TailFile(r.jobLogPath(box.ID, jobID), lines)
}

// finishJob records a job that failed outside the agent and returns its
// terminal record.
func (r *Runner) finishJob(boxID string, job state.Job, cause error) (*state.Box, *state.Job, error) {
	failJobRecord(&job, cause.Error())
	updated, err := r.Store.SetJob(boxID, job)
	if err != nil {
		return nil, &job, err
	}
	return updated, &job, nil
}

// failRunningJob marks the box's running job, if any, as failed. It is used
// when the machine stops underneath the job.
func (r *Runner) failRunningJob(id, reason string) {
	box, err := r.Store.Box(id)
	if err != nil || !box.JobRunning() {
		return
	}
	job := *box.Job
	failJobRecord(&job, reason)
	_, _ = r.Store.SetJob(id, job)
}

// failJobRecord stamps a terminal failed outcome onto a job record.
func failJobRecord(job *state.Job, reason string) {
	job.Finish(state.JobFailed, 0, reason)
}

func (r *Runner) jobLogPath(boxID, jobID string) string {
	return filepath.Join(r.boxDir(boxID), "jobs", jobID+".log")
}

func openJobLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create job log dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create job log: %w", err)
	}
	return f, nil
}
