package box

import (
	"context"
	"sync"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// Gate serialises work on a box by lock policy. Shared holders — shared runs
// and attach sessions — run concurrently with each other; an exclusive holder
// runs alone, waiting for shared work to drain, and shared holders that arrive
// while an exclusive holder waits or runs wait themselves. The gate is fair: a
// waiting exclusive holder blocks later shared holders, so a stream of
// interactive work cannot starve a batch run.
//
// A gate is in-memory and per-process; a restarted daemon has no active
// holders, which is exactly the truth it should report.
type Gate struct {
	mu    sync.Mutex
	boxes map[string]*boxGate
}

// boxGate is one box's holder bookkeeping.
type boxGate struct {
	mu sync.Mutex
	// shared counts active shared holders.
	shared int
	// exclusive reports whether an exclusive holder is active.
	exclusive bool
	// exclusiveWaiting counts exclusive holders that have not acquired yet. A
	// shared holder will not acquire while it is positive, which is the gate's
	// writer preference.
	exclusiveWaiting int
	// waiting counts holders parked in Hold's select, for observation.
	waiting int
	// waiters are the broadcast channels current parked holders select on.
	waiters []chan struct{}
}

// Snapshot is a gate's observable state for one box.
type Snapshot struct {
	// Shared is the number of active shared holders.
	Shared int `json:"shared"`
	// Exclusive reports whether an exclusive holder is active.
	Exclusive bool `json:"exclusive"`
	// Waiting is the number of holders parked waiting to acquire.
	Waiting int `json:"waiting"`
}

// NewGate returns an empty gate.
func NewGate() *Gate {
	return &Gate{boxes: make(map[string]*boxGate)}
}

// Hold acquires boxID for work under policy, blocking until the policy admits
// it or ctx is done. LockExclusive holds the box alone; every other policy —
// including a service task and an attach session — is a shared holder. The
// returned release must be called exactly once; it is nil when Hold returns an
// error.
func (g *Gate) Hold(ctx context.Context, boxID string, policy contract.Lock) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exclusive := policy == contract.LockExclusive
	bg := g.gateFor(boxID)

	bg.mu.Lock()
	if exclusive {
		bg.exclusiveWaiting++
	}
	for {
		if release, ok := bg.tryAcquireLocked(policy); ok {
			if exclusive {
				bg.exclusiveWaiting--
			}
			bg.mu.Unlock()
			return release, nil
		}
		ch := make(chan struct{})
		bg.waiters = append(bg.waiters, ch)
		bg.waiting++
		bg.mu.Unlock()

		select {
		case <-ch:
			bg.mu.Lock()
			bg.waiting--
		case <-ctx.Done():
			bg.mu.Lock()
			bg.waiting--
			bg.removeWaiterLocked(ch)
			if exclusive {
				bg.exclusiveWaiting--
			}
			bg.mu.Unlock()
			return nil, ctx.Err()
		}
	}
}

// TryHold acquires boxID without waiting, reporting whether the policy admits
// it. The returned release must be called exactly once when ok is true.
func (g *Gate) TryHold(boxID string, policy contract.Lock) (release func(), ok bool) {
	bg := g.gateFor(boxID)
	bg.mu.Lock()
	defer bg.mu.Unlock()
	return bg.tryAcquireLocked(policy)
}

// Snapshot reports boxID's current holder state. An unknown box reads as idle.
func (g *Gate) Snapshot(boxID string) Snapshot {
	g.mu.Lock()
	bg := g.boxes[boxID]
	g.mu.Unlock()
	if bg == nil {
		return Snapshot{}
	}
	bg.mu.Lock()
	defer bg.mu.Unlock()
	return Snapshot{Shared: bg.shared, Exclusive: bg.exclusive, Waiting: bg.waiting}
}

// gateFor returns boxID's gate, creating it on first use.
func (g *Gate) gateFor(boxID string) *boxGate {
	g.mu.Lock()
	defer g.mu.Unlock()
	bg := g.boxes[boxID]
	if bg == nil {
		bg = &boxGate{}
		g.boxes[boxID] = bg
	}
	return bg
}

// tryAcquireLocked admits a holder under the current state, or refuses. The
// caller must hold bg.mu.
func (bg *boxGate) tryAcquireLocked(policy contract.Lock) (func(), bool) {
	if policy == contract.LockExclusive {
		if bg.exclusive || bg.shared > 0 {
			return nil, false
		}
		bg.exclusive = true
		return bg.releaseExclusive, true
	}
	// A waiting exclusive holder has priority over new shared holders.
	if bg.exclusive || bg.exclusiveWaiting > 0 {
		return nil, false
	}
	bg.shared++
	return bg.releaseShared, true
}

// releaseShared ends one shared hold, opening the gate for an exclusive holder
// once the last shared holder leaves.
func (bg *boxGate) releaseShared() {
	bg.mu.Lock()
	if bg.shared > 0 {
		bg.shared--
	}
	open := bg.shared == 0
	bg.mu.Unlock()
	if open {
		bg.broadcast()
	}
}

// releaseExclusive ends the exclusive hold.
func (bg *boxGate) releaseExclusive() {
	bg.mu.Lock()
	bg.exclusive = false
	bg.mu.Unlock()
	bg.broadcast()
}

// broadcast wakes every parked holder; each re-checks under the lock and either
// acquires or parks again.
func (bg *boxGate) broadcast() {
	bg.mu.Lock()
	waiters := bg.waiters
	bg.waiters = nil
	bg.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// removeWaiterLocked drops a parked channel that is no longer selected on. The
// caller must hold bg.mu.
func (bg *boxGate) removeWaiterLocked(ch chan struct{}) {
	for i, w := range bg.waiters {
		if w == ch {
			bg.waiters = append(bg.waiters[:i], bg.waiters[i+1:]...)
			return
		}
	}
}
