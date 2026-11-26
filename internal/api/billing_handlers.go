package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/refunddrain"
	"github.com/tokenplatform/publisher/internal/types"
)

func (s *Server) HandleBillingEstablishRefundDrain(w http.ResponseWriter, r *http.Request) {
	if s.refundDrains == nil {
		writeError(w, http.StatusServiceUnavailable, "REFUND_DRAIN_UNAVAILABLE", "refund drain coordinator is unavailable", nil)
		return
	}
	var body struct {
		KeyHashes []string `json:"key_hashes"`
	}
	if err := decodeAdmissionJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	snapshot, err := s.refundDrains.Establish(r.Context(), r.PathValue("drain_id"), body.KeyHashes)
	if err != nil {
		mapRefundDrainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) HandleBillingGetRefundDrain(w http.ResponseWriter, r *http.Request) {
	if s.refundDrains == nil {
		writeError(w, http.StatusServiceUnavailable, "REFUND_DRAIN_UNAVAILABLE", "refund drain coordinator is unavailable", nil)
		return
	}
	snapshot, err := s.refundDrains.Status(r.Context(), r.PathValue("drain_id"))
	if err != nil {
		mapRefundDrainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) HandleBillingReleaseRefundDrain(w http.ResponseWriter, r *http.Request) {
	if s.refundDrains == nil {
		writeError(w, http.StatusServiceUnavailable, "REFUND_DRAIN_UNAVAILABLE", "refund drain coordinator is unavailable", nil)
		return
	}
	if err := s.refundDrains.Release(r.Context(), r.PathValue("drain_id")); err != nil {
		mapRefundDrainError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func mapRefundDrainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, refunddrain.ErrInvalidArgument):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
	case errors.Is(err, refunddrain.ErrNotFound):
		writeError(w, http.StatusNotFound, "REFUND_DRAIN_NOT_FOUND", err.Error(), nil)
	case errors.Is(err, refunddrain.ErrBindingConflict), errors.Is(err, refunddrain.ErrStateConflict):
		writeError(w, http.StatusConflict, "REFUND_DRAIN_CONFLICT", err.Error(), nil)
	case errors.Is(err, refunddrain.ErrStateMissing):
		writeError(w, http.StatusConflict, "REFUND_DRAIN_INCOMPLETE", err.Error(), nil)
	default:
		writeError(w, http.StatusServiceUnavailable, "REFUND_DRAIN_UNAVAILABLE", err.Error(), nil)
	}
}

func (s *Server) HandleBillingSetBalance(w http.ResponseWriter, r *http.Request) {
	id := types.KeyID(r.PathValue("key_id"))
	var body struct {
		HasBalance *bool  `json:"has_balance"`
		Reason     string `json:"reason"`
		AdmissionDecisionRequest
	}
	if err := decodeAdmissionJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if body.HasBalance == nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "has_balance is required", nil)
		return
	}
	decision, err := normalizeAdmissionDecision(body.DecisionID, body.DecisionVersion, body.Revision, s.keyAdmissionStrictMode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	batchID, err := s.pub.SetKeyAdmission(r.Context(), id, *body.HasBalance, body.Reason, decision)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

// HandleBillingActivateKey accepts the complete activation contract in one
// request. Publisher persists it as one composite outbox row and the worker
// applies it in one Redis Lua transaction.
func (s *Server) HandleBillingActivateKey(w http.ResponseWriter, r *http.Request) {
	pathID := types.KeyID(r.PathValue("key_id"))
	var body struct {
		KeyMeta    *KeyMetaRequest `json:"key_meta"`
		HasBalance *bool           `json:"has_balance"`
		Reason     string          `json:"reason,omitempty"`
		AdmissionDecisionRequest
	}
	if err := decodeAdmissionJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	if body.KeyMeta == nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "key_meta is required", nil)
		return
	}
	if body.KeyMeta.KeyID != string(pathID) {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "path key_id must match key_meta.key_id", nil)
		return
	}
	if body.HasBalance == nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "has_balance is required", nil)
		return
	}
	// Activation is new and has no legacy caller, so never permit an
	// unversioned decision on this money-safety boundary.
	decision, err := normalizeAdmissionDecision(body.DecisionID, body.DecisionVersion, body.Revision, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	batchID, err := s.pub.ActivateKey(r.Context(), body.KeyMeta.ToKeyMeta(), *body.HasBalance, body.Reason, decision)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleBillingSetBalanceBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items  []KeyBalanceItemRequest `json:"items"`
		Reason string                  `json:"reason"`
		AdmissionDecisionRequest
	}
	if err := decodeAdmissionJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	items := make([]types.KeyAdmissionItem, len(body.Items))
	for i, it := range body.Items {
		if it.HasBalance == nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf("item %d: has_balance is required", i), nil)
			return
		}
		decisionRequest, err := mergeAdmissionDecisionRequest(body.AdmissionDecisionRequest, it.AdmissionDecisionRequest)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf("item %d: %v", i, err), nil)
			return
		}
		decision, err := normalizeAdmissionDecision(decisionRequest.DecisionID, decisionRequest.DecisionVersion, decisionRequest.Revision, s.keyAdmissionStrictMode)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf("item %d: %v", i, err), nil)
			return
		}
		items[i] = it.ToTypes(*it.HasBalance, body.Reason, decision)
	}
	batchID, err := s.pub.SetKeyAdmissionBulk(r.Context(), items)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleBillingDeleteBalance(w http.ResponseWriter, r *http.Request) {
	id := types.KeyID(r.PathValue("key_id"))
	var body AdmissionDecisionRequest
	if r.Body != nil && r.Body != http.NoBody && (r.ContentLength != 0) {
		if err := decodeAdmissionJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
			return
		}
	}
	decision, err := normalizeAdmissionDecision(body.DecisionID, body.DecisionVersion, body.Revision, s.keyAdmissionStrictMode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
		return
	}
	batchID, err := s.pub.DeleteKeyAdmission(r.Context(), id, decision)
	if err != nil {
		mapPublisherError(w, err)
		return
	}
	writeAccepted(w, batchID.String())
}

func (s *Server) HandleBillingGetAdmissionDelivery(w http.ResponseWriter, r *http.Request) {
	parsed, err := uuid.Parse(r.PathValue("batch_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "batch_id must be UUID", nil)
		return
	}
	receipt, err := s.pub.GetKeyAdmissionDelivery(r.Context(), types.BatchID(parsed))
	if err != nil {
		if errors.Is(err, publisher.ErrAdmissionDeliveryNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "admission delivery batch not found", nil)
			return
		}
		mapPublisherError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

// decodeAdmissionJSON is intentionally stricter than the temporary global
// decoder. Admission ordering fields are money-safety controls: a typo must
// fail at the Publisher boundary instead of silently downgrading to legacy.
func decodeAdmissionJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return ctErr
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON value")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func normalizeAdmissionDecision(decisionID string, decisionVersion, revision *int64, strict bool) (types.KeyAdmissionDecision, error) {
	decisionID = strings.TrimSpace(decisionID)
	if decisionVersion != nil && *decisionVersion <= 0 {
		return types.KeyAdmissionDecision{}, fmt.Errorf("decision_version must be a positive integer")
	}
	if revision != nil && *revision <= 0 {
		return types.KeyAdmissionDecision{}, fmt.Errorf("revision must be a positive integer")
	}
	if decisionVersion != nil && revision != nil && *decisionVersion != *revision {
		return types.KeyAdmissionDecision{}, fmt.Errorf("decision_version conflicts with legacy revision")
	}

	if strict {
		if decisionID == "" || decisionVersion == nil {
			return types.KeyAdmissionDecision{}, fmt.Errorf("strict admission mode requires explicit decision_id and decision_version")
		}
	}

	var version int64
	if decisionVersion != nil {
		version = *decisionVersion
	} else if revision != nil {
		version = *revision
	}

	if decisionID == "" {
		if decisionVersion != nil {
			return types.KeyAdmissionDecision{}, fmt.Errorf("decision_id is required with decision_version")
		}
		if revision != nil {
			return types.KeyAdmissionDecision{ID: fmt.Sprintf("legacy-revision:%d", version), Version: version, Legacy: true}, nil
		}
		return types.KeyAdmissionDecision{Legacy: true}, nil
	}
	parsedID, err := uuid.Parse(decisionID)
	if err != nil {
		return types.KeyAdmissionDecision{}, fmt.Errorf("decision_id must be UUID: %v", err)
	}
	if version <= 0 {
		return types.KeyAdmissionDecision{}, fmt.Errorf("decision_version is required with decision_id")
	}
	return types.KeyAdmissionDecision{ID: parsedID.String(), Version: version}, nil
}

func mergeAdmissionDecisionRequest(parent, item AdmissionDecisionRequest) (AdmissionDecisionRequest, error) {
	itemProvided := item.DecisionID != "" || item.DecisionVersion != nil || item.Revision != nil
	if !itemProvided {
		return parent, nil
	}
	parentProvided := parent.DecisionID != "" || parent.DecisionVersion != nil || parent.Revision != nil
	if !parentProvided {
		return item, nil
	}
	parentNormalized, err := normalizeAdmissionDecision(parent.DecisionID, parent.DecisionVersion, parent.Revision, false)
	if err != nil {
		return AdmissionDecisionRequest{}, err
	}
	itemNormalized, err := normalizeAdmissionDecision(item.DecisionID, item.DecisionVersion, item.Revision, false)
	if err != nil {
		return AdmissionDecisionRequest{}, err
	}
	if parentNormalized != itemNormalized {
		return AdmissionDecisionRequest{}, fmt.Errorf("item decision conflicts with bulk decision")
	}
	return item, nil
}
