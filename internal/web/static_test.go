package web

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"shutdowner/internal/power"
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

// The dashboard shows the logo above the card, which needs both the markup to
// point at the asset and the asset to be served.
func TestDashboardShowsTheLogo(t *testing.T) {
	e := newTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(e.sessionCookie(t))

	res := do(t, e.handler, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if !strings.Contains(res.Body.String(), `src="/static/logo.png"`) {
		t.Error("the dashboard does not reference /static/logo.png")
	}

	asset := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/static/logo.png", nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("logo status = %d, want 200", asset.Code)
	}
	if ct := asset.Header().Get("Content-Type"); !strings.Contains(ct, "image/png") {
		t.Errorf("Content-Type = %q, want it to contain %q", ct, "image/png")
	}
	if !bytes.HasPrefix(asset.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
		t.Error("the body does not start with a PNG signature")
	}
}

// actionColour matches the per-action colour declarations in app.css.
var actionColour = regexp.MustCompile(`\.action\[data-action="([a-z]+)"\]\s*\{\s*--action:\s*([^;]+);`)

// Every action the dashboard offers gets a colour, and no two share one. Adding
// an action without styling it would otherwise ship a button that silently
// falls back to looking like Sleep.
func TestEveryActionHasItsOwnColour(t *testing.T) {
	css, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("reading app.css: %v", err)
	}

	colours := map[power.Action]string{}
	for _, m := range actionColour.FindAllStringSubmatch(string(css), -1) {
		colours[power.Action(m[1])] = strings.TrimSpace(m[2])
	}

	usedBy := map[string]power.Action{}
	for _, a := range []power.Action{
		power.ActionShutdown, power.ActionRestart, power.ActionSleep, power.ActionHibernate,
	} {
		colour, ok := colours[a]
		if !ok {
			t.Errorf("app.css sets no --action colour for %q", a)
			continue
		}
		if other, clash := usedBy[colour]; clash {
			t.Errorf("%q and %q are both %s", a, other, colour)
		}
		usedBy[colour] = a
	}
}

// The dialog has to offer all three timings, and the missed banner has to exist
// for app.js to fill in.
func TestDashboardOffersTheWhenControls(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(e.sessionCookie(t))

	body := do(t, e.handler, r).Body.String()
	for _, want := range []string{
		`name="when" value="now"`,
		`name="when" value="in"`,
		`name="when" value="at"`,
		`id="when-in-value"`,
		`id="when-in-unit"`,
		`id="when-at"`,
		`id="missed"`,
		`id="dismiss"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the dashboard is missing %s", want)
		}
	}
}

func TestMissingStaticAssetIs404(t *testing.T) {
	e := newTestEnv(t)
	res := do(t, e.handler, httptest.NewRequest(http.MethodGet, "/static/nope.css", nil))
	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.Code)
	}
}
