// Package manager provides a lifecycle manager that starts and stops a
// per-interface runtime as tap interfaces appear and disappear.
package manager

import (
	"context"
	"log/slog"

	"go.uber.org/fx"

	"github.com/wyattanderson/pve-imds/internal/tapwatch"
)

// InterfaceRuntime is the per-interface worker.
type InterfaceRuntime interface {
	Run(ctx context.Context) error
}

// RuntimeFactory constructs an InterfaceRuntime for a named tap interface.
// Stored as unexported field on Manager; tests override directly (same package).
type RuntimeFactory func(name string) InterfaceRuntime

type managedIface struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when Run returns
}

// Manager implements tapwatch.EventSink and manages the lifecycle of
// per-interface runtimes.
type Manager struct {
	log     *slog.Logger
	factory RuntimeFactory
	events  chan tapwatch.Event
	active  map[string]*managedIface
}

// New constructs a Manager with the given runtime factory.
func New(log *slog.Logger, factory RuntimeFactory) *Manager {
	return &Manager{
		log:     log,
		factory: factory,
		events:  make(chan tapwatch.Event, 64),
		active:  make(map[string]*managedIface),
	}
}

// HandleLinkEvent implements tapwatch.EventSink. It enqueues the event
// non-blocking, dropping it only if ctx is cancelled.
func (m *Manager) HandleLinkEvent(ctx context.Context, ev tapwatch.Event) {
	select {
	case m.events <- ev:
	case <-ctx.Done():
	}
}

// run is the event loop. Call in a goroutine; returns when ctx is cancelled.
func (m *Manager) run(ctx context.Context) {
	defer m.stopAll()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-m.events:
			switch ev.Type {
			case tapwatch.Created:
				m.startIface(ctx, ev.Name)
			case tapwatch.Deleted:
				m.stopIface(ev.Name)
			}
		}
	}
}

func (m *Manager) startIface(parentCtx context.Context, name string) {
	if _, exists := m.active[name]; exists {
		m.log.Warn("duplicate Created event, ignoring", "name", name)
		return
	}
	rt := m.factory(name)
	ctx, cancel := context.WithCancel(parentCtx)
	mi := &managedIface{cancel: cancel, done: make(chan struct{})}
	m.active[name] = mi
	go func() {
		defer close(mi.done)
		if err := rt.Run(ctx); err != nil && ctx.Err() == nil {
			m.log.Error("runtime exited with error", "name", name, "err", err)
		}
	}()
	m.log.Info("started interface runtime", "name", name)
}

func (m *Manager) stopIface(name string) {
	mi, exists := m.active[name]
	if !exists {
		m.log.Warn("Deleted event for unknown interface, ignoring", "name", name)
		return
	}
	delete(m.active, name)
	mi.cancel()
	<-mi.done
	m.log.Info("stopped interface runtime", "name", name)
}

func (m *Manager) stopAll() {
	for name, mi := range m.active {
		mi.cancel()
		<-mi.done
		m.log.Info("stopped interface runtime", "name", name)
	}
	m.active = make(map[string]*managedIface)
}

// Register is an fx.Invoke target that wires the manager's event loop into
// the fx lifecycle. It has no awareness of the watcher.
func Register(lc fx.Lifecycle, m *Manager) {
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() { defer close(loopDone); m.run(ctx) }()
			return nil
		},
		OnStop: func(_ context.Context) error {
			cancel()
			<-loopDone
			return nil
		},
	})
}

// stubRuntime blocks until ctx is cancelled. Used as the default factory.
type stubRuntime struct{ name string }

func (r *stubRuntime) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func stubRuntimeFactory(name string) InterfaceRuntime {
	return &stubRuntime{name: name}
}
