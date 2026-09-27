package steam

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// rewriteTransport sends every request (steamcommunity.com, api.steampowered.com, …) to the test server,
// keeping the original host in X-Orig-Host so handlers can tell the endpoints apart.
type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("X-Orig-Host", r.URL.Host)
	r2.URL.Scheme = t.target.Scheme
	r2.URL.Host = t.target.Host
	r2.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(r2)
}

// newTestSession returns a logged-in-looking session whose HTTP traffic goes to handler.
func newTestSession(t *testing.T, handler http.Handler) *Session {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	s := NewSession(&http.Client{Transport: rewriteTransport{target: u}}, "")
	s.accessToken = "test-access-token"
	s.sessionID = "test-session-id"
	s.deviceID = "android:test"
	s.oauth.SteamID = SteamID(76561198000000001)
	return s
}
