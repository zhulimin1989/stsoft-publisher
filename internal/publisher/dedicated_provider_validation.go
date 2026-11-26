package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/tokenplatform/publisher/internal/types"
)

type RuntimeProviderLookup interface {
	LookupRuntimeProvider(ctx context.Context, providerID string) (*types.ProviderInfo, bool, error)
}

type DedicatedProviderValidationMode string

const (
	DedicatedProviderValidationModeOff     DedicatedProviderValidationMode = "off"
	DedicatedProviderValidationModeWarn    DedicatedProviderValidationMode = "warn"
	DedicatedProviderValidationModeEnforce DedicatedProviderValidationMode = "enforce"
)

type DedicatedProviderValidationConfig struct {
	ProviderIDFormat          DedicatedProviderValidationMode
	ProviderExists            DedicatedProviderValidationMode
	ProviderDedicatedAccess   DedicatedProviderValidationMode
	ProviderProtocolBinding   DedicatedProviderValidationMode
	ProviderOwnerScopeMatches DedicatedProviderValidationMode
}

const (
	dedicatedProviderCheckIDFormat        = "provider_id_format"
	dedicatedProviderCheckExists          = "provider_exists"
	dedicatedProviderCheckDedicatedAccess = "provider_dedicated_access"
	dedicatedProviderCheckProtocolBinding = "protocol_binding"
	dedicatedProviderCheckOwnerScope      = "owner_scope"
)

var dedicatedProviderIDPattern = regexp.MustCompile(`^pc_[A-Za-z0-9_-]+$`)

func DefaultDedicatedProviderValidationConfig() DedicatedProviderValidationConfig {
	return DedicatedProviderValidationConfig{
		ProviderIDFormat:          DedicatedProviderValidationModeEnforce,
		ProviderExists:            DedicatedProviderValidationModeEnforce,
		ProviderDedicatedAccess:   DedicatedProviderValidationModeEnforce,
		ProviderProtocolBinding:   DedicatedProviderValidationModeEnforce,
		ProviderOwnerScopeMatches: DedicatedProviderValidationModeEnforce,
	}
}

func ParseDedicatedProviderValidationMode(raw string) (DedicatedProviderValidationMode, error) {
	mode := DedicatedProviderValidationMode(strings.ToLower(strings.TrimSpace(raw)))
	if mode == "" {
		return DedicatedProviderValidationModeEnforce, nil
	}
	if !knownDedicatedProviderValidationMode(mode) {
		return "", fmt.Errorf("dedicated provider validation mode %q must be one of off, warn, enforce", raw)
	}
	return mode, nil
}

func DedicatedProviderValidationConfigFromStrings(providerIDFormat, providerExists, dedicatedAccess, protocolBinding, ownerScope string) (DedicatedProviderValidationConfig, error) {
	cfg := DefaultDedicatedProviderValidationConfig()

	var err error
	if cfg.ProviderIDFormat, err = parseDedicatedProviderValidationModeField(dedicatedProviderCheckIDFormat, providerIDFormat); err != nil {
		return DedicatedProviderValidationConfig{}, err
	}
	if cfg.ProviderExists, err = parseDedicatedProviderValidationModeField(dedicatedProviderCheckExists, providerExists); err != nil {
		return DedicatedProviderValidationConfig{}, err
	}
	if cfg.ProviderDedicatedAccess, err = parseDedicatedProviderValidationModeField(dedicatedProviderCheckDedicatedAccess, dedicatedAccess); err != nil {
		return DedicatedProviderValidationConfig{}, err
	}
	if cfg.ProviderProtocolBinding, err = parseDedicatedProviderValidationModeField(dedicatedProviderCheckProtocolBinding, protocolBinding); err != nil {
		return DedicatedProviderValidationConfig{}, err
	}
	if cfg.ProviderOwnerScopeMatches, err = parseDedicatedProviderValidationModeField(dedicatedProviderCheckOwnerScope, ownerScope); err != nil {
		return DedicatedProviderValidationConfig{}, err
	}
	return cfg, nil
}

func parseDedicatedProviderValidationModeField(name, raw string) (DedicatedProviderValidationMode, error) {
	mode, err := ParseDedicatedProviderValidationMode(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return mode, nil
}

func (cfg DedicatedProviderValidationConfig) withDefaults() DedicatedProviderValidationConfig {
	def := DefaultDedicatedProviderValidationConfig()
	if cfg.ProviderIDFormat == "" {
		cfg.ProviderIDFormat = def.ProviderIDFormat
	}
	if cfg.ProviderExists == "" {
		cfg.ProviderExists = def.ProviderExists
	}
	if cfg.ProviderDedicatedAccess == "" {
		cfg.ProviderDedicatedAccess = def.ProviderDedicatedAccess
	}
	if cfg.ProviderProtocolBinding == "" {
		cfg.ProviderProtocolBinding = def.ProviderProtocolBinding
	}
	if cfg.ProviderOwnerScopeMatches == "" {
		cfg.ProviderOwnerScopeMatches = def.ProviderOwnerScopeMatches
	}
	return cfg
}

func (cfg DedicatedProviderValidationConfig) validate() error {
	for name, mode := range map[string]DedicatedProviderValidationMode{
		dedicatedProviderCheckIDFormat:        cfg.ProviderIDFormat,
		dedicatedProviderCheckExists:          cfg.ProviderExists,
		dedicatedProviderCheckDedicatedAccess: cfg.ProviderDedicatedAccess,
		dedicatedProviderCheckProtocolBinding: cfg.ProviderProtocolBinding,
		dedicatedProviderCheckOwnerScope:      cfg.ProviderOwnerScopeMatches,
	} {
		if !knownDedicatedProviderValidationMode(mode) {
			return fmt.Errorf("%s: dedicated provider validation mode %q must be one of off, warn, enforce", name, mode)
		}
	}
	return nil
}

func knownDedicatedProviderValidationMode(mode DedicatedProviderValidationMode) bool {
	switch mode {
	case DedicatedProviderValidationModeOff, DedicatedProviderValidationModeWarn, DedicatedProviderValidationModeEnforce:
		return true
	default:
		return false
	}
}

type redisRuntimeProviderLookup struct {
	rdb *redis.Client
}

func NewRedisRuntimeProviderLookup(rdb *redis.Client) RuntimeProviderLookup {
	return &redisRuntimeProviderLookup{rdb: rdb}
}

func (l *redisRuntimeProviderLookup) LookupRuntimeProvider(ctx context.Context, providerID string) (*types.ProviderInfo, bool, error) {
	if l == nil || l.rdb == nil {
		return nil, false, fmt.Errorf("redis runtime provider lookup is not configured")
	}
	raw, err := l.rdb.HGet(ctx, KeyProviders, providerID).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var info types.ProviderInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, false, fmt.Errorf("decode provider %s from %s: %w", providerID, KeyProviders, err)
	}
	if info.ID == "" {
		info.ID = providerID
	}
	return &info, true, nil
}

func (p *Impl) SetRuntimeProviderLookup(lookup RuntimeProviderLookup) {
	p.runtimeProviderLookup = lookup
}

func (p *Impl) SetDedicatedProviderValidationConfig(cfg DedicatedProviderValidationConfig) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		slog.Warn("invalid dedicated provider validation config ignored", "error", err)
		return
	}
	p.dedicatedProviderValidation = cfg
}

func (p *Impl) validateKeyMetaForPublish(ctx context.Context, meta *types.KeyMeta) error {
	if err := validateKeyMeta(meta); err != nil {
		return err
	}
	if meta == nil || meta.DedicatedUpstream == nil {
		return nil
	}
	cfg := p.dedicatedProviderValidation.withDefaults()
	if err := cfg.validate(); err != nil {
		return fmt.Errorf("%w: dedicated provider validation config invalid: %v", ErrInvalidArgument, err)
	}
	return validateDedicatedProviderEndpoints(ctx, meta, p.runtimeProviderLookup, cfg)
}

func validateDedicatedProviderEndpoints(ctx context.Context, meta *types.KeyMeta, lookup RuntimeProviderLookup, cfg DedicatedProviderValidationConfig) error {
	if meta == nil || meta.DedicatedUpstream == nil {
		return nil
	}
	for i, endpoint := range meta.DedicatedUpstream.Endpoints {
		label := fmt.Sprintf("keymeta.dedicated_upstream.endpoints[%d]", i)
		providerID := strings.TrimSpace(endpoint.ProviderID)
		if !dedicatedProviderIDPattern.MatchString(providerID) {
			if err := handleDedicatedProviderIssue(ctx, cfg.ProviderIDFormat, dedicatedProviderCheckIDFormat, providerID, endpoint.ClientProtocol,
				fmt.Sprintf("%s.provider_id must match ^pc_[A-Za-z0-9_-]+$: %s", label, providerID)); err != nil {
				return err
			}
		}

		if lookup == nil {
			if err := handleDedicatedProviderIssue(ctx, cfg.ProviderExists, dedicatedProviderCheckExists, providerID, endpoint.ClientProtocol,
				fmt.Sprintf("%s.provider_id provider lookup unavailable", label)); err != nil {
				return err
			}
			continue
		}
		provider, ok, err := lookup.LookupRuntimeProvider(ctx, providerID)
		if err != nil {
			if err := handleDedicatedProviderIssue(ctx, cfg.ProviderExists, dedicatedProviderCheckExists, providerID, endpoint.ClientProtocol,
				fmt.Sprintf("%s.provider_id lookup failed: %v", label, err)); err != nil {
				return err
			}
			continue
		}
		if !ok || provider == nil {
			if err := handleDedicatedProviderIssue(ctx, cfg.ProviderExists, dedicatedProviderCheckExists, providerID, endpoint.ClientProtocol,
				fmt.Sprintf("%s.provider_id provider not found: %s", label, providerID)); err != nil {
				return err
			}
			continue
		}

		if provider.Visibility != types.ProviderVisibilityDedicatedOnly {
			if err := handleDedicatedProviderIssue(ctx, cfg.ProviderDedicatedAccess, dedicatedProviderCheckDedicatedAccess, providerID, endpoint.ClientProtocol,
				fmt.Sprintf("%s.provider_id provider visibility must be dedicated_only: %s", label, provider.Visibility)); err != nil {
				return err
			}
		}
		if !stringSliceContains(provider.AllowedRouteKinds, types.ProviderRouteKindDedicated) {
			if err := handleDedicatedProviderIssue(ctx, cfg.ProviderDedicatedAccess, dedicatedProviderCheckDedicatedAccess, providerID, endpoint.ClientProtocol,
				fmt.Sprintf("%s.provider_id provider allowed_route_kinds must include dedicated_route", label)); err != nil {
				return err
			}
		}
		if !providerHasClientProtocolBinding(provider, endpoint.ClientProtocol) {
			if err := handleDedicatedProviderIssue(ctx, cfg.ProviderProtocolBinding, dedicatedProviderCheckProtocolBinding, providerID, endpoint.ClientProtocol,
				fmt.Sprintf("%s.provider_id protocol_binding missing for client_protocol %s", label, endpoint.ClientProtocol)); err != nil {
				return err
			}
		}
		if err := dedicatedOwnerScopeMismatch(meta.ScopeAttrs, provider.OwnerScope); err != nil {
			if err := handleDedicatedProviderIssue(ctx, cfg.ProviderOwnerScopeMatches, dedicatedProviderCheckOwnerScope, providerID, endpoint.ClientProtocol,
				fmt.Sprintf("%s.provider_id owner_scope mismatch: %v", label, err)); err != nil {
				return err
			}
		}
	}
	return nil
}

func handleDedicatedProviderIssue(ctx context.Context, mode DedicatedProviderValidationMode, check, providerID, clientProtocol, msg string) error {
	switch mode {
	case DedicatedProviderValidationModeOff:
		return nil
	case DedicatedProviderValidationModeWarn:
		slog.WarnContext(ctx, "publisher dedicated provider validation warning",
			"check", check,
			"provider_id", providerID,
			"client_protocol", clientProtocol,
			"error", msg,
		)
		return nil
	case DedicatedProviderValidationModeEnforce:
		return fmt.Errorf("%w: %s", ErrInvalidArgument, msg)
	default:
		return fmt.Errorf("%w: dedicated provider validation mode %q invalid for %s", ErrInvalidArgument, mode, check)
	}
}

func providerHasClientProtocolBinding(provider *types.ProviderInfo, clientProtocol string) bool {
	if provider == nil {
		return false
	}
	for _, binding := range provider.ProtocolBindings {
		for _, accepts := range binding.Accepts {
			if accepts.Protocol == clientProtocol {
				return true
			}
		}
	}
	return false
}

// dedicatedOwnerScopeMismatch enforces the relaxed owner_scope contract from
// ADR-0003 (rev 2026-06-25) / ADR-0009: we do NOT support BYOK, so a shared
// dedicated provider does not carry a per-customer owner_scope. owner_scope is
// optional and coarse-grained (product_id at most); on a shared provider
// tenant_id/entitlement_id are expected to be null. Absence of owner_scope (or
// of any individual field) is therefore legal and must not fail publish —
// per-customer isolation lives on the key's own credentials/entitlement. We
// only verify that any owner_scope dimension that IS present matches the key's
// scope attrs (coarse, non-null dimension satisfied by the key).
func dedicatedOwnerScopeMismatch(scopeAttrs map[string]string, ownerScope *types.OwnerScope) error {
	if ownerScope == nil {
		// Shared provider with no owner_scope: isolation is the key, valid.
		return nil
	}
	if ownerScope.TenantID != "" {
		if !scopeAttrMatches(scopeAttrs, ownerScope.TenantID, "tenant_id", "usr.tenant_id") {
			return fmt.Errorf("tenant_id does not match")
		}
	}
	if ownerScope.ProductID != "" {
		if !scopeAttrMatches(scopeAttrs, ownerScope.ProductID, "product_id", "usr.product_id", "usr.coding_plan_id") {
			return fmt.Errorf("product_id does not match")
		}
	}
	if ownerScope.EntitlementID != "" {
		if !scopeAttrMatches(scopeAttrs, ownerScope.EntitlementID, "entitlement_id", "usr.entitlement_id", "usr.coding_plan_entitlement_id") {
			return fmt.Errorf("entitlement_id does not match")
		}
	}
	// An empty-but-present owner_scope is also legal: a shared provider may carry
	// no concrete customer dimension at all.
	return nil
}

func scopeAttrMatches(scopeAttrs map[string]string, want string, keys ...string) bool {
	for _, key := range keys {
		if scopeAttrs[key] == want {
			return true
		}
	}
	return false
}

func stringSliceContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
