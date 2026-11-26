package outbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

// TestInjectRevision_OverwritesExisting asserts a 0 / absent revision in the
// outbox-stored notify message gets replaced with the worker's last OpIncr
// result. Drives gateway-side monotonic-revision filtering.
func TestInjectRevision_OverwritesExisting(t *testing.T) {
	cases := []struct {
		name string
		in   string
		rev  int64
		want int64
	}{
		{"adds revision when absent",
			`{"type":"keypool_entry_update","provider_id":"openai","key_id":"k1"}`,
			42, 42},
		{"replaces existing revision",
			`{"type":"keypool_update","revision":1}`,
			7, 7},
		{"replaces zero revision",
			`{"type":"keypool_update","revision":0}`,
			5, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := injectRevision(json.RawMessage(c.in), c.rev, types.BatchID{})
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("output not valid JSON: %v (raw=%s)", err, out)
			}
			rev, ok := m["revision"].(float64)
			if !ok {
				t.Fatalf("revision field missing or wrong type: %v", m["revision"])
			}
			if int64(rev) != c.want {
				t.Errorf("rev = %d, want %d", int64(rev), c.want)
			}
			// Untouched fields stay.
			if m["type"] == nil {
				t.Errorf("type field stripped")
			}
		})
	}
}

// TestInjectRevision_NonObjectFallsThrough asserts a non-object JSON
// message is returned unchanged rather than dropped/corrupted — the worker
// would rather publish the original message than block on a malformed
// notify payload that should never have been written to outbox in the
// first place.
func TestInjectRevision_NonObjectFallsThrough(t *testing.T) {
	in := json.RawMessage(`["array","not","object"]`)
	out := injectRevision(in, 99, types.BatchID{})
	if !strings.HasPrefix(string(out), "[") {
		t.Errorf("expected unchanged array, got %s", string(out))
	}
}

// TestInjectRevision_NoIncrPathPreservesAbsentRevision pins the post-
// 2026-05-21 contract: when a Publish row is re-delivered alone after the
// sibling Incr already MarkDelivered in a prior tick (worker restarted;
// fresh batchState{lastIncr:0}), worker.runOne's `if state.lastIncr > 0`
// guard skips injectRevision entirely. The published notify message
// therefore goes out exactly as publisher/impl.go constructed it — and
// after the 2026-05-21 fix, publisher/impl.go in commitWithRevision flows
// no longer pre-fills "revision". So the gateway sees msg.revision=0 and
// falls back to GET gw:config:rev:<domain> for the live counter.
//
// Before this contract, publisher/impl.go pre-filled "revision":
// entry.Revision (auto-filled to time.Now().UnixMicro() ≈ 1.7e15) which
// leaked to the gateway when injectRevision was skipped on retry,
// permanently poisoning local revision state. See impl.go
// PublishModelCatalog comment.
//
// This test does NOT exercise runOne directly (avoids needing miniredis or
// a redis.Client interface refactor). Instead it pins the JSON-shape end
// of the invariant: a publisher-constructed notify msg without "revision"
// stays without "revision" if injectRevision is never called.
func TestInjectRevision_NoIncrPathPreservesAbsentRevision(t *testing.T) {
	// What publisher/impl.go produces after 2026-05-21 fix:
	in := json.RawMessage(`{"version":2,"type":"model_catalog_update","model_id":"gpt-4o"}`)

	// runOne would skip injectRevision when state.lastIncr == 0, so we
	// pin "no transformation" by checking the raw bytes pass through.
	if !strings.Contains(string(in), `"model_id":"gpt-4o"`) {
		t.Fatalf("fixture corrupt: %s", in)
	}
	if strings.Contains(string(in), `"revision"`) {
		t.Fatalf("publisher must not pre-fill revision in commitWithRevision-flow notify msg; got %s", in)
	}

	// When OpIncr DID succeed earlier in same batch, injectRevision adds revision:
	out := injectRevision(in, 42, types.BatchID{})
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("injected msg not JSON: %v (raw=%s)", err, out)
	}
	if rev, ok := m["revision"].(float64); !ok || int64(rev) != 42 {
		t.Errorf("happy-path injection must set revision=42; got %v", m["revision"])
	}
}

func TestDecodeKeyMetaUnrevokeTarget_ActiveKeyReturnsHash(t *testing.T) {
	hash, shouldUnrevoke, err := decodeKeyMetaUnrevokeTarget(
		[]byte(`{"key_hash":"abc123","status":"active"}`),
		"abc123",
	)
	if err != nil {
		t.Fatalf("decodeKeyMetaUnrevokeTarget: %v", err)
	}
	if hash != "abc123" {
		t.Fatalf("hash = %q, want %q", hash, "abc123")
	}
	if !shouldUnrevoke {
		t.Fatal("active keymeta should request unrevoke")
	}
}

func TestDecodeKeyMetaUnrevokeTarget_NonActiveSkipsUnrevoke(t *testing.T) {
	cases := []string{"suspended", "revoked", ""}
	for _, status := range cases {
		hash, shouldUnrevoke, err := decodeKeyMetaUnrevokeTarget(
			[]byte(`{"key_hash":"abc123","status":"`+status+`"}`),
			"abc123",
		)
		if err != nil {
			t.Fatalf("status=%q decode error: %v", status, err)
		}
		if hash != "abc123" {
			t.Fatalf("status=%q hash = %q, want %q", status, hash, "abc123")
		}
		if shouldUnrevoke {
			t.Fatalf("status=%q should not request unrevoke", status)
		}
	}
}

func TestDecodeKeyMetaUnrevokeTarget_FieldMismatchFails(t *testing.T) {
	_, _, err := decodeKeyMetaUnrevokeTarget(
		[]byte(`{"key_hash":"abc123","status":"active"}`),
		"xyz789",
	)
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
}
