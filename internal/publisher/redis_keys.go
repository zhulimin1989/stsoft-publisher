package publisher

import (
	"fmt"
	"strings"
)

// KeyPoolKey assembles "gw:keypool:{provider_id}" — the Redis Hash that
// holds every key entry for one provider, keyed by key_id.
func KeyPoolKey(providerID string) string {
	return KeyKeyPoolPrefix + strings.TrimSpace(providerID)
}

// Redis key names — single source of truth for the gateway data-plane
// wire contract. Any change here is a cross-repo break that must be
// coordinated with gateway/internal/configsync/redis_keys.go.
const (
	// gw:keymeta (Hash, field=KeyHash) — unit 1
	KeyKeymeta = "gw:keymeta"

	// gw:key_balance (Hash, field=KeyID, value="1"|"0") — unit 2 primary
	KeyKeyBalance = "gw:key_balance"
	// gw:key_balance_reason (Hash, field=KeyID, value=reason str) — unit 2 sidecar
	KeyKeyBalanceReason = "gw:key_balance_reason"
	// gw:key_balance_revision (Hash, field=KeyID, value=int64 micros) — Lua CAS guard
	KeyKeyBalanceRevision = "gw:key_balance_revision"
	// Billing-owned admission decision sidecars. These are intentionally
	// separate from KeyKeyBalanceRevision, whose values come from Publisher's
	// internal sequence and therefore cannot order Billing business decisions.
	KeyAdmissionDecisionVersion = "gw:key_admission_decision_version"
	KeyAdmissionDecisionID      = "gw:key_admission_decision_id"
	KeyAdmissionFingerprint     = "gw:key_admission_fingerprint"
	KeyAdmissionTombstone       = "gw:key_admission_tombstone"
	KeyAdmissionLegacyVersion   = "gw:key_admission_legacy_version"

	// gw:revoked_keys (Set, member=KeyHash) — unit 3
	KeyRevokedKeys = "gw:revoked_keys"

	// gw:ip_blocklist (Set, member=ip) — unit 4
	KeyBlockedIPs = "gw:ip_blocklist"

	// gw:model_catalog (Hash, field=model_id) — unit 5
	KeyModelCatalog = "gw:model_catalog"

	// gw:routes (Hash, field=model_id) — unit 6
	KeyRoutes = "gw:routes"

	// gw:keypool:{provider_id} (Hash, field=key_id, value=KeyPoolEntry JSON)
	// One sub-hash per provider; each upstream API key is one independent
	// hash field so add/remove/disable of a single key never rewrites the
	// whole pool. Strategy + revision live in gw:keypool_meta below.
	KeyKeyPoolPrefix = "gw:keypool:"

	// gw:keypool_meta (Hash, field=provider_id, value=KeyPoolMeta JSON)
	// Pool-level metadata (Strategy + Revision) that does not change per
	// key. Kept in a separate Hash so writers can update meta independently
	// from the per-key entries.
	KeyKeyPoolMeta = "gw:keypool_meta"

	// gw:providers (Hash, field=provider_id) — unit 8
	KeyProviders = "gw:providers"

	// gw:platform:config (String) — unit 9
	KeyPlatformConfig = "gw:platform:config"

	// gw:product_policies (Hash, field=product_id) — platform-domain
	// product runtime policy. Missing field means legacy gateway routing
	// compatibility: no product-level route/key boundary is applied.
	KeyProductPolicies = "gw:product_policies"
	// gw:product_policy_revisions (Hash, field=product_id, value=int64)
	// publisher-side CAS/tombstone guard for product policy update/delete
	// ordering. Gateway does not need to read this sidecar.
	KeyProductPolicyRevisions = "gw:product_policy_revisions"

	// gw:routing:overrides (Hash, field=scope_key) — unit 10
	KeyProviderOverrides = "gw:routing:overrides"

	// Pub/Sub channels.
	ChannelConfigUpdate    = "gw:config:update"
	ChannelSecurityUpdate  = "gw:security:update"
	ChannelAdmissionUpdate = "gw:admission:update"

	// Domain revision counters. Each commit in a given domain INCRs the
	// corresponding key (via an OpIncr outbox row) and the resulting value
	// is stamped into the PUBLISH message's "revision" field. Gateway
	// filters incoming messages with revision <= local-cached revision per
	// domain, so monotonic INCR is required for in-order cache reload.
	// Names must match gateway/internal/configsync/redis_keys.go.
	KeyTopologyRevision   = "gw:config:rev:topology"   // model_catalog + routes + providers
	KeyCredentialRevision = "gw:config:rev:credential" // keypool
	KeyPlatformRevision   = "gw:config:rev:platform"   // platform config
	KeySecurityRevision   = "gw:config:rev:security"   // security policy
)

// Domain enumerates the four config domains used by the gateway's
// monotonic-revision filter. Each publisher write that should drive a
// gateway cache reload picks the domain matching the affected gw:* key.
type Domain string

const (
	DomainTopology   Domain = "topology"
	DomainCredential Domain = "credential"
	DomainPlatform   Domain = "platform"
	DomainSecurity   Domain = "security"
)

// RevisionKeyFor maps a Domain to the Redis revision counter key.
func RevisionKeyFor(d Domain) string {
	switch d {
	case DomainTopology:
		return KeyTopologyRevision
	case DomainCredential:
		return KeyCredentialRevision
	case DomainPlatform:
		return KeyPlatformRevision
	case DomainSecurity:
		return KeySecurityRevision
	default:
		return ""
	}
}

// OverrideScopeKey assembles the standard <provider_code>|<base_url>|<upstream_model>
// hash-field for unit 10. Trims spaces, then concatenates with the literal
// pipe separator — mirrors gateway-side scope parsing.
func OverrideScopeKey(providerCode, baseURL, upstreamModel string) string {
	return fmt.Sprintf("%s|%s|%s",
		strings.TrimSpace(providerCode),
		strings.TrimSpace(baseURL),
		strings.TrimSpace(upstreamModel),
	)
}
