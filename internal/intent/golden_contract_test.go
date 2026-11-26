package intent

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

func TestGoldenRuntimeIntentFixturesCompile(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "contracts", "golden", "runtime_intent.*.json"))
	if err != nil {
		t.Fatalf("glob runtime intent golden fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatalf("contracts/golden missing runtime_intent.*.json fixtures")
	}

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var in RuntimeIntent
			decodeIntentGoldenStrict(t, name, raw, &in)

			out, err := Compile(in)
			if err != nil {
				t.Fatalf("compile %s: %v", name, err)
			}
			if strings.Contains(name, "product_policy_delete") {
				if len(out.ProductPolicyDeletes) != 1 || out.ProductPolicyDeletes[0].ProductID != "prod_coding" {
					t.Fatalf("compile %s produced unexpected deletes: %#v", name, out.ProductPolicyDeletes)
				}
				return
			}
			if len(out.Providers) == 0 || len(out.KeyPools) == 0 || len(out.ModelCatalog) == 0 || len(out.Routes) == 0 || len(out.ProductPolicies) == 0 {
				t.Fatalf("compile %s produced incomplete runtime config: providers=%d keyPools=%d catalog=%d routes=%d policies=%d",
					name, len(out.Providers), len(out.KeyPools), len(out.ModelCatalog), len(out.Routes), len(out.ProductPolicies))
			}

			if strings.Contains(name, "channelized_shared_key") {
				assertChannelizedSharedKeyGolden(t, out)
			}
		})
	}
}

func assertChannelizedSharedKeyGolden(t *testing.T, out *RuntimeConfig) {
	t.Helper()
	for _, pool := range out.KeyPools {
		for _, entry := range pool.Entries {
			if entry.ProviderAccountRef != "provider-account-kimi-main" {
				t.Fatalf("channelized shared-key golden lost provider_account_ref: provider=%s entry=%+v", pool.Meta.ProviderID, entry)
			}
		}
	}
	if !hasSharedCredentialWarning(out.Warnings) {
		t.Fatalf("channelized shared-key golden missing shared_credential_group warning: %#v", out.Warnings)
	}
	if !hasPreferAssignment(out.ProductPolicies) {
		t.Fatalf("channelized shared-key golden missing PREFER assignment in compiled route graph")
	}
	if !hasTestingChannelShadowPolicy(out.EffectivePolicies) {
		t.Fatalf("channelized shared-key golden missing shadow effective policy for testing channel")
	}
}

func hasSharedCredentialWarning(warnings []CompileWarning) bool {
	for _, warning := range warnings {
		if warning.Code == "shared_credential_group" {
			return true
		}
	}
	return false
}

func hasPreferAssignment(policies []types.ProductRuntimePolicy) bool {
	for _, policy := range policies {
		if policy.RouteGraph == nil {
			continue
		}
		for _, assignment := range policy.RouteGraph.Assignments {
			if assignment.Mode == "PREFER" {
				return true
			}
		}
	}
	return false
}

func hasTestingChannelShadowPolicy(policies []types.EffectivePolicy) bool {
	for _, policy := range policies {
		if policy.ScopeID == "pc_102" && policy.EnforcementMode == "shadow" {
			return true
		}
	}
	return false
}

func decodeIntentGoldenStrict(t *testing.T, name string, raw []byte, dst any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		t.Fatalf("decode %s into RuntimeIntent: %v", name, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("decode %s trailing JSON: %v", name, err)
	}
}
