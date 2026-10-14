package box_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/box"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
)

// TestRunSharedRunsConcurrently proves two shared runs hold the box at the same
// time: both reach the engine before either is released.
func TestRunSharedRunsConcurrently(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: "/src/app"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	release := make(chan struct{})
	entered := make(chan string, 2)
	h.engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		entered <- spec.Argv[0]
		<-release
		return container.ExecResult{}, nil
	}

	errs := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		go func(n string) {
			_, err := h.m.Run(ctx, created.ID, contract.LockShared, container.ExecSpec{Argv: []string{n}}, io.Discard)
			errs <- err
		}(name)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("a shared run did not reach the engine while the other was held")
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("shared run: %v", err)
		}
	}
}

// TestAcquireSharedIsAnExclusiveBlocker proves an attach session (#144) regains
// the box as shared work: a shared run joins it, while an exclusive run waits
// until the session leaves.
func TestAcquireSharedIsAnExclusiveBlocker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: "/src/app"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	release, err := h.m.AcquireShared(ctx, created.ID)
	if err != nil {
		t.Fatalf("AcquireShared: %v", err)
	}

	sharedEntered := make(chan struct{})
	h.engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		if spec.Argv[0] == "shared" {
			close(sharedEntered)
		}
		return container.ExecResult{}, nil
	}
	if _, err := h.m.Run(ctx, created.ID, contract.LockShared, container.ExecSpec{Argv: []string{"shared"}}, io.Discard); err != nil {
		t.Fatalf("shared run while attached: %v", err)
	}
	select {
	case <-sharedEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("a shared run did not reach the engine while an attach session held the box")
	}

	blocked, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := h.m.Run(blocked, created.ID, contract.LockExclusive, container.ExecSpec{Argv: []string{"exclusive"}}, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exclusive run while a shared holder remained = %v, want context deadline exceeded", err)
	}

	release()
	if _, err := h.m.Run(ctx, created.ID, contract.LockExclusive, container.ExecSpec{Argv: []string{"exclusive"}}, io.Discard); err != nil {
		t.Fatalf("exclusive run after the shared holder left: %v", err)
	}
}
