package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	intentcompiler "github.com/tokenplatform/publisher/internal/intent"
	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/types"
)

func (s *Server) HandleAdminPublishIntent(w http.ResponseWriter, r *http.Request) {
	caller := publisher.CallerFromContext(r.Context())
	if strings.TrimSpace(caller.IdempotencyID) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"Idempotency-Key header required for /admin/api/v1/providers:from-intent", nil)
		return
	}

	var body intentcompiler.RuntimeIntent
	if err := decodeStrictJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	compiled, err := intentcompiler.Compile(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	// ADR-0016: compile warnings never block publishing; surface them in the
	// response payload (below) and in logs so misdeclared paths are visible.
	for _, warning := range compiled.Warnings {
		slog.WarnContext(r.Context(), "intent compile warning",
			"event", "intent_compile_warning",
			"code", warning.Code,
			"message", warning.Message,
			"providers", warning.Providers,
		)
	}

	response := map[string]any{
		"status":             "accepted",
		"schema_version":     body.SchemaVersion,
		"effective_policies": compiled.EffectivePolicies,
		"warnings":           compiled.Warnings,
	}

	result, err := s.pub.PublishIntent(r.Context(), publisher.IntentPublishRequest{
		Providers:            compiled.Providers,
		KeyPools:             compiled.KeyPools,
		ModelCatalog:         compiled.ModelCatalog,
		Routes:               compiled.Routes,
		ProductPolicies:      compiled.ProductPolicies,
		ProductPolicyDeletes: compiled.ProductPolicyDeletes,
	})
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	response["provider_batches"] = batchIDStrings(result.ProviderBatches)
	response["keypool_batches"] = batchIDStrings(result.KeyPoolBatches)
	response["model_catalog_batches"] = batchIDStrings(result.ModelCatalogBatches)
	response["model_route_batches"] = batchIDStrings(result.ModelRouteBatches)
	response["product_policy_batches"] = batchIDStrings(result.ProductPolicyBatches)
	response["product_policy_delete_batches"] = batchIDStrings(result.ProductPolicyDeleteBatches)

	writeJSON(w, http.StatusAccepted, response)
}

func batchIDStrings(ids []types.BatchID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

func decodeStrictJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return ctErr
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return err
	} else if err == nil {
		return errors.New("request body must contain a single JSON document")
	}
	return nil
}

func logDeprecatedProviderFinishedConfigEndpoint(r *http.Request) {
	slog.WarnContext(r.Context(), "deprecated provider finished-config endpoint used; migrate to intent endpoint before billing cutover deletion",
		"event", "deprecated_provider_finished_config_endpoint_used",
		"path", r.URL.Path,
		"migration_target", "/admin/api/v1/providers:from-intent",
	)
}
