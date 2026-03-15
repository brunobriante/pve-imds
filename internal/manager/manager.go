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

// RuntimeFactory constructs an InterfaceRuntime for a tap interface.
// ifindex is the primary key; name is provided for logging/debugging.
// Stored as unexported field on Manager; tests override directly (same package).
type RuntimeFactory func(ifindex int32, name string) InterfaceRuntime

type managedIface struct {
	name   string
	cancel context.CancelFunc
	done   chan struct{} // closed when Run returns
}

// Manager implements tapwatch.EventSink and manages the lifecycle of
// per-interface runtimes.
type Manager struct {
	log     *slog.Logger
	factory RuntimeFactory
	events  chan tapwatch.Event
	active  map[int32]*managedIface // keyed by ifindex
}

// New constructs a Manager with the given runtime factory.
func New(log *slog.Logger, factory RuntimeFactory) *Manager {
	return &Manager{
		log:     log,
		factory: factory,
		events:  make(chan tapwatch.Event, 64),
		active:  make(map[int32]*managedIface),
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
				m.startIface(ctx, ev.Index, ev.Name)
			case tapwatch.Deleted:
				m.stopIface(ev.Index)
			}
		}
	}
}

func (m *Manager) startIface(parentCtx context.Context, ifindex int32, name string) {
	if _, exists := m.active[ifindex]; exists {
		m.log.Warn("duplicate Created event, ignoring", "ifindex", ifindex, "name", name)
		return
	}
	rt := m.factory(ifindex, name)
	ctx, cancel := context.WithCancel(parentCtx)
	mi := &managedIface{name: name, cancel: cancel, done: make(chan struct{})}
	m.active[ifindex] = mi
	go func() {
		defer close(mi.done)
		if err := rt.Run(ctx); err != nil && ctx.Err() == nil {
			m.log.Error("runtime exited with error", "ifindex", ifindex, "name", name, "err", err)
		}
	}()
	m.log.Info("started interface runtime", "ifindex", ifindex, "name", name)
}

func (m *Manager) stopIface(ifindex int32) {
	mi, exists := m.active[ifindex]
	if !exists {
		m.log.Warn("Deleted event for unknown interface, ignoring", "ifindex", ifindex)
		return
	}
	delete(m.active, ifindex)
	mi.cancel()
	<-mi.done
	m.log.Info("stopped interface runtime", "ifindex", ifindex, "name", mi.name)
}

func (m *Manager) stopAll() {
	for ifindex, mi := range m.active {
		mi.cancel()
		<-mi.done
		m.log.Info("stopped interface runtime", "ifindex", ifindex, "name", mi.name)
	}
	m.active = make(map[int32]*managedIface)
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
type stubRuntime struct {
	ifindex int32
	name    string
}

func (r *stubRuntime) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func stubRuntimeFactory(ifindex int32, name string) InterfaceRuntime {
	return &stubRuntime{ifindex: ifindex, name: name}
}
