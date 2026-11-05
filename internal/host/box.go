package host

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

const (
	hooksDir = "/tmp/pluto-hooks"
	stateDir = "/tmp/pluto-state"
	// interruptedCode is what entryScript records for a runner cut off by a restart.
	interruptedCode = 137
	// runnerImage is GitHub's official runner image, the default box.
	runnerImage = "ghcr.io/actions/actions-runner:latest"
)

// entryScript runs the one-time runner once, then keeps the box alive so a
// failed run can be entered. Restarting a used box must not run the runner again,
// so the started marker comes first. A box that restarts with a started runner
// and no exit code was interrupted, which is recorded as code 137.
const entryScript = `mkdir -p ` + stateDir + `
if [ -f ` + stateDir + `/started ]; then
  [ -f ` + stateDir + `/runner-exit ] || echo 137 > ` + stateDir + `/runner-exit
  exec sleep infinity
fi
touch ` + stateDir + `/started
jit="$PLUTO_JIT"
unset PLUTO_JIT
./run.sh --jitconfig "$jit" > ` + stateDir + `/runner.log 2>&1
echo $? > ` + stateDir + `/runner-exit
exec sleep infinity
`

// preJobHook runs before any step of a job. It records the job and refuses work
// that could come from another repository. It fails closed: an event isn't
// allowed unless it's listed here, and a pull request whose origin can't be read
// doesn't run. issue_comment and merge_group are left out on purpose, because
// those workflows commonly check out fork code that the payload can't reveal.
const preJobHook = `#!/bin/sh
state=${PLUTO_STATE_DIR:-` + stateDir + `}
{
  echo "github_run_id=$GITHUB_RUN_ID"
  echo "workflow=$GITHUB_WORKFLOW"
  echo "job=$GITHUB_JOB"
  echo "event=$GITHUB_EVENT_NAME"
  echo "ref=$GITHUB_REF"
  echo "sha=$GITHUB_SHA"
  echo "workspace=$GITHUB_WORKSPACE"
} > "$state/job.env"
refuse() {
  touch "$state/refused"
  echo "pluto: refused this job: $1"
  echo "Nothing in this job ran on this host."
  exit 1
}
case "$GITHUB_EVENT_NAME" in
  push|workflow_dispatch|schedule|repository_dispatch|create|delete|release)
    exit 0 ;;
  pull_request|pull_request_target|pull_request_review|pull_request_review_comment)
    head=$(jq -r '.pull_request.head.repo.full_name // empty' "$GITHUB_EVENT_PATH" 2>/dev/null) ;;
  workflow_run)
    head=$(jq -r '.workflow_run.head_repository.full_name // empty' "$GITHUB_EVENT_PATH" 2>/dev/null) ;;
  *)
    refuse "Pluto doesn't run the $GITHUB_EVENT_NAME event, because it can bring in code from a fork." ;;
esac
if [ -n "$head" ] && [ "$head" = "$GITHUB_REPOSITORY" ]; then
  exit 0
fi
refuse "its code comes from another repository (${head:-unknown})."
`

// statusScript prints what the host needs to know about a box as key=value lines.
const statusScript = `#!/bin/sh
state=` + stateDir + `
cat "$state/job.env" 2>/dev/null
[ -f "$state/refused" ] && echo refused=1
[ -f "$state/runner-exit" ] && echo "exit=$(cat "$state/runner-exit")"
result=$(grep -h 'completed with result: ' _diag/*.log 2>/dev/null | tail -n 1 | sed 's/.*completed with result: //')
[ -n "$result" ] && echo "result=$result"
exit 0
`

// boxStatus is a box's own account of its job.
type boxStatus struct {
	GitHubRunID int64
	Workflow    string
	Job         string
	Event       string
	Ref         string
	SHA         string
	Workspace   string
	Refused     bool
	Exited      bool
	ExitCode    int
	Result      string
}

func (s boxStatus) jobStarted() bool { return s.GitHubRunID != 0 || s.Job != "" }

func parseStatus(out []byte) boxStatus {
	var s boxStatus
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "github_run_id":
			s.GitHubRunID, _ = strconv.ParseInt(v, 10, 64)
		case "workflow":
			s.Workflow = v
		case "job":
			s.Job = v
		case "event":
			s.Event = v
		case "ref":
			s.Ref = v
		case "sha":
			s.SHA = v
		case "workspace":
			s.Workspace = v
		case "refused":
			s.Refused = true
		case "exit":
			s.Exited = true
			s.ExitCode, _ = strconv.Atoi(v)
		case "result":
			s.Result = v
		}
	}
	return s
}
