package config

import (
	"fmt"
	"strings"
)

// DashboardConfig groups server-side data-source declarations for the web
// dashboard ([dashboard] in city.toml).
type DashboardConfig struct {
	// Velocity configures the /velocity endpoint's aggregate metrics.
	Velocity VelocityConfig `toml:"velocity,omitempty"`
}

// VelocityConfig configures the dashboard's windowed velocity metrics.
type VelocityConfig struct {
	// GitHubRepos lists "owner/repo" slugs whose merged/created pull requests
	// count toward the PR velocity tiles. Empty means: fall back to the
	// repositories declared by [[github.pr_monitor]]; if that is empty too,
	// PR metrics report unavailable (local metrics are unaffected).
	GitHubRepos []string `toml:"github_repos,omitempty"`
}

// GitHubRepoSlugs returns the effective velocity repo list: the explicit
// [dashboard.velocity] github_repos when present, otherwise the distinct
// owner/repo pairs from [[github.pr_monitor]]. Order is preserved,
// duplicates removed.
func (c *City) GitHubRepoSlugs() []string {
	seen := make(map[string]bool)
	var out []string
	add := func(slug string) {
		slug = strings.TrimSpace(slug)
		if slug == "" || seen[slug] {
			return
		}
		seen[slug] = true
		out = append(out, slug)
	}
	for _, slug := range c.Dashboard.Velocity.GitHubRepos {
		add(slug)
	}
	if len(out) > 0 {
		return out
	}
	for _, m := range c.GitHub.PRMonitors {
		owner := strings.TrimSpace(m.Owner)
		repo := strings.TrimSpace(m.Repo)
		if owner != "" && repo != "" {
			add(owner + "/" + repo)
		}
	}
	return out
}

// ValidateDashboardConfig rejects malformed velocity repo slugs early so a
// typo surfaces at config load, not as a GitHub 404 at request time.
func ValidateDashboardConfig(cfg *City) error {
	for _, slug := range cfg.Dashboard.Velocity.GitHubRepos {
		trimmed := strings.TrimSpace(slug)
		parts := strings.Split(trimmed, "/")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return fmt.Errorf("dashboard.velocity.github_repos: %q is not an owner/repo slug", slug)
		}
	}
	return nil
}
