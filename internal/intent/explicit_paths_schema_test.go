package intent

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

// ADR-0016 U1: client_exposure gains an optional paths[] input (explicit path
// declarations: inbound_protocol + channel). served_inbound_protocols is
// deprecated as an input (still legal during the transition; paths wins when
// both are present).
func TestClientExposureSchemaExplicitPaths(t *testing.T) {
	schema := clientExposureSchema()
	properties := schema["properties"].(map[string]any)

	paths, ok := properties["paths"].(map[string]any)
	if !ok {
		t.Fatalf("client_exposure schema missing paths property")
	}
	if paths["type"] != "array" {
		t.Fatalf("paths must be an array, got %#v", paths["type"])
	}
	item := paths["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Fatalf("paths item must forbid additional properties: %#v", item)
	}
	if !reflect.DeepEqual(item["required"], []string{"inbound_protocol", "channel"}) {
		t.Fatalf("paths item required=%#v", item["required"])
	}
	itemProps := item["properties"].(map[string]any)
	inbound := itemProps["inbound_protocol"].(map[string]any)
	if !reflect.DeepEqual(inbound["enum"], []any{"anthropic", "gemini", "openai", "responses"}) {
		t.Fatalf("paths item inbound_protocol enum=%#v", inbound["enum"])
	}
	channel := itemProps["channel"].(map[string]any)
	if !reflect.DeepEqual(channel["enum"], []any{"passthrough", "responses_to_chat", "anthropic_to_chat"}) {
		t.Fatalf("paths item channel enum=%#v", channel["enum"])
	}

	// paths is optional (transition period: served-only intents stay valid).
	if containsString(schema["required"].([]string), "paths") {
		t.Fatalf("paths must be optional, required=%#v", schema["required"])
	}
	// served_inbound_protocols stays required (rollback safety during the
	// transition; producers keep emitting it until U4), but is marked deprecated.
	if !containsString(schema["required"].([]string), "served_inbound_protocols") {
		t.Fatalf("served_inbound_protocols must stay required during transition, required=%#v", schema["required"])
	}
	served := properties["served_inbound_protocols"].(map[string]any)
	if served["deprecated"] != true {
		t.Fatalf("served_inbound_protocols must be marked deprecated (ADR-0016): %#v", served)
	}
	if desc, _ := served["description"].(string); desc == "" {
		t.Fatalf("served_inbound_protocols deprecation needs a description explaining paths supersedes it")
	}
}

// The intent Go type must round-trip paths[] (strict decode, no unknown fields).
func TestClientExposureDecodesExplicitPaths(t *testing.T) {
	raw := []byte(`{
		"provider_id": "pc_x",
		"served_inbound_protocols": ["openai"],
		"paths": [
			{"inbound_protocol": "openai", "channel": "passthrough"},
			{"inbound_protocol": "responses", "channel": "responses_to_chat"}
		]
	}`)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var exposure ClientExposure
	if err := dec.Decode(&exposure); err != nil {
		t.Fatalf("decode client_exposure with paths: %v", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("trailing JSON: %v", err)
	}
	want := []types.ProtocolPath{
		{InboundProtocol: "openai", Channel: "passthrough"},
		{InboundProtocol: "responses", Channel: "responses_to_chat"},
	}
	if !reflect.DeepEqual(exposure.Paths, want) {
		t.Fatalf("paths=%#v want %#v", exposure.Paths, want)
	}
}
