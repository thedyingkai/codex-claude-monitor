// Package server implements the authenticated quota monitor HTTP API.
package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"codex-claude-monitor/internal/firmware"
	"codex-claude-monitor/internal/model"
	"codex-claude-monitor/internal/store"
)

const (
	MaxReportBodySize = 64 << 10
	MaxFutureSkew     = 5 * time.Minute
	MaxReportAge      = 15 * time.Minute

	defaultAgentRequestsPerMinute   = 120
	defaultDisplayRequestsPerMinute = 120
	defaultAuthAttemptsPerMinute    = 240
	defaultGlobalAuthPerMinute      = 6000
	defaultConcurrentAuth           = 8
)

type Config struct {
	Store *store.Store

	// FirmwareDir contains a locally published manifest and image. An empty
	// value leaves the authenticated firmware endpoints available but empty.
	FirmwareDir string

	// Logger defaults to a discard logger. Request headers and bearer tokens
	// are never included in log records.
	Logger *slog.Logger
	// Now is injectable for deterministic tests.
	Now func() time.Time

	AgentRequestsPerMinute      int
	DisplayRequestsPerMinute    int
	AuthAttemptsPerMinute       int
	GlobalAuthAttemptsPerMinute int
	ConcurrentAuth              int
}

type Server struct {
	store       *store.Store
	logger      *slog.Logger
	now         func() time.Time
	mux         *http.ServeMux
	firmwareDir string

	limiter             *fixedWindowLimiter
	agentRateLimit      int
	displayRateLimit    int
	authRateLimit       int
	globalAuthRateLimit int
	authSlots           chan struct{}
	healthMu            sync.Mutex
	healthCheckedAt     time.Time
	healthErr           error
}

func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("server store is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.AgentRequestsPerMinute <= 0 {
		cfg.AgentRequestsPerMinute = defaultAgentRequestsPerMinute
	}
	if cfg.DisplayRequestsPerMinute <= 0 {
		cfg.DisplayRequestsPerMinute = defaultDisplayRequestsPerMinute
	}
	if cfg.AuthAttemptsPerMinute <= 0 {
		cfg.AuthAttemptsPerMinute = defaultAuthAttemptsPerMinute
	}
	if cfg.GlobalAuthAttemptsPerMinute <= 0 {
		cfg.GlobalAuthAttemptsPerMinute = defaultGlobalAuthPerMinute
	}
	if cfg.ConcurrentAuth <= 0 {
		cfg.ConcurrentAuth = defaultConcurrentAuth
	}

	s := &Server{
		store:               cfg.Store,
		logger:              cfg.Logger,
		now:                 cfg.Now,
		limiter:             newFixedWindowLimiter(time.Minute),
		agentRateLimit:      cfg.AgentRequestsPerMinute,
		displayRateLimit:    cfg.DisplayRequestsPerMinute,
		authRateLimit:       cfg.AuthAttemptsPerMinute,
		globalAuthRateLimit: cfg.GlobalAuthAttemptsPerMinute,
		authSlots:           make(chan struct{}, cfg.ConcurrentAuth),
		mux:                 http.NewServeMux(),
		firmwareDir:         strings.TrimSpace(cfg.FirmwareDir),
	}
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("POST /api/v1/agent/report", s.handleAgentReport)
	s.mux.HandleFunc("GET /api/v1/display/snapshot", s.handleDisplaySnapshot)
	s.mux.HandleFunc("GET /api/v1/display/firmware/e32r28t/manifest", s.handleFirmwareManifest)
	s.mux.HandleFunc("GET /api/v1/display/firmware/e32r28t/{file}", s.handleFirmwareDownload)
	return s, nil
}

// Handler returns the complete API handler. Server also implements
// http.Handler directly, so either value can be passed to http.Server.
func (s *Server) Handler() http.Handler { return s }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	// Deliberately do not add Access-Control-Allow-* headers. The API is for
	// agents and the embedded display, not browser origins.
	// Reject non-canonical paths before ServeMux can generate an automatic
	// redirect. Embedded clients never need redirects, especially for a
	// credential-bearing firmware download.
	if r.URL.Path == "" || path.Clean(r.URL.Path) != r.URL.Path {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Public readiness checks can be polled aggressively. Cache the database
	// result briefly so unauthenticated traffic cannot turn /healthz into an
	// unbounded stream of SQLite operations.
	now := s.now().UTC()
	s.healthMu.Lock()
	if s.healthCheckedAt.IsZero() || now.Sub(s.healthCheckedAt) >= time.Second || now.Before(s.healthCheckedAt) {
		s.healthErr = s.store.Ping(r.Context())
		s.healthCheckedAt = now
	}
	err := s.healthErr
	s.healthMu.Unlock()
	if err != nil {
		s.logger.Error("database health check failed")
		writeError(w, http.StatusServiceUnavailable, "unhealthy", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	record, ok := s.authenticate(w, r, store.ScopeAgentWrite, s.agentRateLimit)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxReportBodySize)
	var report model.AgentReport
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "report exceeds 64 KiB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not a valid versioned report")
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "report exceeds 64 KiB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object")
		return
	}

	if record.AgentID != report.AgentID {
		writeError(w, http.StatusForbidden, "agent_mismatch", "token is bound to a different agent")
		return
	}
	now := s.now().UTC()
	if err := ValidateAgentReport(report, now); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_report", err.Error())
		return
	}
	if err := s.store.ReplaceAgentReport(r.Context(), report, now); err != nil {
		if errors.Is(err, store.ErrStaleReport) {
			writeError(w, http.StatusConflict, "stale_report", "report is not newer than the stored snapshot")
			return
		}
		s.logger.Error("store agent report failed", "agent_id", report.AgentID)
		writeError(w, http.StatusInternalServerError, "storage_error", "could not store report")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDisplaySnapshot(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(w, r, store.ScopeDisplayRead, s.displayRateLimit); !ok {
		return
	}
	snapshot, err := s.store.BuildDisplaySnapshot(r.Context(), s.now().UTC())
	if err != nil {
		s.logger.Error("build display snapshot failed")
		writeError(w, http.StatusInternalServerError, "storage_error", "could not build display snapshot")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleFirmwareManifest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(w, r, store.ScopeDisplayRead, s.displayRateLimit); !ok {
		return
	}
	manifest, err := firmware.LoadManifest(s.firmwareDir)
	if err != nil {
		s.writeFirmwareError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) handleFirmwareDownload(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(w, r, store.ScopeDisplayRead, s.displayRateLimit); !ok {
		return
	}
	fileName := r.PathValue("file")
	if !strings.HasSuffix(fileName, ".bin") || strings.Count(fileName, ".bin") != 1 {
		writeError(w, http.StatusNotFound, "firmware_not_found", "firmware version not found")
		return
	}
	version := strings.TrimSuffix(fileName, ".bin")
	file, manifest, err := firmware.OpenVerifiedImage(r.Context(), s.firmwareDir, version)
	if err != nil {
		s.writeFirmwareError(w, err)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(manifest.SizeBytes, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, firmware.ImageName(manifest.Board, manifest.Version)))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, file); err != nil {
		// The response may already contain a partial image, so only log a
		// redacted diagnostic. The device validates both length and SHA-256.
		s.logger.Warn("firmware download interrupted", "version", manifest.Version)
	}
}

func (s *Server) writeFirmwareError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, firmware.ErrNotFound):
		writeError(w, http.StatusNotFound, "firmware_not_found", "firmware version not found")
	case errors.Is(err, firmware.ErrInvalidManifest), errors.Is(err, firmware.ErrHashMismatch):
		s.logger.Error("published firmware validation failed")
		writeError(w, http.StatusServiceUnavailable, "firmware_invalid", "published firmware is unavailable")
	default:
		s.logger.Error("published firmware read failed")
		writeError(w, http.StatusServiceUnavailable, "firmware_unavailable", "published firmware is unavailable")
	}
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, scope store.TokenScope, limit int) (store.TokenRecord, bool) {
	// Bound work before touching SQLite. The supplied Caddy deployment makes
	// RemoteAddr common to all public clients, so the normal ceiling is keyed by
	// a one-way credential fingerprint as well; one invalid token cannot consume
	// the allowance of a legitimate token. A much higher aggregate ceiling and
	// the authSlots semaphore bound rotating-token floods.
	now := s.now().UTC()
	allowed, retryAfter := s.limiter.Allow("auth:global", now, s.globalAuthRateLimit)
	if !allowed {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", max(1, int(math.Ceil(retryAfter.Seconds())))))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "authentication request limit exceeded")
		return store.TokenRecord{}, false
	}
	raw, bearerOK := parseBearer(r.Header.Get("Authorization"))
	fingerprint := "malformed"
	if bearerOK {
		digest := sha256.Sum256([]byte(raw))
		fingerprint = fmt.Sprintf("%x", digest[:12])
	}
	allowed, retryAfter = s.limiter.Allow("auth:"+string(scope)+":"+clientAddress(r.RemoteAddr)+":"+fingerprint, now, s.authRateLimit)
	if !allowed {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", max(1, int(math.Ceil(retryAfter.Seconds())))))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "authentication request limit exceeded")
		return store.TokenRecord{}, false
	}
	select {
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "authentication capacity exceeded")
		return store.TokenRecord{}, false
	}

	if !bearerOK {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
		return store.TokenRecord{}, false
	}
	record, err := s.store.AuthenticateToken(r.Context(), raw, scope)
	if err != nil {
		if !errors.Is(err, store.ErrInvalidToken) {
			s.logger.Error("token authentication failed", "scope", scope)
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
		return store.TokenRecord{}, false
	}
	allowed, retryAfter = s.limiter.Allow(string(scope)+":"+record.ID, s.now().UTC(), limit)
	if !allowed {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", max(1, int(math.Ceil(retryAfter.Seconds())))))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "request limit exceeded")
		return store.TokenRecord{}, false
	}
	return record, true
}

func clientAddress(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && host != "" {
		return host
	}
	if remoteAddr == "" {
		return "unknown"
	}
	return remoteAddr
}

func parseBearer(value string) (string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" || len(parts[1]) > 256 {
		return "", false
	}
	return parts[1], true
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("additional JSON value")
	}
	return err
}

// ValidateAgentReport validates the complete v1 report before it reaches the
// database. It is exported so agent integration tests can exercise the exact
// server-side contract without opening a listener.
func ValidateAgentReport(report model.AgentReport, now time.Time) error {
	if report.SchemaVersion != model.SchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %d", report.SchemaVersion)
	}
	if !validAgentID(report.AgentID) {
		return errors.New("agentId must be 1-64 characters using letters, digits, dot, underscore, or hyphen")
	}
	if report.SentAt.IsZero() {
		return errors.New("sentAt is required")
	}
	now = now.UTC()
	if report.SentAt.After(now.Add(MaxFutureSkew)) {
		return errors.New("sentAt is too far in the future")
	}
	if report.SentAt.Before(now.Add(-MaxReportAge)) {
		return errors.New("sentAt is too old")
	}
	if report.Providers == nil {
		return errors.New("providers is required; use an empty object for no provider state")
	}
	if report.ActiveTasks == nil {
		return errors.New("activeTasks is required; use an empty array for no active tasks")
	}
	if len(report.ActiveTasks) > 256 {
		return errors.New("activeTasks must contain at most 256 entries")
	}

	for provider, providerReport := range report.Providers {
		if provider != model.ProviderCodex && provider != model.ProviderClaude {
			return fmt.Errorf("unsupported provider %q", provider)
		}
		if len(providerReport.AuthState) > 64 || len(providerReport.Plan) > 128 || len(providerReport.Source) > 128 || len(providerReport.ErrorCode) > 128 {
			return fmt.Errorf("%s provider metadata is too long", provider)
		}
		if providerReport.ObservedAt.IsZero() {
			if providerReport.Windows.FiveHour != nil || providerReport.Windows.SevenDay != nil {
				return fmt.Errorf("%s observedAt is required when quota windows are present", provider)
			}
		} else if providerReport.ObservedAt.After(report.SentAt.Add(MaxFutureSkew)) || providerReport.ObservedAt.After(now.Add(MaxFutureSkew)) {
			return fmt.Errorf("%s observedAt is too far in the future", provider)
		}
		if err := validateWindow(providerReport.Windows.FiveHour, string(provider)+" fiveHour"); err != nil {
			return err
		}
		if err := validateWindow(providerReport.Windows.SevenDay, string(provider)+" sevenDay"); err != nil {
			return err
		}
	}

	seen := make(map[string]struct{}, len(report.ActiveTasks))
	for i, task := range report.ActiveTasks {
		if task.Provider != model.ProviderCodex && task.Provider != model.ProviderClaude {
			return fmt.Errorf("activeTasks[%d] has unsupported provider", i)
		}
		if task.Kind != model.TaskMain && task.Kind != model.TaskSub {
			return fmt.Errorf("activeTasks[%d] has unsupported kind", i)
		}
		if !validSessionID(task.SessionID) {
			return fmt.Errorf("activeTasks[%d] has invalid sessionId", i)
		}
		if task.StartedAt.IsZero() || task.LastSeenAt.IsZero() {
			return fmt.Errorf("activeTasks[%d] requires startedAt and lastSeenAt", i)
		}
		if task.LastSeenAt.Before(task.StartedAt) {
			return fmt.Errorf("activeTasks[%d] lastSeenAt precedes startedAt", i)
		}
		if task.StartedAt.After(now.Add(MaxFutureSkew)) || task.LastSeenAt.After(now.Add(MaxFutureSkew)) ||
			task.StartedAt.After(report.SentAt.Add(MaxFutureSkew)) || task.LastSeenAt.After(report.SentAt.Add(MaxFutureSkew)) {
			return fmt.Errorf("activeTasks[%d] timestamp is too far in the future", i)
		}
		key := string(task.Provider) + "\x00" + task.SessionID
		if _, exists := seen[key]; exists {
			return fmt.Errorf("activeTasks[%d] duplicates a provider/sessionId pair", i)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateWindow(window *model.LimitWindow, name string) error {
	if window == nil {
		return nil
	}
	if math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0) || window.UsedPercent < 0 || window.UsedPercent > 100 {
		return fmt.Errorf("%s usedPercent must be between 0 and 100", name)
	}
	if math.IsNaN(window.RemainingPercent) || math.IsInf(window.RemainingPercent, 0) || window.RemainingPercent < 0 || window.RemainingPercent > 100 {
		return fmt.Errorf("%s remainingPercent must be between 0 and 100", name)
	}
	if math.Abs((window.UsedPercent+window.RemainingPercent)-100) > 0.01 {
		return fmt.Errorf("%s usedPercent and remainingPercent must sum to 100", name)
	}
	if window.ResetsAt == nil {
		if window.UsedPercent != 0 || window.RemainingPercent != 100 {
			return fmt.Errorf("%s resetsAt may be null only for an unused window", name)
		}
		return nil
	}
	if window.ResetsAt.IsZero() {
		return fmt.Errorf("%s resetsAt must be a valid timestamp or null", name)
	}
	return nil
}

func validAgentID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validSessionID(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type fixedWindowEntry struct {
	start time.Time
	count int
}

type fixedWindowLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	entries map[string]fixedWindowEntry
}

func newFixedWindowLimiter(window time.Duration) *fixedWindowLimiter {
	return &fixedWindowLimiter{window: window, entries: make(map[string]fixedWindowEntry)}
}

func (l *fixedWindowLimiter) Allow(key string, now time.Time, limit int) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[key]
	if !ok || now.Sub(entry.start) >= l.window || now.Before(entry.start) {
		l.entries[key] = fixedWindowEntry{start: now, count: 1}
		l.prune(now)
		return true, 0
	}
	if entry.count >= limit {
		return false, entry.start.Add(l.window).Sub(now)
	}
	entry.count++
	l.entries[key] = entry
	return true, 0
}

func (l *fixedWindowLimiter) prune(now time.Time) {
	if len(l.entries) < 4096 {
		return
	}
	for key, entry := range l.entries {
		if now.Sub(entry.start) >= 2*l.window || now.Before(entry.start) {
			delete(l.entries, key)
		}
	}
}
