package githubmonitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ResolveToken finds a GitHub API token: GITHUB_TOKEN or GH_TOKEN from the
// environment first, then `gh auth token`. It is the shared resolver for every
// GitHub-touching caller (gc github CLI, velocity endpoint) so token
// precedence stays identical across them.
func ResolveToken(ctx context.Context) (string, error) {
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(key)); token != "" {
			return token, nil
		}
	}
	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("GitHub token not found in GITHUB_TOKEN/GH_TOKEN and `gh auth token` failed: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("GitHub token not found: `gh auth token` returned empty output")
	}
	return token, nil
}
