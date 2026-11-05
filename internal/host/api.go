package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/Siddhj2206/pluto/internal/engine"
	"github.com/Siddhj2206/pluto/internal/github"
	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/version"
)

// Info describes a host to clients.
type Info struct {
	Version      string   `json:"version"`
	Engine       string   `json:"engine"`
	Capabilities []string `json:"capabilities"`
	Labels       []string `json:"labels"`
}

// Failure is the API's error body. Next is the command that helps, when there is one.
type Failure struct {
	Error string `json:"error"`
	Next  string `json:"next,omitempty"`
}

// ConnectRequest connects a repo.
type ConnectRequest struct {
	Repo  string `json:"repo"`
	Token string `json:"token"`
}

// EnterResponse says how to open a shell in a box.
type EnterResponse struct {
	Argv    []string `json:"argv"`
	Warning string   `json:"warning,omitempty"`
}

var repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}

func fail(w http.ResponseWriter, code int, msg, next string) {
	writeJSON(w, code, Failure{Error: msg, Next: next})
}

// Handler serves the host's API.
func (h *Host) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", h.handleInfo)
	mux.HandleFunc("POST /v1/repos", h.handleConnect)
	mux.HandleFunc("GET /v1/repos", h.handleRepos)
	mux.HandleFunc("GET /v1/runs", h.handleRuns)
	mux.HandleFunc("GET /v1/runs/{id}", h.handleRun)
	mux.HandleFunc("GET /v1/runs/{id}/logs", h.handleLogs)
	mux.HandleFunc("POST /v1/runs/{id}/enter", h.handleEnter)
	mux.HandleFunc("POST /v1/runs/{id}/cancel", h.handleCancel)
	mux.HandleFunc("POST /v1/uploads", h.handleUpload)
	mux.HandleFunc("POST /v1/tasks/run", h.handleRunTask)
	mux.HandleFunc("GET /v1/boxes", h.handleBoxes)
	mux.HandleFunc("POST /v1/boxes/reset", h.handleBoxReset)
	mux.HandleFunc("POST /v1/grants", h.handleGrant)
	mux.HandleFunc("DELETE /v1/grants", h.handleRevoke)
	mux.HandleFunc("POST /v1/secrets", h.handleSecretSet)
	mux.HandleFunc("GET /v1/secrets", h.handleSecretList)
	mux.HandleFunc("DELETE /v1/secrets", h.handleSecretDelete)
	mux.HandleFunc("POST /v1/schedules", h.handleScheduleEnable)
	mux.HandleFunc("GET /v1/schedules", h.handleSchedules)
	mux.HandleFunc("DELETE /v1/schedules", h.handleScheduleDisable)
	mux.HandleFunc("POST /v1/schedules/run", h.handleScheduleRun)
	mux.HandleFunc("POST /v1/boxes/enter", h.handleBoxEnter)
	return mux
}

func (h *Host) handleInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Info{
		Version:      version.Version,
		Engine:       h.Engine.Name(),
		Capabilities: version.Capabilities,
		Labels:       Labels(),
	})
}

func (h *Host) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "invalid request: "+err.Error(), "")
		return
	}
	if !repoName.MatchString(req.Repo) || req.Token == "" {
		fail(w, http.StatusBadRequest, "a repo in owner/name form and a token are required", "")
		return
	}
	if err := h.GitHub(req.Token).CheckAdmin(r.Context(), req.Repo); err != nil {
		var se *github.StatusError
		switch {
		case errors.Is(err, github.ErrUnauthorized):
			fail(w, http.StatusForbidden, err.Error(), "pluto ci connect github   (with a new token)")
		case errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusForbidden):
			fail(w, http.StatusForbidden, "GitHub won't show "+req.Repo+" to this token.", "check the token's repository access, then run: pluto ci connect github")
		case errors.As(err, &se):
			fail(w, http.StatusBadGateway, err.Error(), "")
		default:
			fail(w, http.StatusForbidden, err.Error(), "")
		}
		return
	}
	if err := h.Store.PutRepo(r.Context(), store.Repo{Name: req.Repo, Token: req.Token, ConnectedAt: h.now()}); err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, h.Status(req.Repo))
}

func (h *Host) handleRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := h.Store.Repos(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	out := make([]RepoStatus, 0, len(repos))
	for _, repo := range repos {
		out = append(out, h.Status(repo.Name))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Host) handleRuns(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		limit = 30
	}
	var runs []store.Run
	if since := r.URL.Query().Get("since"); since != "" {
		d, perr := time.ParseDuration(since)
		if perr != nil || d <= 0 {
			fail(w, http.StatusBadRequest, "--since should look like 90m or 24h", "")
			return
		}
		runs, err = h.Store.RunsSince(r.Context(), h.now().Add(-d), limit)
	} else {
		runs, err = h.Store.Runs(r.Context(), limit)
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if runs == nil {
		runs = []store.Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func (h *Host) findRun(w http.ResponseWriter, r *http.Request) (store.Run, bool) {
	id := r.PathValue("id")
	run, err := h.Store.Run(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "no run named "+id, "pluto history")
		return run, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return run, false
	}
	return run, true
}

func (h *Host) handleRun(w http.ResponseWriter, r *http.Request) {
	if run, ok := h.findRun(w, r); ok {
		writeJSON(w, http.StatusOK, run)
	}
}

func (h *Host) handleLogs(w http.ResponseWriter, r *http.Request) {
	run, ok := h.findRun(w, r)
	if !ok {
		return
	}
	if run.Kind == store.KindTask {
		h.taskLogs(w, r, run)
		return
	}
	if run.GitHubRunID == 0 {
		msg := "run " + run.ID + " has no job log"
		if run.Reason != "" {
			msg += ": " + run.Reason
		}
		fail(w, http.StatusConflict, msg, "")
		return
	}
	repo, err := h.Store.Repo(r.Context(), run.Repo)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	data, err := h.GitHub(repo.Token).JobLog(r.Context(), run.Repo, run.GitHubRunID, run.Box)
	if err != nil {
		var se *github.StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			if run.Active() {
				fail(w, http.StatusConflict, "GitHub doesn't have this job's log yet. It publishes logs when a job finishes.", "pluto logs "+run.ID+"   (after the job finishes)")
				return
			}
			fail(w, http.StatusConflict, "GitHub doesn't have this job's log. It may be publishing it still, or it may have expired.", "pluto logs "+run.ID+"   (try again shortly)")
			return
		}
		fail(w, http.StatusBadGateway, err.Error(), "")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := w.Write(data); err != nil {
		log.Printf("writing logs: %v", err)
	}
}

func (h *Host) handleEnter(w http.ResponseWriter, r *http.Request) {
	run, ok := h.findRun(w, r)
	if !ok {
		return
	}
	if run.State == store.StateQueued {
		fail(w, http.StatusConflict, "run "+run.ID+" is still queued, so it has no box yet.", "pluto history")
		return
	}
	if run.Kind != store.KindTask && !run.BoxKept && !run.Active() {
		fail(w, http.StatusConflict, fmt.Sprintf("run %s's box is gone. Pluto keeps failed boxes for %d hours and removes others right away.", run.ID, int(h.Config.KeepFailed.Hours())), "pluto logs "+run.ID)
		return
	}
	running, err := h.Engine.Running(r.Context(), run.Box)
	if errors.Is(err, engine.ErrNotFound) {
		fail(w, http.StatusConflict, "run "+run.ID+"'s box is gone.", "pluto logs "+run.ID)
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if !running {
		if run.Active() && run.Kind != store.KindTask {
			fail(w, http.StatusConflict, "run "+run.ID+"'s box has stopped. Pluto is checking on it.", "pluto history")
			return
		}
		if err := h.Engine.Resume(r.Context(), run.Box); err != nil {
			fail(w, http.StatusInternalServerError, err.Error(), "")
			return
		}
	}
	writeJSON(w, http.StatusOK, EnterResponse{Argv: h.Engine.EnterArgv(run.Box, run.Workspace)})
}
