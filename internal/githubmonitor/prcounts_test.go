package githubmonitor

import (
	"strings"
	"testing"
)

func TestDecodePRCounts(t *testing.T) {
	body := `{"data":{"merged":{"issueCount":46},"created":{"issueCount":52}}}`
	counts, err := DecodePRCounts(strings.NewReader(body))
	if err != nil {
		t.Fatalf("DecodePRCounts() error = %v", err)
	}
	if counts.Merged != 46 || counts.Created != 52 {
		t.Fatalf("DecodePRCounts() = %+v, want {Merged:46 Created:52}", counts)
	}
}

func TestDecodePRCountsSurfacesGraphQLErrors(t *testing.T) {
	body := `{"data":{"merged":{"issueCount":0},"created":{"issueCount":0}},` +
		`"errors":[{"message":"API rate limit exceeded"}]}`
	_, err := DecodePRCounts(strings.NewReader(body))
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("DecodePRCounts() error = %v, want rate-limit GraphQL error", err)
	}
}

func TestDecodePRCountsRejectsMalformedJSON(t *testing.T) {
	if _, err := DecodePRCounts(strings.NewReader("not-json")); err == nil {
		t.Fatal("DecodePRCounts() error = nil, want decode error")
	}
}
