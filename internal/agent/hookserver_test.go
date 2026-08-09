package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codex-claude-monitor/internal/hooks"
	"codex-claude-monitor/internal/model"
)

func TestHookHandlerAuthenticatesAndIngestsEvent(t *testing.T) {
	ledger := NewLedger(time.Minute)
	handler, err := NewHookHandler(HookServerConfig{Secret: "secret", Ledger: ledger})
	if err != nil {
		t.Fatal(err)
	}
	event := hooks.Event{Provider: model.ProviderCodex, Name: "UserPromptSubmit", SessionID: "s", At: time.Now()}
	payload, _ := json.Marshal(event)
	request := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || len(ledger.Snapshot()) != 1 {
		t.Fatalf("code=%d tasks=%+v", response.Code, ledger.Snapshot())
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewReader(payload))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated code=%d", response.Code)
	}
}

func TestHookHandlerParsesStatusLine(t *testing.T) {
	var received model.ProviderReport
	handler, _ := NewHookHandler(HookServerConfig{Secret: "secret", Ledger: NewLedger(time.Minute), StatusLine: func(report model.ProviderReport) { received = report }})
	request := httptest.NewRequest(http.MethodPost, "/v1/statusline", strings.NewReader(`{"rate_limits":{"seven_day":{"used_percentage":8,"resets_at":"2026-08-09T00:00:00Z"}}}`))
	request.Header.Set("X-Quota-Monitor-Secret", "secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || received.Windows.SevenDay == nil {
		t.Fatalf("code=%d report=%+v", response.Code, received)
	}
}

func TestHookHandlerRejectsOversizedBody(t *testing.T) {
	handler, _ := NewHookHandler(HookServerConfig{Secret: "secret", Ledger: NewLedger(time.Minute), MaxPayload: 8})
	request := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(`{"larger":true}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code=%d", response.Code)
	}
}

func TestServeHookServerRejectsNonLoopback(t *testing.T) {
	listener := &fakeListener{addr: &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 1}}
	err := ServeHookServer(context.Background(), listener, HookServerConfig{Secret: "s", Ledger: NewLedger(time.Minute)})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected loopback error, got %v", err)
	}
}

type fakeListener struct{ addr net.Addr }

func (l *fakeListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l *fakeListener) Close() error              { return nil }
func (l *fakeListener) Addr() net.Addr            { return l.addr }
