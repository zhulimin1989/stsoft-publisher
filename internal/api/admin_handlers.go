package api

import (
	"log/slog"
	"net/http"

	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/types"
)

// checkPathBodyKey enforces the publisher convention: when a URL path
// carries an identifier (e.g. `{provider_id}`) and the JSON body also
// has the same logical field, the path is authoritative. Body may
// either omit the field (`""`) or supply the identical value; any
// other body value is a 400. Caller-friendliness: this catches the
// "you typed the wrong provider_id in the URL bar" class of mistake.
func checkPathBodyKey(w http.ResponseWriter, label, pathVal, bodyVal string) bool {
	if bodyVal != "" && bodyVal != pathVal {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"body."+label+" does not match path "+label,
			map[string]any{"path": pathVal, "body": bodyVal})
		return false
	}
	return true
}

func routeProviderIDs(entries []types.RouteEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ProviderID)
	}
	return ids
}

func (s *Server) HandleAdminPutProvider(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("provider_id")
	logDeprecatedProviderFinishedConfigEndpoint(r)
	var body ProviderInfoRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if !checkPathBodyKey(w, "id", providerID, body.ID) {
		return
	}
	body.ID = providerID // path is authoritative
	info, err := body.ToProviderInfo()
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.PublishProvider(r.Context(), providerID, info)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminCompileProviderPreview(w http.ResponseWriter, r *http.Request) {
	var body ProviderInfoRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	info, err := body.ToProviderInfo()
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if err := publisher.ValidateProviderInfoForPreview(info); err != nil {
		mapPublisherError(w, err)
		return
	}
	bindingNames := make([]string, 0, len(info.ProtocolBindings))
	for _, binding := range info.ProtocolBindings {
		bindingNames = append(bindingNames, binding.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":                 info,
		"default_protocol_binding": info.DefaultProtocolBinding,
		"binding_names":            bindingNames,
	})
}

func (s *Server) HandleAdminDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id, err := s.pub.DeleteProvider(r.Context(), r.PathValue("provider_id"))
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminPutModelCatalog(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model_id")
	var body ModelCatalogEntryRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if !checkPathBodyKey(w, "model_id", modelID, body.ModelID) {
		return
	}
	body.ModelID = modelID
	id, err := s.pub.PublishModelCatalog(r.Context(), modelID, body.ToModelCatalogEntry())
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminPutModelCatalogBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []ModelCatalogItemRequest `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	items := make([]types.ModelCatalogItem, len(body.Items))
	for i, it := range body.Items {
		items[i] = it.ToTypes()
	}
	id, err := s.pub.PublishModelCatalogBulk(r.Context(), items)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminDeleteModelCatalog(w http.ResponseWriter, r *http.Request) {
	id, err := s.pub.DeleteModelCatalog(r.Context(), r.PathValue("model_id"))
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminPutModelRoute(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model_id")
	var body RouteConfigRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if !checkPathBodyKey(w, "model_id", modelID, body.ModelID) {
		return
	}
	body.ModelID = modelID
	route := body.ToRouteConfig()
	slog.InfoContext(r.Context(), "admin put model route decoded",
		"event", "admin_put_model_route_decoded",
		"model_id", modelID,
		"provider_ids", routeProviderIDs(route.Providers),
		"provider_count", len(route.Providers),
		"provider_strategy", route.ProviderStrategy,
		"idempotency_key", r.Header.Get("Idempotency-Key"),
		"caller_request_id", r.Header.Get("X-Request-Id"),
		"caller_trace_id", r.Header.Get("X-Trace-Id"),
	)
	id, err := s.pub.PublishModelRoute(r.Context(), modelID, route)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminPutModelRouteBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []ModelRouteItemRequest `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	items := make([]types.ModelRouteItem, len(body.Items))
	for i, it := range body.Items {
		items[i] = it.ToTypes()
	}
	id, err := s.pub.PublishModelRouteBulk(r.Context(), items)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminDeleteModelRoute(w http.ResponseWriter, r *http.Request) {
	id, err := s.pub.DeleteModelRoute(r.Context(), r.PathValue("model_id"))
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminPutKeyPool(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("provider_id")
	var body KeyPoolConfigRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if !checkPathBodyKey(w, "meta.provider_id", providerID, body.Meta.ProviderID) {
		return
	}
	body.Meta.ProviderID = providerID
	id, err := s.pub.PublishKeyPool(r.Context(), providerID, body.ToKeyPoolConfig())
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminPutKeyPoolBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []KeyPoolItemRequest `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	items := make([]types.KeyPoolItem, len(body.Items))
	for i, it := range body.Items {
		items[i] = it.ToTypes()
	}
	id, err := s.pub.PublishKeyPoolBulk(r.Context(), items)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminDeleteKeyPool(w http.ResponseWriter, r *http.Request) {
	id, err := s.pub.DeleteKeyPool(r.Context(), r.PathValue("provider_id"))
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminPutKeyPoolEntry(w http.ResponseWriter, r *http.Request) {
	pathKeyID := r.PathValue("key_id")
	var entry types.KeyPoolEntry
	if err := decodeJSON(r, &entry); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if !checkPathBodyKey(w, "key_id", pathKeyID, entry.KeyID) {
		return
	}
	entry.KeyID = pathKeyID
	id, err := s.pub.PublishKeyPoolEntry(r.Context(), r.PathValue("provider_id"), &entry)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminPutKeyPoolEntryBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []types.KeyPoolEntryItem `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.PublishKeyPoolEntryBulk(r.Context(), body.Items)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminDeleteKeyPoolEntry(w http.ResponseWriter, r *http.Request) {
	id, err := s.pub.DeleteKeyPoolEntry(r.Context(), r.PathValue("provider_id"), r.PathValue("key_id"))
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminDeleteKeyPoolEntryBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []types.KeyPoolEntryDeleteItem `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.DeleteKeyPoolEntryBulk(r.Context(), body.Items)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminPutPlatformConfig(w http.ResponseWriter, r *http.Request) {
	var body PlatformConfigRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.PublishPlatformConfig(r.Context(), body.ToPlatformConfig())
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminPutProductRuntimePolicy(w http.ResponseWriter, r *http.Request) {
	productID := r.PathValue("product_id")
	var body ProductRuntimePolicyRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if !checkPathBodyKey(w, "product_id", productID, body.ProductID) {
		return
	}
	body.ProductID = productID
	id, err := s.pub.PublishProductRuntimePolicy(r.Context(), productID, body.ToProductRuntimePolicy())
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleAdminDeleteProductRuntimePolicy(w http.ResponseWriter, r *http.Request) {
	id, err := s.pub.DeleteProductRuntimePolicy(r.Context(), r.PathValue("product_id"))
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}
