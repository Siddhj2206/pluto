package box

import (
	"context"
	"io"
	"sync"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// serviceSupervisor keeps a box's declared service tasks running for the box's
// life. Starting a box — its first use or a start after a stop — launches every
// service task afresh; stopping or removing the box ends them. Services are not
// gated by lock policy: they run continuously in the background while shared
// and exclusive runs come and go.
type serviceSupervisor struct {
	engine container.Engine
	mu     sync.Mutex
	runs   map[string]context.CancelFunc
}

func newServiceSupervisor(engine container.Engine) *serviceSupervisor {
	return &serviceSupervisor{engine: engine, runs: make(map[string]context.CancelFunc)}
}

// start launches every service task for a box, cancelling any services left
// from a previous run so a restart is always fresh. Each service runs until the
// box stops or is removed.
func (s *serviceSupervisor) start(box *state.Box, services []contract.ServiceTask) {
	s.stop(box.ID)
	if len(services) == 0 || box.EngineID == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.runs[box.ID] = cancel
	s.mu.Unlock()
	for _, svc := range services {
		spec := container.ExecSpec{Argv: svc.Exec.Command.Argv(), Env: svc.Exec.Env}
		go func() {
			_, _ = s.engine.Exec(ctx, box.EngineID, spec, io.Discard)
		}()
	}
}

// stop ends a box's running services.
func (s *serviceSupervisor) stop(boxID string) {
	s.mu.Lock()
	cancel := s.runs[boxID]
	delete(s.runs, boxID)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
