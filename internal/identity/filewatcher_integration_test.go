//go:build integration

package identity

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// spyResolver records calls made by the FileWatcher. It satisfies resolverSink.
type spyResolver struct {
	mu               sync.Mutex
	reloadConfigs    []int
	invalidatedVMIDs []int
}

func (s *spyResolver) ReloadConfig(vmid int) {
	s.mu.Lock()
	s.reloadConfigs = append(s.reloadConfigs, vmid)
	s.mu.Unlock()
}

func (s *spyResolver) invalidateByVMID(vmid int) {
	s.mu.Lock()
	s.invalidatedVMIDs = append(s.invalidatedVMIDs, vmid)
	s.mu.Unlock()
}

func (s *spyResolver) hasReloadConfig(vmid int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.reloadConfigs {
		if v == vmid {
			return true
		}
	}
	return false
}

func (s *spyResolver) hasInvalidated(vmid int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.invalidatedVMIDs {
		if v == vmid {
			return true
		}
	}
	return false
}

// startWatcher creates a FileWatcher watching the given temp dir and starts
// Run in a goroutine. Returns a cancel func to stop it.
func startWatcher(t *testing.T, spy *spyResolver, confDir string) context.CancelFunc {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	fw, err := newFileWatcherWithDirs(spy, log, confDir)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel() })
	go func() { _ = fw.Run(ctx) }()
	return cancel
}

const (
	eventTimeout = 2 * time.Second
	pollInterval = 25 * time.Millisecond
)

// TestIntegrationConfWrite verifies that writing a .conf file triggers
// ReloadConfig with the correct VMID.
func TestIntegrationConfWrite(t *testing.T) {
	confDir := t.TempDir()
	spy := &spyResolver{}
	startWatcher(t, spy, confDir)

	const vmid = 42
	path := filepath.Join(confDir, fmt.Sprintf("%d.conf", vmid))
	require.NoError(t, os.WriteFile(path, []byte("name: vm42\n"), 0644))

	require.Eventually(t, func() bool { return spy.hasReloadConfig(vmid) },
		eventTimeout, pollInterval, "ReloadConfig(%d) not called after conf write", vmid)
}

// TestIntegrationConfDelete verifies that removing a .conf file triggers
// invalidateByVMID with the correct VMID.
func TestIntegrationConfDelete(t *testing.T) {
	confDir := t.TempDir()
	spy := &spyResolver{}
	startWatcher(t, spy, confDir)

	const vmid = 99
	path := filepath.Join(confDir, fmt.Sprintf("%d.conf", vmid))
	require.NoError(t, os.WriteFile(path, []byte("name: vm99\n"), 0644))

	// Wait for the write event to be processed before deleting.
	require.Eventually(t, func() bool { return spy.hasReloadConfig(vmid) },
		eventTimeout, pollInterval)

	require.NoError(t, os.Remove(path))

	require.Eventually(t, func() bool { return spy.hasInvalidated(vmid) },
		eventTimeout, pollInterval, "invalidateByVMID(%d) not called after conf delete", vmid)
}
