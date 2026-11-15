package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrNoToken reports that no token is stored for a key. Callers (e.g. a request
// path that prefers OAuth but can fall back to an API key) match it with
// errors.Is to distinguish "not logged in" from a real refresh failure.
var ErrNoToken = errors.New("oauth: no stored token")

// defaultRefreshBuffer refreshes a token this long before its hard expiry.
const defaultRefreshBuffer = 60 * time.Second

const oidcWellKnownPath = "/.well-known/openid-configuration"

// Manager ties the token store, provider registry, and HTTP client together to
// serve fresh access tokens. It is the high-level entrypoint the
// CLI and request paths use.
type Manager struct {
	store    *Store
	registry *Registry
	client   *http.Client
	env      map[string]string
	now      func() time.Time
	buffer   time.Duration
	// refreshLocks serializes concurrent refreshes per key so parallel callers
	// don't each spend the single-use refresh token; the loser reuses the rotated
	// token. refreshMu guards the map (M7).
	refreshMu    sync.Mutex
	refreshLocks map[string]*sync.Mutex
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Store      *Store
	Registry   *Registry
	HTTPClient *http.Client
	Env        map[string]string
	// AllowPresets opts this manager into the baked-in OAuth presets without the
	// operator exporting QG_OAUTH_ALLOW_PRESETS — used by the runtime token
	// refresh for a provider the user chose to sign into, whose preset client
	// identity is public (e.g. xAI). It layers the
	// flag onto Env (or the process environment when Env is nil), preserving any
	// QG_OAUTH_<NAME>_* overrides. Leave false for hermetic tests.
	AllowPresets  bool
	Now           func() time.Time
	RefreshBuffer time.Duration
}

// NewManager builds a Manager, filling defaults.
func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("oauth: manager requires a store")
	}
	registry := opts.Registry
	if registry == nil {
		registry = NewRegistry()
	}
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	buffer := opts.RefreshBuffer
	if buffer <= 0 {
		buffer = defaultRefreshBuffer
	}
	env := opts.Env
	if opts.AllowPresets {
		env = envWithPresetsAllowed(env)
	}
	return &Manager{
		store: opts.Store, registry: registry, client: client,
		env: env, now: now, buffer: buffer,
	}, nil
}

// resolveEndpoints fills missing authorize/token/device endpoints from issuer
// discovery (RFC 8414 then OIDC), leaving any explicitly-pinned endpoint intact.
func (m *Manager) resolveEndpoints(ctx context.Context, cfg Config) (Config, error) {
	if trimmed(cfg.IssuerURL) == "" {
		return cfg, nil
	}
	if cfg.AuthorizationEndpoint != "" && cfg.TokenEndpoint != "" && cfg.DeviceAuthorizationEndpoint != "" {
		return cfg, nil
	}
	// Discovery is best-effort: a failure is non-fatal because pinned endpoints
	// may already be sufficient for the chosen flow. Only merge on success (and
	// never return from the error branch, which would trip the nilerr linter).
	if meta, err := m.discover(ctx, cfg.IssuerURL); err == nil {
		// A discovered endpoint must pass the same https/loopback rule as a
		// configured one before it is merged, so discovery metadata can never
		// silently downgrade the login to an insecure or attacker-controlled
		// endpoint. Fail closed on a bad endpoint rather than merging it.
		if cfg.AuthorizationEndpoint == "" && meta.AuthorizationEndpoint != "" {
			if err := ValidateEndpointURL(meta.AuthorizationEndpoint); err != nil {
				return cfg, fmt.Errorf("oauth: discovered authorization endpoint: %w", err)
			}
			cfg.AuthorizationEndpoint = meta.AuthorizationEndpoint
		}
		if cfg.TokenEndpoint == "" && meta.TokenEndpoint != "" {
			if err := ValidateEndpointURL(meta.TokenEndpoint); err != nil {
				return cfg, fmt.Errorf("oauth: discovered token endpoint: %w", err)
			}
			cfg.TokenEndpoint = meta.TokenEndpoint
		}
		if cfg.DeviceAuthorizationEndpoint == "" && meta.DeviceAuthorizationEndpoint != "" {
			if err := ValidateEndpointURL(meta.DeviceAuthorizationEndpoint); err != nil {
				return cfg, fmt.Errorf("oauth: discovered device authorization endpoint: %w", err)
			}
			cfg.DeviceAuthorizationEndpoint = meta.DeviceAuthorizationEndpoint
		}
	}
	return cfg, nil
}

// discover tries the OAuth (RFC 8414) well-known path, then the OIDC
// openid-configuration path (some issuers publish only the latter).
func (m *Manager) discover(ctx context.Context, issuer string) (ServerMetadata, error) {
	meta, err := DiscoverAuthorizationServer(ctx, m.client, issuer)
	if err == nil && (meta.AuthorizationEndpoint != "" || meta.TokenEndpoint != "") {
		return meta, nil
	}
	oidcURL := strings.TrimRight(strings.TrimSpace(issuer), "/") + oidcWellKnownPath
	return fetchMetadata(ctx, m.client, oidcURL)
}

// GetFresh returns a valid access token for key, refreshing on-demand if the
// stored token is expired or within the refresh buffer. Mirrors
// checkAndRefreshOAuthTokenIfNeeded.
func (m *Manager) GetFresh(ctx context.Context, key string) (string, error) {
	token, err := m.loadToken(key)
	if err != nil {
		return "", err
	}
	// A still-valid token is returned without resolving the provider config, so a
	// readable login never depends on config that is only needed to refresh (e.g.
	// an opt-in preset client_id). Only an actual refresh resolves endpoints.
	if !token.NeedsRefresh(m.now(), m.buffer) {
		return token.AccessToken, nil
	}
	cfg, err := m.resolveConfigForKey(ctx, key)
	if err != nil {
		return "", err
	}
	return m.refreshAndSave(ctx, key, cfg, token)
}

// Handle401 forces a refresh after an upstream 401, returning the new access
// token. Mirrors handleOAuth401Error.
func (m *Manager) Handle401(ctx context.Context, key string) (string, error) {
	token, err := m.loadToken(key)
	if err != nil {
		return "", err
	}
	cfg, err := m.resolveConfigForKey(ctx, key)
	if err != nil {
		return "", err
	}
	return m.refreshAndSave(ctx, key, cfg, token)
}

func (m *Manager) refreshAndSave(ctx context.Context, key string, cfg Config, current Token) (string, error) {
	lock := m.keyLock(key)
	lock.Lock()
	defer lock.Unlock()
	// Re-load inside the critical section: if a concurrent caller already refreshed
	// (the access token changed), reuse it rather than spending the single-use
	// refresh token a second time — the provider would reject the second use (M7).
	if reloaded, err := m.loadToken(key); err == nil && reloaded.AccessToken != current.AccessToken {
		return reloaded.AccessToken, nil
	}
	refreshed, err := Refresh(ctx, m.client, cfg, current, m.now)
	if err != nil {
		return "", err
	}
	if err := m.store.Save(key, refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

// keyLock returns the per-key refresh mutex, creating it on first use, so all
// refreshes for one key are serialized.
func (m *Manager) keyLock(key string) *sync.Mutex {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	if m.refreshLocks == nil {
		m.refreshLocks = map[string]*sync.Mutex{}
	}
	lock, ok := m.refreshLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		m.refreshLocks[key] = lock
	}
	return lock
}

// loadToken loads a stored token for key without resolving any provider config,
// so reading a still-valid login never depends on refresh-only configuration.
func (m *Manager) loadToken(key string) (Token, error) {
	if err := ValidateKey(key); err != nil {
		return Token{}, err
	}
	token, ok, err := m.store.Load(key)
	if err != nil {
		return Token{}, err
	}
	if !ok {
		return Token{}, fmt.Errorf("%w for %q", ErrNoToken, key)
	}
	return token, nil
}

// resolveConfigForKey resolves the provider OAuth config for a provider-token key.
// It is only needed to refresh a token (the endpoints + client identity), not to
// read one.
func (m *Manager) resolveConfigForKey(ctx context.Context, key string) (Config, error) {
	name := strings.TrimPrefix(key, KeyPrefixProvider)
	if name == key {
		return Config{}, fmt.Errorf("oauth: refresh is only supported for provider tokens (got %q)", key)
	}
	cfg, _, err := m.registry.ResolveConfig(name, m.env)
	if err != nil {
		return Config{}, err
	}
	// Fill any missing token/authorize/device endpoints from issuer discovery so a
	// provider configured with only QG_OAUTH_<NAME>_ISSUER_URL can still refresh
	// (refreshAndSave requires the token endpoint).
	cfg, err = m.resolveEndpoints(ctx, cfg)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Logout removes a provider's stored token, reporting whether one was present.
func (m *Manager) Logout(name string) (bool, error) {
	return m.store.Delete(ProviderKey(name))
}

// Reset clears all persistent state in the underlying store.
func (m *Manager) Reset() error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.Reset()
}

// StatusAll returns the status of every provider login.
func (m *Manager) StatusAll() ([]Status, error) {
	return m.store.Status(KeyPrefixProvider)
}
