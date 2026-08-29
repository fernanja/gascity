package githubmonitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PRCounts is the number of pull requests merged and created in a repository
// since a point in time, from GitHub's search API.
type PRCounts struct {
	Merged  int
	Created int
}

// CountPullRequests returns how many PRs were merged and created in
// owner/repo at or after since. One GraphQL request per call: two aliased
// search queries, each answered by GitHub's exact issueCount — no paging.
func (c *GraphQLClient) CountPullRequests(ctx context.Context, owner, repo string, since time.Time) (PRCounts, error) {
	if c == nil {
		return PRCounts{}, fmt.Errorf("GitHub client is nil")
	}
	if c.token == "" {
		return PRCounts{}, fmt.Errorf("GitHub token is required")
	}
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return PRCounts{}, fmt.Errorf("GitHub owner and repo are required")
	}

	stamp := since.UTC().Format(time.RFC3339)
	payload := prCountsRequest{
		Query: prCountsQuery,
		Variables: prCountsQueryVariables{
			MergedQuery:  fmt.Sprintf("repo:%s/%s is:pr merged:>=%s", owner, repo, stamp),
			CreatedQuery: fmt.Sprintf("repo:%s/%s is:pr created:>=%s", owner, repo, stamp),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return PRCounts{}, fmt.Errorf("encoding GitHub GraphQL request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return PRCounts{}, fmt.Errorf("creating GitHub GraphQL request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return PRCounts{}, fmt.Errorf("calling GitHub GraphQL: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body close is best-effort

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return PRCounts{}, fmt.Errorf("GitHub GraphQL HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return DecodePRCounts(resp.Body)
}

// DecodePRCounts decodes the aliased two-search response body. Exported for
// canned-JSON decode tests, mirroring DecodePullRequestsPage.
func DecodePRCounts(r io.Reader) (PRCounts, error) {
	var envelope struct {
		Data struct {
			Merged struct {
				IssueCount int `json:"issueCount"`
			} `json:"merged"`
			Created struct {
				IssueCount int `json:"issueCount"`
			} `json:"created"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(r).Decode(&envelope); err != nil {
		return PRCounts{}, fmt.Errorf("decoding GitHub GraphQL response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			if msg := strings.TrimSpace(e.Message); msg != "" {
				messages = append(messages, msg)
			}
		}
		return PRCounts{}, fmt.Errorf("GitHub GraphQL errors: %s", strings.Join(messages, "; "))
	}
	return PRCounts{
		Merged:  envelope.Data.Merged.IssueCount,
		Created: envelope.Data.Created.IssueCount,
	}, nil
}

type prCountsRequest struct {
	Query     string                 `json:"query"`
	Variables prCountsQueryVariables `json:"variables"`
}

type prCountsQueryVariables struct {
	MergedQuery  string `json:"mergedQuery"`
	CreatedQuery string `json:"createdQuery"`
}

const prCountsQuery = `
query GasCityPRVelocity($mergedQuery: String!, $createdQuery: String!) {
  merged: search(type: ISSUE, query: $mergedQuery) {
    issueCount
  }
  created: search(type: ISSUE, query: $createdQuery) {
    issueCount
  }
}
`
