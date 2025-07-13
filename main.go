package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mdlayher/netlink"
)

// Linux netlink constants
const (
	NETLINK_ROUTE = 0
	RTNLGRP_LINK  = 1
	RTM_NEWLINK   = 16
	RTM_DELLINK   = 17
	IFLA_IFNAME   = 3
	IFF_UP        = 1 << 0
	IFF_RUNNING   = 1 << 6
)

// IfInfomsg represents the netlink interface info message
type IfInfomsg struct {
	Family uint8
	_      uint8
	Type   uint16
	Index  int32
	Flags  uint32
	Change uint32
}

// TAPDeviceWatcher watches for TAP device creation and deletion events
type TAPDeviceWatcher struct {
	conn *netlink.Conn
}

// NewTAPDeviceWatcher creates a new TAP device watcher
func NewTAPDeviceWatcher() (*TAPDeviceWatcher, error) {
	conn, err := netlink.Dial(NETLINK_ROUTE, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial netlink: %w", err)
	}

	return &TAPDeviceWatcher{
		conn: conn,
	}, nil
}

// Watch starts watching for TAP device events
func (w *TAPDeviceWatcher) Watch(ctx context.Context) error {
	// Subscribe to RTNLGRP_LINK events (network interface events)
	if err := w.conn.JoinGroup(RTNLGRP_LINK); err != nil {
		return fmt.Errorf("failed to join RTNLGRP_LINK group: %w", err)
	}

	log.Println("Started watching for TAP device events...")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Receive netlink messages
			msgs, err := w.conn.Receive()
			if err != nil {
				log.Printf("Error receiving netlink messages: %v", err)
				continue
			}

			for _, msg := range msgs {
				w.handleNetlinkMessage(msg)
			}
		}
	}
}

// handleNetlinkMessage processes individual netlink messages
func (w *TAPDeviceWatcher) handleNetlinkMessage(msg netlink.Message) {
	// Parse the netlink message header
	if len(msg.Data) < binary.Size(IfInfomsg{}) {
		return
	}

	var ifinfo IfInfomsg
	if err := binary.Read(bytes.NewReader(msg.Data[:binary.Size(IfInfomsg{})]), binary.LittleEndian, &ifinfo); err != nil {
		log.Printf("Error parsing ifinfomsg: %v", err)
		return
	}

	// Extract interface name from netlink attributes
	attrs, err := netlink.NewAttributeDecoder(msg.Data[binary.Size(IfInfomsg{}):])
	if err != nil {
		log.Printf("Error creating attribute decoder: %v", err)
		return
	}

	var ifname string
	for attrs.Next() {
		if attrs.Type() == IFLA_IFNAME {
			ifname = attrs.String()
			break
		}
	}

	if ifname == "" {
		return
	}

	// Check if this is a TAP device
	if !strings.HasPrefix(ifname, "tap") {
		return
	}

	// Determine the event type based on the message type
	switch msg.Header.Type {
	case RTM_NEWLINK:
		log.Printf("TAP device created: %s", ifname)
	case RTM_DELLINK:
		log.Printf("TAP device deleted: %s", ifname)
	}

	log.Printf("TAP device %s flags: %0b", ifname, ifinfo.Flags)

	if ifinfo.Flags&IFF_UP > 0 {
		log.Printf("TAP device %s is up", ifname)
	} else {
		log.Printf("TAP device %s is not up", ifname)
	}
}

// Close closes the netlink connection
func (w *TAPDeviceWatcher) Close() error {
	return w.conn.Close()
}

func main() {
	// Set up signal handling for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Create the TAP device watcher
	watcher, err := NewTAPDeviceWatcher()
	if err != nil {
		log.Fatalf("Failed to create TAP device watcher: %v", err)
	}
	defer watcher.Close()

	// Start watching in a goroutine
	go func() {
		if err := watcher.Watch(ctx); err != nil {
			log.Printf("Watcher error: %v", err)
		}
	}()

	log.Println("TAP device watcher started. Press Ctrl+C to stop.")

	// Wait for shutdown signal
	<-ctx.Done()
	log.Println("Shutting down...")
	cancel()
}
