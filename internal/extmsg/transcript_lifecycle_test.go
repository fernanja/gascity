package extmsg

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestTranscriptLifecycleClosesDeliveredAndAnswered pins the gc-p08bi
// lifecycle: an open transcript bead means pending work. An outbound entry
// closes as delivered at record time (recording happens at the provider-ack
// point), and its delivery closes every prior still-open inbound as answered.
// An inbound arriving AFTER the reply stays open — it is genuinely
// unanswered — and the transcript reader still returns every entry in order
// regardless of bead status.
func TestTranscriptLifecycleClosesDeliveredAndAnswered(t *testing.T) {
	freezeTestClock(t)
	store := beads.NewMemStore()
	svc := NewServices(store).Transcript
	ref := testConversationRef()

	appendEntry := func(kind TranscriptMessageKind, msgID, text string, at time.Time) ConversationTranscriptRecord {
		t.Helper()
		rec, err := svc.Append(context.Background(), AppendTranscriptInput{
			Caller:            testAdapterCaller(),
			Conversation:      ref,
			Kind:              kind,
			Provenance:        TranscriptProvenanceLive,
			ProviderMessageID: msgID,
			Text:              text,
			CreatedAt:         at,
		})
		if err != nil {
			t.Fatalf("Append(%s %s): %v", kind, msgID, err)
		}
		return rec
	}
	beadStatus := func(id string) (status, reason, answeredBy string) {
		t.Helper()
		b, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		return b.Status, b.Metadata["closed_reason"], b.Metadata["answered_by"]
	}

	in1 := appendEntry(TranscriptMessageInbound, "m-1", "are you there?", testNow())
	in2 := appendEntry(TranscriptMessageInbound, "m-2", "hello??", testNow().Add(time.Minute))
	reply := appendEntry(TranscriptMessageOutbound, "m-3", "here — answering both", testNow().Add(2*time.Minute))
	late := appendEntry(TranscriptMessageInbound, "m-4", "one more question", testNow().Add(3*time.Minute))

	if st, reason, _ := beadStatus(reply.ID); st != "closed" || reason != "delivered" {
		t.Fatalf("outbound = status %q reason %q, want closed/delivered", st, reason)
	}
	for _, in := range []ConversationTranscriptRecord{in1, in2} {
		st, reason, by := beadStatus(in.ID)
		if st != "closed" || reason != "answered" || by != reply.ID {
			t.Fatalf("inbound %s = status %q reason %q answered_by %q, want closed/answered/%s",
				in.ID, st, reason, by, reply.ID)
		}
	}
	if st, _, _ := beadStatus(late.ID); st != "closed" {
		// The post-reply inbound must remain open: it is unanswered work.
		if st != "open" {
			t.Fatalf("late inbound = status %q, want open", st)
		}
	} else {
		t.Fatalf("late inbound closed — an unanswered message must stay open")
	}

	// The reader is status-agnostic: history is intact and ordered.
	records, err := svc.List(context.Background(), ListTranscriptInput{
		Caller:       testControllerCaller(),
		Conversation: ref,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("List returned %d records, want 4 (closed entries must remain readable)", len(records))
	}
	for i, want := range []string{in1.ID, in2.ID, reply.ID, late.ID} {
		if records[i].ID != want {
			t.Fatalf("List[%d] = %s, want %s", i, records[i].ID, want)
		}
	}
}
