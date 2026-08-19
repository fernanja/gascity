package beads

import (
	"context"
	"testing"

	beadslib "github.com/steveyegge/beads"
)

// workFilterMatchesStatus mirrors the backing store's status selection for
// GetReadyWork spies. With the embedded beads dependency pinned to v1.2.2
// (gc-kjk3l) WorkFilter carries only the singular Status field, so Ready()
// issues one call per open-class status and each spy call matches exactly
// that status.
func workFilterMatchesStatus(filter beadslib.WorkFilter, status beadslib.Status) bool {
	return status == filter.Status
}

// Ready pays one GetReadyWork call per open-class backing status: the v1.2.2
// (tested 1.1-line) WorkFilter has no Statuses field, so the single-call
// multi-status shape (sr-5rz) is unavailable until the dependency moves
// forward onto a tested post-sr-5rz release. This test pins the loop shape:
// every status in nativeDoltOpenReadyStatuses is queried exactly once, and
// rows returned for multiple statuses dedupe to one bead.
func TestNativeDoltStoreReadyOneBackingCallPerStatus(t *testing.T) {
	var calls []beadslib.WorkFilter
	storage := &nativeDoltStorageSpy{
		getReadyWork: func(_ context.Context, filter beadslib.WorkFilter) ([]*beadslib.Issue, error) {
			calls = append(calls, filter)
			return []*beadslib.Issue{
				{ID: "gc-open", Title: "open", Status: beadslib.StatusOpen, IssueType: beadslib.TypeTask, Priority: 2},
			}, nil
		},
	}
	store := newNativeDoltStoreForTest(storage)

	got, err := store.Ready(ReadyQuery{TierMode: TierBoth})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gc-open" {
		t.Fatalf("Ready = %+v, want the one open bead (deduped across per-status calls)", got)
	}
	if len(calls) != len(nativeDoltOpenReadyStatuses) {
		t.Fatalf("backing GetReadyWork calls = %d, want %d (one per open-class status; filters: %+v)",
			len(calls), len(nativeDoltOpenReadyStatuses), calls)
	}
	for i, st := range nativeDoltOpenReadyStatuses {
		if calls[i].Status != st {
			t.Fatalf("call %d Status = %q, want %q", i, calls[i].Status, st)
		}
		if !calls[i].IncludeEphemeral {
			t.Fatalf("call %d IncludeEphemeral must be set for TierBoth", i)
		}
	}
}

func TestNativeDoltStoreReadyAssigneePerStatusCalls(t *testing.T) {
	var calls []beadslib.WorkFilter
	storage := &nativeDoltStorageSpy{
		getReadyWork: func(_ context.Context, filter beadslib.WorkFilter) ([]*beadslib.Issue, error) {
			calls = append(calls, filter)
			return nil, nil
		},
	}
	store := newNativeDoltStoreForTest(storage)

	if _, err := store.Ready(ReadyQuery{TierMode: TierBoth, Assignee: "probe-me", Limit: 3}); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(calls) != len(nativeDoltOpenReadyStatuses) {
		t.Fatalf("backing GetReadyWork calls = %d, want %d", len(calls), len(nativeDoltOpenReadyStatuses))
	}
	for i := range calls {
		if calls[i].Assignee == nil || *calls[i].Assignee != "probe-me" {
			t.Fatalf("call %d Assignee filter = %v, want probe-me", i, calls[i].Assignee)
		}
		if calls[i].Limit != 0 {
			t.Fatalf("call %d backing Limit = %d, want 0 (limit applies client-side after the gc post-filter)", i, calls[i].Limit)
		}
	}
}
