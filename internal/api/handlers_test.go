package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// checkPathBodyKey enforces the publisher API convention: when a URL
// path identifier (e.g. {provider_id}) coexists with the same logical
// field in the JSON body, the path is authoritative and the body must
// either omit it or supply the identical value. See 2026-05-16 audit
// (docs/2026-05-16-api-field-ambiguity-audit.md §5 P1 #8).

func TestCheckPathBodyKey_BodyEmpty_OK(t *testing.T) {
	rec := httptest.NewRecorder()
	if ok := checkPathBodyKey(rec, "model_id", "gpt-4o", ""); !ok {
		t.Fatalf("body empty should pass; got rec.Code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCheckPathBodyKey_BodyMatchesPath_OK(t *testing.T) {
	rec := httptest.NewRecorder()
	if ok := checkPathBodyKey(rec, "model_id", "gpt-4o", "gpt-4o"); !ok {
		t.Fatalf("body matches path should pass; got rec.Code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCheckPathBodyKey_BodyDiffers_400(t *testing.T) {
	rec := httptest.NewRecorder()
	if ok := checkPathBodyKey(rec, "model_id", "gpt-4o", "claude-3-5-sonnet"); ok {
		t.Fatal("body != path should fail (path is authoritative)")
	}
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "body.model_id does not match path model_id") {
		t.Errorf("error message should name the field; body=%s", rec.Body.String())
	}
}
