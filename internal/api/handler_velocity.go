package api

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/githubmonitor"
)

const (
	velocityDefaultWindowHours = 72
	velocityMaxWindowHours     = 168
	// velocityLocalCacheMaxAge bounds how often the Dolt stores are re-scanned
	// for the local aggregate; the dashboard polls at 15s, the stores change
	// far slower than that.
	velocityLocalCacheMaxAge = 60 * time.Second
	// velocityGitHubCacheMaxAge bounds GitHub API calls (rate-limited, slow).
	velocityGitHubCacheMaxAge = 5 * time.Minute
	// velocityGitHubLastGoodMaxAge bounds how long a failed refresh may keep
	// serving the retained last-good GitHub reading (marked Stale).
	velocityGitHubLastGoodMaxAge = 24 * time.Hour
	// velocityLocalLastGoodMaxAge bounds how long an expired local aggregate
	// may serve while a background rebuild replaces it.
	velocityLocalLastGoodMaxAge = 24 * time.Hour
	velocityGitHubFetchTimeout  = 15 * time.Second
	// velocityGroupCap caps every group list; the remainder rolls up into an
	// "other" row so pathological group cardinality cannot bloat the wire.
	velocityGroupCap = 12
)

// velocityPRCounter is the GitHub surface the handler needs; a seam so tests
// can stub GitHub without HTTP.
type velocityPRCounter interface {
	CountPullRequests(ctx context.Context, owner, repo string, since time.Time) (githubmonitor.PRCounts, error)
}

// Package-level seams mirroring cmd/gc's newGitHubPRBackfillClient pattern.
var (
	newVelocityPRCounter = func(token string) velocityPRCounter {
		return githubmonitor.NewGraphQLClient(token)
	}
	resolveVelocityGitHubToken = githubmonitor.ResolveToken
	// velocityRebuildLaunch runs the async local-aggregate rebuild. A seam so
	// tests can run it inline instead of racing a goroutine.
	velocityRebuildLaunch = func(rebuild func()) { go rebuild() }
)

func (s *Server) humaHandleVelocity(ctx context.Context, input *VelocityInput) (*VelocityOutput, error) {
	windowHours := input.WindowHours
	if windowHours <= 0 {
		windowHours = velocityDefaultWindowHours
	}
	if windowHours > velocityMaxWindowHours {
		windowHours = velocityMaxWindowHours
	}
	now := time.Now().UTC()
	since := now.Add(-time.Duration(windowHours) * time.Hour)

	localKey := fmt.Sprintf("velocity:local:%d", windowHours)
	body, ok := cachedResponseWithinAgeAs[VelocityBody](s, localKey, velocityLocalCacheMaxAge)
	if !ok {
		// The full multi-store closed-bead scan takes ~10s on a busy city.
		// Blocking a dashboard poll on it every cache expiry makes the tile
		// flap, so an expired-but-retained aggregate serves immediately while
		// one background rebuild refreshes the cache. Only a cold start (no
		// retained aggregate at all) pays the scan inline.
		if retained, okRetained := cachedResponseWithinAgeAs[VelocityBody](s, velocityLocalLastGoodKey(windowHours), velocityLocalLastGoodMaxAge); okRetained {
			body = retained
			s.kickVelocityLocalRebuild(windowHours)
		} else {
			body = s.rebuildVelocityLocal(windowHours)
		}
	}
	body.PRs = s.velocityPRs(ctx, windowHours, since, now)
	return &VelocityOutput{Body: body}, nil
}

func velocityLocalLastGoodKey(windowHours int) string {
	return fmt.Sprintf("velocity:local-lastgood:%d", windowHours)
}

// rebuildVelocityLocal computes the local aggregate for the window and stores
// it under both the fresh and last-good cache keys.
func (s *Server) rebuildVelocityLocal(windowHours int) VelocityBody {
	now := time.Now().UTC()
	since := now.Add(-time.Duration(windowHours) * time.Hour)
	body := VelocityBody{
		WindowHours: windowHours,
		Since:       since.Format(time.RFC3339),
		GeneratedAt: now.Format(time.RFC3339Nano),
	}
	body.Runs, body.Beads = s.buildVelocityLocal(since, windowHours)
	s.storeResponse(fmt.Sprintf("velocity:local:%d", windowHours), 0, body)
	s.storeResponse(velocityLocalLastGoodKey(windowHours), 0, body)
	return body
}

// kickVelocityLocalRebuild starts one background rebuild per window;
// concurrent polls that find the same expired cache coalesce onto it.
func (s *Server) kickVelocityLocalRebuild(windowHours int) {
	key := fmt.Sprintf("velocity:rebuild:%d", windowHours)
	if _, alreadyRunning := s.velocityRebuilds.LoadOrStore(key, struct{}{}); alreadyRunning {
		return
	}
	velocityRebuildLaunch(func() {
		defer s.velocityRebuilds.Delete(key)
		s.rebuildVelocityLocal(windowHours)
	})
}

// buildVelocityLocal aggregates closed workflow roots and closed beads across
// every workflow store. Store failures degrade to partial results, never to a
// failed response (the orders_feed idiom).
func (s *Server) buildVelocityLocal(since time.Time, windowHours int) (VelocityRuns, VelocityBeads) {
	runs := VelocityRuns{Series: make([]int, windowHours)}
	beadsAgg := VelocityBeads{Series: make([]int, windowHours)}
	formulaCounts := make(map[string]*VelocityFormulaRuns)
	typeCounts := make(map[string]int)
	kindCounts := make(map[string]int)

	for _, info := range workflowStores(s.state) {
		if info.store == nil {
			continue
		}

		closed, err := info.store.List(beads.ListQuery{
			Status:        "closed",
			IncludeClosed: true,
			ClosedAfter:   since,
			SkipLabels:    true,
		})
		if err != nil {
			log.Printf("api: velocity closed-bead list failed for %s: %v", info.ref, err)
			msg := info.ref + " store unavailable"
			runs.Partial = true
			runs.PartialReasons = append(runs.PartialReasons, msg)
			beadsAgg.Partial = true
			beadsAgg.PartialReasons = append(beadsAgg.PartialReasons, msg)
			continue
		}
		for _, bead := range closed {
			bucket := velocityBucketIndex(velocityCloseTime(bead), since, windowHours)
			if isWorkflowRoot(bead) {
				runs.Completed++
				runs.Series[bucket]++
				name := workflowFormulaName(bead)
				entry := formulaCounts[name]
				if entry == nil {
					entry = &VelocityFormulaRuns{Formula: name}
					formulaCounts[name] = entry
				}
				entry.Completed++
				switch strings.TrimSpace(bead.Metadata[beadmeta.OutcomeMetadataKey]) {
				case "pass":
					entry.Passed++
				case "fail":
					entry.Failed++
				case "skipped":
					entry.Skipped++
				}
			}
			// session beads are session-lifecycle churn, not work throughput.
			if bead.Type == "session" {
				continue
			}
			beadsAgg.Closed++
			beadsAgg.Series[bucket]++
			beadType := strings.TrimSpace(bead.Type)
			if beadType == "" {
				beadType = "task"
			}
			typeCounts[beadType]++
			kind := strings.TrimSpace(bead.Metadata[beadmeta.KindMetadataKey])
			if kind == "" {
				kind = "(work)"
			}
			kindCounts[kind]++
		}

		// Wisp-tier churn (order runs and other ephemeral/no-history rows) is
		// counted separately: its volume is ~3x the durable tiers and would
		// drown every other group if merged in.
		wisps, err := info.store.List(beads.ListQuery{
			Status:        "closed",
			IncludeClosed: true,
			ClosedAfter:   since,
			SkipLabels:    true,
			TierMode:      beads.TierWisps,
		})
		if err != nil {
			log.Printf("api: velocity wisp list failed for %s: %v", info.ref, err)
			beadsAgg.Partial = true
			beadsAgg.PartialReasons = append(beadsAgg.PartialReasons, info.ref+" wisp tier unavailable")
			continue
		}
		beadsAgg.WispsClosed += len(wisps)
	}

	runs.ByFormula = cappedFormulaRuns(formulaCounts)
	beadsAgg.ByType = cappedGroupCounts(typeCounts)
	beadsAgg.ByKind = cappedGroupCounts(kindCounts)
	return runs, beadsAgg
}

// velocityCloseTime mirrors the beads layer's close-time reference: the
// recorded ClosedAt when present, else UpdatedAt (a close bumps it), else
// CreatedAt.
func velocityCloseTime(bead beads.Bead) time.Time {
	if bead.ClosedAt != nil && !bead.ClosedAt.IsZero() {
		return *bead.ClosedAt
	}
	if !bead.UpdatedAt.IsZero() {
		return bead.UpdatedAt
	}
	return bead.CreatedAt
}

// velocityBucketIndex maps a close time to its hour bucket in [0, windowHours),
// oldest hour first. Out-of-range values clamp to the nearest edge so clock
// skew can never panic the aggregation.
func velocityBucketIndex(closed, since time.Time, windowHours int) int {
	index := int(closed.Sub(since) / time.Hour)
	if index < 0 {
		return 0
	}
	if index >= windowHours {
		return windowHours - 1
	}
	return index
}

func cappedFormulaRuns(counts map[string]*VelocityFormulaRuns) []VelocityFormulaRuns {
	out := make([]VelocityFormulaRuns, 0, len(counts))
	for _, entry := range counts {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Completed != out[j].Completed {
			return out[i].Completed > out[j].Completed
		}
		return out[i].Formula < out[j].Formula
	})
	if len(out) <= velocityGroupCap {
		return out
	}
	other := VelocityFormulaRuns{Formula: "other"}
	for _, entry := range out[velocityGroupCap:] {
		other.Completed += entry.Completed
		other.Passed += entry.Passed
		other.Failed += entry.Failed
		other.Skipped += entry.Skipped
	}
	return append(out[:velocityGroupCap:velocityGroupCap], other)
}

func cappedGroupCounts(counts map[string]int) []VelocityGroupCount {
	out := make([]VelocityGroupCount, 0, len(counts))
	for group, closed := range counts {
		out = append(out, VelocityGroupCount{Group: group, Closed: closed})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Closed != out[j].Closed {
			return out[i].Closed > out[j].Closed
		}
		return out[i].Group < out[j].Group
	})
	if len(out) <= velocityGroupCap {
		return out
	}
	other := VelocityGroupCount{Group: "other"}
	for _, entry := range out[velocityGroupCap:] {
		other.Closed += entry.Closed
	}
	return append(out[:velocityGroupCap:velocityGroupCap], other)
}

// velocityPRs resolves the GitHub slice: fresh cache → live fetch → retained
// last-good (Stale) → unavailable-with-reason. Local metrics never depend on
// this path succeeding.
func (s *Server) velocityPRs(ctx context.Context, windowHours int, since, now time.Time) VelocityPRs {
	repos := s.state.Config().GitHubRepoSlugs()
	if len(repos) == 0 {
		return VelocityPRs{UnavailableReason: "no repositories configured for PR velocity (dashboard.velocity.github_repos)"}
	}

	freshKey := fmt.Sprintf("velocity:github:%d", windowHours)
	lastGoodKey := fmt.Sprintf("velocity:github-lastgood:%d", windowHours)
	if prs, ok := cachedResponseWithinAgeAs[VelocityPRs](s, freshKey, velocityGitHubCacheMaxAge); ok {
		return prs
	}

	prs, err := fetchVelocityPRs(ctx, repos, since)
	if err == nil {
		prs.FetchedAt = now.Format(time.RFC3339)
		s.storeResponse(freshKey, 0, prs)
		s.storeResponse(lastGoodKey, 0, prs)
		return prs
	}
	log.Printf("api: velocity GitHub PR counts failed: %v", err)
	if retained, ok := cachedResponseWithinAgeAs[VelocityPRs](s, lastGoodKey, velocityGitHubLastGoodMaxAge); ok {
		retained.Stale = true
		return retained
	}
	return VelocityPRs{UnavailableReason: velocityUnavailableReason(err)}
}

func fetchVelocityPRs(ctx context.Context, repos []string, since time.Time) (VelocityPRs, error) {
	// Detached from request cancellation: a browser that aborts a poll
	// mid-flight (tab switch, superseding refresh) must not kill the
	// `gh auth token` subprocess or the GraphQL calls — that failure mode
	// kept the GitHub cache from ever priming under real dashboard load
	// (observed 2026-08-29: "gh auth token failed: context canceled").
	// The fetch's own timeout stays the bound.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), velocityGitHubFetchTimeout)
	defer cancel()

	token, err := resolveVelocityGitHubToken(fetchCtx)
	if err != nil {
		return VelocityPRs{}, err
	}
	client := newVelocityPRCounter(token)

	prs := VelocityPRs{Available: true}
	for _, slug := range repos {
		owner, repo, ok := strings.Cut(slug, "/")
		if !ok {
			return VelocityPRs{}, fmt.Errorf("velocity repo %q is not an owner/repo slug", slug)
		}
		counts, err := client.CountPullRequests(fetchCtx, owner, repo, since)
		if err != nil {
			return VelocityPRs{}, fmt.Errorf("%s: %w", slug, err)
		}
		prs.Merged += counts.Merged
		prs.Created += counts.Created
		prs.Repos = append(prs.Repos, VelocityPRRepo{Repo: slug, Merged: counts.Merged, Created: counts.Created})
	}
	return prs, nil
}

// velocityUnavailableReason compresses fetch errors to a dashboard-safe
// one-liner (no tokens, no URLs with credentials).
func velocityUnavailableReason(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "token not found"):
		return "GitHub token unavailable (set GITHUB_TOKEN or authenticate gh)"
	case strings.Contains(msg, "rate limit"):
		return "GitHub API rate limit exceeded"
	case strings.Contains(msg, "context deadline exceeded"):
		return "GitHub API timed out"
	}
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return "GitHub PR counts unavailable: " + msg
}
