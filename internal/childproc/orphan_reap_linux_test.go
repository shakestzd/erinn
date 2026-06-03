//go:build linux

package childproc

import (
	"syscall"
	"testing"
)

// TestChildSysProcAttrPdeathsigLinux verifies that on Linux Pdeathsig is set
// to SIGTERM so the kernel delivers the signal to the child the moment the
// parent dies.
func TestChildSysProcAttrPdeathsigLinux(t *testing.T) {
	attr := childSysProcAttr()
	if attr == nil {
		t.Fatal("childSysProcAttr() returned nil")
	}
	if attr.Pdeathsig != syscall.SIGTERM {
		t.Errorf("Pdeathsig = %v; want SIGTERM (%v)", attr.Pdeathsig, syscall.SIGTERM)
	}
}
