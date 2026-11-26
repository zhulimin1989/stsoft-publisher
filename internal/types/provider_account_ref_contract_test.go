package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateProviderAccountRef(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "omitted legacy", value: ""},
		{name: "registry slug", value: "provider-account-glm-main"},
		{name: "billing fixture style", value: "overseas-prod-1"},
		{name: "UUID remains opaque", value: "550e8400-e29b-41d4-a716-446655440000"},
		{name: "mixed case and separators", value: "Provider.Account:GLM_01"},
		{name: "leading whitespace", value: " pa_550e8400-e29b-41d4-a716-446655440000", wantErr: true},
		{name: "embedded whitespace", value: "pa_550e8400-e29b-41d4-a716-4466 54440000", wantErr: true},
		{name: "trailing whitespace", value: "pa_550e8400-e29b-41d4-a716-446655440000\n", wantErr: true},
		{name: "leading punctuation", value: "_provider-account", wantErr: true},
		{name: "slash", value: "provider/account", wantErr: true},
		{name: "base url", value: "https://api.example.com", wantErr: true},
		{name: "secret ref", value: "vault://providers/zhipu/key-a", wantErr: true},
		{name: "domain-like opaque id", value: "api.example.com"},
		{name: "max length", value: strings.Repeat("a", ProviderAccountRefMaxLength)},
		{name: "too long", value: strings.Repeat("a", ProviderAccountRefMaxLength+1), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProviderAccountRef(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateProviderAccountRef(%q) error=%v wantErr=%v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestKeyPoolEntryProviderAccountRefJSONRoundTrip(t *testing.T) {
	want := KeyPoolEntry{
		SchemaVersion:      1,
		Revision:           1700000000000000,
		KeyID:              "key-zhipu-primary",
		ProviderAccountRef: "provider-account-glm-main",
		Ciphertext:         "vault://providers/zhipu/key-zhipu-primary",
		Priority:           1,
		Weight:             100,
		RPMLimit:           500,
		TPMLimit:           100000,
		Status:             "enabled",
	}

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal KeyPoolEntry: %v", err)
	}
	if !strings.Contains(string(raw), `"provider_account_ref":"provider-account-glm-main"`) {
		t.Fatalf("provider_account_ref missing from wire payload: %s", raw)
	}

	var got KeyPoolEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal KeyPoolEntry: %v", err)
	}
	if got.ProviderAccountRef != want.ProviderAccountRef {
		t.Fatalf("provider_account_ref=%q want %q", got.ProviderAccountRef, want.ProviderAccountRef)
	}
	if got.ProviderAccountRef == got.KeyID || got.ProviderAccountRef == got.Ciphertext {
		t.Fatalf("provider_account_ref must be independent of credential identity/material: %+v", got)
	}
}

func TestKeyPoolEntryLegacyJSONWithoutProviderAccountRefRemainsCompatible(t *testing.T) {
	legacy := []byte(`{
		"schema_version":1,
		"revision":1700000000000000,
		"key_id":"legacy-key",
		"ciphertext":"vault://providers/zhipu/legacy-key",
		"priority":1,
		"weight":100,
		"rpm_limit":500,
		"tpm_limit":100000,
		"status":"enabled"
	}`)

	var entry KeyPoolEntry
	if err := json.Unmarshal(legacy, &entry); err != nil {
		t.Fatalf("legacy KeyPoolEntry must remain decodable: %v", err)
	}
	if entry.ProviderAccountRef != "" {
		t.Fatalf("legacy provider_account_ref=%q want empty", entry.ProviderAccountRef)
	}

	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal legacy KeyPoolEntry: %v", err)
	}
	if strings.Contains(string(raw), "provider_account_ref") {
		t.Fatalf("optional provider_account_ref must stay omitted for legacy entries: %s", raw)
	}
}
