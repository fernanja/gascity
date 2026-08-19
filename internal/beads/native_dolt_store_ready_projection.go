package beads

import (
	"fmt"
)

// NativeDoltStore hands readiness to the cache — listIncludesCompleteDependencies
// reports true, which latches depsComplete — so it must also supply the column
// that makes the cache's answer equal its own. Losing this method silently
// reverts readiness to the weaker dependency-derived predicate, so pin it.
var _ readyProjectionEnrichmentStore = (*NativeDoltStore)(nil)

// enrichReadyProjectionForCache fills bd's denormalized is_blocked column onto
// items so a cache over this store answers readiness with the same predicate
// the store's own Ready() uses.
//
// It is not an optimization; it is what makes the cache's answer EQUAL the
// backing's. NativeDoltStore.listIncludesCompleteDependencies reports true, so
// a CachingStore over it latches depsComplete and serves readiness itself. With
// the column absent every Bead.IsBlocked is nil — beadFromNativeIssue cannot
// set it, because beadslib's types.Issue carries no is_blocked field — and
// cachedBeadReady then derives readiness from the bead's OWN direct
// blocks/waits-for/conditional-blocks deps. That fallback is weaker than the
// column in two ways that matter to gascity, which creates parent-child edges
// pervasively:
//
//   - is_blocked propagates transitively DOWN parent-child edges
//     (issueops.markBlockedTemplateForIssues joins `d.type = 'parent-child' AND
//     p.is_blocked = 1`), so a child of a blocked parent carries no blocking
//     edge of its own and reads ready to the fallback.
//   - cachedBeadReady treats a dep as blocking only when the target's status is
//     resident in the same cache, so an edge onto another store's row — a
//     relocated `gcg-` graph bead — is invisible to it.
//
// GetReadyWork filters `is_blocked = 0` (sqlbuild.ReadyWhere), so either gap
// offers the control dispatcher a step whose gate has not opened, the
// regression #3218 closed.
//
// The read is one batched IsBlockedBatch over the active set — the same
// SELECT id, is_blocked FROM {issues,wisps} WHERE id IN (...) that BdStore
// spends a `bd sql` subprocess on — issued in-process on the store's existing
// connection and inside withReadRetry, so it recovers from a managed-Dolt
// rebind like every other native read.
//
// A storage that cannot answer the column at all reports
// ErrReadyProjectionUnsupported rather than a silently weaker cache: the
// CachingStore then latches its degrade, declines every readiness handle, and
// takes the backing's own Ready. Slower, and correct.
//
// The embedded beads dependency is pinned to v1.2.2 — the tested 1.1-line
// code re-released after the accidental untested v1.2.0/v1.2.1 (gc-kjk3l) —
// which predates BlockedQuerier/IsBlockedBatch entirely, so the projection is
// unconditionally unsupported: this method reports
// ErrReadyProjectionUnsupported and the cache serves the backing's own Ready.
// Restore the IsBlockedBatch enrichment (see git history for this file) when
// the dependency moves forward onto a tested release that carries the
// projection surface again.
func (s *NativeDoltStore) enrichReadyProjectionForCache(items []Bead) ([]Bead, error) {
	if len(items) == 0 {
		return items, nil
	}
	return items, fmt.Errorf(
		"native ready projection: %w: embedded beads v1.2.2 (1.1 line) has no BlockedQuerier/IsBlockedBatch",
		ErrReadyProjectionUnsupported)
}
