package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/refunddrain"
	"github.com/tokenplatform/publisher/internal/types"
)

type refundDrainServiceStub struct {
	establish func(context.Context, string, []string) (refunddrain.Snapshot, error)
	status    func(context.Context, string) (refunddrain.Snapshot, error)
	release   func(context.Context, string) error
}

func (s *refundDrainServiceStub) Establish(ctx context.Context, drainID string, keyHashes []string) (refunddrain.Snapshot, error) {
	return s.establish(ctx, drainID, keyHashes)
}

func (s *refundDrainServiceStub) Status(ctx context.Context, drainID string) (refunddrain.Snapshot, error) {
	return s.status(ctx, drainID)
}

func (s *refundDrainServiceStub) Release(ctx context.Context, drainID string) error {
	return s.release(ctx, drainID)
}

func TestRefundDrainRoutesAreSynchronousAndBillingScoped(t *testing.T) {
	keyHash := strings.Repeat("a", 64)
	snapshot := refunddrain.Snapshot{
		ProtocolVersion: refunddrain.ProtocolVersion,
		DrainID:         "ent-1",
		KeyCount:        1,
		Ready:           true,
	}
	service := &refundDrainServiceStub{
		establish: func(_ context.Context, drainID string, keyHashes []string) (refunddrain.Snapshot, error) {
			if drainID != "ent-1" || len(keyHashes) != 1 || keyHashes[0] != keyHash {
				t.Fatalf("Establish(%q, %v)", drainID, keyHashes)
			}
			return snapshot, nil
		},
		status: func(_ context.Context, drainID string) (refunddrain.Snapshot, error) {
			if drainID != "ent-1" {
				t.Fatalf("Status(%q)", drainID)
			}
			return snapshot, nil
		},
		release: func(_ context.Context, drainID string) error {
			if drainID != "ent-1" {
				t.Fatalf("Release(%q)", drainID)
			}
			return nil
		},
	}
	server := NewServer(&stubPublisher{}, fixtureAuth(t))
	server.SetRefundDrainCoordinator(service)
	handler := server.Handler()

	put := httptest.NewRequest(http.MethodPut, "/billing/api/v1/refund-drains/ent-1", strings.NewReader(`{"key_hashes":["`+keyHash+`"]}`))
	put.Header.Set("Authorization", "Bearer billing-token")
	put.Header.Set("Content-Type", "application/json")
	putResponse := httptest.NewRecorder()
	handler.ServeHTTP(putResponse, put)
	if putResponse.Code != http.StatusOK || !strings.Contains(putResponse.Body.String(), `"ready":true`) {
		t.Fatalf("PUT status=%d body=%s", putResponse.Code, putResponse.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/billing/api/v1/refund-drains/ent-1", nil)
	get.Header.Set("Authorization", "Bearer billing-token")
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/billing/api/v1/refund-drains/ent-1", nil)
	deleteRequest.Header.Set("Authorization", "Bearer billing-token")
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusAccepted || !strings.Contains(deleteResponse.Body.String(), `"status":"accepted"`) {
		t.Fatalf("DELETE status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}

	admin := httptest.NewRequest(http.MethodGet, "/billing/api/v1/refund-drains/ent-1", nil)
	admin.Header.Set("Authorization", "Bearer admin-token")
	adminResponse := httptest.NewRecorder()
	handler.ServeHTTP(adminResponse, admin)
	if adminResponse.Code != http.StatusForbidden {
		t.Fatalf("admin status=%d body=%s", adminResponse.Code, adminResponse.Body.String())
	}
}

func TestRefundDrainRouteFailsClosedWithoutCoordinator(t *testing.T) {
	handler := NewServer(&stubPublisher{}, fixtureAuth(t)).Handler()
	request := httptest.NewRequest(http.MethodPut, "/billing/api/v1/refund-drains/ent-1", strings.NewReader(`{"key_hashes":[]}`))
	request.Header.Set("Authorization", "Bearer billing-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "REFUND_DRAIN_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestNormalizeAdmissionDecisionRejectsVersionAliasesThatConflict(t *testing.T) {
	_, err := normalizeAdmissionDecision(
		uuid.NewString(),
		int64Ptr(12),
		int64Ptr(11),
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "decision_version") {
		t.Fatalf("expected decision_version/revision conflict, got %v", err)
	}
}

func TestAdmissionDeliveryReceiptRouteAndBillingAuthBoundary(t *testing.T) {
	batchID := types.BatchID(uuid.New())
	pub := &stubPublisher{
		getAdmissionDeliveryFunc: func(_ context.Context, got types.BatchID) (publisher.AdmissionDeliveryReceipt, error) {
			if got != batchID {
				t.Fatalf("batch id=%s want %s", got, batchID)
			}
			return publisher.AdmissionDeliveryReceipt{BatchID: got, Status: publisher.AdmissionDeliveryPending, Total: 1, PendingCount: 1}, nil
		},
	}
	h := NewServer(pub, fixtureAuth(t)).Handler()
	path := "/billing/api/v1/key-admission-deliveries/" + batchID.String()

	billingReq := httptest.NewRequest(http.MethodGet, path, nil)
	billingReq.Header.Set("Authorization", "Bearer billing-token")
	billingResp := httptest.NewRecorder()
	h.ServeHTTP(billingResp, billingReq)
	if billingResp.Code != http.StatusOK || !strings.Contains(billingResp.Body.String(), `"status":"pending"`) {
		t.Fatalf("billing receipt response=%d body=%s", billingResp.Code, billingResp.Body.String())
	}

	adminReq := httptest.NewRequest(http.MethodGet, path, nil)
	adminReq.Header.Set("Authorization", "Bearer admin-token")
	adminResp := httptest.NewRecorder()
	h.ServeHTTP(adminResp, adminReq)
	if adminResp.Code != http.StatusForbidden {
		t.Fatalf("cross-namespace receipt access=%d want 403 body=%s", adminResp.Code, adminResp.Body.String())
	}
}

func TestAtomicKeyActivationRoutePassesOneCompleteContractToPublisher(t *testing.T) {
	keyID := uuid.NewString()
	keyHash := strings.Repeat("a", 64)
	decisionID := uuid.NewString()
	called := false
	pub := &stubPublisher{
		activateKeyFunc: func(_ context.Context, meta *types.KeyMeta, hasBalance bool, reason string, decision types.KeyAdmissionDecision) (types.BatchID, error) {
			called = true
			if meta.KeyID != keyID || meta.KeyHash != keyHash || meta.Status != "active" {
				t.Fatalf("unexpected KeyMeta: %+v", meta)
			}
			if hasBalance || reason != "account_debt" {
				t.Fatalf("unexpected admission: allowed=%v reason=%q", hasBalance, reason)
			}
			if decision.ID != decisionID || decision.Version != 7 || decision.Legacy {
				t.Fatalf("unexpected decision: %+v", decision)
			}
			return types.BatchID(uuid.New()), nil
		},
	}
	h := NewServer(pub, fixtureAuth(t)).Handler()
	body := `{
		"key_meta":{"key_id":"` + keyID + `","key_hash":"` + keyHash + `","status":"active","expires_at":0,"ip_whitelist":[],"allowed_models":[]},
		"has_balance":false,
		"reason":"account_debt",
		"decision_id":"` + decisionID + `",
		"decision_version":7
	}`
	req := httptest.NewRequest(http.MethodPut, "/billing/api/v1/key-activation/"+keyID, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer billing-token")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted || !called {
		t.Fatalf("activation status=%d called=%v body=%s", resp.Code, called, resp.Body.String())
	}
}

func TestAdmissionHandlersRejectUnknownAndTrailingJSON(t *testing.T) {
	keyID := uuid.NewString()
	h := NewServer(&stubPublisher{}, fixtureAuth(t)).Handler()
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "single unknown", method: http.MethodPut, path: "/billing/api/v1/key-balance/" + keyID, body: `{"has_balance":true,"typo_version":1}`},
		{name: "single trailing", method: http.MethodPut, path: "/billing/api/v1/key-balance/" + keyID, body: `{"has_balance":true} {}`},
		{name: "bulk unknown", method: http.MethodPut, path: "/billing/api/v1/key-balance/bulk", body: `{"items":[{"key_id":"` + keyID + `","has_balance":true,"typo":1}]}`},
		{name: "delete unknown", method: http.MethodDelete, path: "/billing/api/v1/key-balance/" + keyID, body: `{"typo_version":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer billing-token")
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 body=%s", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestAdmissionStrictModeRequiresExplicitDecisionOnHTTPWriteAndDelete(t *testing.T) {
	keyID := uuid.NewString()
	srv := NewServer(&stubPublisher{}, fixtureAuth(t))
	srv.SetKeyAdmissionStrictMode(true)
	h := srv.Handler()

	for _, tc := range []struct {
		method string
		body   string
	}{
		{method: http.MethodPut, body: `{"has_balance":true}`},
		{method: http.MethodDelete, body: `{}`},
	} {
		req := httptest.NewRequest(tc.method, "/billing/api/v1/key-balance/"+keyID, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer billing-token")
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, req)
		if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "strict admission mode") {
			t.Fatalf("method=%s status=%d body=%s", tc.method, resp.Code, resp.Body.String())
		}
	}

	decisionID := uuid.NewString()
	req := httptest.NewRequest(http.MethodPut, "/billing/api/v1/key-balance/"+keyID,
		strings.NewReader(`{"has_balance":true,"decision_id":"`+decisionID+`","decision_version":1}`))
	req.Header.Set("Authorization", "Bearer billing-token")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("valid strict write status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestAdmissionSetRequiresHasBalancePresence(t *testing.T) {
	keyID := uuid.NewString()
	h := NewServer(&stubPublisher{}, fixtureAuth(t)).Handler()
	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{name: "single", path: "/billing/api/v1/key-balance/" + keyID, body: `{"reason":"account_debt"}`},
		{name: "bulk item", path: "/billing/api/v1/key-balance/bulk", body: `{"reason":"account_debt","items":[{"key_id":"` + keyID + `"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer billing-token")
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "has_balance is required") {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestBulkTopLevelReasonAppliesOnlyToDeniedItems(t *testing.T) {
	deniedID := uuid.NewString()
	allowedID := uuid.NewString()
	var captured []types.KeyAdmissionItem
	pub := &stubPublisher{
		setKeyAdmissionBulkFunc: func(_ context.Context, items []types.KeyAdmissionItem) (types.BatchID, error) {
			captured = append([]types.KeyAdmissionItem(nil), items...)
			return types.BatchID(uuid.New()), nil
		},
	}
	h := NewServer(pub, fixtureAuth(t)).Handler()
	body := `{"reason":"account_debt","items":[` +
		`{"key_id":"` + deniedID + `","has_balance":false},` +
		`{"key_id":"` + allowedID + `","has_balance":true}]}`
	req := httptest.NewRequest(http.MethodPut, "/billing/api/v1/key-balance/bulk", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer billing-token")
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("mixed bulk status=%d body=%s", resp.Code, resp.Body.String())
	}
	if len(captured) != 2 || captured[0].Reason != "account_debt" || captured[1].Reason != "" {
		t.Fatalf("bulk reasons=%+v", captured)
	}
}

func TestNormalizeAdmissionDecisionMapsLegacyRevisionWithoutDroppingIt(t *testing.T) {
	got, err := normalizeAdmissionDecision("", nil, int64Ptr(42), false)
	if err != nil {
		t.Fatalf("normalize legacy revision: %v", err)
	}
	if got.Version != 42 {
		t.Fatalf("version=%d want 42", got.Version)
	}
	if got.ID != "legacy-revision:42" || !got.Legacy {
		t.Fatalf("legacy revision mapping = %+v", got)
	}
}

func TestNormalizeAdmissionDecisionStrictRequiresExplicitIDAndPositiveVersion(t *testing.T) {
	cases := []struct {
		name            string
		decisionID      string
		decisionVersion *int64
		revision        *int64
	}{
		{name: "missing all"},
		{name: "revision alias has no explicit id", revision: int64Ptr(9)},
		{name: "zero version", decisionID: uuid.NewString(), decisionVersion: int64Ptr(0)},
		{name: "id without version", decisionID: uuid.NewString()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeAdmissionDecision(tc.decisionID, tc.decisionVersion, tc.revision, true); err == nil {
				t.Fatal("expected strict validation error")
			}
		})
	}

	wantID := uuid.NewString()
	got, err := normalizeAdmissionDecision(wantID, int64Ptr(10), int64Ptr(10), true)
	if err != nil {
		t.Fatalf("strict valid decision: %v", err)
	}
	if got.ID != wantID || got.Version != 10 || got.Legacy {
		t.Fatalf("strict decision = %+v", got)
	}
}

func TestNormalizeAdmissionDecisionCanonicalizesUUIDIdentity(t *testing.T) {
	id := uuid.New()
	upper := strings.ToUpper(id.String())
	got, err := normalizeAdmissionDecision(upper, int64Ptr(5), nil, true)
	if err != nil {
		t.Fatalf("normalize uppercase UUID: %v", err)
	}
	if got.ID != id.String() {
		t.Fatalf("decision id=%q want canonical %q", got.ID, id.String())
	}
}
