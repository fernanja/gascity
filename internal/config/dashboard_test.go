package config

import "testing"

func TestGitHubRepoSlugsPrefersExplicitVelocityList(t *testing.T) {
	cfg := &City{}
	cfg.Dashboard.Velocity.GitHubRepos = []string{"acme/app", "acme/app", " acme/tools "}
	cfg.GitHub.PRMonitors = []GitHubPRMonitor{{Owner: "other", Repo: "repo"}}

	got := cfg.GitHubRepoSlugs()
	want := []string{"acme/app", "acme/tools"}
	if len(got) != len(want) {
		t.Fatalf("GitHubRepoSlugs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GitHubRepoSlugs() = %v, want %v", got, want)
		}
	}
}

func TestGitHubRepoSlugsFallsBackToPRMonitors(t *testing.T) {
	cfg := &City{}
	cfg.GitHub.PRMonitors = []GitHubPRMonitor{
		{Owner: "acme", Repo: "app"},
		{Owner: "acme", Repo: "app"},
		{Owner: "", Repo: "half"},
	}
	got := cfg.GitHubRepoSlugs()
	if len(got) != 1 || got[0] != "acme/app" {
		t.Fatalf("GitHubRepoSlugs() = %v, want [acme/app]", got)
	}
}

func TestGitHubRepoSlugsEmptyWhenNothingConfigured(t *testing.T) {
	cfg := &City{}
	if got := cfg.GitHubRepoSlugs(); len(got) != 0 {
		t.Fatalf("GitHubRepoSlugs() = %v, want empty", got)
	}
}

func TestValidateDashboardConfigRejectsMalformedSlug(t *testing.T) {
	cfg := &City{}
	cfg.Dashboard.Velocity.GitHubRepos = []string{"acme/app", "not-a-slug"}
	if err := ValidateDashboardConfig(cfg); err == nil {
		t.Fatal("ValidateDashboardConfig() = nil, want error for malformed slug")
	}
	cfg.Dashboard.Velocity.GitHubRepos = []string{"acme/app"}
	if err := ValidateDashboardConfig(cfg); err != nil {
		t.Fatalf("ValidateDashboardConfig() = %v, want nil", err)
	}
}
