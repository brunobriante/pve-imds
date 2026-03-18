//go:build linux

package iface

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"syscall"

	"golang.org/x/sys/unix"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	xdplink "gvisor.dev/gvisor/pkg/tcpip/link/xdp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"

	"github.com/wyattanderson/pve-imds/internal/identity"
	"github.com/wyattanderson/pve-imds/internal/manager"
	"github.com/wyattanderson/pve-imds/internal/xdp"
)

// Runtime is the per-interface worker. It sets up an AF_XDP socket, attaches
// an XDP program to redirect IMDS traffic, and serves HTTP on the gvisor stack.
type Runtime struct {
	log      *slog.Logger
	resolver *identity.Resolver
	ifindex  int32  // primary identifier
	name     string // for logging/debugging only
}

// New constructs a Runtime for the given tap interface.
func New(log *slog.Logger, resolver *identity.Resolver, ifindex int32, name string) *Runtime {
	return &Runtime{log: log, resolver: resolver, ifindex: ifindex, name: name}
}

// NewFactory returns a manager.RuntimeFactory that constructs a Runtime for
// each tap interface, sharing the provided logger and identity resolver.
func NewFactory(log *slog.Logger, resolver *identity.Resolver) manager.RuntimeFactory {
	return func(ifindex int32, name string) manager.InterfaceRuntime {
		return New(log, resolver, ifindex, name)
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
	defer syscall.Close(sockfd)

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
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		rec, err := r.resolver.RecordByName(r.name, r.ifindex)
		if err != nil {
			http.Error(w, fmt.Sprintf("identity lookup failed: %v", err), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "node:     %s\n", rec.Node)
		fmt.Fprintf(w, "vmid:     %d\n", rec.VMID)
		fmt.Fprintf(w, "netindex: %d\n", rec.NetIndex)
		fmt.Fprintf(w, "pid:      %d\n", rec.ProcessInfo.PID)
		fmt.Fprintf(w, "\n[vmconfig]\n")
		fmt.Fprintf(w, "name:        %s\n", rec.Config.Name)
		fmt.Fprintf(w, "ostype:      %s\n", rec.Config.OSType)
		fmt.Fprintf(w, "description: %s\n", rec.Config.Description)
		fmt.Fprintf(w, "tags:        %v\n", rec.Config.Tags)
		for idx, dev := range rec.Config.Networks {
			fmt.Fprintf(w, "net%d:        model=%s mac=%s bridge=%s\n", idx, dev.Model, dev.MAC, dev.Bridge)
		}
		for k, v := range rec.Config.Raw {
			fmt.Fprintf(w, "%s: %s\n", k, v)
		}
	})
	return serveIMDS(ctx, listener, mux)
}
