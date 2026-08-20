package tmux

import (
	"errors"
	"testing"
	"time"
)

// noSleep is a sleep stub so the confirm loop runs instantly under test.
func noSleep(time.Duration) {}

// TestSubmitEnterAndConfirmReEntersWhileIdle proves the ga-bwm fix: when the
// first Enter is lost (the pane stays idle with the message still drafted), the
// loop re-sends Enter, and the send that lands drives the agent busy.
func TestSubmitEnterAndConfirmReEntersWhileIdle(t *testing.T) {
	var enters int
	// Busy only becomes true once a second Enter has been sent, i.e. the first
	// Enter raced the paste and was dropped.
	busy := func() (bool, error) { return enters >= 2, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, nil, false, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true (re-sent Enter should submit)")
	}
	if enters != 2 {
		t.Fatalf("enters = %d, want 2 (initial + one re-send)", enters)
	}
}

// TestSubmitEnterAndConfirmStopsWhenBusy proves the common case: a single Enter
// that submits is confirmed on the first poll with no wasted re-send.
func TestSubmitEnterAndConfirmStopsWhenBusy(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return enters >= 1, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, nil, false, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true")
	}
	if enters != 1 {
		t.Fatalf("enters = %d, want 1 (no re-send once submitted)", enters)
	}
}

// TestSubmitEnterAndConfirmNoDoubleSubmitOnFastTurn proves the safety property:
// if a turn goes busy after the first send's polls but before a re-send, the
// pre-re-send busy check catches it and no second Enter is issued.
func TestSubmitEnterAndConfirmNoDoubleSubmitOnFastTurn(t *testing.T) {
	var enters int
	var busyCalls int
	busy := func() (bool, error) {
		busyCalls++
		// Idle for the first send's polls; busy at the pre-re-send check.
		return busyCalls > submitConfirmPollsPerSend, nil
	}
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, nil, false, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true")
	}
	if enters != 1 {
		t.Fatalf("enters = %d, want 1 (pre-re-send busy check must prevent double-submit)", enters)
	}
}

// TestSubmitEnterAndConfirmBestEffortWhenNeverBusy proves that a pane which
// never reports busy is delivered best-effort (bounded re-sends, no error) so
// the caller's contract (nil == delivered to tmux) is preserved.
func TestSubmitEnterAndConfirmBestEffortWhenNeverBusy(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return false, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, nil, false, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if confirmed {
		t.Fatal("confirmed = true, want false")
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d (bounded best-effort sends)", enters, submitEnterMaxSends)
	}
}

// TestSubmitEnterAndConfirmClearsStaleSendError proves a transient first-send
// failure followed by a successful send (busy never observed) is reported as
// best-effort delivery (false, nil), not a stale error — matching the
// historical "nil == handed to tmux" contract.
func TestSubmitEnterAndConfirmClearsStaleSendError(t *testing.T) {
	var enters int
	sendEnter := func() error {
		enters++
		if enters == 1 {
			return errors.New("transient: no server yet")
		}
		return nil
	}
	busy := func() (bool, error) { return false, nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, nil, false, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil (later send succeeded)", err)
	}
	if confirmed {
		t.Fatal("confirmed = true, want false (never busy)")
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d", enters, submitEnterMaxSends)
	}
}

// TestSubmitEnterAndConfirmReturnsSendError proves a genuine tmux-layer send
// failure (session gone) is surfaced, matching the pre-fix contract.
func TestSubmitEnterAndConfirmReturnsSendError(t *testing.T) {
	sendErr := errors.New("no server")
	var enters int
	sendEnter := func() error { enters++; return sendErr }
	busy := func() (bool, error) { return false, nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, nil, false, noSleep)
	if confirmed {
		t.Fatal("confirmed = true, want false")
	}
	if !errors.Is(err, sendErr) {
		t.Fatalf("err = %v, want sendErr chain", err)
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d", enters, submitEnterMaxSends)
	}
}

// TestSubmitEnterAndConfirmBusyPaneFalsePositive is the regression test for
// gc-6h603 (ra-oudpha "LIVE RESIDUAL"): a nudge sent into an ALREADY-busy pane
// must not confirm off the pre-existing busy indicator while the draft still
// sits in the composer. With a plain-Enter sequence (enterOnly), the loop
// re-sends Enter into the busy pane — claude queues the draft — and confirms
// only when the draft actually leaves the composer.
func TestSubmitEnterAndConfirmBusyPaneFalsePositive(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return true, nil } // busy BEFORE the nudge, stays busy
	draftGone := func() (bool, error) { return enters >= 2, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, draftGone, true, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true (re-sent Enter queues the draft in a busy pane)")
	}
	if enters != 2 {
		t.Fatalf("enters = %d, want 2 (first Enter swallowed by paste race, second lands)", enters)
	}
}

// TestSubmitEnterAndConfirmDraftGoneIsPrimary proves the draft signal confirms
// a submit even when the busy indicator never fires (fast turn, or a provider
// whose busy render lags).
func TestSubmitEnterAndConfirmDraftGoneIsPrimary(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return false, nil }
	draftGone := func() (bool, error) { return enters >= 1, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, draftGone, true, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true (draft left the composer)")
	}
	if enters != 1 {
		t.Fatalf("enters = %d, want 1", enters)
	}
}

// TestSubmitEnterAndConfirmDraftStuckSurfacesUnconfirmed proves a draft that
// never leaves the composer is reported unconfirmed (false, nil) after bounded
// sends — the caller surfaces ErrNudgeSubmitUnconfirmed and the nudge requeues
// instead of being acked as delivered.
func TestSubmitEnterAndConfirmDraftStuckSurfacesUnconfirmed(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return true, nil }
	draftGone := func() (bool, error) { return false, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, draftGone, true, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if confirmed {
		t.Fatal("confirmed = true, want false (draft never left the composer)")
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d", enters, submitEnterMaxSends)
	}
}

// TestSubmitEnterAndConfirmEscapeSequenceNeverResentIntoBusyPane proves the
// codex hazard guard: a multi-key (Escape-leading) sequence is not re-sent
// once the pane is busy; the draft state decides the verdict instead.
func TestSubmitEnterAndConfirmEscapeSequenceNeverResentIntoBusyPane(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return enters >= 1, nil } // busy after first send
	draftGone := func() (bool, error) { return enters >= 1, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, draftGone, false, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true")
	}
	if enters != 1 {
		t.Fatalf("enters = %d, want 1 (Escape-leading sequence must not repeat into a busy pane)", enters)
	}
}

// TestDraftSnippetAndComposerLines pins the composer-observation helpers.
func TestDraftSnippetAndComposerLines(t *testing.T) {
	if got := draftSnippet("check the redis upgrade decision"); got != "check the redis upgrade decision" {
		t.Fatalf("draftSnippet = %q", got)
	}
	long := "0123456789012345678901234567890123456789"
	if got := draftSnippet(long); len([]rune(got)) != 32 {
		t.Fatalf("draftSnippet long = %q (len %d), want 32 runes", got, len([]rune(got)))
	}
	if got := draftSnippet("\n\n  second line is first non-empty  \nrest"); got != "second line is first non-empty" {
		t.Fatalf("draftSnippet multiline = %q", got)
	}
	lines := []string{
		"⏺ Working on it",
		"────────────",
		"❯ check the redis upgrade decision", // composer draft (last marker line)
		"  ⏵⏵ bypass permissions on",
	}
	if !composerLinesHoldDraft(lines, "check the redis upgrade decision") {
		t.Fatal("composerLinesHoldDraft = false, want true (draft on last ❯ line)")
	}
	// Live-verified rendering: a submitted message echoes into the transcript
	// with the SAME ❯ marker, above the now-empty composer. Only the LAST
	// marker line may decide, or a fresh echo reads as a stuck draft.
	submitted := []string{
		"❯ check the redis upgrade decision", // transcript echo of the submit
		"  handle after this turn",
		"────────────",
		"❯ ", // empty composer
		"  ⏵⏵ bypass permissions on",
	}
	if composerLinesHoldDraft(submitted, "check the redis upgrade decision") {
		t.Fatal("composerLinesHoldDraft = true, want false (transcript ❯ echo must not count)")
	}
}
