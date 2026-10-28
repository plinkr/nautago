package platform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsProcessRunningMatchesSubstrings(t *testing.T) {
	if len(processNames()) == 0 {
		t.Skip("the process table is not available here")
	}

	fragment := filepath.Base(os.Args[0])
	if fragment == "" || fragment == "nautago-absent" {
		t.Skip("the test binary has no usable name")
	}
	if !IsProcessRunning(fragment) {
		t.Errorf("IsProcessRunning(%q) = false, want the running test binary", fragment)
	}
}

func TestIsProcessRunningIgnoresCase(t *testing.T) {
	if len(processNames()) == 0 {
		t.Skip("the process table is not available here")
	}
	fragment := filepath.Base(os.Args[0])
	if !IsProcessRunning(strings.ToUpper(fragment)) {
		t.Errorf("IsProcessRunning(%q) = false, want a case insensitive match", fragment)
	}
}

func TestIsProcessRunningRejectsAnAbsentProcess(t *testing.T) {
	if IsProcessRunning("nautago-process-that-does-not-exist") {
		t.Error("a process that cannot exist was reported as running")
	}
}

func TestStopOpenVPNIsANoOpWhenItIsNotRunning(t *testing.T) {
	if IsProcessRunning("openvpn") {
		t.Skip("openvpn is running on this machine")
	}
	if err := StopOpenVPN(context.Background()); err != nil {
		t.Errorf("StopOpenVPN: %v", err)
	}
}

func TestProtonVPNIsNotReportedWhenNetworkManagerIsAbsent(t *testing.T) {
	if _, err := exec.LookPath("nmcli"); err != nil {
		if IsProtonVPNRunning(context.Background()) {
			t.Error("a ProtonVPN tunnel was reported without Network Manager")
		}
		if err := StopProtonVPN(context.Background()); err != nil {
			t.Errorf("StopProtonVPN: %v", err)
		}
	}
}

func TestStopProtonVPNRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := StopProtonVPN(ctx); err != nil {
		t.Errorf("StopProtonVPN: %v", err)
	}
}

func TestWaitProcessGoneReturnsWhenTheProcessDisappears(t *testing.T) {
	done := make(chan error, 1)
	go func() { done <- waitProcessGone(context.Background(), "nautago-absent", 50*time.Millisecond) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("waitProcessGone: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("waitProcessGone did not return for an absent process")
	}
}

func TestWaitProcessGoneHonoursCancellation(t *testing.T) {
	if len(processNames()) == 0 {
		t.Skip("the process table is not available here")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitProcessGone(ctx, filepath.Base(os.Args[0]), time.Hour)
	if err == nil {
		t.Error("waitProcessGone ignored a cancelled context")
	}
}

func TestProcessNamesAreReadable(t *testing.T) {
	if len(processNames()) == 0 {
		t.Skip("the process table is not available on this platform")
	}
}
