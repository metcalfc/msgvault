package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/msgvault/internal/carddavserver"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/testutil"
)

const servedTestAPIKey = "served-test-api-key-0123456789"

type servedFixture struct {
	srv      *Server
	tokenDir string
	password string
}

func newServedFixture(t *testing.T, mutate func(cfg *config.Config)) servedFixture {
	t.Helper()
	dataDir := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{APIPort: 8080, APIKey: servedTestAPIKey, TrustedProxies: []string{"127.0.0.1"}},
		Data:   config.DataConfig{DataDir: dataDir},
	}
	cfg.CardDAV.Serve.Enabled = true
	cfg.CardDAV.Serve.DisplayName = "Served"
	if mutate != nil {
		mutate(cfg)
	}
	handler, err := carddavserver.New(carddavserver.Options{Store: testutil.NewTestStore(t), DisplayName: cfg.CardDAV.Serve.DisplayName})
	require.NoError(t, err)
	srv := NewServerWithOptions(ServerOptions{Config: cfg, Store: &mockStore{}, Logger: testLogger(), CardDAVServed: handler})
	require.NotNil(t, srv.cardDAVServed)
	password := "correct-horse-battery-staple"
	_, err = carddavserver.SaveCredential(cfg.TokensDir(), "device", password)
	require.NoError(t, err)
	return servedFixture{srv: srv, tokenDir: cfg.TokensDir(), password: password}
}

type servedRequest struct {
	method, path, remote, host string
	basic                      *[2]string
	headers                    map[string]string
}

func (f servedFixture) do(t *testing.T, in servedRequest) *httptest.ResponseRecorder {
	t.Helper()
	method := in.method
	if method == "" {
		method = "PROPFIND"
	}
	path := in.path
	if path == "" {
		path = "/dav/addressbooks/me/msgvault/"
	}
	request := httptest.NewRequest(method, path, strings.NewReader(""))
	request.RemoteAddr = "127.0.0.1:40000"
	if in.remote != "" {
		request.RemoteAddr = in.remote
	}
	if in.host != "" {
		request.Host = in.host
	}
	request.Header.Set("Depth", "0")
	if in.basic != nil {
		request.SetBasicAuth(in.basic[0], in.basic[1])
	}
	for key, value := range in.headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(recorder, request)
	return recorder
}

func TestServedBookRequiresBasicAndAcceptsDeviceCredentialOnLoopback(t *testing.T) {
	assert := assert.New(t)
	f := newServedFixture(t, nil)
	good := &[2]string{"device", f.password}

	unauthenticated := f.do(t, servedRequest{})
	assert.Equal(http.StatusUnauthorized, unauthenticated.Code)
	assert.Contains(unauthenticated.Header().Get("WWW-Authenticate"), "Basic")

	ok := f.do(t, servedRequest{basic: good})
	assert.Equal(http.StatusMultiStatus, ok.Code)
	assert.Contains(ok.Body.String(), "<C:addressbook/>")

	wellKnown := f.do(t, servedRequest{method: http.MethodGet, path: carddavserver.WellKnownPath})
	assert.Equal(http.StatusMovedPermanently, wellKnown.Code)
	assert.Equal("/dav/", wellKnown.Header().Get("Location"))

	options := f.do(t, servedRequest{method: http.MethodOptions})
	assert.Equal(http.StatusOK, options.Code)

	wrong := f.do(t, servedRequest{basic: &[2]string{"device", "not-the-password"}})
	assert.Equal(http.StatusUnauthorized, wrong.Code)
}

func TestServedBookCredentialDomainsDoNotOverlap(t *testing.T) {
	assert := assert.New(t)
	f := newServedFixture(t, nil)

	apiKeyOnDAV := f.do(t, servedRequest{headers: map[string]string{"X-Api-Key": servedTestAPIKey}})
	assert.Equal(http.StatusUnauthorized, apiKeyOnDAV.Code)
	bearerOnDAV := f.do(t, servedRequest{headers: map[string]string{"Authorization": "Bearer " + servedTestAPIKey}})
	assert.Equal(http.StatusUnauthorized, bearerOnDAV.Code)

	deviceOnAPI := f.do(t, servedRequest{method: http.MethodGet, path: "/api/v1/stats", basic: &[2]string{"device", f.password}})
	assert.Equal(http.StatusUnauthorized, deviceOnAPI.Code)
}

func TestServedBookRefusesBasicOverPlainHTTPUnlessAllowed(t *testing.T) {
	assert := assert.New(t)
	good := &[2]string{"device", ""}

	f := newServedFixture(t, nil)
	good[1] = f.password
	remote := f.do(t, servedRequest{basic: good, remote: "100.100.1.2:5000"})
	assert.Equal(http.StatusForbidden, remote.Code)

	viaProxyHTTPS := f.do(t, servedRequest{basic: good, headers: map[string]string{
		"X-Forwarded-Proto": "https", "X-Forwarded-For": "100.100.1.2", "X-Forwarded-Host": "host.tailnet.ts.net",
	}})
	assert.Equal(http.StatusMultiStatus, viaProxyHTTPS.Code)

	viaProxyHTTP := f.do(t, servedRequest{basic: good, headers: map[string]string{
		"X-Forwarded-Proto": "http", "X-Forwarded-For": "100.100.1.2",
	}})
	assert.Equal(http.StatusForbidden, viaProxyHTTP.Code)

	allowed := newServedFixture(t, func(cfg *config.Config) {
		cfg.CardDAV.Serve.AllowPlainHTTPFrom = []string{"100.64.0.0/10"}
	})
	tailnet := allowed.do(t, servedRequest{basic: &[2]string{"device", allowed.password}, remote: "100.100.1.2:5000"})
	assert.Equal(http.StatusMultiStatus, tailnet.Code)
	elsewhere := allowed.do(t, servedRequest{basic: &[2]string{"device", allowed.password}, remote: "203.0.113.9:5000"})
	assert.Equal(http.StatusForbidden, elsewhere.Code)
}

func TestServedBookBypassesKeylessHostGuard(t *testing.T) {
	f := newServedFixture(t, func(cfg *config.Config) { cfg.Server.APIKey = "" })
	response := f.do(t, servedRequest{basic: &[2]string{"device", f.password}, host: "host.tailnet.ts.net"})
	assert.Equal(t, http.StatusMultiStatus, response.Code)
}

func TestServedBookLocksOutRepeatedFailuresPerClient(t *testing.T) {
	assert := assert.New(t)
	f := newServedFixture(t, nil)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	f.srv.cardDAVServed.now = func() time.Time { return now }
	bad := &[2]string{"device", "wrong-password-here"}
	for range cardDAVServedFailureLimit {
		assert.Equal(http.StatusUnauthorized, f.do(t, servedRequest{basic: bad}).Code)
	}
	locked := f.do(t, servedRequest{basic: bad})
	assert.Equal(http.StatusTooManyRequests, locked.Code)
	assert.NotEmpty(locked.Header().Get("Retry-After"))
	// The right password is refused too while the pair is locked.
	assert.Equal(http.StatusTooManyRequests, f.do(t, servedRequest{basic: &[2]string{"device", f.password}}).Code)
	// Another client, identified through the trusted proxy, is unaffected.
	other := f.do(t, servedRequest{basic: &[2]string{"device", f.password}, headers: map[string]string{
		"X-Forwarded-Proto": "https", "X-Forwarded-For": "100.100.9.9",
	}})
	assert.Equal(http.StatusMultiStatus, other.Code)

	now = now.Add(cardDAVServedLockInitial + time.Second)
	assert.Equal(http.StatusMultiStatus, f.do(t, servedRequest{basic: &[2]string{"device", f.password}}).Code)
}

func TestServedBookRequiresTailscaleLoginWhenConfigured(t *testing.T) {
	assert := assert.New(t)
	f := newServedFixture(t, func(cfg *config.Config) { cfg.CardDAV.Serve.RequireTailscaleLogin = "owner@example.test" })
	good := &[2]string{"device", f.password}
	missing := f.do(t, servedRequest{basic: good, headers: map[string]string{"X-Forwarded-Proto": "https"}})
	assert.Equal(http.StatusForbidden, missing.Code)
	wrong := f.do(t, servedRequest{basic: good, headers: map[string]string{
		"X-Forwarded-Proto": "https", "Tailscale-User-Login": "someone@example.test",
	}})
	assert.Equal(http.StatusForbidden, wrong.Code)
	right := f.do(t, servedRequest{basic: good, headers: map[string]string{
		"X-Forwarded-Proto": "https", "Tailscale-User-Login": "Owner@example.test",
	}})
	assert.Equal(http.StatusMultiStatus, right.Code)
	// The header is not trusted from a direct connection.
	direct := f.do(t, servedRequest{basic: good, remote: "100.100.1.2:5000", headers: map[string]string{
		"Tailscale-User-Login": "owner@example.test",
	}})
	assert.Equal(http.StatusForbidden, direct.Code)
}

func TestServedBookPicksUpCredentialChangesWithoutRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newServedFixture(t, nil)
	assert.Equal(http.StatusMultiStatus, f.do(t, servedRequest{basic: &[2]string{"device", f.password}}).Code)

	time.Sleep(20 * time.Millisecond)
	_, err := carddavserver.SaveCredential(f.tokenDir, "phone", "another-long-password")
	require.NoError(err)
	assert.Equal(http.StatusUnauthorized, f.do(t, servedRequest{basic: &[2]string{"device", f.password}}).Code)
	assert.Equal(http.StatusMultiStatus, f.do(t, servedRequest{basic: &[2]string{"phone", "another-long-password"}}).Code)

	require.NoError(carddavserver.ClearCredential(f.tokenDir))
	assert.Equal(http.StatusUnauthorized, f.do(t, servedRequest{basic: &[2]string{"phone", "another-long-password"}}).Code)
}

func TestServedBookAuthenticatedRequestsBypassGlobalRateLimit(t *testing.T) {
	f := newServedFixture(t, nil)
	good := &[2]string{"device", f.password}
	for i := range 40 {
		response := f.do(t, servedRequest{basic: good, headers: map[string]string{"X-Forwarded-Proto": "https"}})
		require.Equal(t, http.StatusMultiStatus, response.Code, "request %d", i)
	}
}
