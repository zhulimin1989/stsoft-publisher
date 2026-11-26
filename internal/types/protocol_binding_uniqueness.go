package types

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateBindingUniqueness rejects publisher output that would require the
// gateway to disambiguate two bindings for the same inbound request key.
func ValidateBindingUniqueness(bindings []ProtocolBinding) error {
	return validateBindingUniqueness(bindings)
}

type protocolBindingRef struct {
	index int
	name  string
}

func validateBindingUniqueness(bindings []ProtocolBinding) error {
	seen := make(map[protocolBindingUniquenessKey]protocolBindingRef)
	for i, binding := range bindings {
		ref := protocolBindingRef{index: i, name: binding.Name}
		for _, accept := range binding.Accepts {
			for _, key := range protocolBindingUniquenessKeys(accept) {
				prev, ok := seen[key]
				if !ok {
					seen[key] = ref
					continue
				}
				if prev.index == i {
					continue
				}
				return fmt.Errorf(
					"duplicate protocol binding key %s appears in bindings %s and %s",
					key,
					protocolBindingRefLabel(prev),
					protocolBindingRefLabel(ref),
				)
			}
		}
	}
	return nil
}

type protocolBindingUniquenessKey struct {
	protocol        string
	capability      string
	requestContract string
	models          string
}

func protocolBindingUniquenessKeys(accept ProtocolBindingAccept) []protocolBindingUniquenessKey {
	capabilities := accept.Capabilities
	if len(capabilities) == 0 {
		capabilities = []string{""}
	}

	models := append([]string(nil), accept.Models...)
	sort.Strings(models)
	modelKey := strings.Join(models, "\x00")

	out := make([]protocolBindingUniquenessKey, 0, len(capabilities))
	for _, capability := range capabilities {
		out = append(out, protocolBindingUniquenessKey{
			protocol:        accept.Protocol,
			capability:      capability,
			requestContract: accept.RequestContract,
			models:          modelKey,
		})
	}
	return out
}

func (k protocolBindingUniquenessKey) String() string {
	models := "*"
	if k.models != "" {
		models = "[" + strings.Join(strings.Split(k.models, "\x00"), ",") + "]"
	}
	return fmt.Sprintf(
		"protocol=%s capability=%s request_contract=%s models=%s",
		k.protocol,
		k.capability,
		k.requestContract,
		models,
	)
}

func protocolBindingRefLabel(ref protocolBindingRef) string {
	if ref.name == "" {
		return fmt.Sprintf("<unnamed:%d>", ref.index)
	}
	return fmt.Sprintf("%s[%d]", ref.name, ref.index)
}
