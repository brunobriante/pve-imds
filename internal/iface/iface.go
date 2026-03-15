//go:build linux

package iface

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	xdplink "gvisor.dev/gvisor/pkg/tcpip/link/xdp"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"

	"github.com/wyattanderson/pve-imds/internal/manager"
	"github.com/wyattanderson/pve-imds/internal/xdp"
)

// Runtime is the per-interface worker. It sets up an AF_XDP socket, attaches
// an XDP program to redirect IMDS traffic, and serves HTTP on the gvisor stack.
type Runtime struct {
	log     *slog.Logger
	ifindex int32  // primary identifier
	name    string // for logging/debugging only
}

// New constructs a Runtime for the given tap interface.
func New(log *slog.Logger, ifindex int32, name string) *Runtime {
	return &Runtime{log: log, ifindex: ifindex, name: name}
}

// NewFactory returns a manager.RuntimeFactory that constructs a Runtime for
// each tap interface, sharing the provided logger.
func NewFactory(log *slog.Logger) manager.RuntimeFactory {
	return func(ifindex int32, name string) manager.InterfaceRuntime {
		return New(log, ifindex, name)
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

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, arp.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
		HandleLocal:        true,
	})
	defer s.Close()

	const nicID = tcpip.NICID(1)

	sarp := xdp.NewStaticARPEndpoint(r.log, le, s, nicID)

	if tcpipErr := s.CreateNIC(nicID, sarp); tcpipErr != nil {
		return fmt.Errorf("create NIC: %v", tcpipErr)
	}
	if tcpipErr := s.EnableNIC(nicID); tcpipErr != nil {
		return fmt.Errorf("enable NIC: %v", tcpipErr)
	}

	imdsAddr := tcpip.AddrFrom4([4]byte{169, 254, 169, 254})
	protocolAddr := tcpip.ProtocolAddress{
		Protocol: ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   imdsAddr,
			PrefixLen: 32,
		},
	}
	if tcpipErr := s.AddProtocolAddress(nicID, protocolAddr, stack.AddressProperties{}); tcpipErr != nil {
		return fmt.Errorf("add protocol address: %v", tcpipErr)
	}

	zeroSubnet, err := tcpip.NewSubnet(
		tcpip.AddrFrom4([4]byte{}),
		tcpip.MaskFrom(strings.Repeat("\x00", 4)),
	)
	if err != nil {
		return fmt.Errorf("create default subnet: %w", err)
	}
	s.SetRouteTable([]tcpip.Route{{
		Destination: zeroSubnet,
		NIC:         nicID,
	}})

	listener, err := gonet.ListenTCP(s, tcpip.FullAddress{Addr: imdsAddr, Port: 80}, ipv4.ProtocolNumber)
	if err != nil {
		return fmt.Errorf("listen TCP 169.254.169.254:80: %w", err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello, World!"))
	})
	server := &http.Server{Handler: mux}

	g, _ := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutCtx)
	})
	return g.Wait()
}
