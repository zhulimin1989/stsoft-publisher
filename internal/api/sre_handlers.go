package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/types"
)

func (s *Server) HandleSREDisableKey(w http.ResponseWriter, r *http.Request) {
	hash := types.KeyHash(r.PathValue("key_hash"))
	var body struct {
		Reason   string `json:"reason"`
		Operator string `json:"operator"`
	}
	_ = decodeJSON(r, &body)
	if body.Reason == "" || body.Operator == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "reason and operator required", nil)
		return
	}
	id, err := s.pub.DisableKey(r.Context(), hash)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREEnableKey(w http.ResponseWriter, r *http.Request) {
	hash := types.KeyHash(r.PathValue("key_hash"))
	var body struct {
		Reason   string `json:"reason"`
		Operator string `json:"operator"`
	}
	_ = decodeJSON(r, &body)
	if body.Reason == "" || body.Operator == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "reason and operator required", nil)
		return
	}
	id, err := s.pub.EnableKey(r.Context(), hash)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREDisableKeyBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyHashes []types.KeyHash `json:"key_hashes"`
		Reason    string          `json:"reason"`
		Operator  string          `json:"operator"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if body.Reason == "" || body.Operator == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "reason and operator required", nil)
		return
	}
	id, err := s.pub.DisableKeysBulk(r.Context(), body.KeyHashes)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREEnableKeyBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyHashes []types.KeyHash `json:"key_hashes"`
		Reason    string          `json:"reason"`
		Operator  string          `json:"operator"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if body.Reason == "" || body.Operator == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "reason and operator required", nil)
		return
	}
	id, err := s.pub.EnableKeysBulk(r.Context(), body.KeyHashes)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREAddIP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IP string `json:"ip"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.AddBlockedIP(r.Context(), body.IP)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREAddIPBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IPs []string `json:"ips"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.AddBlockedIPsBulk(r.Context(), body.IPs)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSRERemoveIP(w http.ResponseWriter, r *http.Request) {
	id, err := s.pub.RemoveBlockedIP(r.Context(), r.PathValue("ip"))
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSRERemoveIPBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IPs []string `json:"ips"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.RemoveBlockedIPsBulk(r.Context(), body.IPs)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREReplaceIPs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IPs []string `json:"ips"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.PublishBlockedIPs(r.Context(), body.IPs)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREPutOverride(w http.ResponseWriter, r *http.Request) {
	var body ProviderOverrideRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.PublishProviderOverride(r.Context(), body.Scope, body.ToProviderOverride())
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREPutOverrideBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []ProviderOverrideItemRequest `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	items := make([]types.ProviderOverrideItem, len(body.Items))
	for i, it := range body.Items {
		items[i] = it.ToTypes()
	}
	id, err := s.pub.PublishProviderOverrideBulk(r.Context(), items)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREDeleteOverride(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope string `json:"scope"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.DeleteProviderOverride(r.Context(), body.Scope)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREDeleteOverrideBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scopes []string `json:"scopes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	id, err := s.pub.DeleteProviderOverrideBulk(r.Context(), body.Scopes)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, id.String())
}

func (s *Server) HandleSREReplay(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BatchID *types.BatchID `json:"batch_id,omitempty"`
		Since   *string        `json:"since,omitempty"`
		Until   *string        `json:"until,omitempty"`
		Status  string         `json:"status,omitempty"`
		Limit   int            `json:"limit,omitempty"`
		DryRun  bool           `json:"dry_run,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	req := publisher.ReplayRequest{
		BatchID: body.BatchID,
		Status:  publisher.ReplayStatus(body.Status),
		Limit:   body.Limit,
		DryRun:  body.DryRun,
	}
	if body.Since != nil {
		t, err := time.Parse(time.RFC3339, *body.Since)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"since must be RFC3339 (e.g. 2026-05-16T08:00:00Z)",
				map[string]any{"got": *body.Since, "parse_error": err.Error()})
			return
		}
		u := t.Unix()
		req.Since = &u
	}
	if body.Until != nil {
		t, err := time.Parse(time.RFC3339, *body.Until)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"until must be RFC3339 (e.g. 2026-05-16T08:00:00Z)",
				map[string]any{"got": *body.Until, "parse_error": err.Error()})
			return
		}
		u := t.Unix()
		req.Until = &u
	}
	res, err := s.pub.Replay(r.Context(), req)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(res)
}
