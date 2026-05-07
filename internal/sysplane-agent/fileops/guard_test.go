package fileops_test

import (
	"testing"

	"github.com/jimyag/sysplane/internal/sysplane-agent/fileops"
)

func TestPathGuard_AllowAll(t *testing.T) {
	g := fileops.NewPathGuard(nil, nil)
	if err := g.Check("/home/user/data"); err != nil {
		t.Fatalf("expected allow, got: %v", err)
	}
}

func TestPathGuard_BlocklistDenies(t *testing.T) {
	g := fileops.NewPathGuard(nil, []string{"/proc", "/sys"})
	if err := g.Check("/proc/1/maps"); err == nil {
		t.Fatal("expected block for /proc/1/maps")
	}
	if err := g.Check("/sys/kernel"); err == nil {
		t.Fatal("expected block for /sys/kernel")
	}
	if err := g.Check("/home/user"); err != nil {
		t.Fatalf("expected allow for /home/user, got: %v", err)
	}
}

func TestPathGuard_AllowlistDeniesOutside(t *testing.T) {
	g := fileops.NewPathGuard([]string{"/data"}, nil)
	if err := g.Check("/data/file.txt"); err != nil {
		t.Fatalf("expected allow, got: %v", err)
	}
	if err := g.Check("/etc/passwd"); err == nil {
		t.Fatal("expected deny for /etc/passwd")
	}
}

func TestPathGuard_BlocklistOverridesAllowlist(t *testing.T) {
	// /proc is both allowed and blocked — block wins.
	g := fileops.NewPathGuard([]string{"/proc"}, []string{"/proc"})
	if err := g.Check("/proc/cpuinfo"); err == nil {
		t.Fatal("expected block to win over allowlist")
	}
}

func TestPathGuard_TraversalBlocked(t *testing.T) {
	g := fileops.NewPathGuard([]string{"/data"}, []string{"/proc"})
	// ../../etc/passwd resolves to /etc/passwd (outside /data)
	if err := g.Check("/data/../../etc/passwd"); err == nil {
		t.Fatal("expected traversal to be denied")
	}
}

func TestPathGuard_PrefixNoFalsePositive(t *testing.T) {
	// /proc2 must NOT be blocked just because /proc is blocked.
	g := fileops.NewPathGuard(nil, []string{"/proc"})
	if err := g.Check("/proc2/something"); err != nil {
		t.Fatalf("expected /proc2 to be allowed, got: %v", err)
	}
}
