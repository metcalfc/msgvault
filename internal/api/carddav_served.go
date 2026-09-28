package api

import (
	"crypto/sha256"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.kenn.io/msgvault/internal/carddavserver"
	"go.kenn.io/msgvault/internal/config"
)

const (
	cardDAVServedRealm = `Basic realm="msgvault contacts", charset="UTF-8"`
	// Failed authentications per window before a (username, client) pair is
	// locked, and the lock schedule.
	cardDAVServedFailureLimit  = 10
	cardDAVServedFailureWindow = 5 * time.Minute
	cardDAVServedLockInitial   = time.Minute
	cardDAVServedLockMaximum   = time.Hour
	// Verified credentials are remembered so a device's burst of requests
	// costs one argon2 derivation, not one per request.
	cardDAVServedVerifiedTTL = 10 * time.Minute
	tailscaleLoginHeader     = "Tailscale-User-Login"
)

// cardDAVServedGate authenticates device requests for the served address
// book. It is the only place the device credential is accepted, and it never
// accepts the API key, a browser session, or an agent token.
type cardDAVServedGate struct {
	handler   *carddavserver.Handler
	cfg       config.CardDAVServeConfig
	plainHTTP []netip.Prefix
	tokenDir  string
	logger    *slog.Logger
	server    *Server
	now       func() time.Time

	mu       sync.Mutex
	verified map[[32]byte]time.Time
	failures map[string]*cardDAVServedFailure
	// stamp identifies the credential file the verified cache was built
	// from, so a password change takes effect on the next request.
	stamp string
}

type cardDAVServedFailure struct {
	count       int
	windowStart time.Time
	lockedUntil time.Time
	lockLength  time.Duration
}

func newCardDAVServedGate(server *Server, handler *carddavserver.Handler, cfg *config.Config, logger *slog.Logger) (*cardDAVServedGate, error) {
	prefixes, err := cfg.CardDAV.Serve.PlainHTTPPrefixes()
	if err != nil {
		return nil, err
	}
	return &cardDAVServedGate{
		handler: handler, cfg: cfg.CardDAV.Serve, plainHTTP: prefixes, tokenDir: cfg.TokensDir(),
		logger: logger, server: server, now: time.Now,
		verified: map[[32]byte]time.Time{}, failures: map[string]*cardDAVServedFailure{},
	}, nil
}

// servedPath reports whether the request targets the served address book.
func (g *cardDAVServedGate) servedPath(requestPath string) bool {
	prefix := g.handler.Prefix()
	return requestPath == carddavserver.WellKnownPath || requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/")
}

// ServeHTTP enforces the credential and transport rules, then hands the
// request to the DAV handler.
func (g *cardDAVServedGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == carddavserver.WellKnownPath {
		g.handler.WellKnown().ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodOptions {
		g.handler.ServeHTTP(w, r)
		return
	}
	username, password, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", cardDAVServedRealm)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if reason := g.plaintextReason(r); reason != "" {
		g.logger.Warn("carddav served: refused Basic credentials over an unencrypted path",
			"remote_addr", r.RemoteAddr, "reason", reason)
		http.Error(w, "device credentials are accepted only over HTTPS or an allowed private network", http.StatusForbidden)
		return
	}
	client := g.clientIdentity(r)
	if retryAfter, locked := g.locked(username, client); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		http.Error(w, "too many failed authentications", http.StatusTooManyRequests)
		return
	}
	if !g.authenticate(username, password) {
		g.recordFailure(username, client)
		w.Header().Set("WWW-Authenticate", cardDAVServedRealm)
		http.Error(w, "invalid device credentials", http.StatusUnauthorized)
		return
	}
	if g.cfg.RequireTailscaleLogin != "" {
		login := r.Header.Get(tailscaleLoginHeader)
		if !g.server.isTrustedProxy(r) || !strings.EqualFold(login, g.cfg.RequireTailscaleLogin) {
			g.logger.Warn("carddav served: Tailscale login did not match", "remote_addr", r.RemoteAddr, "login", login)
			http.Error(w, "request is not from the required Tailscale identity", http.StatusForbidden)
			return
		}
	}
	g.handler.ServeHTTP(w, r)
}

// authenticated reports whether the request carries a valid device
// credential, using the verified cache. It never records a failure; the
// rate-limit exemption calls it and must not count as an attempt.
func (g *cardDAVServedGate) authenticated(r *http.Request) bool {
	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}
	key := verifiedKey(username, password)
	g.mu.Lock()
	expires, hit := g.verified[key]
	g.mu.Unlock()
	return hit && g.now().Before(expires)
}

func verifiedKey(username, password string) [32]byte {
	return sha256.Sum256([]byte(username + "\x00" + password))
}

// authenticate verifies against the credential file, consulting and filling
// the verified cache.
func (g *cardDAVServedGate) authenticate(username, password string) bool {
	key := verifiedKey(username, password)
	now := g.now()
	stamp := credentialStamp(g.tokenDir)
	g.mu.Lock()
	if stamp != g.stamp {
		g.verified = map[[32]byte]time.Time{}
		g.stamp = stamp
	}
	expires, hit := g.verified[key]
	g.mu.Unlock()
	if hit && now.Before(expires) {
		return true
	}
	credential, err := carddavserver.LoadCredential(g.tokenDir)
	if err != nil {
		if !errors.Is(err, carddavserver.ErrNoCredential) {
			g.logger.Error("carddav served: credential file unreadable", "error", err)
		}
		return false
	}
	if !credential.Verify(username, password) {
		return false
	}
	g.mu.Lock()
	g.verified[key] = now.Add(cardDAVServedVerifiedTTL)
	g.mu.Unlock()
	return true
}

// credentialStamp summarizes the credential file's identity cheaply. A
// missing file has its own stamp so clearing the credential also drops the
// cache.
func credentialStamp(tokenDir string) string {
	info, err := os.Stat(filepath.Join(tokenDir, carddavserver.CredentialFilename))
	if err != nil {
		return "absent"
	}
	return strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
}

// plaintextReason returns why Basic credentials must not be accepted on this
// connection, or "" when the path is encrypted or explicitly allowed.
func (g *cardDAVServedGate) plaintextReason(r *http.Request) string {
	if r.TLS != nil {
		return ""
	}
	addr, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		return "unparseable remote address"
	}
	addr = addr.Unmap()
	for _, prefix := range g.plainHTTP {
		if prefix.Contains(addr) {
			return ""
		}
	}
	if !addr.IsLoopback() {
		return "plain HTTP from a non-loopback address"
	}
	if g.server.isTrustedProxy(r) {
		scheme, _, err := g.server.effectiveRequestOrigin(r)
		if err != nil {
			return "invalid forwarded headers"
		}
		if scheme == schemeHTTPS {
			return ""
		}
		if !forwardedHeadersPresent(r) {
			return ""
		}
		return "trusted proxy forwarded a plain HTTP request"
	}
	if forwardedHeadersPresent(r) {
		return "forwarded headers from an untrusted proxy"
	}
	return ""
}

func forwardedHeadersPresent(r *http.Request) bool {
	for _, name := range []string{"Forwarded", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-For"} {
		if r.Header.Get(name) != "" {
			return true
		}
	}
	return false
}

// clientIdentity keys the failure throttle. Behind a trusted proxy every
// connection is loopback, so the forwarded client address is used instead.
func (g *cardDAVServedGate) clientIdentity(r *http.Request) string {
	if g.server.isTrustedProxy(r) {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first, _, _ := strings.Cut(forwarded, ",")
			return strings.TrimSpace(first)
		}
	}
	return clientIP(r)
}

func (g *cardDAVServedGate) locked(username, client string) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.failures[username+"\x00"+client]
	if !ok {
		return 0, false
	}
	now := g.now()
	if now.Before(entry.lockedUntil) {
		return entry.lockedUntil.Sub(now), true
	}
	return 0, false
}

func (g *cardDAVServedGate) recordFailure(username, client string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := username + "\x00" + client
	now := g.now()
	entry, ok := g.failures[key]
	if !ok || now.Sub(entry.windowStart) > cardDAVServedFailureWindow {
		if !ok {
			entry = &cardDAVServedFailure{}
			g.failures[key] = entry
		}
		entry.count = 0
		entry.windowStart = now
	}
	entry.count++
	if entry.count < cardDAVServedFailureLimit {
		return
	}
	if entry.lockLength == 0 {
		entry.lockLength = cardDAVServedLockInitial
	} else {
		entry.lockLength = min(entry.lockLength*2, cardDAVServedLockMaximum)
	}
	entry.lockedUntil = now.Add(entry.lockLength)
	entry.count = 0
	entry.windowStart = now
	g.logger.Warn("carddav served: locked out after repeated failures",
		"username", username, "client", client, "lock", entry.lockLength.String())
	// Keep the map bounded under a spray of usernames.
	if len(g.failures) > 10_000 {
		for other, candidate := range g.failures {
			if now.After(candidate.lockedUntil) && now.Sub(candidate.windowStart) > cardDAVServedFailureWindow {
				delete(g.failures, other)
			}
		}
	}
}
