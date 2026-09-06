package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// writeExecutableCheckScript creates an executable regular file at
// <base>/<relPath>, creating parent directories as needed — the shape a real
// .gc/scripts/checks/*.sh gate script takes on disk.
func writeExecutableCheckScript(t *testing.T, base, relPath string) {
	t.Helper()
	full := filepath.Join(base, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

const ralphCheckFormulaTOML = `
formula = "implement-owned-work"

[[steps]]
id = "implement"
title = "Implement owned work"

[steps.ralph]
max_attempts = 3

[steps.ralph.check]
mode = "exec"
path = "scripts/checks/verify.sh"
`

// TestControlGateScriptsCheckMissingScriptAtCityScope pins the exact
// gc-3gimnj scenario: a control-gated (ralph.check) formula reachable from
// the city formula layer whose declared check script does not exist under
// the city root. This must fail loudly via `gc doctor`, not silently defer
// the discovery to the control-dispatcher's first quarantine.
func TestControlGateScriptsCheckMissingScriptAtCityScope(t *testing.T) {
	cityDir := t.TempDir()
	formulaDir := t.TempDir()
	writeDoctorFormula(t, formulaDir, "implement-owned-work", ralphCheckFormulaTOML)
	// Deliberately do NOT create <cityDir>/scripts/checks/verify.sh.

	check := NewControlGateScriptsCheck(&config.City{
		FormulaLayers: config.FormulaLayers{City: []string{formulaDir}},
	}, cityDir, nil)

	result := check.Run(&CheckContext{})
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want Error; details:\n%s", result.Status, strings.Join(result.Details, "\n"))
	}
	wantPath := filepath.Join(cityDir, "scripts", "checks", "verify.sh")
	joined := strings.Join(result.Details, "\n")
	if !strings.Contains(joined, wantPath) {
		t.Fatalf("Details = %q, want a line naming the missing script path %q", joined, wantPath)
	}
	if !strings.Contains(joined, "implement-owned-work") || !strings.Contains(joined, "implement") {
		t.Fatalf("Details = %q, want the offending formula/step named for actionability", joined)
	}
}

// TestControlGateScriptsCheckOKWhenScriptPresent is the negative control: the
// identical formula, but with its check script actually in place and
// executable, must pass cleanly.
func TestControlGateScriptsCheckOKWhenScriptPresent(t *testing.T) {
	cityDir := t.TempDir()
	formulaDir := t.TempDir()
	writeDoctorFormula(t, formulaDir, "implement-owned-work", ralphCheckFormulaTOML)
	writeExecutableCheckScript(t, cityDir, "scripts/checks/verify.sh")

	check := NewControlGateScriptsCheck(&config.City{
		FormulaLayers: config.FormulaLayers{City: []string{formulaDir}},
	}, cityDir, nil)

	result := check.Run(&CheckContext{})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want OK; details:\n%s", result.Status, strings.Join(result.Details, "\n"))
	}
}

// TestControlGateScriptsCheckNotExecutable pins the "exists but forgot
// chmod +x" case, distinct from ENOENT.
func TestControlGateScriptsCheckNotExecutable(t *testing.T) {
	cityDir := t.TempDir()
	formulaDir := t.TempDir()
	writeDoctorFormula(t, formulaDir, "implement-owned-work", ralphCheckFormulaTOML)
	full := filepath.Join(cityDir, "scripts", "checks", "verify.sh")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	check := NewControlGateScriptsCheck(&config.City{
		FormulaLayers: config.FormulaLayers{City: []string{formulaDir}},
	}, cityDir, nil)

	result := check.Run(&CheckContext{})
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want Error; details:\n%s", result.Status, strings.Join(result.Details, "\n"))
	}
	if !strings.Contains(strings.Join(result.Details, "\n"), "not executable") {
		t.Fatalf("Details = %v, want a \"not executable\" diagnostic", result.Details)
	}
}

// TestControlGateScriptsCheckRigScopeResolvesAgainstRigRoot confirms a
// rig-scoped formula's check script resolves against that rig's OWN root,
// not the city root — matching runRalphCheck's default resolution base for
// rig-scoped dispatch (the rig's store path), and matching the real
// formula-flow incident (a rig-local .gc/scripts/checks/ gap, not a
// city-level one).
func TestControlGateScriptsCheckRigScopeResolvesAgainstRigRoot(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := t.TempDir()
	formulaDir := t.TempDir()
	writeDoctorFormula(t, formulaDir, "implement-owned-work", ralphCheckFormulaTOML)

	cfg := &config.City{
		FormulaLayers: config.FormulaLayers{
			Rigs: map[string][]string{"formula-flow": {formulaDir}},
		},
	}
	rigs := []config.Rig{{Name: "formula-flow", Path: rigDir}}

	// Script present under the CITY root only: still a failure, because
	// resolution must happen against the rig root.
	writeExecutableCheckScript(t, cityDir, "scripts/checks/verify.sh")
	check := NewControlGateScriptsCheck(cfg, cityDir, rigs)
	result := check.Run(&CheckContext{})
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want Error when the script only exists under the city root; details:\n%s",
			result.Status, strings.Join(result.Details, "\n"))
	}

	// Now place it under the rig root instead: must pass.
	writeExecutableCheckScript(t, rigDir, "scripts/checks/verify.sh")
	result = check.Run(&CheckContext{})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want OK once the script exists under the rig root; details:\n%s",
			result.Status, strings.Join(result.Details, "\n"))
	}
}

// TestControlGateScriptsCheckDedupesRepeatedReferences confirms that many
// steps/formulas referencing the identical missing script collapse into one
// reported issue rather than one line per occurrence (the real incident's 15
// "Implement owned work" beads all shared the same underlying gate script).
func TestControlGateScriptsCheckDedupesRepeatedReferences(t *testing.T) {
	cityDir := t.TempDir()
	formulaDir := t.TempDir()
	writeDoctorFormula(t, formulaDir, "implement-owned-work-a", ralphCheckFormulaTOML)
	writeDoctorFormula(t, formulaDir, "implement-owned-work-b", strings.Replace(
		ralphCheckFormulaTOML, `formula = "implement-owned-work"`, `formula = "implement-owned-work-b"`, 1))

	check := NewControlGateScriptsCheck(&config.City{
		FormulaLayers: config.FormulaLayers{City: []string{formulaDir}},
	}, cityDir, nil)

	result := check.Run(&CheckContext{})
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want Error; details:\n%s", result.Status, strings.Join(result.Details, "\n"))
	}
	if len(result.Details) != 1 {
		t.Fatalf("Details = %v, want exactly 1 deduped issue for the shared script path, got %d", result.Details, len(result.Details))
	}
}

// TestControlGateScriptsCheckSkipsSuspendedRigsImplicitly documents that this
// check trusts its activeRigs input rather than re-deriving suspension state:
// a rig simply absent from the slice (as buildDoctorChecks arranges for a
// suspended rig) is never scanned.
func TestControlGateScriptsCheckSkipsSuspendedRigsImplicitly(t *testing.T) {
	cityDir := t.TempDir()
	formulaDir := t.TempDir()
	writeDoctorFormula(t, formulaDir, "implement-owned-work", ralphCheckFormulaTOML)

	cfg := &config.City{
		FormulaLayers: config.FormulaLayers{
			Rigs: map[string][]string{"suspended-rig": {formulaDir}},
		},
	}
	// activeRigs deliberately omits "suspended-rig".
	check := NewControlGateScriptsCheck(cfg, cityDir, nil)
	result := check.Run(&CheckContext{})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want OK for a rig excluded from activeRigs; details:\n%s",
			result.Status, strings.Join(result.Details, "\n"))
	}
}

// TestControlGateScriptsCheckIgnoresFormulasWithoutRalphChecks confirms an
// ordinary formula with no ralph.check step never triggers this check —
// only steps declaring an exec-mode ralph check gate matter.
func TestControlGateScriptsCheckIgnoresFormulasWithoutRalphChecks(t *testing.T) {
	cityDir := t.TempDir()
	formulaDir := t.TempDir()
	writeDoctorFormula(t, formulaDir, "plain", `
formula = "plain"

[[steps]]
id = "work"
title = "Work"
`)

	check := NewControlGateScriptsCheck(&config.City{
		FormulaLayers: config.FormulaLayers{City: []string{formulaDir}},
	}, cityDir, nil)

	result := check.Run(&CheckContext{})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want OK for a formula with no ralph.check steps; details:\n%s",
			result.Status, strings.Join(result.Details, "\n"))
	}
}

func TestControlGateScriptsCheckNilConfig(t *testing.T) {
	check := NewControlGateScriptsCheck(nil, t.TempDir(), nil)
	result := check.Run(&CheckContext{})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want OK for nil config", result.Status)
	}
}
