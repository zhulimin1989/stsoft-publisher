package publisher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

func TestValidateProviderInfo_AcceptsAnthropicFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "types", "testdata", "providerinfo_protocol_bindings_only_anthropic.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var info types.ProviderInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if err := validateProviderInfo(&info); err != nil {
		t.Fatalf("anthropic fixture must pass provider validation: %v", err)
	}
}
