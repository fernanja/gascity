package graphv2

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	convoycore "github.com/gastownhall/gascity/internal/convoy"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/formulatest"
)

func TestPrepareInvocationCreatesInputConvoyForBeadTarget(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	inv, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	if inv.InputConvoy == "" {
		t.Fatalf("invocation = %+v, want input convoy", inv)
	}
	if got := inv.Vars[ConvoyIDVar]; got != inv.InputConvoy {
		t.Fatalf("vars[%s] = %q, want %q", ConvoyIDVar, got, inv.InputConvoy)
	}
	members, err := convoycore.Members(store, inv.InputConvoy, true)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 1 || members[0].ID != target.ID {
		t.Fatalf("members = %+v, want target %s", members, target.ID)
	}
	created, err := store.Get(inv.InputConvoy)
	if err != nil {
		t.Fatalf("Get(input convoy): %v", err)
	}
	if created.Type != "convoy" {
		t.Fatalf("input convoy type = %q, want convoy", created.Type)
	}
	wantMetadata := map[string]string{
		"gc.synthetic":       "true",
		"gc.idempotency_key": DispatchIdempotencyKey(target.ID, "work", nil),
	}
	if !maps.Equal(created.Metadata, wantMetadata) {
		t.Fatalf("input convoy metadata = %+v, want %+v", created.Metadata, wantMetadata)
	}

	// A repeated invocation for the same (target, formula, vars) is a dispatch
	// RETRY, not a new dispatch: it must reuse the open input convoy instead of
	// minting a duplicate (gc-wnjjc — retried dispatches over Dolt contention
	// created duplicate input convoys and, once, a full duplicate workflow root,
	// because a fresh convoy ID changes the RootKey the root singleton dedupes on).
	again, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation again: %v", err)
	}
	if again.InputConvoy != inv.InputConvoy {
		t.Fatalf("retried invocation minted a duplicate input convoy: first=%s second=%s", inv.InputConvoy, again.InputConvoy)
	}
	convoys, err := store.List(beads.ListQuery{Type: "convoy"})
	if err != nil {
		t.Fatalf("List convoys: %v", err)
	}
	if len(convoys) != 1 {
		t.Fatalf("open input convoys after retry = %d, want 1 (%+v)", len(convoys), convoys)
	}
}

func TestNormalizeInputConvoyRejectsNilStore(t *testing.T) {
	_, err := NormalizeInputConvoy(nil, "target")
	if err == nil {
		t.Fatal("NormalizeInputConvoy succeeded, want nil-store error")
	}
	if !strings.Contains(err.Error(), "requires a bead store") {
		t.Fatalf("error = %q, want nil-store message", err)
	}
}

func TestCreateSingleItemInputConvoyRejectsNilStore(t *testing.T) {
	_, err := CreateSingleItemInputConvoy(nil, beads.Bead{ID: "target", Status: "open"})
	if err == nil {
		t.Fatal("CreateSingleItemInputConvoy succeeded, want nil-store error")
	}
	if !strings.Contains(err.Error(), "requires a bead store") {
		t.Fatalf("error = %q, want nil-store message", err)
	}
}

func TestRootKeyIgnoresConvoyIDRuntimeVar(t *testing.T) {
	base := RootKey("convoy-1", "graph-work", map[string]string{
		ConvoyIDVar: "convoy-1",
		"mode":      "review",
	}, "", "")
	same := RootKey("convoy-1", "graph-work", map[string]string{
		ConvoyIDVar: "different-preview-value",
		"mode":      "review",
	}, "", "")
	if same != base {
		t.Fatalf("RootKey changed when only %s changed: %q vs %q", ConvoyIDVar, same, base)
	}
	changed := RootKey("convoy-1", "graph-work", map[string]string{
		ConvoyIDVar: "convoy-1",
		"mode":      "implement",
	}, "", "")
	if changed == base {
		t.Fatalf("RootKey did not change when non-reserved vars changed: %q", changed)
	}
}

func TestLockKeySerializesSameKey(t *testing.T) {
	var active int32
	var overlapped int32
	var wg sync.WaitGroup

	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := LockKey("same-root-key")
			defer unlock()
			if n := atomic.AddInt32(&active, 1); n > 1 {
				atomic.StoreInt32(&overlapped, 1)
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&active, -1)
		}()
	}

	wg.Wait()
	if atomic.LoadInt32(&overlapped) != 0 {
		t.Fatal("LockKey allowed concurrent critical sections for the same key")
	}
}

func TestPrepareInvocationUsesFormulaCompilerRequirement(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
type = "workflow"

[requires]
formula_compiler = ">=2.0.0"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	isGraph, _, err := IsGraphV2Formula("work", []string{dir})
	if err != nil {
		t.Fatalf("IsGraphV2Formula: %v", err)
	}
	if !isGraph {
		t.Fatal("IsGraphV2Formula = false, want formula_compiler requirement to opt into graph semantics")
	}
	inv, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	if inv.InputConvoy == "" {
		t.Fatalf("InputConvoy empty for formula_compiler graph invocation: %+v", inv)
	}
}

func TestPrepareInvocationHonorsFormulaRef(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	invocationGitOK(t)
	root := initInvocationRepo(t)
	formulaDir := filepath.Join(root, "formulas")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFormula(t, formulaDir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	runInvocationGit(t, root, "add", "formulas/work.formula.toml")
	runInvocationGit(t, root, "commit", "-m", "graph formula")

	writeFormula(t, formulaDir, "work.formula.toml", `
formula = "work"
version = 1
type = "workflow"

[[steps]]
id = "legacy"
title = "Legacy working tree edit"
`)
	t.Setenv("GC_FORMULA_REF", "main")

	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}
	inv, err := PrepareInvocation(context.Background(), store, "work", []string{formulaDir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	if inv.InputConvoy == "" {
		t.Fatalf("InputConvoy empty; graph.v2 loader read working-tree formula instead of GC_FORMULA_REF")
	}
	if got := inv.Vars[ConvoyIDVar]; got != inv.InputConvoy {
		t.Fatalf("vars[%s] = %q, want %q", ConvoyIDVar, got, inv.InputConvoy)
	}
}

func TestPrepareInvocationRejectsClosedTarget(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}
	if err := store.Close(target.ID); err != nil {
		t.Fatalf("Close target: %v", err)
	}

	_, err = PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want closed target error")
	}
	if !strings.Contains(err.Error(), "is closed") {
		t.Fatalf("error = %q, want closed target", err)
	}
}

func TestPrepareInvocationDoesNotReusePreviousInputConvoy(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}
	existing, err := store.Create(beads.Bead{
		Title:    "existing input",
		Type:     "convoy",
		Metadata: map[string]string{"gc.synthetic": "true"},
	})
	if err != nil {
		t.Fatalf("Create existing input convoy: %v", err)
	}

	inv, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	if inv.InputConvoy == existing.ID {
		t.Fatalf("InputConvoy = %q, want a fresh input convoy", inv.InputConvoy)
	}
	members, err := convoycore.Members(store, inv.InputConvoy, true)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 1 || members[0].ID != target.ID {
		t.Fatalf("members = %+v, want target %s", members, target.ID)
	}
}

func TestPrepareInvocationUsesExistingConvoyTarget(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()
	convoy, err := store.Create(beads.Bead{Title: "input", Type: "convoy"})
	if err != nil {
		t.Fatalf("Create convoy: %v", err)
	}

	inv, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, convoy.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	if inv.InputConvoy != convoy.ID {
		t.Fatalf("invocation = %+v, want existing convoy", inv)
	}
}

func TestPrepareInvocationRejectsCallerReservedVars(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	_, err = PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, map[string]string{"issue": target.ID})
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want reserved var error")
	}
	if !strings.Contains(err.Error(), "reserved variable") {
		t.Fatalf("error = %q, want reserved variable", err)
	}
}

func TestPrepareInvocationRejectsMissingParentRuntimeVarsBeforeNormalizingTarget(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}} with {{missing}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	_, err = PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want missing runtime var error")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error = %q, want missing runtime var", err)
	}
	inputConvoys, err := store.List(beads.ListQuery{Type: "convoy"})
	if err != nil {
		t.Fatalf("List input convoys: %v", err)
	}
	if len(inputConvoys) != 0 {
		t.Fatalf("input convoys = %+v, want none before validation succeeds", inputConvoys)
	}
}

func TestPrepareInvocationTargetlessRejectsConvoyReferenceFromExpansion(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "parent.formula.toml", `
formula = "parent"
version = 2
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "work"
title = "Work"
expand = "needs-convoy"
`)
	writeFormula(t, dir, "needs-convoy.formula.toml", `
formula = "needs-convoy"
version = 2
contract = "graph.v2"
type = "expansion"

[[template]]
id = "{target}.expanded"
title = "Expanded {{convoy_id}}"
`)

	_, err := PrepareInvocation(context.Background(), beads.NewMemStore(), "parent", []string{dir}, "", nil)
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want targetless expanded convoy_id error")
	}
	if !strings.Contains(err.Error(), "convoy_id requires a targeted formulas v2 invocation") {
		t.Fatalf("error = %q, want expanded convoy_id target error", err)
	}
}

func TestPrepareInvocationTargetlessRejectsConditionedConvoyReferenceFromExpansionBeforeFiltering(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "parent.formula.toml", `
formula = "parent"
version = 2
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "work"
title = "Work"
expand = "conditioned-convoy"
`)
	writeFormula(t, dir, "conditioned-convoy.formula.toml", `
formula = "conditioned-convoy"
version = 2
type = "expansion"

[[template]]
id = "{target}.targetless-only"
title = "Targetless-only work"
condition = "!{{convoy_id}}"
`)

	_, err := PrepareInvocation(context.Background(), beads.NewMemStore(), "parent", []string{dir}, "", nil)
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want targetless expanded condition convoy_id error")
	}
	if !strings.Contains(err.Error(), "convoy_id requires a targeted formulas v2 invocation") {
		t.Fatalf("error = %q, want expanded condition convoy_id target error", err)
	}
}

func TestPrepareInvocationTargetlessRejectsConditionedConvoyReferenceBeforeFiltering(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 2
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "skip-when-targeted"
title = "Only targetless"
condition = "!{{convoy_id}}"
`)

	_, err := PrepareInvocation(context.Background(), beads.NewMemStore(), "work", []string{dir}, "", nil)
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want targetless conditioned convoy_id error")
	}
	if !strings.Contains(err.Error(), "convoy_id requires a targeted formulas v2 invocation") {
		t.Fatalf("error = %q, want conditioned convoy_id target error", err)
	}
}

func TestPreparePreviewInvocationUsesPreviewInputConvoyForBeadTarget(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	inv, err := PreparePreviewInvocation(context.Background(), store, "work", []string{dir}, target.ID, false, nil)
	if err != nil {
		t.Fatalf("PreparePreviewInvocation: %v", err)
	}
	want := previewInputConvoyPrefix + target.ID
	if inv.InputConvoy != want {
		t.Fatalf("preview invocation = %+v, want preview input convoy %q", inv, want)
	}
	if got := inv.Vars[ConvoyIDVar]; got != want {
		t.Fatalf("vars[%s] = %q, want %q", ConvoyIDVar, got, want)
	}
	matches, err := store.List(beads.ListQuery{Type: "convoy"})
	if err != nil {
		t.Fatalf("List convoys: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("preview created input convoys = %+v, want none", matches)
	}
}

func TestPreparePreviewInvocationRoutingIdentityTargetSkipsBeadLookup(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()

	// A routing identity (e.g. a workflow root's gc.routed_to value) has no
	// bead-store entry; the preview must not fail the bead lookup.
	inv, err := PreparePreviewInvocation(context.Background(), store, "work", []string{dir}, "myrig/worker", true, nil)
	if err != nil {
		t.Fatalf("PreparePreviewInvocation: %v", err)
	}
	want := previewInputConvoyPrefix + "myrig/worker"
	if inv.InputConvoy != want {
		t.Fatalf("preview invocation = %+v, want routing-identity preview input convoy %q", inv, want)
	}
	if got := inv.Vars[ConvoyIDVar]; got != want {
		t.Fatalf("vars[%s] = %q, want %q", ConvoyIDVar, got, want)
	}
	matches, err := store.List(beads.ListQuery{Type: "convoy"})
	if err != nil {
		t.Fatalf("List convoys: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("preview created input convoys = %+v, want none", matches)
	}
}

func TestPreviewInputConvoyIDForRoutingIdentity(t *testing.T) {
	got := PreviewInputConvoyIDForRoutingIdentity("  myrig/worker  ")
	want := previewInputConvoyPrefix + "myrig/worker"
	if got != want {
		t.Fatalf("PreviewInputConvoyIDForRoutingIdentity = %q, want %q", got, want)
	}
}

func TestPrepareInvocationRejectsUnsupportedDrainFromExpansionBeforeNormalizingTarget(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "parent.formula.toml", `
formula = "parent"
version = 2
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "work"
title = "Work"
expand = "bad-drain"
`)
	writeFormula(t, dir, "bad-drain.formula.toml", `
formula = "bad-drain"
version = 2
contract = "graph.v2"
type = "expansion"

[[template]]
id = "{target}.drain"
title = "Drain"

[template.drain]
context = "separate"
formula = "item"
max_units = 101
`)
	writeFormula(t, dir, "item.formula.toml", `
formula = "item"
version = 2
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "work"
title = "Item {{convoy_id}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	_, err = PrepareInvocation(context.Background(), store, "parent", []string{dir}, target.ID, nil)
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want expanded drain validation error")
	}
	if !strings.Contains(err.Error(), `max_units must be <= 100`) {
		t.Fatalf("error = %q, want expanded drain max_units error", err)
	}
	matches, err := store.List(beads.ListQuery{Type: "convoy"})
	if err != nil {
		t.Fatalf("List input convoys: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("input convoys = %+v, want none before validation succeeds", matches)
	}
}

func TestPrepareInvocationRejectsNonGraphDrainItemFormulaBeforeNormalizingTarget(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "parent.formula.toml", `
formula = "parent"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "drain"
title = "Drain"

[steps.drain]
context = "separate"
formula = "item"
`)
	writeFormula(t, dir, "item.formula.toml", `
formula = "item"
version = 1
type = "workflow"

[[steps]]
id = "work"
title = "Legacy item"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	_, err = PrepareInvocation(context.Background(), store, "parent", []string{dir}, target.ID, nil)
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want drain item graph.v2 error")
	}
	if !strings.Contains(err.Error(), "must declare the formulas v2 contract ([requires] formula_compiler = \">=2.0.0\")") {
		t.Fatalf("error = %q, want formulas v2 item formula message", err)
	}
	matches, err := store.List(beads.ListQuery{Type: "convoy"})
	if err != nil {
		t.Fatalf("List input convoys: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("input convoys = %+v, want none before validation succeeds", matches)
	}
}

func TestPrepareInvocationRejectsDrainItemFormulaMissingRuntimeVars(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "parent.formula.toml", `
formula = "parent"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "drain"
title = "Drain"

[steps.drain]
context = "separate"
formula = "item"
`)
	writeFormula(t, dir, "item.formula.toml", `
formula = "item"
version = 1
contract = "graph.v2"
type = "workflow"

[vars]
[vars.extra]
required = true

[[steps]]
id = "work"
title = "Item {{convoy_id}} {{extra}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	_, err = PrepareInvocation(context.Background(), store, "parent", []string{dir}, target.ID, nil)
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want drain item runtime var error")
	}
	if !strings.Contains(err.Error(), "runtime vars") || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("error = %q, want missing item runtime var", err)
	}
}

func TestPrepareInvocationPassesParentRuntimeVarsToDrainItemValidation(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "parent.formula.toml", `
formula = "parent"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "drain"
title = "Drain"

[steps.drain]
context = "separate"
formula = "item"
`)
	writeFormula(t, dir, "item.formula.toml", `
formula = "item"
version = 1
contract = "graph.v2"
type = "workflow"

[vars]
[vars.extra]
required = true

[[steps]]
id = "work"
title = "Item {{convoy_id}} {{extra}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	inv, err := PrepareInvocation(context.Background(), store, "parent", []string{dir}, target.ID, map[string]string{"extra": "provided"})
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	if got := inv.Vars["extra"]; got != "provided" {
		t.Fatalf("vars[extra] = %q, want provided", got)
	}
}

func TestPrepareInvocationPassesParentDefaultVarsToDrainItemValidation(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "parent.formula.toml", `
formula = "parent"
version = 1
contract = "graph.v2"
type = "workflow"

[vars]
[vars.extra]
default = "from-default"

[[steps]]
id = "drain"
title = "Drain"

[steps.drain]
context = "separate"
formula = "item"
`)
	writeFormula(t, dir, "item.formula.toml", `
formula = "item"
version = 1
contract = "graph.v2"
type = "workflow"

[vars]
[vars.extra]
required = true

[[steps]]
id = "work"
title = "Item {{convoy_id}} {{extra}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	inv, err := PrepareInvocation(context.Background(), store, "parent", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	if got := inv.Vars["extra"]; got != "from-default" {
		t.Fatalf("vars[extra] = %q, want formula default", got)
	}
}

func TestRuntimeVarsMetadataExcludesReservedVars(t *testing.T) {
	raw := RuntimeVarsMetadata(map[string]string{
		ConvoyIDVar: "CONVOY-1",
		"issue":     "BD-1",
		"extra":     "provided",
	})
	if strings.Contains(raw, ConvoyIDVar) || strings.Contains(raw, "issue") {
		t.Fatalf("RuntimeVarsMetadata = %q, want reserved vars excluded", raw)
	}
	parsed, err := ParseRuntimeVarsMetadata(raw)
	if err != nil {
		t.Fatalf("ParseRuntimeVarsMetadata: %v", err)
	}
	if len(parsed) != 1 || parsed["extra"] != "provided" {
		t.Fatalf("parsed = %#v, want only extra", parsed)
	}
}

func writeFormula(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.TrimSpace(contents)+"\n"), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}
}

func invocationGitOK(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available in PATH")
	}
}

func initInvocationRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runInvocationGit(t, root, "init", "-b", "main")
	runInvocationGit(t, root, "config", "user.email", "test@example.com")
	runInvocationGit(t, root, "config", "user.name", "test")
	runInvocationGit(t, root, "config", "commit.gpgsign", "false")
	return root
}

func runInvocationGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestPrepareInvocationResolvesDeprecatedIssueAlias(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "legacy.formula.toml", `
formula = "legacy"
version = 1
contract = "graph.v2"
type = "workflow"

[vars]
[vars.issue]
description = "legacy work bead"
required = true

[[steps]]
id = "inspect"
title = "Inspect {{issue}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	inv, err := PrepareInvocation(context.Background(), store, "legacy", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	if got := inv.Vars[LegacyIssueVar]; got != target.ID {
		t.Fatalf("vars[%s] = %q, want single tracked member %q", LegacyIssueVar, got, target.ID)
	}
	if got := inv.Vars[ConvoyIDVar]; got != inv.InputConvoy {
		t.Fatalf("vars[%s] = %q, want %q", ConvoyIDVar, got, inv.InputConvoy)
	}
	if len(inv.Deprecations) == 0 {
		t.Fatal("invocation has no deprecations, want issue alias warnings")
	}
	for _, d := range inv.Deprecations {
		if !strings.Contains(d, "2941") || !strings.Contains(d, "deprecated") {
			t.Fatalf("deprecation %q missing migration pointer", d)
		}
	}
}

func TestPrepareInvocationIssueAliasRequiresSingleMemberConvoy(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "legacy.formula.toml", `
formula = "legacy"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{issue}}"
`)
	store := beads.NewMemStore()
	convoy, err := store.Create(beads.Bead{Title: "convoy", Type: "convoy"})
	if err != nil {
		t.Fatalf("Create convoy: %v", err)
	}
	for _, title := range []string{"one", "two"} {
		member, err := store.Create(beads.Bead{Title: title, Type: "task"})
		if err != nil {
			t.Fatalf("Create member: %v", err)
		}
		if err := convoycore.TrackItem(store, convoy.ID, member.ID); err != nil {
			t.Fatalf("TrackItem: %v", err)
		}
	}

	_, err = PrepareInvocation(context.Background(), store, "legacy", []string{dir}, convoy.ID, nil)
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want single-member alias error")
	}
	if !strings.Contains(err.Error(), "exactly one tracked member") {
		t.Fatalf("error = %q, want single-member alias message", err)
	}
}

func TestPrepareInvocationWithoutLegacyRefsDoesNotInjectIssue(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	inv, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	if _, ok := inv.Vars[LegacyIssueVar]; ok {
		t.Fatalf("vars = %+v, want no injected issue alias", inv.Vars)
	}
	if len(inv.Deprecations) != 0 {
		t.Fatalf("deprecations = %v, want none", inv.Deprecations)
	}
}

func TestPrepareInvocationStillRejectsCallerSuppliedIssue(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "legacy.formula.toml", `
formula = "legacy"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{issue}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	_, err = PrepareInvocation(context.Background(), store, "legacy", []string{dir}, target.ID, map[string]string{"issue": "user-bead"})
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want reserved-var rejection")
	}
	if !strings.Contains(err.Error(), "cannot be supplied by the caller") {
		t.Fatalf("error = %q, want caller-reserved message", err)
	}
}

func TestRootKeyIgnoresDeprecatedIssueRuntimeVar(t *testing.T) {
	base := RootKey("convoy-1", "graph-work", map[string]string{
		"mode": "review",
	}, "", "")
	withAlias := RootKey("convoy-1", "graph-work", map[string]string{
		"mode":          "review",
		LegacyIssueVar:  "gc-member",
		legacyBeadIDVar: "gc-member",
	}, "", "")
	if base != withAlias {
		t.Fatalf("RootKey with alias vars = %q, want %q (issue/bead_id must not affect idempotence keys)", withAlias, base)
	}
}

// depAddFailingStore fails every DepAdd, simulating the cross-store dep-add
// failure that aborts input-convoy tracking mid-pour.
type depAddFailingStore struct {
	beads.Store
}

func (s depAddFailingStore) DepAdd(fromID, _, _ string) error {
	return fmt.Errorf("resolving issue ID %s: no issue found matching %q", fromID, fromID)
}

func TestCreateSingleItemInputConvoyClosesConvoyOnTrackFailure(t *testing.T) {
	mem := beads.NewMemStore()
	target, err := mem.Create(beads.Bead{Title: "work item", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	store := depAddFailingStore{Store: mem}

	_, err = CreateSingleItemInputConvoy(store, target)
	if err == nil {
		t.Fatal("CreateSingleItemInputConvoy succeeded, want tracking failure")
	}
	// The synthetic convoy minted for this pour must not survive as an open
	// claim-attracting bead.
	open, err := mem.List(beads.ListQuery{Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("open synthetic convoys after failed pour = %d, want 0 (ids: %v)", len(open), open)
	}
}

// depListFailingStore mints and tracks convoys normally but fails every
// DepList, simulating the cross-store membership read anomaly that makes
// ResolveLegacyIssueAlias fail after PrepareInvocation has already minted the
// synthetic input convoy.
type depListFailingStore struct {
	beads.Store
}

func (s depListFailingStore) DepList(_, _ string) ([]beads.Dep, error) {
	return nil, fmt.Errorf("cross-store membership read failed")
}

func TestPrepareInvocationClosesSyntheticConvoyOnLegacyAliasFailure(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "legacy.formula.toml", `
formula = "legacy"
version = 1
contract = "graph.v2"
type = "workflow"

[vars]
[vars.issue]
description = "legacy work bead"
required = true

[[steps]]
id = "inspect"
title = "Inspect {{issue}}"
`)
	mem := beads.NewMemStore()
	target, err := mem.Create(beads.Bead{Title: "work item", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}
	// DepAdd (convoy tracking) still succeeds, so NormalizeInputConvoy mints the
	// synthetic convoy; the later DepList inside ResolveLegacyIssueAlias fails.
	store := depListFailingStore{Store: mem}

	_, err = PrepareInvocation(context.Background(), store, "legacy", []string{dir}, target.ID, nil)
	if err == nil {
		t.Fatal("PrepareInvocation succeeded, want legacy alias resolution failure")
	}
	if !strings.Contains(err.Error(), "resolving deprecated issue alias") {
		t.Fatalf("error = %q, want deprecated issue alias failure", err)
	}
	// The synthetic convoy minted for the bead target before the alias failure
	// must not survive as an open claim-attracting bead.
	open, err := mem.List(beads.ListQuery{Type: "convoy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("open synthetic convoys after failed pour = %d, want 0 (ids: %v)", len(open), open)
	}
}

// TestSyntheticInputConvoyIsWorkClassAndCoResidentWithItsTarget pins the
// creation path against the classification.
//
// A synthetic input convoy is a WORK bead, and that is not a filing preference:
// the convoy's whole content is a `tracks` edge to its target, and TrackItemIn
// refuses an edge whose member is owned by another class store. So the two have
// to agree — the classification says work, and the mint has to land where the
// target is. NormalizeInputConvoy makes that structural by resolving the target
// from the same handle it mints through; this test is what fails if a future
// caller splits them.
func TestSyntheticInputConvoyIsWorkClassAndCoResidentWithItsTarget(t *testing.T) {
	work := beads.NewMemStore()
	target, err := work.Create(beads.Bead{Title: "a work bead", Type: "task"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}

	convoyID, err := NormalizeInputConvoy(work, target.ID)
	if err != nil {
		t.Fatalf("NormalizeInputConvoy: %v", err)
	}
	convoy, err := work.Get(convoyID)
	if err != nil {
		t.Fatalf("the input convoy is not in the target's store: %v", err)
	}
	if got := coordclass.Classify(convoy); got != coordclass.ClassWork {
		t.Fatalf("Classify(input convoy) = %s, want work; a convoy owned by any other class cannot hold a tracks edge to its work target", got)
	}
	tracked, err := convoycore.HasTrack(work, convoyID, target.ID)
	if err != nil {
		t.Fatalf("HasTrack: %v", err)
	}
	if !tracked {
		t.Fatalf("input convoy %s does not track target %s", convoyID, target.ID)
	}
}

// TestDispatchIdempotencyKeyStability pins the dispatch key contract the
// retry-duplication guard (gc-wnjjc) depends on: identical (target, formula,
// normalized vars) yield the same key across attempts; reserved runtime vars
// never perturb it; and any real identity change yields a distinct key.
func TestDispatchIdempotencyKeyStability(t *testing.T) {
	base := DispatchIdempotencyKey("gc-1", "build-from-plan", map[string]string{"plan_path": "p.md"})
	if again := DispatchIdempotencyKey("gc-1", "build-from-plan", map[string]string{"plan_path": "p.md"}); again != base {
		t.Fatalf("key not stable across attempts: %q vs %q", again, base)
	}
	withReserved := DispatchIdempotencyKey("gc-1", "build-from-plan", map[string]string{
		"plan_path":     "p.md",
		ConvoyIDVar:     "convoy-A",
		LegacyIssueVar:  "gc-1",
		legacyBeadIDVar: "gc-1",
	})
	if withReserved != base {
		t.Fatalf("reserved vars changed the key: %q vs %q", withReserved, base)
	}
	if k := DispatchIdempotencyKey("gc-2", "build-from-plan", map[string]string{"plan_path": "p.md"}); k == base {
		t.Fatalf("different target shares key %q", k)
	}
	if k := DispatchIdempotencyKey("gc-1", "build-basic", map[string]string{"plan_path": "p.md"}); k == base {
		t.Fatalf("different formula shares key %q", k)
	}
	if k := DispatchIdempotencyKey("gc-1", "build-from-plan", map[string]string{"plan_path": "other.md"}); k == base {
		t.Fatalf("different vars share key %q", k)
	}
}

// TestPrepareInvocationRetryReusesConvoyLeftOpenByFailedAttempt covers the
// observed duplicate-input-convoy incident shape (gc-wnjjc): attempt 1 mints
// an input convoy and then fails AFTER the mint in a way that leaves the
// convoy open (its best-effort close lost to Dolt contention). The retry must
// converge on that stranded open convoy, not mint a second one beside it.
func TestPrepareInvocationRetryReusesConvoyLeftOpenByFailedAttempt(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	first, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	// Attempt 1 "failed" downstream and its CloseSyntheticInputConvoy was lost
	// to contention: the convoy is still open. Nothing to simulate — the convoy
	// IS open at this point; the retry is the interesting half.
	retry, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation retry: %v", err)
	}
	if retry.InputConvoy != first.InputConvoy {
		t.Fatalf("retry minted a second input convoy %s beside stranded %s", retry.InputConvoy, first.InputConvoy)
	}
	if got := retry.Vars[ConvoyIDVar]; got != first.InputConvoy {
		t.Fatalf("retry vars[%s] = %q, want reused convoy %q", ConvoyIDVar, got, first.InputConvoy)
	}
}

// TestPrepareInvocationRetryAfterClosedConvoyMintsFresh proves the reuse guard
// never resurrects a CONSUMED input convoy: once the prior workflow finished
// (its input convoy closed), a re-dispatch of the same bead is a genuinely new
// dispatch and gets a fresh convoy.
func TestPrepareInvocationRetryAfterClosedConvoyMintsFresh(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	first, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation: %v", err)
	}
	if err := store.Close(first.InputConvoy); err != nil {
		t.Fatalf("Close first convoy: %v", err)
	}

	second, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation second: %v", err)
	}
	if second.InputConvoy == first.InputConvoy {
		t.Fatalf("second dispatch resurrected closed convoy %s", first.InputConvoy)
	}
}

// TestPrepareInvocationDistinctVarsMintDistinctConvoys proves the guard keys
// on the normalized vars: same bead + formula with different effective vars is
// a different dispatch identity, not a retry.
func TestPrepareInvocationDistinctVarsMintDistinctConvoys(t *testing.T) {
	formulatest.EnableV2ForTest(t)
	dir := t.TempDir()
	writeFormula(t, dir, "work.formula.toml", `
formula = "work"
version = 1
contract = "graph.v2"
type = "workflow"

[vars]
[vars.mode]
default = "review"

[[steps]]
id = "inspect"
title = "Inspect {{convoy_id}} in {{mode}}"
`)
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	review, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, nil)
	if err != nil {
		t.Fatalf("PrepareInvocation (default vars): %v", err)
	}
	implement, err := PrepareInvocation(context.Background(), store, "work", []string{dir}, target.ID, map[string]string{"mode": "implement"})
	if err != nil {
		t.Fatalf("PrepareInvocation (mode=implement): %v", err)
	}
	if implement.InputConvoy == review.InputConvoy {
		t.Fatalf("distinct vars shared input convoy %s", review.InputConvoy)
	}
}

// TestCloseSyntheticInputConvoySkipsConvoyConsumedByLiveRoot pins the reuse
// guard's cleanup counterpart: with convoy reuse a FAILED retry can share its
// input convoy with an already-launched live workflow root, and its failure
// cleanup must not close that root's provenance link out from under it. Once
// no live consumer remains, the close proceeds as before — and the consumer
// itself (dispatch finalization passes the root as targetID) is never blocked
// by its own reference.
func TestCloseSyntheticInputConvoySkipsConvoyConsumedByLiveRoot(t *testing.T) {
	store := beads.NewMemStore()
	target, err := store.Create(beads.Bead{Title: "target", Type: "task"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}
	convoy, err := CreateSingleItemInputConvoy(store, target)
	if err != nil {
		t.Fatalf("CreateSingleItemInputConvoy: %v", err)
	}
	root, err := store.Create(beads.Bead{
		Title: "workflow root",
		Type:  "task",
		Metadata: map[string]string{
			"gc.kind":             "workflow",
			"gc.formula_contract": "graph.v2",
			"gc.input_convoy_id":  convoy.ID,
		},
	})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}

	// A failed retry's cleanup (targetID = the source bead) must skip the close.
	CloseSyntheticInputConvoy(store, convoy.ID, target.ID)
	got, err := store.Get(convoy.ID)
	if err != nil {
		t.Fatalf("Get convoy: %v", err)
	}
	if got.Status == "closed" {
		t.Fatalf("cleanup closed input convoy %s while live root %s still consumes it", convoy.ID, root.ID)
	}

	// The consuming root's own finalization (targetID = the root) is not
	// blocked by its own reference.
	CloseSyntheticInputConvoy(store, convoy.ID, root.ID)
	got, err = store.Get(convoy.ID)
	if err != nil {
		t.Fatalf("Get convoy after finalize close: %v", err)
	}
	if got.Status != "closed" {
		t.Fatalf("finalization did not close convoy %s (status %s)", convoy.ID, got.Status)
	}
}
