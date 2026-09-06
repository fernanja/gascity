package doctor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/git"
)

// RigWorktreeConfigCheck detects and repairs shared-rig-root git-config
// corruption caused by dedicated/linked-worktree provisioning.
//
// Background (gc-qi40jt): on 2026-09-05 a core.worktree key appeared in the
// SHARED /Users/fernanja/ascent/.git/config — the rig root's own top-level
// config, not a per-worktree private one — breaking every git operation
// against that checkout for every session until a human manually unset it.
// The exact external writer was never proven (this repo's own
// worktree-provisioning code was audited end to end and contains no `git
// config` call of any kind — the only git-mutating command in the whole
// dedicated-worktree feature is the `git worktree add` in city.toml's
// per-role pre_start), but the reproducible mechanism is well documented in
// git's own manuals and was confirmed empirically against this machine's
// installed git: a plain `git config <key> <value>` — the default --local
// scope, and also the silent fallback --worktree takes whenever
// extensions.worktreeConfig is unset (see `git help config`'s --worktree
// section and `git help worktree`'s CONFIGURATION FILE section) — always
// resolves to the single config file shared by the main worktree and every
// linked worktree of the same repository, never a worktree-private file,
// regardless of which worktree the command's cwd/-C happens to be. Any tool
// that performs a git-config write intended to be scoped to "this worktree"
// while operating inside one of gascity's "dedicated" linked worktrees (see
// city.toml's per-role work_dir/pre_start: `git -C RigRoot worktree add
// --detach WorkDir main`) can poison the shared rig root this way — the
// actual writer need not be gascity's own code; it only needs to run
// somewhere inside the worktree family of the rig repo. (A matching,
// separately filed defect class exists in anthropics/claude-code #72714 and
// #88747, writing core.hooksPath the same way from its own worktree
// feature.)
//
// This check hardens against recurrence at both ends:
//   - proactively enables extensions.worktreeConfig on the rig root, so a
//     tool that correctly scopes its own write with `git config --worktree`
//     lands in that worktree's own private config.worktree file instead of
//     the shared one;
//   - detects (and can auto-heal) an ALREADY-poisoned core.worktree key in
//     the rig root's own local config. An ordinary rig checkout (not bare,
//     not set up with --separate-git-dir) should never legitimately carry
//     this key at its own root, so any presence is treated as an active
//     incident, closing the gap between "corruption happens" and "a human
//     notices broken git and manually unsets it" down to the next doctor run.
//
// Reads and writes below all target the rig root's config FILE directly via
// `git config --file <rig>/.git/config`, never `git -C <rig> config --local`.
// This is load-bearing, not stylistic: once core.worktree names a path that
// doesn't exist (the exact shape of the real incident), git's ordinary
// repository/work-tree setup — which every `-C <dir> config --local`
// invocation still goes through first, even for an unrelated read like
// --get — fails closed with "fatal: Invalid path '...': No such file or
// directory" before it ever gets to honoring --local or --get. `--file`
// parses the named file directly and never resolves a work tree, so it is
// the one form of `git config` guaranteed to still work on an
// already-poisoned repo — confirmed empirically against this machine's
// installed git. A detector or fixer that used -C/--local here would itself
// be broken by the exact condition it exists to repair.
type RigWorktreeConfigCheck struct {
	rig     config.Rig
	gitPath func(name string) (string, error) // injectable for tests
}

// NewRigWorktreeConfigCheck creates a rig worktree-config-isolation check
// for the given rig.
func NewRigWorktreeConfigCheck(rig config.Rig) *RigWorktreeConfigCheck {
	return &RigWorktreeConfigCheck{rig: rig, gitPath: exec.LookPath}
}

// Name returns the check identifier.
func (c *RigWorktreeConfigCheck) Name() string { return "rig:" + c.rig.Name + ":worktree-config" }

// WarmupEligible returns true so this check runs during `gc start` warm-up:
// an actively poisoned rig root breaks every git operation immediately, so
// surfacing it right at start is more useful than waiting for an operator to
// run `gc doctor`.
func (c *RigWorktreeConfigCheck) WarmupEligible() bool { return true }

// CanFix returns true — both the poisoned key and the missing extension are
// mechanically repairable.
func (c *RigWorktreeConfigCheck) CanFix() bool { return true }

// Run checks the rig root's own config FILE (not the live repository — see
// the type doc comment for why) for an active core.worktree poisoning and
// for the extensions.worktreeConfig hardening.
func (c *RigWorktreeConfigCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name()}

	gitBin, err := c.gitPath("git")
	if err != nil {
		r.Status = StatusWarning
		r.Severity = SeverityAdvisory
		r.Message = fmt.Sprintf("rig %q: unable to check worktree config — git unavailable", c.rig.Name)
		return r
	}

	configPath := filepath.Join(c.rig.Path, ".git", "config")
	if _, statErr := os.Stat(configPath); statErr != nil {
		r.Status = StatusWarning
		r.Severity = SeverityAdvisory
		r.Message = fmt.Sprintf("rig %q: no .git/config found — not a plain git checkout", c.rig.Name)
		return r
	}

	poisoned, _, perr := rigConfigFileGet(gitBin, configPath, "core.worktree")
	if perr != nil {
		r.Status = StatusWarning
		r.Severity = SeverityAdvisory
		r.Message = fmt.Sprintf("rig %q: unable to read git config — %v", c.rig.Name, perr)
		return r
	}
	if poisoned {
		r.Status = StatusError
		r.Message = fmt.Sprintf("rig %q: core.worktree is set in the SHARED root .git/config — git operations against this checkout are likely fatally broken (git validates the work-tree path on every invocation)", c.rig.Name)
		r.FixHint = fmt.Sprintf("run: git config --file %q --unset core.worktree", configPath)
		return r
	}

	present, value, eerr := rigConfigFileGet(gitBin, configPath, "extensions.worktreeConfig", "--type=bool")
	if eerr != nil {
		r.Status = StatusWarning
		r.Severity = SeverityAdvisory
		r.Message = fmt.Sprintf("rig %q: unable to read extensions.worktreeConfig — %v", c.rig.Name, eerr)
		return r
	}
	if !present || value != "true" {
		r.Status = StatusWarning
		r.Severity = SeverityAdvisory
		r.Message = fmt.Sprintf("rig %q: extensions.worktreeConfig is not enabled — dedicated/linked worktrees can still poison the shared root config", c.rig.Name)
		r.FixHint = fmt.Sprintf("run: git config --file %q extensions.worktreeConfig true", configPath)
		return r
	}

	r.Status = StatusOK
	r.Message = fmt.Sprintf("rig %q: worktree config isolation OK (extensions.worktreeConfig enabled, no core.worktree leak)", c.rig.Name)
	return r
}

// Fix unsets a poisoned core.worktree key (if present) and enables
// extensions.worktreeConfig, both directly against the rig root's config
// file. Both steps are idempotent and safe to run even when only one is
// needed.
func (c *RigWorktreeConfigCheck) Fix(_ *CheckContext) error {
	gitBin, err := c.gitPath("git")
	if err != nil {
		return fmt.Errorf("git unavailable: %w", err)
	}

	configPath := filepath.Join(c.rig.Path, ".git", "config")
	if _, statErr := os.Stat(configPath); statErr != nil {
		return fmt.Errorf("no .git/config at %q: %w", configPath, statErr)
	}

	poisoned, _, perr := rigConfigFileGet(gitBin, configPath, "core.worktree")
	if perr != nil {
		return fmt.Errorf("reading core.worktree: %w", perr)
	}
	if poisoned {
		cmd := exec.Command(gitBin, "config", "--file", configPath, "--unset", "core.worktree")
		cmd.Env = git.SanitizedEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("unsetting core.worktree: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}

	setCmd := exec.Command(gitBin, "config", "--file", configPath, "extensions.worktreeConfig", "true")
	setCmd.Env = git.SanitizedEnv()
	if out, err := setCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("enabling extensions.worktreeConfig: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// rigConfigFileGet runs `git config --file configPath [typeArgs...] --get
// key` with the process's ambient git-locating environment variables
// stripped (git.SanitizedEnv — see gc-qi40jt). Using --file (rather than -C
// dir config --local) is deliberate: it parses configPath directly without
// ever resolving a repository or work tree, so it keeps working even when
// the file itself contains a core.worktree pointing at a path that no
// longer exists — the exact condition this check exists to detect. See the
// type doc comment for the full rationale.
//
// present=false, err=nil means the key is simply unset (or configPath
// doesn't exist / isn't valid config — git's normal exit status 1 for a
// missing --get key) — not a failure. Any other non-zero exit, or a failure
// to even start git, is returned as err.
func rigConfigFileGet(gitBin, configPath, key string, typeArgs ...string) (present bool, value string, err error) {
	args := append([]string{"config", "--file", configPath}, typeArgs...)
	args = append(args, "--get", key)
	cmd := exec.Command(gitBin, args...)
	cmd.Env = git.SanitizedEnv()
	out, runErr := cmd.Output()
	if runErr == nil {
		return true, strings.TrimSpace(string(out)), nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
		return false, "", nil
	}
	return false, "", runErr
}
