package publisher

import (
	"context"
	"errors"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

// G7 Phase B (design §4): publisher-side validation of the
// ingress_product_assertions PlatformConfig field is STRUCTURAL ONLY.
// The assertion layer is fail-open by construction (unlisted gate =
// unasserted = pass), so the publisher must not do business-level
// gatekeeping here — no pattern whitelist, no product_id reference check.
// It only rejects entries that carry no usable information.

func validAssertions() []types.IngressProductAssertion {
	return []types.IngressProductAssertion{
		{
			Pattern:         "POST /api/coding/paas/v4/chat/completions",
			AllowedProducts: []string{"pc_glm_coding"},
			Mode:            types.IngressAssertionModeWarn,
		},
		{
			Pattern:         "POST /v1/messages",
			AllowedProducts: []string{"pc_payg", "pc_glm_coding", "pc_kimi_coding"},
			Mode:            types.IngressAssertionModeEnforce,
		},
		{
			Pattern:         "GET /v1/models",
			AllowedProducts: []string{"pc_payg"},
			// Mode empty == warn (fail-open default; gateway treats
			// absent mode as warn).
		},
	}
}

func TestValidateIngressProductAssertions_NilAndEmptyPass(t *testing.T) {
	if err := validateIngressProductAssertions(nil); err != nil {
		t.Fatalf("nil assertions (feature unused) must pass, got %v", err)
	}
	if err := validateIngressProductAssertions([]types.IngressProductAssertion{}); err != nil {
		t.Fatalf("empty assertions must pass, got %v", err)
	}
}

func TestValidateIngressProductAssertions_ValidEntriesPass(t *testing.T) {
	if err := validateIngressProductAssertions(validAssertions()); err != nil {
		t.Fatalf("valid warn/enforce/empty-mode entries must pass, got %v", err)
	}
}

func TestValidateIngressProductAssertions_RejectsBlankPattern(t *testing.T) {
	for _, pattern := range []string{"", "   "} {
		a := []types.IngressProductAssertion{{
			Pattern:         pattern,
			AllowedProducts: []string{"pc_payg"},
		}}
		err := validateIngressProductAssertions(a)
		if err == nil {
			t.Fatalf("pattern %q should be rejected", pattern)
		}
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("want ErrInvalidArgument, got %v", err)
		}
	}
}

func TestValidateIngressProductAssertions_RejectsEmptyProducts(t *testing.T) {
	for _, products := range [][]string{nil, {}} {
		a := []types.IngressProductAssertion{{
			Pattern:         "POST /v1/messages",
			AllowedProducts: products,
		}}
		err := validateIngressProductAssertions(a)
		if err == nil {
			t.Fatalf("allowed_products=%v should be rejected (an assertion with no products carries no information)", products)
		}
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("want ErrInvalidArgument, got %v", err)
		}
	}
}

func TestValidateIngressProductAssertions_RejectsBlankProductEntry(t *testing.T) {
	a := []types.IngressProductAssertion{{
		Pattern:         "POST /v1/messages",
		AllowedProducts: []string{"pc_payg", "  "},
	}}
	if err := validateIngressProductAssertions(a); err == nil {
		t.Fatal("blank product id entry should be rejected")
	}
}

func TestValidateIngressProductAssertions_RejectsUnknownMode(t *testing.T) {
	// Publisher rejects unknown modes at the input boundary (a typo like
	// "enforece" silently degrading to warn would be a silent failure).
	// Gateway's absent/unknown→warn fallback still covers version skew
	// and admin-direct Redis writes — both sides stay fail-open.
	for _, mode := range []string{"shadow", "WARN", "Enforce", "off"} {
		a := []types.IngressProductAssertion{{
			Pattern:         "POST /v1/messages",
			AllowedProducts: []string{"pc_payg"},
			Mode:            mode,
		}}
		err := validateIngressProductAssertions(a)
		if err == nil {
			t.Fatalf("mode %q should be rejected", mode)
		}
		if !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("want ErrInvalidArgument, got %v", err)
		}
	}
}

func TestValidateIngressProductAssertions_RejectsDuplicatePattern(t *testing.T) {
	// The wire contract is a set keyed by pattern (gateway compiles it
	// into map[pattern]assertion); two entries claiming the same gate
	// would make the publish ambiguous (map last-wins).
	a := []types.IngressProductAssertion{
		{Pattern: "POST /v1/messages", AllowedProducts: []string{"pc_payg"}},
		{Pattern: "POST /v1/messages", AllowedProducts: []string{"pc_glm_coding"}},
	}
	if err := validateIngressProductAssertions(a); err == nil {
		t.Fatal("duplicate pattern should be rejected")
	}
}

func TestPublishPlatformConfig_RejectsInvalidIngressAssertions(t *testing.T) {
	// store=nil is safe: validation must fail before any commit attempt.
	p := &Impl{}
	cfg := &types.PlatformConfig{
		IngressProductAssertions: []types.IngressProductAssertion{
			{Pattern: "", AllowedProducts: []string{"pc_payg"}},
		},
	}
	_, err := p.PublishPlatformConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("PublishPlatformConfig must reject structurally invalid ingress assertions")
	}
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("want ErrInvalidArgument, got %v", err)
	}
}
