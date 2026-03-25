//go:build linux

// Package iface manages AF_XDP sockets attached to tap interfaces.
package iface

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	xdplink "gvisor.dev/gvisor/pkg/tcpip/link/xdp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"

	"github.com/wyattanderson/pve-imds/internal/identity"
	"github.com/wyattanderson/pve-imds/internal/imds"
	"github.com/wyattanderson/pve-imds/internal/manager"
	"github.com/wyattanderson/pve-imds/internal/xdp"
)

var (
	ifaceInFlight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pve_imds_http_in_flight_requests",
		Help: "Number of HTTP requests currently being served, by interface.",
	}, []string{"interface"})

	ifaceDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pve_imds_http_request_duration_seconds",
		Help:    "HTTP request latency by interface and status code class.",
		Buckets: prometheus.DefBuckets,
	}, []string{"interface", "code"})

	ifaceRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pve_imds_http_requests_total",
		Help: "Total HTTP requests served, by interface and status code class.",
	}, []string{"interface", "code"})
)

// Runtime is the per-interface worker. It sets up an AF_XDP socket, attaches
// an XDP program to redirect IMDS traffic, and serves HTTP on the gvisor stack.
type Runtime struct {
	log      *slog.Logger
	resolver *identity.Resolver
	server   imds.Server
	ifindex  int32  // primary identifier
	name     string // for logging/debugging only
}

// New constructs a Runtime for the given tap interface.
func New(log *slog.Logger, resolver *identity.Resolver, server imds.Server, ifindex int32, name string) *Runtime {
	return &Runtime{log: log, resolver: resolver, server: server, ifindex: ifindex, name: name}
}

// NewFactory returns a manager.RuntimeFactory that constructs a Runtime for
// each tap interface, sharing the provided logger, identity resolver, and IMDS
// server.
func NewFactory(log *slog.Logger, resolver *identity.Resolver, server imds.Server) manager.RuntimeFactory {
	return func(ifindex int32, name string) manager.InterfaceRuntime {
		return New(log, resolver, server, ifindex, name)
	}
}

// Run implements manager.InterfaceRuntime. It blocks until ctx is cancelled or
// a fatal error occurs.
func (r *Runtime) Run(ctx context.Context) error {
	iface, err := net.InterfaceByIndex(int(r.ifindex))
	if err != nil {
		return fmt.Errorf("get interface %d (%s): %w", r.ifindex, r.name, err)
	}

	sockfd, err := syscall.Socket(unix.AF_XDP, syscall.SOCK_RAW, 0)
	if err != nil {
		return fmt.Errorf("create AF_XDP socket: %w", err)
	}
	defer syscall.Close(sockfd) //nolint:errcheck

	cleanup, err := xdp.LoadAndAttach(sockfd, iface)
	if err != nil {
		return err
	}
	defer cleanup()

	mac, err := tcpip.ParseMACAddress(iface.HardwareAddr.String())
	if err != nil {
		return fmt.Errorf("parse MAC address: %w", err)
	}

	le, err := xdplink.New(&xdplink.Options{
		FD:                sockfd,
		Address:           mac,
		Bind:              true,
		InterfaceIndex:    iface.Index,
		RXChecksumOffload: true,
	})
	if err != nil {
		return fmt.Errorf("create XDP link endpoint: %w", err)
	}

	s, err := newIMDSStack(r.log, le)
	if err != nil {
		return err
	}
	defer s.Close()

	listener, err := gonet.ListenTCP(s, tcpip.FullAddress{Addr: imdsAddr, Port: 80}, ipv4.ProtocolNumber)
	if err != nil {
		return fmt.Errorf("listen TCP 169.254.169.254:80: %w", err)
	}
	defer listener.Close() //nolint:errcheck

	log := r.log.With("iface", r.name)
	labels := prometheus.Labels{"interface": r.name}
	base := r.server.NewHandler(r.resolver, r.name, r.ifindex)
	instrumented := promhttp.InstrumentHandlerInFlight(
		ifaceInFlight.With(labels),
		promhttp.InstrumentHandlerDuration(
			ifaceDuration.MustCurryWith(labels),
			promhttp.InstrumentHandlerCounter(
				ifaceRequests.MustCurryWith(labels),
				base,
			),
		),
	)
	handler := imds.LoggingMiddleware(log, instrumented)
	return imds.Serve(ctx, listener, handler)
}
