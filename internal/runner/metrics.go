// Firecracker metrics. Firecracker flushes JSON metrics (at instance start,
// every 60 seconds, and on panic) to the file named by the config's
// metrics_path. The runner reads the latest flush and the daemon surfaces it
// (docs/research/firecracker-operation.md §4 A2).
package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/Siddhj2206/pluto/internal/state"
)

// maxMetricsTail bounds how much of the metrics stream is read when locating
// the latest snapshot, so a long-running box's metrics file never needs to be
// held in memory in full.
const maxMetricsTail = 64 << 10

// Metrics returns the most recent Firecracker metrics snapshot for a box,
// exactly as Firecracker emitted it. Firecracker writes one JSON object per
// flush; the last complete one is the current snapshot.
func (r *Runner) Metrics(box *state.Box) (json.RawMessage, error) {
	return readLastMetrics(metricsPath(r.boxDir(box.ID)))
}

func readLastMetrics(path string) (json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var start int64
	if info.Size() > maxMetricsTail {
		start = info.Size() - maxMetricsTail
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxMetricsTail))
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || !json.Valid(line) {
			continue
		}
		return json.RawMessage(append([]byte(nil), line...)), nil
	}
	return nil, errors.New("no Firecracker metrics recorded yet")
}
