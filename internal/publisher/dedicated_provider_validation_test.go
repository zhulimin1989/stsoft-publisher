package publisher

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

type fakeRuntimeProviderLookup map[string]*types.ProviderInfo

func (f fakeRuntimeProviderLookup) LookupRuntimeProvider(_ context.Context, providerID string) (*types.ProviderInfo, bool, error) {
	info, ok := f[providerID]
	return info, ok, nil
}

func TestDefaultDedicatedProviderValidationConfigEnforcesAllChecks(t *testing.T) {
	cfg := DefaultDedicatedProviderValidationConfig()
	if cfg.ProviderIDFormat != DedicatedProviderValidationModeEnforce ||
		cfg.ProviderExists != DedicatedProviderValidationModeEnforce ||
		cfg.ProviderDedicatedAccess != DedicatedProviderValidationModeEnforce ||
		cfg.ProviderProtocolBinding != DedicatedProviderValidationModeEnforce ||
		cfg.ProviderOwnerScopeMatches != DedicatedProviderValidationModeEnforce {
		t.Fatalf("dedicated provider validation defaults must all be enforce, got %+v", cfg)
	}

	mode, err := ParseDedicatedProviderValidationMode("")
	if err != nil {
		t.Fatalf("ParseDedicatedProviderValidationMode empty: %v", err)
	}
	if mode != DedicatedProviderValidationModeEnforce {
		t.Fatalf("empty dedicated provider validation mode = %q, want enforce", mode)
	}
}

func TestValidateKeyMetaForPublish_DedicatedProviderValidationEnforce(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(meta *types.KeyMeta, providers map[string]*types.ProviderInfo)
		wantText string
	}{
		{
			name: "rejects non pc provider id",
			mutate: func(meta *types.KeyMeta, _ map[string]*types.ProviderInfo) {
				meta.DedicatedUpstream.Endpoints[0].ProviderID = "glm-openai"
			},
			wantText: "provider_id must match ^pc_",
		},
		{
			name: "rejects missing provider",
			mutate: func(meta *types.KeyMeta, providers map[string]*types.ProviderInfo) {
				delete(providers, meta.DedicatedUpstream.Endpoints[0].ProviderID)
			},
			wantText: "provider not found",
		},
		{
			name: "rejects visibility not dedicated only",
			mutate: func(meta *types.KeyMeta, providers map[string]*types.ProviderInfo) {
				providers[meta.DedicatedUpstream.Endpoints[0].ProviderID].Visibility = types.ProviderVisibilityProductRoute
			},
			wantText: "visibility must be dedicated_only",
		},
		{
			name: "rejects missing matching protocol binding",
			mutate: func(meta *types.KeyMeta, providers map[string]*types.ProviderInfo) {
				providers[meta.DedicatedUpstream.Endpoints[0].ProviderID].ProtocolBindings[0].Accepts[0].Protocol = types.ClientProtocolAnthropic
			},
			wantText: "protocol_binding missing",
		},
		{
			name: "rejects owner scope mismatch",
			mutate: func(meta *types.KeyMeta, _ map[string]*types.ProviderInfo) {
				meta.ScopeAttrs["tenant_id"] = "tenant_other"
			},
			wantText: "owner_scope mismatch",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, providers := validDedicatedProviderValidationFixture()
			tc.mutate(meta, providers)
			err := validateDedicatedProviderMetaForPublish(t, meta, providers, dedicatedProviderValidationConfigForMode(DedicatedProviderValidationModeEnforce))
			if err == nil {
				t.Fatal("expected dedicated provider validation rejection")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantText)
			}
		})
	}
}

func TestValidateKeyMetaForPublish_DedicatedProviderValidationAllowsValidProvider(t *testing.T) {
	meta, providers := validDedicatedProviderValidationFixture()
	if err := validateDedicatedProviderMetaForPublish(t, meta, providers, dedicatedProviderValidationConfigForMode(DedicatedProviderValidationModeEnforce)); err != nil {
		t.Fatalf("expected valid dedicated provider to pass enforce validation, got %v", err)
	}
}

// ADR-0003 修订(2026-06-25)/ADR-0009:不做 BYOK,共享 dedicated provider 不带
// per-客户 owner_scope。owner_scope 降为可选 + 粗粒度(最多 product_id);共享
// provider 上 tenant_id/entitlement_id 为 null 合法。publisher 校验不得因缺 tenant
// 而拒绝发布——只要 provider 非 null 维度(此处 product_id)被 key 满足即可。
func TestValidateKeyMetaForPublish_DedicatedProviderWithoutTenantOwnerScopeIsValid(t *testing.T) {
	meta, providers := validDedicatedProviderValidationFixture()
	provider := providers["pc_102"]
	// 共享 dedicated provider: 只保留粗粒度 product_id,不带 per-客户 tenant_id。
	provider.OwnerScope = &types.OwnerScope{ProductID: "prod_coding"}

	if err := validateDedicatedProviderMetaForPublish(t, meta, providers, dedicatedProviderValidationConfigForMode(DedicatedProviderValidationModeEnforce)); err != nil {
		t.Fatalf("expected dedicated provider without owner_scope.tenant_id to pass enforce validation, got %v", err)
	}
}

// owner_scope 可以完全省略: 共享 provider 给该线路所有客户共用,隔离=钥匙。
func TestValidateKeyMetaForPublish_DedicatedProviderWithoutOwnerScopeIsValid(t *testing.T) {
	meta, providers := validDedicatedProviderValidationFixture()
	providers["pc_102"].OwnerScope = nil

	if err := validateDedicatedProviderMetaForPublish(t, meta, providers, dedicatedProviderValidationConfigForMode(DedicatedProviderValidationModeEnforce)); err != nil {
		t.Fatalf("expected dedicated provider without owner_scope to pass enforce validation, got %v", err)
	}
}

func TestValidateKeyMetaForPublish_DedicatedProviderValidationWarnDoesNotReject(t *testing.T) {
	meta, providers := validDedicatedProviderValidationFixture()
	meta.DedicatedUpstream.Endpoints[0].ProviderID = "glm-openai"
	delete(providers, "pc_102")

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)

	err := validateDedicatedProviderMetaForPublish(t, meta, providers, dedicatedProviderValidationConfigForMode(DedicatedProviderValidationModeWarn))
	if err != nil {
		t.Fatalf("warn mode must not reject invalid dedicated provider input, got %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "publisher dedicated provider validation warning") ||
		!strings.Contains(logged, "provider_id_format") {
		t.Fatalf("warn mode should log validation warning, got %s", logged)
	}
}

func TestPublishKey_DedicatedProviderValidationRejectsBeforeCommit(t *testing.T) {
	meta, providers := validDedicatedProviderValidationFixture()
	meta.DedicatedUpstream.Endpoints[0].ProviderID = "glm-openai"

	p := New(nil)
	p.SetRuntimeProviderLookup(fakeRuntimeProviderLookup(providers))
	p.SetDedicatedProviderValidationConfig(dedicatedProviderValidationConfigForMode(DedicatedProviderValidationModeEnforce))

	_, err := p.PublishKey(context.Background(), meta)
	if err == nil {
		t.Fatal("expected PublishKey to reject invalid dedicated provider before commit")
	}
	if !strings.Contains(err.Error(), "provider_id must match ^pc_") {
		t.Fatalf("unexpected PublishKey error: %v", err)
	}
}

func TestPublishKeyBulk_DedicatedProviderValidationRejectsItemBeforeCommit(t *testing.T) {
	_, providers := validDedicatedProviderValidationFixture()
	badMeta, _ := validDedicatedProviderValidationFixture()
	badMeta.DedicatedUpstream.Endpoints[0].ProviderID = "glm-openai"
	ordinary := &types.KeyMeta{
		KeyID:   string(validKeyID()),
		KeyHash: string(validHash()),
		Status:  "active",
	}

	p := New(nil)
	p.SetRuntimeProviderLookup(fakeRuntimeProviderLookup(providers))
	p.SetDedicatedProviderValidationConfig(dedicatedProviderValidationConfigForMode(DedicatedProviderValidationModeEnforce))

	_, err := p.PublishKeyBulk(context.Background(), []*types.KeyMeta{ordinary, badMeta})
	if err == nil {
		t.Fatal("expected PublishKeyBulk to reject invalid dedicated provider before commit")
	}
	if !strings.Contains(err.Error(), "item 1") || !strings.Contains(err.Error(), "provider_id must match ^pc_") {
		t.Fatalf("unexpected PublishKeyBulk error: %v", err)
	}
}

func validateDedicatedProviderMetaForPublish(t *testing.T, meta *types.KeyMeta, providers map[string]*types.ProviderInfo, cfg DedicatedProviderValidationConfig) error {
	t.Helper()
	fillKeyMetaDefaults(meta)
	p := New(nil)
	p.SetRuntimeProviderLookup(fakeRuntimeProviderLookup(providers))
	p.SetDedicatedProviderValidationConfig(cfg)
	return p.validateKeyMetaForPublish(context.Background(), meta)
}

func dedicatedProviderValidationConfigForMode(mode DedicatedProviderValidationMode) DedicatedProviderValidationConfig {
	return DedicatedProviderValidationConfig{
		ProviderIDFormat:          mode,
		ProviderExists:            mode,
		ProviderDedicatedAccess:   mode,
		ProviderProtocolBinding:   mode,
		ProviderOwnerScopeMatches: mode,
	}
}

func validDedicatedProviderValidationFixture() (*types.KeyMeta, map[string]*types.ProviderInfo) {
	meta := &types.KeyMeta{
		KeyID:   string(validKeyID()),
		KeyHash: string(validHash()),
		Status:  "active",
		ScopeAttrs: map[string]string{
			"tenant_id":  "tenant_a",
			"product_id": "prod_coding",
		},
		DedicatedUpstream: validDedicatedUpstream(),
	}
	meta.DedicatedUpstream.Endpoints = []types.DedicatedUpstreamEndpoint{{
		ClientProtocol:   types.ClientProtocolOpenAI,
		ProviderID:       "pc_102",
		ProtocolStrategy: types.DedicatedProtocolChannelPassthrough,
	}}

	return meta, map[string]*types.ProviderInfo{
		"pc_102": validDedicatedProviderInfo("pc_102", types.ClientProtocolOpenAI),
	}
}

func validDedicatedProviderInfo(providerID, clientProtocol string) *types.ProviderInfo {
	return &types.ProviderInfo{
		ID:                providerID,
		Name:              "Dedicated GLM",
		BaseURL:           "https://api.example.test/v1",
		Status:            "active",
		Visibility:        types.ProviderVisibilityDedicatedOnly,
		AllowedRouteKinds: []string{types.ProviderRouteKindDedicated},
		OwnerScope: &types.OwnerScope{
			TenantID:  "tenant_a",
			ProductID: "prod_coding",
		},
		ProtocolBindings: []types.ProtocolBinding{{
			Name:             "openai-chat",
			OutboundProtocol: "openai",
			AuthScheme:       "bearer",
			TranslatorKey:    "passthrough",
			Accepts: []types.ProtocolBindingAccept{{
				Protocol:        clientProtocol,
				Capabilities:    []string{"chat"},
				RequestContract: "chat_completions",
			}},
		}},
		DefaultProtocolBinding: "openai-chat",
	}
}
