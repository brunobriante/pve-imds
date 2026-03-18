package vmproc

import (
	"fmt"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeStat builds a /proc/{pid}/stat line with the given process name and
// starttime. All other fields are filled with plausible but unimportant values.
//
// Field layout (1-indexed):
//
//	1:pid  2:(comm)  3:state  4:ppid  5:pgrp  6:session  7:tty_nr  8:tpgid
//	9:flags  10:minflt  11:cminflt  12:majflt  13:cmajflt  14:utime  15:stime
//	16:cutime  17:cstime  18:priority  19:nice  20:num_threads  21:itrealvalue
//	22:starttime  ...
func makeStat(pid int, comm string, startTime uint64) string {
	// Fields 3–21 (19 placeholder values), then starttime at position 22.
	return fmt.Sprintf(
		"%d (%s) S 1 %d %d 0 -1 4194304 100 0 0 0 10 5 0 0 20 0 1 0 %d 1234567 100 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0",
		pid, comm, pid, pid, startTime,
	)
}

func newFS(t *testing.T) afero.Fs {
	t.Helper()
	return afero.NewMemMapFs()
}

func writePIDFile(t *testing.T, fs afero.Fs, vmid, pid int) {
	t.Helper()
	path := fmt.Sprintf("/var/run/qemu-server/%d.pid", vmid)
	require.NoError(t, afero.WriteFile(fs, path, fmt.Appendf(nil, "%d\n", pid), 0644))
}

func writeStatFile(t *testing.T, fs afero.Fs, pid int, comm string, startTime uint64) {
	t.Helper()
	path := fmt.Sprintf("/proc/%d/stat", pid)
	require.NoError(t, afero.WriteFile(fs, path, []byte(makeStat(pid, comm, startTime)), 0444))
}

// TestLookup is the happy path: PID file and stat file both present.
func TestLookup(t *testing.T) {
	fs := newFS(t)
	writePIDFile(t, fs, 100, 4567)
	writeStatFile(t, fs, 4567, "qemu-system-x86", 98765432)

	info, err := New(fs).Lookup(100)
	require.NoError(t, err)
	assert.Equal(t, 4567, info.PID)
	assert.Equal(t, uint64(98765432), info.StartTime)
}

// TestLookupMissingPIDFile covers a VM that is not running.
func TestLookupMissingPIDFile(t *testing.T) {
	_, err := New(newFS(t)).Lookup(100)
	require.Error(t, err)
}

// TestLookupMissingStatFile covers the race where the process exits between
// reading the PID file and reading /proc/{pid}/stat.
func TestLookupMissingStatFile(t *testing.T) {
	fs := newFS(t)
	writePIDFile(t, fs, 100, 4567)
	// No stat file written — process already gone.

	_, err := New(fs).Lookup(100)
	require.Error(t, err)
}

// TestLookupInvalidPIDContent covers a PID file with non-numeric content.
func TestLookupInvalidPIDContent(t *testing.T) {
	fs := newFS(t)
	require.NoError(t, afero.WriteFile(fs, "/var/run/qemu-server/100.pid", []byte("not-a-pid\n"), 0644))

	_, err := New(fs).Lookup(100)
	require.Error(t, err)
}

// TestLookupZeroPID covers a PID file containing "0", which is invalid.
func TestLookupZeroPID(t *testing.T) {
	fs := newFS(t)
	require.NoError(t, afero.WriteFile(fs, "/var/run/qemu-server/100.pid", []byte("0\n"), 0644))

	_, err := New(fs).Lookup(100)
	require.Error(t, err)
}

// TestReadStartTime verifies that ReadStartTime can be called independently
// (used by the Stage 3 resolver for liveness checks at lookup time).
func TestReadStartTime(t *testing.T) {
	fs := newFS(t)
	writeStatFile(t, fs, 999, "qemu-system-x86", 11111111)

	st, err := New(fs).ReadStartTime(999)
	require.NoError(t, err)
	assert.Equal(t, uint64(11111111), st)
}

// TestParseStartTimeSpacesInComm verifies correct parsing when the process
// name (field 2) contains spaces, which would break a naive whitespace split.
func TestParseStartTimeSpacesInComm(t *testing.T) {
	// "kvm: vm 100" as process name — spaces inside the parens.
	stat := makeStat(1234, "kvm: vm 100", 55555555)
	v, err := parseStartTime(stat)
	require.NoError(t, err)
	assert.Equal(t, uint64(55555555), v)
}

// TestParseStartTimeParensInComm verifies correct parsing when the process
// name contains parentheses (e.g. "qemu(arm)").
func TestParseStartTimeParensInComm(t *testing.T) {
	stat := makeStat(1234, "qemu(arm)", 77777777)
	v, err := parseStartTime(stat)
	require.NoError(t, err)
	assert.Equal(t, uint64(77777777), v)
}

// TestParseStartTimeMalformed covers stat content that has no closing paren.
func TestParseStartTimeMalformed(t *testing.T) {
	_, err := parseStartTime("1234 no-parens-at-all S 1 2 3")
	assert.Error(t, err)
}

// TestParseStartTimeTooShort covers stat content that is truncated before
// field 22.
func TestParseStartTimeTooShort(t *testing.T) {
	_, err := parseStartTime("1234 (comm) S 1 2")
	assert.Error(t, err)
}
