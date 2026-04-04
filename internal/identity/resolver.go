package identity

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/spf13/afero"

	"github.com/wyattanderson/pve-imds/internal/tapwatch"
	"github.com/wyattanderson/pve-imds/internal/vmconfig"
)

var (
	lookupTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pve_imds_identity_lookups_total",
		Help: "Total identity Lookup calls, by result.",
	}, []string{"result"})

	lookupDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "pve_imds_identity_lookup_duration_seconds",
		Help:    "Latency of identity Lookup calls.",
		Buckets: prometheus.ExponentialBuckets(0.000025, 2, 12), // 25µs–51ms
	})

	cacheEntries = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pve_imds_identity_cache_entries",
		Help: "Current number of entries in the identity resolver cache.",
	})
)

// tapIfaceRe matches and captures vmid and netIndex from tap interface names.
var tapIfaceRe = regexp.MustCompile(`^tap(\d+)i(\d+)$`)

// Resolver maintains the in-memory identity cache.
//
// It implements tapwatch.EventSink: Created events trigger populate and Deleted
// events trigger invalidate. The Stage 4 file watcher calls ReloadConfig
// directly when it detects file-system changes.
type Resolver struct {
	fs   afero.Fs
	log  *slog.Logger
	node string

	mu            sync.RWMutex
	entries       map[string]*entry      // key: ifname e.g. "tap100i0"
	vmidToIfnames map[int][]string       // secondary index for config reloads
	pendingIfaces map[int][]pendingIface // interfaces waiting for their config file
}

// New returns a Resolver that reads VM config files from fs. It records the
// local hostname as the node name; if that fails, New returns an error.
func New(fs afero.Fs, log *slog.Logger) (*Resolver, error) {
	node, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("identity: resolve hostname: %w", err)
	}
	return &Resolver{
		fs:            fs,
		log:           log,
		node:          node,
		entries:       make(map[string]*entry),
		vmidToIfnames: make(map[int][]string),
		pendingIfaces: make(map[int][]pendingIface),
	}, nil
}

// Lookup verifies and returns the VM identity for an incoming packet.
//
// It checks, in order:
//  1. ifname is in the cache.
//  2. ifindex matches the one recorded at population time.
//  3. srcMAC matches the netN entry in the parsed config.
//
// All three checks must pass; any failure returns a sentinel error.
func (r *Resolver) Lookup(ifname string, ifindex int32, srcMAC net.HardwareAddr) (*VMRecord, error) {
	start := time.Now()
	defer func() { lookupDuration.Observe(time.Since(start).Seconds()) }()

	r.mu.RLock()
	defer r.mu.RUnlock()

	e, ok := r.entries[ifname]
	if !ok {
		lookupTotal.WithLabelValues("not_found").Inc()
		return nil, ErrNotFound
	}

	if e.ifindex != ifindex {
		lookupTotal.WithLabelValues("ifindex_mismatch").Inc()
		return nil, ErrIfindexMismatch
	}

	dev, ok := e.config.Networks[e.netIndex]
	if !ok {
		lookupTotal.WithLabelValues("network_not_found").Inc()
		return nil, ErrNetworkNotFound
	}

	if !bytes.Equal(dev.MAC, srcMAC) {
		lookupTotal.WithLabelValues("mac_mismatch").Inc()
		return nil, ErrMACMismatch
	}

	lookupTotal.WithLabelValues("ok").Inc()
	return &VMRecord{
		Node:     r.node,
		VMID:     e.vmid,
		NetIndex: e.netIndex,
		IfIndex:  e.ifindex,
		Config:   e.config,
	}, nil
}

// readConfig reads and parses the VM config file for vmid.
func (r *Resolver) readConfig(vmid int) (*vmconfig.VMConfig, error) {
	raw, err := afero.ReadFile(r.fs, fmt.Sprintf("/etc/pve/qemu-server/%d.conf", vmid))
	if err != nil {
		return nil, err
	}
	return vmconfig.ParseConfig(raw)
}

// insertLocked adds a fully-populated cache entry. Must be called with r.mu
// held for writing.
func (r *Resolver) insertLocked(vmid, netIndex int, ifname string, ifindex int32, cfg *vmconfig.VMConfig) {
	r.entries[ifname] = &entry{
		vmid:     vmid,
		netIndex: netIndex,
		ifindex:  ifindex,
		config:   cfg,
	}
	cacheEntries.Inc()
	r.addIfname(vmid, ifname)
}

// ReloadConfig re-reads the config file for vmid and updates all cache entries
// that share it. It also promotes any pending interfaces (tap interfaces that
// appeared before the config file existed) to full cache entries. Called by the
// file watcher on Create/Write events for a .conf file. A parse failure leaves
// the existing cached config and pending entries intact.
func (r *Resolver) ReloadConfig(vmid int) {
	cfg, err := r.readConfig(vmid)
	if err != nil {
		r.log.Warn("identity: reload config: failed", "vmid", vmid, "err", err)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Update existing cache entries.
	for _, ifname := range r.vmidToIfnames[vmid] {
		if e, ok := r.entries[ifname]; ok {
			e.config = cfg
		}
	}

	// Promote pending interfaces that were waiting for this config.
	for _, p := range r.pendingIfaces[vmid] {
		_, netIndex, err := parseIfname(p.ifname)
		if err != nil {
			continue // ifname was already validated in populate; shouldn't happen
		}
		r.log.Debug("identity: promoting pending interface", "ifname", p.ifname, "vmid", vmid)
		r.insertLocked(vmid, netIndex, p.ifname, p.ifindex, cfg)
	}
	delete(r.pendingIfaces, vmid)
}

// HandleLinkEvent implements tapwatch.EventSink.
func (r *Resolver) HandleLinkEvent(ctx context.Context, ev tapwatch.Event) {
	switch ev.Type {
	case tapwatch.Created:
		if err := r.populate(ctx, ev.Name, ev.Index); err != nil {
			r.log.WarnContext(ctx, "identity: populate failed", "ifname", ev.Name, "err", err)
		}
	case tapwatch.Deleted:
		r.invalidate(ev.Name)
	}
}

// populate reads the VM config for the given tap interface and inserts an entry
// into the cache. If the config file is not yet available (e.g., during live
// migration), the interface is registered as pending so that a subsequent
// ReloadConfig call can complete the entry when the file arrives. It is safe
// to call concurrently.
func (r *Resolver) populate(ctx context.Context, ifname string, ifindex int32) error {
	vmid, netIndex, err := parseIfname(ifname)
	if err != nil {
		return err
	}

	cfg, err := r.readConfig(vmid)
	if err != nil {
		// Config not yet available — register as pending.
		r.log.DebugContext(ctx, "identity: config unavailable, registering as pending",
			"ifname", ifname, "vmid", vmid)
		r.mu.Lock()
		r.registerPendingLocked(vmid, ifname, ifindex)
		r.mu.Unlock()
		return fmt.Errorf("read config: %w", err)
	}

	r.log.DebugContext(ctx, "identity: populating cache entry", "ifname", ifname, "vmid", vmid)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.insertLocked(vmid, netIndex, ifname, ifindex, cfg)
	return nil
}

// registerPendingLocked records ifname/ifindex as pending for vmid, replacing
// any existing pending entry for the same ifname (handles ifindex change on
// interface recreation). Must be called with r.mu held for writing.
func (r *Resolver) registerPendingLocked(vmid int, ifname string, ifindex int32) {
	for i, p := range r.pendingIfaces[vmid] {
		if p.ifname == ifname {
			r.pendingIfaces[vmid][i].ifindex = ifindex
			return
		}
	}
	r.pendingIfaces[vmid] = append(r.pendingIfaces[vmid], pendingIface{ifname, ifindex})
}

// RecordByName returns the cached VMRecord for ifname, verifying that the
// kernel ifindex still matches the one recorded at population time. This guards
// against a tap interface being deleted and recreated (with a new VM) before
// the old per-interface Runtime has been torn down.
//
// Source MAC is not checked because it is not available at the HTTP handler layer.
func (r *Resolver) RecordByName(ifname string, ifindex int32) (*VMRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[ifname]
	if !ok {
		return nil, ErrNotFound
	}
	if e.ifindex != ifindex {
		return nil, ErrIfindexMismatch
	}
	return &VMRecord{
		Node:     r.node,
		VMID:     e.vmid,
		NetIndex: e.netIndex,
		IfIndex:  e.ifindex,
		Config:   e.config,
	}, nil
}

// invalidate removes the cache entry for ifname, if present. It also removes
// any pending entry for the same ifname so that a subsequent ReloadConfig does
// not resurrect a deleted interface.
func (r *Resolver) invalidate(ifname string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.entries[ifname]; ok {
		r.log.Debug("identity: evicting cache entry", "ifname", ifname, "vmid", e.vmid)
		delete(r.entries, ifname)
		cacheEntries.Dec()
		r.removeIfname(e.vmid, ifname)
	}

	// Also remove from pending (interface deleted before config arrived).
	vmid, _, err := parseIfname(ifname)
	if err != nil {
		return
	}
	r.pendingIfaces[vmid] = slices.DeleteFunc(r.pendingIfaces[vmid], func(p pendingIface) bool {
		return p.ifname == ifname
	})
	if len(r.pendingIfaces[vmid]) == 0 {
		delete(r.pendingIfaces, vmid)
	}
}

// invalidateByVMID evicts all cache entries for vmid and discards any pending
// interfaces. Called by the file watcher when a config file is removed (VM
// deleted or decommissioned).
func (r *Resolver) invalidateByVMID(vmid int) {
	r.mu.RLock()
	ifnames := slices.Clone(r.vmidToIfnames[vmid])
	r.mu.RUnlock()
	for _, ifname := range ifnames {
		r.invalidate(ifname)
	}
	r.mu.Lock()
	delete(r.pendingIfaces, vmid)
	r.mu.Unlock()
}

// parseIfname extracts vmid and netIndex from a tap interface name of the form
// tap{vmid}i{netIndex}.
func parseIfname(name string) (vmid, netIndex int, err error) {
	m := tapIfaceRe.FindStringSubmatch(name)
	if m == nil {
		return 0, 0, fmt.Errorf("identity: %q is not a tap interface name", name)
	}
	vmid, _ = strconv.Atoi(m[1])
	netIndex, _ = strconv.Atoi(m[2])
	return vmid, netIndex, nil
}
