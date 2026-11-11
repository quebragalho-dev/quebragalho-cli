// Package yieldnow carries the TUI's "send now" request into a running
// agent turn. One Signal lives per run: while a backgroundable tool wait
// (exec_command's yield wait, write_stdin polling) is parked, Request wakes
// it at once and reports true; with nothing parked it reports false and the
// TUI interrupts the turn instead. A cancelled run's Signal never affects
// the next run — the TUI mints a fresh one per run.
package yieldnow

import (
	"context"
	"sync"
)

// Signal wakes one run's backgroundable waits. The zero value is not usable;
// build one with New. Safe for concurrent use.
type Signal struct {
	mu      sync.Mutex
	waiting int
	wake    chan struct{}
}

// New returns a Signal with no waiter parked.
func New() *Signal {
	return &Signal{wake: make(chan struct{})}
}

// Request wakes the currently parked backgroundable waits, if any, and
// reports whether it woke something. Nil-safe: a nil Signal reports false.
func (s *Signal) Request() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waiting == 0 {
		return false
	}
	close(s.wake)
	s.wake = make(chan struct{})
	return true
}

// Park registers the caller as a backgroundable waiter and returns the
// channel that closes when Request fires plus the release func the waiter
// must call when it stops waiting. The release is idempotent, so a double
// release can never drive the waiter count negative. The channel stays valid
// across later Requests: a waiter always observes the Request that overlapped
// its park. Nil-safe: a nil Signal parks on a channel that never fires.
func (s *Signal) Park() (<-chan struct{}, func()) {
	if s == nil {
		return nil, func() {}
	}
	s.mu.Lock()
	s.waiting++
	wake := s.wake
	s.mu.Unlock()
	var once sync.Once
	return wake, func() {
		once.Do(func() {
			s.mu.Lock()
			s.waiting--
			s.mu.Unlock()
		})
	}
}

// ctxKey carries the run's Signal to the tools through the run context.
type ctxKey struct{}

// WithSignal attaches the run's Signal to ctx for the tools to wake on. A
// nil Signal leaves ctx untouched.
func WithSignal(ctx context.Context, s *Signal) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext returns the run's Signal attached to ctx, or nil when the
// caller runs without one (then waits behave exactly as before).
func FromContext(ctx context.Context) *Signal {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(ctxKey{}).(*Signal)
	return s
}
