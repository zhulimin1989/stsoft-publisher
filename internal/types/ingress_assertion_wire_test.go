package types_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

// G7 Phase B wire-shape compatibility with the gateway consumer.
//
// Gateway origin/test (Phase A, 4044847) defines the runtime shape
// model.GateAssertion{Pattern, Products, Mode} fed through
// Server.SetIngressAssertions(map[pattern]GateAssertion), with mode
// constants "warn" / "enforce" and absent/empty mode treated as warn.
// The gw:platform:config JSON contract for the field is design
// G7-product-context-admission.md §4:
//
//	"ingress_product_assertions": [
//	  {"pattern": "...", "allowed_products": ["..."], "mode": "warn"}
//	]
//
// These tests pin the publisher's serialized bytes to that contract.

// gatewayGateAssertionWire mirrors how the gateway-side configsync decoder
// will read one published entry (field names per design §4; semantics per
// gateway model.GateAssertion). Kept local on purpose: publisher must not
// import gateway code, the JSON tags ARE the contract.
type gatewayGateAssertionWire struct {
	Pattern         string   `json:"pattern"`
	AllowedProducts []string `json:"allowed_products"`
	Mode            string   `json:"mode"`
}

func TestIngressProductAssertionWireShape_MatchesDesignContract(t *testing.T) {
	assertions := []types.IngressProductAssertion{
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
	}
	got, err := json.Marshal(assertions)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `[` +
		`{"pattern":"POST /api/coding/paas/v4/chat/completions","allowed_products":["pc_glm_coding"],"mode":"warn"},` +
		`{"pattern":"POST /v1/messages","allowed_products":["pc_payg","pc_glm_coding","pc_kimi_coding"],"mode":"enforce"}` +
		`]`
	if string(got) != want {
		t.Fatalf("wire shape drift.\n got: %s\nwant: %s", got, want)
	}
}

func TestIngressProductAssertionWire_DecodesIntoGatewayShape(t *testing.T) {
	published := []types.IngressProductAssertion{
		{
			Pattern:         "POST /v1/messages",
			AllowedProducts: []string{"pc_payg", "pc_kimi_coding"},
			Mode:            types.IngressAssertionModeEnforce,
		},
		{
			Pattern:         "GET /v1/models",
			AllowedProducts: []string{"pc_payg"},
			// empty mode ⇒ field omitted on the wire ⇒ gateway reads ""
			// ⇒ warn (gateway GateAssertionModeWarn fallback).
		},
	}
	raw, err := json.Marshal(published)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded []gatewayGateAssertionWire
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		t.Fatalf("gateway-shaped strict decode failed — publisher is emitting fields the consumer contract does not know: %v", err)
	}

	want := []gatewayGateAssertionWire{
		{Pattern: "POST /v1/messages", AllowedProducts: []string{"pc_payg", "pc_kimi_coding"}, Mode: "enforce"},
		{Pattern: "GET /v1/models", AllowedProducts: []string{"pc_payg"}, Mode: ""},
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("round-trip mismatch.\n got: %#v\nwant: %#v", decoded, want)
	}
}

func TestIngressAssertionModeConstants_MatchGatewayValues(t *testing.T) {
	// Gateway origin/test internal/model/request_context.go:
	// GateAssertionModeWarn = "warn"; GateAssertionModeEnforce = "enforce".
	if types.IngressAssertionModeWarn != "warn" {
		t.Fatalf("warn constant = %q, gateway expects \"warn\"", types.IngressAssertionModeWarn)
	}
	if types.IngressAssertionModeEnforce != "enforce" {
		t.Fatalf("enforce constant = %q, gateway expects \"enforce\"", types.IngressAssertionModeEnforce)
	}
}

// Default laziness (equivalence regression lock): a PlatformConfig that
// does not configure assertions must serialize without the field at all —
// the published gw:platform:config payload stays byte-identical to the
// pre-G7 output.
func TestPlatformConfigWithoutIngressAssertions_OmitsField(t *testing.T) {
	for name, cfg := range map[string]*types.PlatformConfig{
		"nil slice":   {PlatformRPM: 1},
		"empty slice": {PlatformRPM: 1, IngressProductAssertions: []types.IngressProductAssertion{}},
	} {
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if strings.Contains(string(raw), "ingress_product_assertions") {
			t.Fatalf("%s: unconfigured assertions must not appear on the wire, got %s", name, raw)
		}
	}
}

func TestPlatformConfigIngressAssertions_RoundTrip(t *testing.T) {
	in := &types.PlatformConfig{
		PlatformRPM: 1,
		IngressProductAssertions: []types.IngressProductAssertion{
			{Pattern: "POST /v1/messages", AllowedProducts: []string{"pc_payg"}, Mode: "warn"},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out types.PlatformConfig
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in.IngressProductAssertions, out.IngressProductAssertions) {
		t.Fatalf("round trip mismatch: %#v vs %#v", in.IngressProductAssertions, out.IngressProductAssertions)
	}
}
