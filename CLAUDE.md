# tokenplatform-publisher

## Capability Axis Freeze

Batch 0 of `ARCH-PASSTHROUGH-REDESIGN.md` freezes the capability/feature/param
axes. Do not add new fields or enum values for:

- `supported_features`
- `supported_params`
- `capability_profiles`
- `capability_features`
- `capability_params`
- `capabilityOverrides`
- `request_param_policy`

New protocol conversion support must be modeled as a whole
protocol-to-protocol bridge/profile, not by splitting another
feature/param/capability admission axis. Intentional changes to the frozen
snapshot require an explicit architecture review and an updated
`ARCH-REVIEW-APPROVED ... sha256:<snapshot>` marker.
