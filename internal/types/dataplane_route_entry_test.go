package types

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRouteEntryCapabilitiesWire(t *testing.T) {
	entry := RouteEntry{
		ProviderID:    "openai",
		UpstreamModel: "gpt-4o-2024-08-06",
		Status:        "active",
		Capabilities:  []string{"chat", "tools"},
	}

	body, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"capabilities":["chat","tools"]`) {
		t.Fatalf("capabilities field missing from wire payload: %s", body)
	}

	var out RouteEntry
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out.Capabilities, entry.Capabilities) {
		t.Fatalf("capabilities round-trip mismatch: got %+v want %+v", out.Capabilities, entry.Capabilities)
	}
}

func TestRouteEntryCapabilitiesOmitEmpty(t *testing.T) {
	body, err := json.Marshal(RouteEntry{
		ProviderID:    "openai",
		UpstreamModel: "gpt-4o",
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), `"capabilities"`) {
		t.Fatalf("empty capabilities should be omitted: %s", body)
	}
}
