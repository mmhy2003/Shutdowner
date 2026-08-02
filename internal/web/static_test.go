package web

import (
	"bytes"
	"encoding/binary"
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

func TestFaviconIsServedWithoutASession(t *testing.T) {
	e := newTestEnv(t)
	// Deliberately no cookie: a browser asks for /favicon.ico while it is
	// showing the login page, before anyone has signed in.
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if ct := res.Header().Get("Content-Type"); ct != faviconContentType {
		t.Errorf("Content-Type = %q, want %q", ct, faviconContentType)
	}
	// An .ico starts with a two-byte zero and a type of 1, then the number of
	// images. Checking the count as well is what would catch a truncated or
	// half-written file being embedded.
	body := res.Body.Bytes()
	if len(body) < 6 {
		t.Fatalf("body is %d bytes, too short to be an .ico", len(body))
	}
	if header := body[:4]; !bytes.Equal(header, []byte{0, 0, 1, 0}) {
		t.Errorf("body starts with %x, want an .ico header of 00000100", header)
	}
	if n := binary.LittleEndian.Uint16(body[4:6]); n == 0 {
		t.Error("the .ico declares no images")
	}
}

// The pages have to point at the same URL the browser probes on its own, or the
// icon is fetched twice and cached twice.
func TestPagesLinkTheFavicon(t *testing.T) {
	e := newTestEnv(t)
	dashboard := httptest.NewRequest(http.MethodGet, "/", nil)
	dashboard.AddCookie(e.sessionCookie(t))

	for _, page := range []struct {
		name string
		req  *http.Request
	}{
		{"login", httptest.NewRequest(http.MethodGet, "/login", nil)},
		{"dashboard", dashboard},
	} {
		t.Run(page.name, func(t *testing.T) {
			res := do(t, e.handler, page.req)
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.Code)
			}
			if !strings.Contains(res.Body.String(), `<link rel="icon" href="/favicon.ico">`) {
				t.Error("the page does not link /favicon.ico")
			}
		})
	}
}

func TestMissingStaticAssetIs404(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/static/nope.css", nil))
	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.Code)
	}
}
