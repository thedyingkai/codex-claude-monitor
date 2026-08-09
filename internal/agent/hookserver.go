package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"codex-claude-monitor/internal/collector"
	"codex-claude-monitor/internal/hooks"
	"codex-claude-monitor/internal/model"
)

const MaxHookPayload = 64 * 1024

type HookServerConfig struct {
	Addr       string
	Secret     string
	Ledger     *Ledger
	StatusLine func(model.ProviderReport)
	MaxPayload int64
}

func NewHookHandler(cfg HookServerConfig) (http.Handler, error) {
	if cfg.Secret == "" {
		return nil, errors.New("hook shared secret is required")
	}
	if cfg.Ledger == nil {
		return nil, errors.New("hook ledger is required")
	}
	if cfg.MaxPayload <= 0 {
		cfg.MaxPayload = MaxHookPayload
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/events", func(writer http.ResponseWriter, request *http.Request) {
		if !validLocalSecret(request, cfg.Secret) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		var event hooks.Event
		if err := decodeLimitedJSON(writer, request, cfg.MaxPayload, &event); err != nil {
			return
		}
		if event.Probe || event.SessionID == "" || (event.Provider != model.ProviderCodex && event.Provider != model.ProviderClaude) {
			http.Error(writer, "invalid event", http.StatusBadRequest)
			return
		}
		cfg.Ledger.Apply(event)
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/statusline", func(writer http.ResponseWriter, request *http.Request) {
		if !validLocalSecret(request, cfg.Secret) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, err := readLimitedBody(request.Body, cfg.MaxPayload)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		report, err := collector.ParseClaudeStatusLine(body, time.Now())
		if err != nil {
			http.Error(writer, "invalid status-line payload", http.StatusBadRequest)
			return
		}
		if cfg.StatusLine != nil {
			cfg.StatusLine(report)
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return mux, nil
}

func RunHookServer(ctx context.Context, cfg HookServerConfig) error {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:47632"
	}
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	return ServeHookServer(ctx, listener, cfg)
}

func ServeHookServer(ctx context.Context, listener net.Listener, cfg HookServerConfig) error {
	if !isLoopbackAddress(listener.Addr()) {
		_ = listener.Close()
		return fmt.Errorf("hook ingestion must listen on loopback, got %s", listener.Addr())
	}
	handler, err := NewHookHandler(cfg)
	if err != nil {
		_ = listener.Close()
		return err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func validLocalSecret(request *http.Request, secret string) bool {
	provided := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if provided == "" {
		provided = request.Header.Get("X-Quota-Monitor-Secret")
	}
	expectedHash := sha256.Sum256([]byte(secret))
	providedHash := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) == 1
}

func decodeLimitedJSON(writer http.ResponseWriter, request *http.Request, max int64, target any) error {
	body, err := readLimitedBody(request.Body, max)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusRequestEntityTooLarge)
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(writer, "invalid JSON", http.StatusBadRequest)
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "multiple JSON values", http.StatusBadRequest)
		return errors.New("multiple JSON values")
	}
	return nil
}

func readLimitedBody(reader io.Reader, max int64) ([]byte, error) {
	limited := io.LimitReader(reader, max+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("payload exceeds %d bytes", max)
	}
	return body, nil
}

func isLoopbackAddress(address net.Addr) bool {
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
