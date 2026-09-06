package doctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/git"
)

func TestRigWorktreeConfigCheck_CleanRepoExtensionEnabled_OK(t *testing.T) {
	rigPath := initWorktreeConfigTestRepo(t)
	runGitForWorktreeConfigTest(t, rigPath, "config", "extensions.worktreeConfig", "true")

	c := NewRigWorktreeConfigCheck(config.Rig{Name: "testrig", Path: rigPath})
	r := c.Run(&CheckContext{})

	if r.Status != StatusOK {
		t.Fatalf("status = %d (%s), want StatusOK", r.Status, r.Message)
	}
}

func TestRigWorktreeConfigCheck_ExtensionNotEnabled_WarnsAdvisory(t *testing.T) {
	rigPath := initWorktreeConfigTestRepo(t)

	c := NewRigWorktreeConfigCheck(config.Rig{Name: "testrig", Path: rigPath})
	r := c.Run(&CheckContext{})

	if r.Status != StatusWarning {
		t.Fatalf("status = %d (%s), want StatusWarning", r.Status, r.Message)
	}
	if r.Severity != SeverityAdvisory {
		t.Fatalf("severity = %d, want SeverityAdvisory", r.Severity)
	}
	if !strings.Contains(r.Message, "extensions.worktreeConfig") {
		t.Errorf("message = %q, want mention of extensions.worktreeConfig", r.Message)
	}
	if r.FixHint == "" {
		t.Error("FixHint should be set when extension is disabled")
	}
}

func TestRigWorktreeConfigCheck_PoisonedCoreWorktree_ErrorsBlocking(t *testing.T) {
	rigPath := initWorktreeConfigTestRepo(t)
	// Simulate the gc-qi40jt incident directly: core.worktree present in the
	// rig root's own local config.
	runGitForWorktreeConfigTest(t, rigPath, "config", "--local", "core.worktree", "/some/leaked/path")

	c := NewRigWorktreeConfigCheck(config.Rig{Name: "testrig", Path: rigPath})
	r := c.Run(&CheckContext{})

	if r.Status != StatusError {
		t.Fatalf("status = %d (%s), want StatusError", r.Status, r.Message)
	}
	if r.Severity != SeverityBlocking {
		t.Fatalf("severity = %d, want SeverityBlocking (default) for an active poisoning", r.Severity)
	}
	if !strings.Contains(r.Message, "SHARED") {
		t.Errorf("message = %q, want mention of the SHARED root config", r.Message)
	}
	if !strings.Contains(r.FixHint, "--unset core.worktree") {
		t.Errorf("FixHint = %q, want an unset hint", r.FixHint)
	}
}

func TestRigWorktreeConfigCheck_GitUnavailable_WarnsAdvisory(t *testing.T) {
	c := NewRigWorktreeConfigCheck(config.Rig{Name: "testrig", Path: t.TempDir()})
	c.gitPath = func(string) (string, error) { return "", errors.New("git unavailable") }

	r := c.Run(&CheckContext{})

	if r.Status != StatusWarning {
		t.Fatalf("status = %d (%s), want StatusWarning", r.Status, r.Message)
	}
	if r.Severity != SeverityAdvisory {
		t.Fatalf("severity = %d, want SeverityAdvisory", r.Severity)
	}
}

func TestRigWorktreeConfigCheck_CanFix(t *testing.T) {
	c := NewRigWorktreeConfigCheck(config.Rig{Name: "testrig", Path: t.TempDir()})
	if !c.CanFix() {
		t.Fatal("CanFix should return true")
	}
}

func TestRigWorktreeConfigCheck_WarmupEligible(t *testing.T) {
	c := NewRigWorktreeConfigCheck(config.Rig{Name: "testrig", Path: t.TempDir()})
	if !c.WarmupEligible() {
		t.Fatal("WarmupEligible should return true")
	}
}

func TestRigWorktreeConfigCheck_Fix_UnsetsPoisonAndEnablesExtension(t *testing.T) {
	rigPath := initWorktreeConfigTestRepo(t)
	runGitForWorktreeConfigTest(t, rigPath, "config", "--local", "core.worktree", "/some/leaked/path")

	c := NewRigWorktreeConfigCheck(config.Rig{Name: "testrig", Path: rigPath})
	if err := c.Fix(&CheckContext{}); err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}

	r := c.Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("after Fix: status = %d (%s), want StatusOK", r.Status, r.Message)
	}

	if present, _, err := getRigWorktreeConfigValue(rigPath, "core.worktree"); err != nil || present {
		t.Fatalf("after Fix: core.worktree present=%v err=%v, want absent", present, err)
	}
	if present, value, err := getRigWorktreeConfigValue(rigPath, "extensions.worktreeConfig", "--type=bool"); err != nil || !present || value != "true" {
		t.Fatalf("after Fix: extensions.worktreeConfig present=%v value=%q err=%v, want true", present, value, err)
	}
}

func TestRigWorktreeConfigCheck_Fix_IdempotentOnCleanRepo(t *testing.T) {
	rigPath := initWorktreeConfigTestRepo(t)
	runGitForWorktreeConfigTest(t, rigPath, "config", "extensions.worktreeConfig", "true")

	c := NewRigWorktreeConfigCheck(config.Rig{Name: "testrig", Path: rigPath})
	if err := c.Fix(&CheckContext{}); err != nil {
		t.Fatalf("Fix on already-clean repo returned error: %v", err)
	}
	r := c.Run(&CheckContext{})
	if r.Status != StatusOK {
		t.Fatalf("status = %d (%s), want StatusOK", r.Status, r.Message)
	}
}

// TestRigWorktreeConfigCheck_ReproducesAndHealsLinkedWorktreeConfigLeak
// reproduces the actual gc-qi40jt mechanism end to end against real git
// subprocesses (no mocks): git's --local config scope — the default for a
// plain `git config <key> <value>`, and also the silent fallback --worktree
// takes whenever extensions.worktreeConfig is unset — always resolves to the
// single config file shared by the main worktree and every linked worktree
// of the same repository, never a worktree-private file, regardless of
// which worktree the command's cwd/-C targets (see `git help config`'s
// --worktree section and `git help worktree`'s CONFIGURATION FILE section).
//
// gascity's own dedicated-worktree provisioning (city.toml pre_start: `git
// -C RigRoot worktree add --detach WorkDir main`) never itself runs `git
// config`, so this does not prove which external tool wrote core.worktree
// during the real incident — but it proves any tool that does, while
// operating inside one of these linked worktrees, poisons the shared rig
// root exactly as observed, and that this check's Fix() closes the hole:
// after Fix enables extensions.worktreeConfig, an equivalent *correctly
// worktree-scoped* write (`git config --worktree ...`, the form a
// well-behaved worktree-aware tool should use) lands in the worktree's own
// private config.worktree file instead.
func TestRigWorktreeConfigCheck_ReproducesAndHealsLinkedWorktreeConfigLeak(t *testing.T) {
	rigPath := initWorktreeConfigTestRepo(t)
	worktreePath := filepath.Join(t.TempDir(), "dedicated-worktree")
	runGitForWorktreeConfigTest(t, rigPath, "worktree", "add", "--detach", worktreePath, "main")

	// Step 1: reproduce the leak. A careless tool operating inside the
	// linked worktree runs a plain, unscoped git-config write intending
	// (wrongly, as it turns out) to scope it to just this worktree.
	runGitForWorktreeConfigTest(t, worktreePath, "config", "core.worktree", worktreePath)

	if present, value, err := getRigWorktreeConfigValue(rigPath, "core.worktree"); err != nil || !present {
		t.Fatalf("setup: expected the write from inside the linked worktree to land in the rig root's shared config; present=%v value=%q err=%v", present, value, err)
	}

	// Step 2: the check must catch this as an active incident.
	c := NewRigWorktreeConfigCheck(config.Rig{Name: "testrig", Path: rigPath})
	r := c.Run(&CheckContext{})
	if r.Status != StatusError {
		t.Fatalf("status = %d (%s), want StatusError for the reproduced leak", r.Status, r.Message)
	}

	// Step 3: Fix heals the existing poisoning and hardens against recurrence.
	if err := c.Fix(&CheckContext{}); err != nil {
		t.Fatalf("Fix returned error: %v", err)
	}
	if present, _, err := getRigWorktreeConfigValue(rigPath, "core.worktree"); err != nil || present {
		t.Fatalf("after Fix: core.worktree present=%v err=%v, want absent", present, err)
	}

	// Step 4: forward-protection. A well-behaved tool that correctly scopes
	// its own write with --worktree now lands in the linked worktree's own
	// private config.worktree, not the shared root config.
	runGitForWorktreeConfigTest(t, worktreePath, "config", "--worktree", "core.hookstestkey", "/some/value")
	if present, _, err := getRigWorktreeConfigValue(rigPath, "core.hookstestkey"); err != nil {
		t.Fatalf("reading core.hookstestkey from rig root: %v", err)
	} else if present {
		t.Fatal("core.hookstestkey leaked into the shared rig-root config after Fix enabled extensions.worktreeConfig; --worktree-scoped writes should land in the worktree's own config.worktree instead")
	}
}

// getRigWorktreeConfigValue reads key from rigPath's own .git/config via
// rigConfigFileGet, matching how RigWorktreeConfigCheck itself reads it.
func getRigWorktreeConfigValue(rigPath, key string, typeArgs ...string) (present bool, value string, err error) {
	return rigConfigFileGet("git", filepath.Join(rigPath, ".git", "config"), key, typeArgs...)
}

func initWorktreeConfigTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	dir := t.TempDir()
	runGitForWorktreeConfigTest(t, dir, "init")
	runGitForWorktreeConfigTest(t, dir, "checkout", "-b", "main")
	runGitForWorktreeConfigTest(t, dir, "config", "user.name", "Rig Worktree Config Test")
	runGitForWorktreeConfigTest(t, dir, "config", "user.email", "rig-worktree-config@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	runGitForWorktreeConfigTest(t, dir, "add", "README.md")
	runGitForWorktreeConfigTest(t, dir, "commit", "-m", "initial")
	return dir
}

func runGitForWorktreeConfigTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Env = git.SanitizedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(fullArgs, " "), err, out)
	}
}
