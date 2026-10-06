package daemon_test

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/envcache"
	"github.com/Siddhj2206/pluto/internal/state"
)

// A queued request that arrives while another box is building the same
// environment layer is deferred, not failed, and runs once the layer is
// reusable.
func TestQueuedUpDefersWhileEnvironmentLayerBuilds(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	socket, st, srv := startServer(t, fakeRunner{upFn: func(*state.Box) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return envcache.ErrBuilding
		}
		return nil
	}})
	c := client(socket)
	box := createBoxAt(t, c, writeContract(t, t.TempDir(), "[provision]\ncommand = \"make setup\"\ncache = true\n"))

	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/queue", map[string]any{"up": true})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("queue status = %d, body %s", resp.StatusCode, data)
	}

	clock := &schedulerClock{now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	runScheduler(t, srv, clock)

	waitFor(t, "the deferred queue item to run after the layer build", func() bool {
		items, err := st.Queue()
		return err == nil && len(items) == 1 && items[0].State == state.QueueDone
	})
	got, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want the deferred box to run after reuse", got.State)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Fatalf("Up calls = %d, want a deferral then a successful reuse", calls)
	}
}

// An explicit Up while an environment layer is building is a retryable
// conflict, not a server error.
func TestUpConflictsWhileEnvironmentLayerBuilds(t *testing.T) {
	socket, _ := startWith(t, fakeRunner{upErr: envcache.ErrBuilding})
	c := client(socket)
	box := createBox(t, c)
	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/up", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("up status = %d, want 409, body %s", resp.StatusCode, data)
	}
}
