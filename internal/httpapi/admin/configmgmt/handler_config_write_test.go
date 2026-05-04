package configmgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestUpdateKeyAllowsChangingKeyValue(t *testing.T) {
	h := newAdminTestHandler(t, `{
		"api_keys":[{"key":"old-key","name":"old","remark":"legacy"}]
	}`)
	body := map[string]any{
		"key":    "new-key",
		"name":   "primary",
		"remark": "rotated",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/admin/keys/old-key", bytes.NewReader(b))
	rec := httptest.NewRecorder()

	h.updateKey(rec, requestWithKeyParam(req, "old-key"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	snap := h.Store.Snapshot()
	if len(snap.APIKeys) != 1 {
		t.Fatalf("expected 1 api key, got %#v", snap.APIKeys)
	}
	if snap.APIKeys[0].Key != "new-key" || snap.APIKeys[0].Name != "primary" || snap.APIKeys[0].Remark != "rotated" {
		t.Fatalf("unexpected updated key: %#v", snap.APIKeys[0])
	}
	if len(snap.Keys) != 1 || snap.Keys[0] != "new-key" {
		t.Fatalf("expected only new key in legacy key list, got %#v", snap.Keys)
	}
}

func TestBatchImportPlainAccountText(t *testing.T) {
	h := newAdminTestHandler(t, `{"keys":["k1"],"accounts":[]}`)
	body := map[string]any{
		"accounts_text": "user@example.com:p1\n13800000000:p2\n# skipped",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/import", bytes.NewReader(b))
	rec := httptest.NewRecorder()

	h.batchImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(resp["imported_accounts"].(float64)); got != 2 {
		t.Fatalf("expected 2 imported accounts, got %d body=%v", got, resp)
	}
	if got := len(h.Store.Accounts()); got != 2 {
		t.Fatalf("expected 2 accounts in store, got %d", got)
	}
	if _, ok := h.Store.FindAccount("user@example.com"); !ok {
		t.Fatal("expected email account to be imported")
	}
	if _, ok := h.Store.FindAccount("13800000000"); !ok {
		t.Fatal("expected mobile account to be imported")
	}
}

func TestBatchImportLegacyJSONTreatsEmailInMobileAsEmail(t *testing.T) {
	h := newAdminTestHandler(t, `{"keys":["k1"],"accounts":[]}`)
	body := map[string]any{
		"accounts": []any{
			map[string]any{
				"mobile":   "legacy@example.com",
				"password": "p1",
				"token":    "runtime-token",
			},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/import", bytes.NewReader(b))
	rec := httptest.NewRecorder()

	h.batchImport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := int(resp["imported_accounts"].(float64)); got != 1 {
		t.Fatalf("expected 1 imported account, got %d body=%v", got, resp)
	}
	acc, ok := h.Store.FindAccount("legacy@example.com")
	if !ok {
		t.Fatal("expected legacy JSON email account to be findable by email")
	}
	if acc.Email != "legacy@example.com" || acc.Mobile != "" {
		t.Fatalf("expected legacy mobile email normalized to email, got %#v", acc)
	}
	if acc.Token != "" {
		t.Fatalf("expected imported runtime token to be ignored, got %q", acc.Token)
	}
}

func TestBatchImportPlainAccountTextRejectsInvalidLine(t *testing.T) {
	h := newAdminTestHandler(t, `{"keys":["k1"],"accounts":[]}`)
	body := map[string]any{"accounts_text": "missing-separator"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/import", bytes.NewReader(b))
	rec := httptest.NewRecorder()

	h.batchImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func requestWithKeyParam(req *http.Request, key string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("key", key)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
