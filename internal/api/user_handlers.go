package api

import (
	"net/http"

	"github.com/tokenplatform/publisher/internal/types"
)

func (s *Server) HandleUserPutKey(w http.ResponseWriter, r *http.Request) {
	var body KeyMetaRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	batchID, err := s.pub.PublishKey(r.Context(), body.ToKeyMeta())
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleUserPutKeyBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []*KeyMetaRequest `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if len(body.Items) > types.MaxBulkItems {
		writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "bulk array exceeds maximum",
			map[string]any{"got_count": len(body.Items), "max_count": types.MaxBulkItems})
		return
	}
	metas := make([]*types.KeyMeta, len(body.Items))
	for i, it := range body.Items {
		metas[i] = it.ToKeyMeta()
	}
	batchID, err := s.pub.PublishKeyBulk(r.Context(), metas)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleUserDeleteKey(w http.ResponseWriter, r *http.Request) {
	hash := types.KeyHash(r.PathValue("key_hash"))
	batchID, err := s.pub.DeleteKey(r.Context(), hash)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleUserDeleteKeyBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyHashes []types.KeyHash `json:"key_hashes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	batchID, err := s.pub.DeleteKeyBulk(r.Context(), body.KeyHashes)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleUserAddRevoked(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyHash types.KeyHash `json:"key_hash"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	batchID, err := s.pub.AddRevokedKey(r.Context(), body.KeyHash)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleUserAddRevokedBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyHashes []types.KeyHash `json:"key_hashes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	batchID, err := s.pub.AddRevokedKeysBulk(r.Context(), body.KeyHashes)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleUserRemoveRevoked(w http.ResponseWriter, r *http.Request) {
	hash := types.KeyHash(r.PathValue("key_hash"))
	batchID, err := s.pub.RemoveRevokedKey(r.Context(), hash)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleUserRemoveRevokedBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyHashes []types.KeyHash `json:"key_hashes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	batchID, err := s.pub.RemoveRevokedKeysBulk(r.Context(), body.KeyHashes)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleUserReplaceRevoked(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyHashes []types.KeyHash `json:"key_hashes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	batchID, err := s.pub.PublishRevokedKeys(r.Context(), body.KeyHashes)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}
