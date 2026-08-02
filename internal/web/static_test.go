package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticAssetsAreServed(t *testing.T) {
	e := newTestEnv(t)
	tests := []struct {
		path        string
		contentType string
		mustContain string
	}{
		{"/static/app.css", "text/css", ".action"},
		{"/static/app.js", "javascript", "/api/status"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			res := do(t, e.handler, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.Code)
			}
			if ct := res.Header().Get("Content-Type"); !strings.Contains(ct, tt.contentType) {
				t.Errorf("Content-Type = %q, want it to contain %q", ct, tt.contentType)
			}
			if !strings.Contains(res.Body.String(), tt.mustContain) {
				t.Errorf("%s does not contain %q", tt.path, tt.mustContain)
			}
		})
	}
}

func TestStaticAssetsNeedNoSession(t *testing.T) {
	e := newTestEnv(t)
	// Deliberately no cookie: the CSP references these files by URL, so gating
	// them behind auth would break the login page.
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if res.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", res.Code)
	}
}

func TestMissingStaticAssetIs404(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/static/nope.css", nil))
	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.Code)
	}
}
