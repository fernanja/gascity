package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/githubmonitor"
)

func velocityTestBead(id string, closedAgo time.Duration, beadType string, metadata map[string]string) beads.Bead {
	closedAt := time.Now().UTC().Add(-closedAgo)
	return beads.Bead{
		ID:        id,
		Title:     id,
		Status:    "closed",
		Type:      beadType,
		CreatedAt: closedAt.Add(-time.Hour),
		UpdatedAt: closedAt,
		ClosedAt:  &closedAt,
		Metadata:  metadata,
	}
}

func TestBuildVelocityLocalAggregatesRunsAndBeads(t *testing.T) {
	seed := []beads.Bead{
		velocityTestBead("run-pass", time.Hour, "task", map[string]string{
			"gc.kind": "workflow", "gc.formula_name": "build-basic", "gc.outcome": "pass",
		}),
		velocityTestBead("run-fail", 2*time.Hour, "task", map[string]string{
			"gc.kind": "workflow", "gc.formula_name": "do-work", "gc.outcome": "fail",
		}),
		// Outside the 72h window: excluded from everything.
		velocityTestBead("run-old", 100*time.Hour, "task", map[string]string{
			"gc.kind": "workflow", "gc.formula_name": "build-basic", "gc.outcome": "pass",
		}),
		// Session churn: excluded from bead counts (but not a workflow root).
		velocityTestBead("session-1", time.Hour, "session", nil),
		// Plain work bead with no gc.kind buckets as "(work)".
		velocityTestBead("work-1", 3*time.Hour, "task", nil),
		velocityTestBead("bug-1", 4*time.Hour, "bug", map[string]string{"gc.kind": "spec"}),
	}
	// Open workflow root: never counted.
	open := beads.Bead{
		ID: "run-open", Title: "run-open", Status: "open", Type: "task",
		CreatedAt: time.Now().UTC().Add(-time.Hour),
		Metadata:  map[string]string{"gc.kind": "workflow", "gc.formula_name": "build-basic"},
	}
	// Closed wisp inside the window: counted separately.
	wisp := velocityTestBead("wisp-1", time.Hour, "task", nil)
	wisp.Ephemeral = true

	store := beads.NewMemStoreFrom(10, append(seed, open, wisp), nil)
	state := newFakeState(t)
	state.stores = map[string]beads.Store{"myrig": store}
	s := newServer(state, false)

	since := time.Now().UTC().Add(-72 * time.Hour)
	runs, beadsAgg := s.buildVelocityLocal(since)

	if runs.Completed != 2 {
		t.Fatalf("runs.Completed = %d, want 2", runs.Completed)
	}
	if len(runs.ByFormula) != 2 {
		t.Fatalf("runs.ByFormula = %+v, want 2 formulas", runs.ByFormula)
	}
	byName := map[string]VelocityFormulaRuns{}
	for _, f := range runs.ByFormula {
		byName[f.Formula] = f
	}
	if got := byName["build-basic"]; got.Completed != 1 || got.Passed != 1 {
		t.Fatalf("build-basic = %+v, want Completed 1 Passed 1", got)
	}
	if got := byName["do-work"]; got.Completed != 1 || got.Failed != 1 {
		t.Fatalf("do-work = %+v, want Completed 1 Failed 1", got)
	}

	// run-pass, run-fail, work-1, bug-1 (session excluded, wisp separate).
	if beadsAgg.Closed != 4 {
		t.Fatalf("beads.Closed = %d, want 4", beadsAgg.Closed)
	}
	if beadsAgg.WispsClosed != 1 {
		t.Fatalf("beads.WispsClosed = %d, want 1", beadsAgg.WispsClosed)
	}
	kinds := map[string]int{}
	for _, g := range beadsAgg.ByKind {
		kinds[g.Group] = g.Closed
	}
	if kinds["workflow"] != 2 || kinds["(work)"] != 1 || kinds["spec"] != 1 {
		t.Fatalf("beads.ByKind = %+v, want workflow 2, (work) 1, spec 1", beadsAgg.ByKind)
	}
	types := map[string]int{}
	for _, g := range beadsAgg.ByType {
		types[g.Group] = g.Closed
	}
	if types["task"] != 3 || types["bug"] != 1 {
		t.Fatalf("beads.ByType = %+v, want task 3, bug 1", beadsAgg.ByType)
	}
	if runs.Partial || beadsAgg.Partial {
		t.Fatalf("partial = %v/%v, want false", runs.Partial, beadsAgg.Partial)
	}
}

type failingVelocityStore struct{ *beads.MemStore }

func (f *failingVelocityStore) List(beads.ListQuery) ([]beads.Bead, error) {
	return nil, errors.New("store down")
}

func TestBuildVelocityLocalDegradesToPartialOnStoreFailure(t *testing.T) {
	state := newFakeState(t)
	state.stores = map[string]beads.Store{
		"badrig":  &failingVelocityStore{MemStore: beads.NewMemStore()},
		"goodrig": beads.NewMemStoreFrom(2, []beads.Bead{velocityTestBead("ok-1", time.Hour, "task", nil)}, nil),
	}
	s := newServer(state, false)

	runs, beadsAgg := s.buildVelocityLocal(time.Now().UTC().Add(-72 * time.Hour))
	if !runs.Partial || !beadsAgg.Partial {
		t.Fatalf("partial = %v/%v, want true", runs.Partial, beadsAgg.Partial)
	}
	if beadsAgg.Closed != 1 {
		t.Fatalf("beads.Closed = %d, want 1 from the healthy store", beadsAgg.Closed)
	}
	if len(runs.PartialReasons) == 0 || len(beadsAgg.PartialReasons) == 0 {
		t.Fatal("PartialReasons empty, want the failed store named")
	}
}

type stubPRCounter struct {
	counts map[string]githubmonitor.PRCounts
	err    error
}

func (c *stubPRCounter) CountPullRequests(_ context.Context, owner, repo string, _ time.Time) (githubmonitor.PRCounts, error) {
	if c.err != nil {
		return githubmonitor.PRCounts{}, c.err
	}
	return c.counts[owner+"/"+repo], nil
}

func withVelocityGitHubStubs(t *testing.T, counter velocityPRCounter, tokenErr error) {
	t.Helper()
	oldCounter := newVelocityPRCounter
	oldToken := resolveVelocityGitHubToken
	newVelocityPRCounter = func(string) velocityPRCounter { return counter }
	resolveVelocityGitHubToken = func(context.Context) (string, error) {
		if tokenErr != nil {
			return "", tokenErr
		}
		return "test-token", nil
	}
	t.Cleanup(func() {
		newVelocityPRCounter = oldCounter
		resolveVelocityGitHubToken = oldToken
	})
}

func TestVelocityPRsUnavailableWithoutConfiguredRepos(t *testing.T) {
	state := newFakeState(t)
	s := newServer(state, false)
	now := time.Now().UTC()

	prs := s.velocityPRs(context.Background(), 72, now.Add(-72*time.Hour), now)
	if prs.Available {
		t.Fatal("prs.Available = true, want false with no repos configured")
	}
	if prs.UnavailableReason == "" {
		t.Fatal("prs.UnavailableReason empty, want a reason")
	}
}

func TestVelocityPRsSumsRepoCountsAndRetainsLastGood(t *testing.T) {
	state := newFakeState(t)
	state.cfg.Dashboard.Velocity.GitHubRepos = []string{"acme/app", "acme/tools"}
	s := newServer(state, false)
	now := time.Now().UTC()
	since := now.Add(-72 * time.Hour)

	counter := &stubPRCounter{counts: map[string]githubmonitor.PRCounts{
		"acme/app":   {Merged: 40, Created: 45},
		"acme/tools": {Merged: 6, Created: 7},
	}}
	withVelocityGitHubStubs(t, counter, nil)

	prs := s.velocityPRs(context.Background(), 72, since, now)
	if !prs.Available || prs.Merged != 46 || prs.Created != 52 {
		t.Fatalf("prs = %+v, want available with Merged 46 Created 52", prs)
	}
	if len(prs.Repos) != 2 {
		t.Fatalf("prs.Repos = %+v, want 2 entries", prs.Repos)
	}

	// Fresh cache hit serves without re-fetching even when the counter fails.
	counter.err = errors.New("network down")
	prs = s.velocityPRs(context.Background(), 72, since, now)
	if !prs.Available || prs.Stale {
		t.Fatalf("cached prs = %+v, want fresh available reading", prs)
	}

	// Expire the fresh entry: the retained last-good reading serves as stale.
	s.responseCacheMu.Lock()
	delete(s.responseCacheEntries, fmt.Sprintf("velocity:github:%d", 72))
	s.responseCacheMu.Unlock()
	prs = s.velocityPRs(context.Background(), 72, since, now)
	if !prs.Available || !prs.Stale {
		t.Fatalf("prs after failed refresh = %+v, want stale last-good", prs)
	}
	if prs.Merged != 46 {
		t.Fatalf("stale prs.Merged = %d, want retained 46", prs.Merged)
	}
}

func TestVelocityPRsUnavailableOnTokenFailureWithoutLastGood(t *testing.T) {
	state := newFakeState(t)
	state.cfg.Dashboard.Velocity.GitHubRepos = []string{"acme/app"}
	s := newServer(state, false)
	now := time.Now().UTC()

	withVelocityGitHubStubs(t, &stubPRCounter{}, errors.New("GitHub token not found in GITHUB_TOKEN/GH_TOKEN"))
	prs := s.velocityPRs(context.Background(), 72, now.Add(-72*time.Hour), now)
	if prs.Available {
		t.Fatalf("prs = %+v, want unavailable on token failure", prs)
	}
	if prs.UnavailableReason == "" {
		t.Fatal("UnavailableReason empty, want token guidance")
	}
}

func TestCappedGroupCountsRollsUpBeyondCap(t *testing.T) {
	counts := map[string]int{}
	for i := 0; i < velocityGroupCap+3; i++ {
		counts[fmt.Sprintf("group-%02d", i)] = 100 - i
	}
	out := cappedGroupCounts(counts)
	if len(out) != velocityGroupCap+1 {
		t.Fatalf("len = %d, want cap+1 with other rollup", len(out))
	}
	last := out[len(out)-1]
	if last.Group != "other" || last.Closed != (100-velocityGroupCap)+(100-velocityGroupCap-1)+(100-velocityGroupCap-2) {
		t.Fatalf("rollup = %+v, want other with the tail sum", last)
	}
}
