package sling

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// TestGraphAttachRetryConvergesOnSingleRootAndConvoy covers the gc-wnjjc
// retry-duplication incident end to end: the same source bead dispatched onto
// the same graph formula twice — the shape a client retry takes when the first
// dispatch's success is hidden by Dolt contention noise. Before the dispatch
// idempotency guard, each attempt minted its OWN synthetic input convoy, which
// changed the graphv2 RootKey (it keys on the convoy ID), so the root-key
// singleton never matched and the retry launched a full second live workflow
// root beside the first (observed in production as duplicate input-convoy
// pairs three times and one full duplicate build-from-plan root, killed at a
// cost of 22 beads). With the guard, the retry reuses the open input convoy,
// the RootKey matches, and the launch converges idempotently on the first
// root.
func TestGraphAttachRetryConvergesOnSingleRootAndConvoy(t *testing.T) {
	formulaDir := t.TempDir()
	writeGraphV2ConvoyFormula(t, formulaDir)
	cfg := graphV2SlingTestConfig(t, formulaDir)
	deps := testDeps(cfg, runtime.NewFake(), newFakeRunner().run)
	deps.CityPath = t.TempDir()
	source, err := deps.Store.Create(beads.Bead{ID: "BL-77", Title: "work", Type: "task", Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	a := config.Agent{Name: "mayor", MaxActiveSessions: intPtr(1)}

	first, err := s.AttachFormula(context.Background(), "graph-work", source.ID, a, FormulaOpts{})
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if first.WorkflowID == "" {
		t.Fatalf("first dispatch result = %+v, want workflow", first)
	}

	retry, err := s.AttachFormula(context.Background(), "graph-work", source.ID, a, FormulaOpts{})
	if err != nil {
		t.Fatalf("retried dispatch (must converge idempotently, not error): %v", err)
	}
	if retry.WorkflowID != first.WorkflowID {
		t.Fatalf("retried dispatch launched a second workflow root %q beside %q", retry.WorkflowID, first.WorkflowID)
	}

	if live := liveGraphV2Roots(t, deps.Store); len(live) != 1 {
		t.Fatalf("live graph roots after retry = %d, want exactly one; roots=%+v", len(live), live)
	}
	convoys, err := deps.Store.List(beads.ListQuery{Type: "convoy"})
	if err != nil {
		t.Fatalf("List convoys: %v", err)
	}
	var synthetic []string
	for _, c := range convoys {
		if c.Metadata["gc.synthetic"] == "true" {
			synthetic = append(synthetic, c.ID)
		}
	}
	if len(synthetic) != 1 {
		t.Fatalf("open synthetic input convoys after retry = %d, want exactly one (ids: %v)", len(synthetic), synthetic)
	}
}
