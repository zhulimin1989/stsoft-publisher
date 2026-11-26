package types

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderInfoAnthropicFixtureUsesNativeBinding(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "providerinfo_protocol_bindings_only_anthropic.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var info ProviderInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if info.DefaultProtocolBinding != "anthropic-messages" {
		t.Fatalf("default binding=%q want anthropic-messages", info.DefaultProtocolBinding)
	}
	if len(info.ProtocolBindings) != 1 {
		t.Fatalf("binding count=%d want 1", len(info.ProtocolBindings))
	}
	binding := info.ProtocolBindings[0]
	if binding.Name == "openai-chat-to-anthropic" {
		t.Fatal("fixture must not use legacy cross-protocol openai-chat-to-anthropic binding")
	}
	if binding.Name != "anthropic-messages" ||
		binding.OutboundProtocol != "anthropic" ||
		binding.TranslatorKey != "passthrough" ||
		binding.Bridge != "" {
		t.Fatalf("binding mismatch: %+v", binding)
	}
	if len(binding.Accepts) != 1 {
		t.Fatalf("accept count=%d want 1", len(binding.Accepts))
	}
	accept := binding.Accepts[0]
	if accept.Protocol != "anthropic" || accept.RequestContract != "anthropic_messages" {
		t.Fatalf("accept mismatch: %+v", accept)
	}
}
