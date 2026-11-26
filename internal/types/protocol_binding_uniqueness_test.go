package types

import (
	"strings"
	"testing"
)

func TestValidateBindingUniquenessAllowsChatAndEmbeddings(t *testing.T) {
	bindings := []ProtocolBinding{
		{
			Name: "openai-chat",
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
			}},
		},
		{
			Name: "openai-embeddings",
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"embeddings"},
				RequestContract: "embeddings",
			}},
		},
	}

	if err := validateBindingUniqueness(bindings); err != nil {
		t.Fatalf("validateBindingUniqueness: %v", err)
	}
}

func TestValidateBindingUniquenessRejectsDuplicateCompleteKey(t *testing.T) {
	bindings := []ProtocolBinding{
		{
			Name:     "openai-chat-low-priority",
			Priority: 1,
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
			}},
		},
		{
			Name:     "openai-chat-high-priority",
			Priority: 100,
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
			}},
		},
	}

	err := validateBindingUniqueness(bindings)
	if err == nil {
		t.Fatal("validateBindingUniqueness succeeded, want duplicate key error")
	}
	msg := err.Error()
	for _, want := range []string{
		"protocol=openai",
		"capability=chat",
		"request_contract=chat_completions",
		"openai-chat-low-priority",
		"openai-chat-high-priority",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

func TestValidateBindingUniquenessNormalizesModelOrder(t *testing.T) {
	bindings := []ProtocolBinding{
		{
			Name: "openai-chat-a",
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
				Models:          []string{"gpt-4.1", "gpt-4o"},
			}},
		},
		{
			Name: "openai-chat-b",
			Accepts: []ProtocolBindingAccept{{
				Protocol:        "openai",
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
				Models:          []string{"gpt-4o", "gpt-4.1"},
			}},
		},
	}

	err := validateBindingUniqueness(bindings)
	if err == nil {
		t.Fatal("validateBindingUniqueness succeeded, want duplicate sorted-model key error")
	}
	if !strings.Contains(err.Error(), "models=[gpt-4.1,gpt-4o]") {
		t.Fatalf("error %q missing sorted models", err)
	}
}
