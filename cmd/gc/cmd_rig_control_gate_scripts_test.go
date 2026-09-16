package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// rigAddControlGateFormulaTOML mirrors the ralph-check formula shape from the
// real formula-flow incident (gc-rucsm): a control-gated step whose exec
// check script does not exist on disk yet.
const rigAddControlGateFormulaTOML = `
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

func writeRigAddTestFormula(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWarnMissingControlGateScriptsWarnsOnMissingScript pins the exact
// gc-rucsm scenario: a freshly provisioned rig whose active formulas declare
// a ralph-check gate script that has not been materialized yet must get a
// warning at `gc rig add` time, not silence until the first dispatch.
func TestWarnMissingControlGateScriptsWarnsOnMissingScript(t *testing.T) {
	cityPath := t.TempDir()
	rigPath := t.TempDir()
	formulaDir := t.TempDir()
	writeRigAddTestFormula(t, formulaDir, "implement-owned-work", rigAddControlGateFormulaTOML)
	// Deliberately do NOT create <rigPath>/scripts/checks/verify.sh.

	cfg := &config.City{
		FormulaLayers: config.FormulaLayers{
			Rigs: map[string][]string{"formula-flow": {formulaDir}},
		},
	}

	var stderr bytes.Buffer
	warnMissingControlGateScripts(&stderr, cfg, cityPath, config.Rig{Name: "formula-flow", Path: rigPath})

	out := stderr.String()
	if !strings.Contains(out, "gc rig add: warning:") {
		t.Fatalf("stderr = %q, want a %q warning", out, "gc rig add: warning:")
	}
	wantPath := filepath.Join(rigPath, "scripts", "checks", "verify.sh")
	if !strings.Contains(out, wantPath) {
		t.Fatalf("stderr = %q, want it to name the missing script path %q", out, wantPath)
	}
}

// TestWarnMissingControlGateScriptsSilentWhenScriptPresent is the negative
// control: the identical formula, but with its check script already in
// place and executable, must produce no warning.
func TestWarnMissingControlGateScriptsSilentWhenScriptPresent(t *testing.T) {
	cityPath := t.TempDir()
	rigPath := t.TempDir()
	formulaDir := t.TempDir()
	writeRigAddTestFormula(t, formulaDir, "implement-owned-work", rigAddControlGateFormulaTOML)

	scriptPath := filepath.Join(rigPath, "scripts", "checks", "verify.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.City{
		FormulaLayers: config.FormulaLayers{
			Rigs: map[string][]string{"formula-flow": {formulaDir}},
		},
	}

	var stderr bytes.Buffer
	warnMissingControlGateScripts(&stderr, cfg, cityPath, config.Rig{Name: "formula-flow", Path: rigPath})

	if out := stderr.String(); out != "" {
		t.Fatalf("stderr = %q, want no warning when the script is present and executable", out)
	}
}

// TestWarnMissingControlGateScriptsNilConfigNoop confirms a nil city config
// (e.g. a reload race) is a silent no-op, not a panic.
func TestWarnMissingControlGateScriptsNilConfigNoop(t *testing.T) {
	var stderr bytes.Buffer
	warnMissingControlGateScripts(&stderr, nil, t.TempDir(), config.Rig{Name: "x", Path: t.TempDir()})
	if out := stderr.String(); out != "" {
		t.Fatalf("stderr = %q, want no output for nil config", out)
	}
}

// TestWarnMissingControlGateScriptsEmptyPathNoop confirms a rig with no
// resolved path is skipped rather than scanned against an empty base.
func TestWarnMissingControlGateScriptsEmptyPathNoop(t *testing.T) {
	var stderr bytes.Buffer
	cfg := &config.City{}
	warnMissingControlGateScripts(&stderr, cfg, t.TempDir(), config.Rig{Name: "x", Path: ""})
	if out := stderr.String(); out != "" {
		t.Fatalf("stderr = %q, want no output when the rig path is empty", out)
	}
}
