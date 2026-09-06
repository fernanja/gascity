package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/formula"
)

// ControlGateScriptsCheck verifies that every ralph-check gate script a
// city's or a rig's active formulas declare (a step's `ralph.check.path`)
// actually exists on disk as an executable regular file, for the city-level
// formula layer and every non-suspended rig.
//
// Why this exists (gastownhall/gascity control-dispatch quarantine gap):
// the control-dispatcher resolves a ralph check's script path
// (convergence.ResolveConditionPath, invoked from runRalphCheck in
// internal/dispatch/ralph.go) the moment a gated step's check bead becomes
// ready to run. A rig that has control-gated (ralph/check) formulas active
// but has not yet had its .gc/scripts/checks/ scripts provisioned currently
// only discovers that at dispatch time, after real work has already run and
// hard-quarantined. This check catches the gap proactively: `gc doctor`
// fails loudly with the exact missing/broken script path before any bead
// ever reaches that gate.
//
// Scope note: FormulaLayers.Rigs[rigName] already includes the city-shared
// formula layers (see config.FormulaLayers), so a shared/topology formula is
// deliberately re-scanned once per active rig, each time resolved against
// that rig's OWN root — a shared formula's check script must exist under
// every rig that actually dispatches it, and each rig has its own
// .gc/scripts/checks/ copy. This mirrors runRalphCheck's own default
// resolution base (the dispatching store's own path), so it is not
// redundant: it is the same per-rig fact runRalphCheck itself would check at
// dispatch time, checked once per rig instead of once per failure.
//
// Known limitation: a step that overrides gc.work_dir (metadata, not a
// formula-visible field in the general case) can resolve its check path
// against a different base at dispatch time than the city/rig root this
// check assumes — most such overrides point at a per-task worktree that
// does not exist until dispatch anyway, so this check cannot meaningfully
// pre-validate them. Likewise, ralph-check steps introduced only via
// runtime `expand`/`on_complete` inlining (rather than a formula's own
// Steps/Template) are not captured by this static scan. Both are accepted,
// narrow gaps for a proactive pre-dispatch scan, not a promise of full
// runtime-equivalent resolution.
type ControlGateScriptsCheck struct {
	cfg        *config.City
	cityPath   string
	activeRigs []config.Rig
}

// NewControlGateScriptsCheck creates the check. activeRigs should already be
// filtered to non-suspended rigs with a resolved Path — the same
// effective-suspension filter callers already apply before registering the
// other per-rig doctor checks (opening a suspended rig's bead store can
// trigger bd auto-starting an orphan Dolt server).
func NewControlGateScriptsCheck(cfg *config.City, cityPath string, activeRigs []config.Rig) *ControlGateScriptsCheck {
	return &ControlGateScriptsCheck{cfg: cfg, cityPath: cityPath, activeRigs: activeRigs}
}

// Name returns the check identifier shown by gc doctor.
func (c *ControlGateScriptsCheck) Name() string { return "control-gate-scripts" }

// CanFix reports whether this check supports automatic remediation.
func (c *ControlGateScriptsCheck) CanFix() bool { return false }

// Fix is a no-op — a missing check script needs author-written content, not
// a mechanical repair.
func (c *ControlGateScriptsCheck) Fix(_ *CheckContext) error { return nil }

// WarmupEligible opts this check out of gc start's warm-up scan; it is a
// deliberate, on-demand gc doctor check rather than a startup gate.
func (c *ControlGateScriptsCheck) WarmupEligible() bool { return false }

// Run scans every reachable formula's ralph-check steps and reports any
// whose declared script does not resolve to an executable regular file.
func (c *ControlGateScriptsCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name()}
	if c.cfg == nil {
		r.Status = StatusOK
		r.Message = "city config unavailable"
		return r
	}

	var issues []string
	if len(c.cfg.FormulaLayers.City) > 0 {
		issues = append(issues, c.checkScope("city", c.cityPath, c.cfg.FormulaLayers.City)...)
	}
	for _, rig := range c.activeRigs {
		base := strings.TrimSpace(rig.Path)
		if base == "" {
			continue
		}
		paths := c.cfg.FormulaLayers.Rigs[rig.Name]
		if len(paths) == 0 {
			continue
		}
		issues = append(issues, c.checkScope("rig:"+rig.Name, base, paths)...)
	}

	if len(issues) == 0 {
		r.Status = StatusOK
		r.Message = "all control-gated (ralph check) formulas have their check scripts in place"
		return r
	}
	sort.Strings(issues)
	r.Status = StatusError
	r.Message = fmt.Sprintf("%d control-gated formula check script(s) missing or not executable", len(issues))
	r.Details = issues
	r.FixHint = "create the missing check script(s) as executable regular files (conventionally under .gc/scripts/checks/) before dispatching against this scope -- an unresolvable gc.check_path currently hard-quarantines the control bead, discarding any work its subject already completed, the first time the gate runs"
	return r
}

// checkScope resolves every ralph-check gate script declared by formulas
// reachable from formulaPaths against base and reports any that do not
// exist as an executable regular file. Malformed formulas (parse/resolve
// errors) are silently skipped here — FormulaRequirementsCheck already
// surfaces those.
func (c *ControlGateScriptsCheck) checkScope(scopeName, base string, formulaPaths []string) []string {
	src := formula.SourceFromEnv()
	parser := formula.NewParser(formulaPaths...).SetSource(src)
	winners := formula.ResolveAllWithSource(src, formulaPaths)

	names := make([]string, 0, len(winners))
	for name := range winners {
		names = append(names, name)
	}
	sort.Strings(names)

	var issues []string
	reported := make(map[string]bool)
	reportStep := func(formulaName string, step *formula.Step) {
		checkPath := strings.TrimSpace(step.Ralph.Check.Path)
		if checkPath == "" {
			return
		}
		resolvedPath := checkPath
		if !filepath.IsAbs(checkPath) {
			resolvedPath = filepath.Join(base, checkPath)
		}
		if reported[resolvedPath] {
			return
		}

		info, statErr := os.Stat(resolvedPath)
		var problem string
		switch {
		case statErr != nil:
			problem = statErr.Error()
		case !info.Mode().IsRegular():
			problem = "not a regular file"
		case info.Mode().Perm()&0o111 == 0:
			problem = "not executable"
		default:
			return
		}
		reported[resolvedPath] = true
		issues = append(issues, fmt.Sprintf("%s: formula %q step %q: check script %s: %s",
			scopeName, formulaName, step.ID, resolvedPath, problem))
	}

	for _, name := range names {
		f, err := parser.ParseFile(winners[name])
		if err != nil {
			continue
		}
		resolved, err := parser.Resolve(f)
		if err != nil {
			continue
		}
		visit := func(step *formula.Step) { reportStep(name, step) }
		walkRalphCheckSteps(resolved.Steps, visit)
		walkRalphCheckSteps(resolved.Template, visit)
	}

	return issues
}

// walkRalphCheckSteps recursively visits every step (including nested
// Children and Loop.Body steps) that carries a ralph exec-check gate,
// calling visit once per such step.
func walkRalphCheckSteps(steps []*formula.Step, visit func(*formula.Step)) {
	for _, step := range steps {
		if step == nil {
			continue
		}
		if step.Ralph != nil && step.Ralph.Check != nil && strings.TrimSpace(step.Ralph.Check.Path) != "" {
			visit(step)
		}
		if len(step.Children) > 0 {
			walkRalphCheckSteps(step.Children, visit)
		}
		if step.Loop != nil && len(step.Loop.Body) > 0 {
			walkRalphCheckSteps(step.Loop.Body, visit)
		}
	}
}
