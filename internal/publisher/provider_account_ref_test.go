package publisher

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/outbox"
	"github.com/tokenplatform/publisher/internal/types"
)

const testProviderAccountRef = "provider-account-glm-main"

func TestBuildPublishKeyPoolRowsPreservesProviderAccountRefInSnapshot(t *testing.T) {
	pool := &types.KeyPoolConfig{
		Meta:    types.KeyPoolMeta{ProviderID: "zhipu", Strategy: "priority"},
		Entries: []types.KeyPoolEntry{validProviderAccountEntry("key-primary")},
	}

	rows, err := buildPublishKeyPoolRows("zhipu", pool)
	if err != nil {
		t.Fatalf("buildPublishKeyPoolRows: %v", err)
	}
	assertProviderAccountRefInKeyPoolHSet(t, rows, "zhipu", "key-primary")
}

func TestBuildPublishKeyPoolEntryRowsPreservesProviderAccountRef(t *testing.T) {
	entry := validProviderAccountEntry("key-primary")
	rows, err := buildPublishKeyPoolEntryRows("zhipu", &entry)
	if err != nil {
		t.Fatalf("buildPublishKeyPoolEntryRows: %v", err)
	}
	assertProviderAccountRefInKeyPoolHSet(t, rows, "zhipu", "key-primary")
}

func TestBuildPublishKeyPoolEntryBulkRowsPreservesProviderAccountRefPerEntry(t *testing.T) {
	first := validProviderAccountEntry("key-primary")
	second := validProviderAccountEntry("key-secondary")
	second.ProviderAccountRef = "overseas-prod-1"

	rows, err := buildPublishKeyPoolEntryBulkRows([]types.KeyPoolEntryItem{
		{ProviderID: "zhipu", Entry: &first},
		{ProviderID: "zhipu", Entry: &second},
	})
	if err != nil {
		t.Fatalf("buildPublishKeyPoolEntryBulkRows: %v", err)
	}

	want := map[string]string{
		"key-primary":   testProviderAccountRef,
		"key-secondary": "overseas-prod-1",
	}
	for _, row := range rows {
		if row.Op != outbox.OpHSet || row.RedisKey != KeyPoolKey("zhipu") {
			continue
		}
		var entry types.KeyPoolEntry
		if err := json.Unmarshal(row.Payload, &entry); err != nil {
			t.Fatalf("decode keypool payload: %v payload=%s", err, row.Payload)
		}
		if got, ok := want[row.RedisField]; !ok || entry.ProviderAccountRef != got {
			t.Fatalf("entry %q provider_account_ref=%q want %q", row.RedisField, entry.ProviderAccountRef, got)
		}
		delete(want, row.RedisField)
	}
	if len(want) != 0 {
		t.Fatalf("bulk rows missing key entries: %v", want)
	}
}

func TestKeyPoolWritersRejectMissingProviderAccountRef(t *testing.T) {
	t.Run("single entry", func(t *testing.T) {
		entry := validProviderAccountEntry("key-primary")
		entry.ProviderAccountRef = ""
		rows, err := buildPublishKeyPoolEntryRows("zhipu", &entry)
		if err == nil {
			t.Fatal("single-entry write accepted missing provider_account_ref")
		}
		if len(rows) != 0 {
			t.Fatalf("rejected single-entry write produced outbox rows: %+v", rows)
		}
	})

	t.Run("bulk entries", func(t *testing.T) {
		entry := validProviderAccountEntry("key-primary")
		entry.ProviderAccountRef = ""
		rows, err := buildPublishKeyPoolEntryBulkRows([]types.KeyPoolEntryItem{{ProviderID: "zhipu", Entry: &entry}})
		if err == nil {
			t.Fatal("bulk entry write accepted missing provider_account_ref")
		}
		if len(rows) != 0 {
			t.Fatalf("rejected bulk entry write produced outbox rows: %+v", rows)
		}
	})

	t.Run("whole-pool snapshot", func(t *testing.T) {
		entry := validProviderAccountEntry("key-primary")
		entry.ProviderAccountRef = ""
		pool := &types.KeyPoolConfig{
			Meta:    types.KeyPoolMeta{ProviderID: "zhipu"},
			Entries: []types.KeyPoolEntry{entry},
		}
		rows, err := buildPublishKeyPoolRows("zhipu", pool)
		if err == nil {
			t.Fatal("whole-pool write accepted missing provider_account_ref")
		}
		if len(rows) != 0 {
			t.Fatalf("rejected whole-pool write produced destructive outbox rows: %+v", rows)
		}
	})
}

func TestKeyPoolWritersRejectInvalidOrCredentialDerivedProviderAccountRef(t *testing.T) {
	tests := []struct {
		name string
		ref  string
	}{
		{name: "whitespace", ref: "acct glm main"},
		{name: "base URL", ref: "https://api.zhipu.example"},
		{name: "key id", ref: "key-primary"},
		{name: "credential material", ref: "vault://providers/zhipu/key-primary"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := validProviderAccountEntry("key-primary")
			entry.ProviderAccountRef = tc.ref
			if _, err := buildPublishKeyPoolEntryRows("zhipu", &entry); err == nil {
				t.Fatalf("buildPublishKeyPoolEntryRows accepted provider_account_ref=%q", tc.ref)
			}

			entry = validProviderAccountEntry("key-primary")
			entry.ProviderAccountRef = tc.ref
			pool := &types.KeyPoolConfig{
				Meta:    types.KeyPoolMeta{ProviderID: "zhipu"},
				Entries: []types.KeyPoolEntry{entry},
			}
			if _, err := buildPublishKeyPoolRows("zhipu", pool); err == nil {
				t.Fatalf("buildPublishKeyPoolRows accepted provider_account_ref=%q", tc.ref)
			}
		})
	}

	t.Run("opaque ref equals ciphertext", func(t *testing.T) {
		entry := validProviderAccountEntry("key-primary")
		entry.Ciphertext = "credential-ref-1"
		entry.ProviderAccountRef = entry.Ciphertext
		if _, err := buildPublishKeyPoolEntryRows("zhipu", &entry); err == nil || !strings.Contains(err.Error(), "must not reuse credential") {
			t.Fatalf("ciphertext-derived provider_account_ref error=%v", err)
		}
	})
}

func validProviderAccountEntry(keyID string) types.KeyPoolEntry {
	return types.KeyPoolEntry{
		KeyID:              keyID,
		ProviderAccountRef: testProviderAccountRef,
		Ciphertext:         "vault://providers/zhipu/" + keyID,
		Priority:           1,
		Weight:             100,
		RPMLimit:           500,
		TPMLimit:           100000,
		Status:             "enabled",
	}
}

func assertProviderAccountRefInKeyPoolHSet(t *testing.T, rows []outbox.OutboxRow, providerID, keyID string) {
	t.Helper()
	for _, row := range rows {
		if row.Op != outbox.OpHSet || row.RedisKey != KeyPoolKey(providerID) || row.RedisField != keyID {
			continue
		}
		var entry types.KeyPoolEntry
		if err := json.Unmarshal(row.Payload, &entry); err != nil {
			t.Fatalf("decode keypool payload: %v payload=%s", err, row.Payload)
		}
		if entry.ProviderAccountRef != testProviderAccountRef {
			t.Fatalf("outbox provider_account_ref=%q want %q payload=%s", entry.ProviderAccountRef, testProviderAccountRef, row.Payload)
		}
		t.Logf("outbox keypool artifact: %s", row.Payload)
		return
	}
	t.Fatalf("missing keypool HSET row provider=%q key=%q: %+v", providerID, keyID, rows)
}
