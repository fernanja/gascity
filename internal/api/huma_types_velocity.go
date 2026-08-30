package api

// VelocityInput is the Huma input for GET /v0/city/{cityName}/velocity.
type VelocityInput struct {
	CityScope
	WindowHours int `query:"window_hours" required:"false" minimum:"1" maximum:"168" doc:"Aggregation window in hours. Defaults to 72."`
}

// VelocityPRRepo is one repository's contribution to the PR velocity counts.
type VelocityPRRepo struct {
	Repo    string `json:"repo" doc:"owner/repo slug."`
	Merged  int    `json:"merged" doc:"Pull requests merged in the window."`
	Created int    `json:"created" doc:"Pull requests created in the window."`
}

// VelocityPRs is the GitHub-sourced slice of the velocity aggregate. It
// follows the UsageBody availability idiom: when Available is false the
// counts are absent facts, never zeroed aggregates masquerading as real data.
type VelocityPRs struct {
	Available         bool             `json:"available" doc:"True when PR counts were resolved from GitHub (possibly from a recent cache)."`
	Merged            int              `json:"merged" doc:"Pull requests merged across all configured repositories in the window."`
	Created           int              `json:"created" doc:"Pull requests created across all configured repositories in the window."`
	Repos             []VelocityPRRepo `json:"repos,omitempty" doc:"Per-repository breakdown."`
	FetchedAt         string           `json:"fetched_at,omitempty" doc:"RFC3339 time the GitHub counts were fetched."`
	Stale             bool             `json:"stale,omitempty" doc:"True when a refresh failed and these counts are a retained last-good reading."`
	UnavailableReason string           `json:"unavailable_reason,omitempty" doc:"Why PR counts are unavailable, when Available is false."`
}

// VelocityGroupCount is one group's completion count within the window.
type VelocityGroupCount struct {
	Group  string `json:"group" doc:"Group key (formula name, issue type, or gc.kind)."`
	Closed int    `json:"closed" doc:"Beads closed in the window for this group."`
}

// VelocityFormulaRuns is one formula's completed workflow runs in the window.
type VelocityFormulaRuns struct {
	Formula   string `json:"formula" doc:"Formula name."`
	Completed int    `json:"completed" doc:"Workflow roots closed in the window."`
	Passed    int    `json:"passed" doc:"Runs whose gc.outcome is pass."`
	Failed    int    `json:"failed" doc:"Runs whose gc.outcome is fail."`
	Skipped   int    `json:"skipped" doc:"Runs whose gc.outcome is skipped."`
}

// VelocityRuns aggregates completed workflow runs over the window.
type VelocityRuns struct {
	Completed      int                   `json:"completed" doc:"Workflow roots closed in the window across all stores."`
	ByFormula      []VelocityFormulaRuns `json:"by_formula,omitempty" doc:"Per-formula breakdown, largest first, capped with an 'other' rollup."`
	Series         []int                 `json:"series,omitempty" doc:"Hourly completion counts across the window, oldest hour first; length equals window_hours."`
	Partial        bool                  `json:"partial,omitempty" doc:"True when at least one bead store could not be read."`
	PartialReasons []string              `json:"partial_reasons,omitempty" doc:"Stores or reads that were skipped."`
}

// VelocityBeads aggregates bead completions over the window.
type VelocityBeads struct {
	Closed         int                  `json:"closed" doc:"Beads closed in the window across all stores (sessions and wisps excluded)."`
	ByType         []VelocityGroupCount `json:"by_type,omitempty" doc:"Completions grouped by issue type."`
	ByKind         []VelocityGroupCount `json:"by_kind,omitempty" doc:"Completions grouped by gc.kind; beads with no kind report as '(work)'."`
	Series         []int                `json:"series,omitempty" doc:"Hourly closure counts across the window (same exclusions as Closed), oldest hour first; length equals window_hours."`
	WispsClosed    int                  `json:"wisps_closed" doc:"Wisp-tier (ephemeral/no-history) closures in the window, reported separately from Closed."`
	Partial        bool                 `json:"partial,omitempty" doc:"True when at least one bead store could not be read."`
	PartialReasons []string             `json:"partial_reasons,omitempty" doc:"Stores or reads that were skipped."`
}

// VelocityBody is the windowed throughput aggregate returned by
// GET /v0/city/{cityName}/velocity.
type VelocityBody struct {
	WindowHours int           `json:"window_hours" doc:"Aggregation window in hours."`
	Since       string        `json:"since" doc:"RFC3339 start of the window (server clock)."`
	GeneratedAt string        `json:"generated_at" doc:"RFC3339 time the aggregate was built."`
	PRs         VelocityPRs   `json:"prs" doc:"GitHub pull-request counts."`
	Runs        VelocityRuns  `json:"runs" doc:"Completed formula runs."`
	Beads       VelocityBeads `json:"beads" doc:"Bead completions."`
}

// VelocityOutput is the Huma output envelope for GET /v0/city/{cityName}/velocity.
type VelocityOutput struct {
	Body VelocityBody
}
