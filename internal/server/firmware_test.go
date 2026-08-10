package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"codex-claude-monitor/internal/firmware"
)

func TestFirmwareManifestAndDownload(t *testing.T) {
	directory := t.TempDir()
	payload := []byte("streamed e32r28t firmware")
	manifest := publishServerFixtureFirmware(t, directory, "0.3.0", payload)
	f := newServerFixture(t, func(cfg *Config) { cfg.FirmwareDir = directory })

	manifestResponse := serve(f.handler, http.MethodGet, "/api/v1/display/firmware/e32r28t/manifest", f.displayToken, nil)
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("manifest status = %d, body = %s", manifestResponse.Code, manifestResponse.Body.String())
	}
	if got := manifestResponse.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("manifest Content-Type = %q", got)
	}
	var decoded firmware.Manifest
	if err := json.Unmarshal(manifestResponse.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != manifest {
		t.Fatalf("manifest = %+v; want %+v", decoded, manifest)
	}

	downloadResponse := serve(f.handler, http.MethodGet, "/api/v1/display/firmware/e32r28t/0.3.0.bin", f.displayToken, nil)
	if downloadResponse.Code != http.StatusOK {
		t.Fatalf("download status = %d, body = %s", downloadResponse.Code, downloadResponse.Body.String())
	}
	if got := downloadResponse.Body.Bytes(); string(got) != string(payload) {
		t.Fatalf("download body = %q; want %q", got, payload)
	}
	if got := downloadResponse.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("download Content-Type = %q", got)
	}
	if got := downloadResponse.Header().Get("Content-Length"); got != strconv.Itoa(len(payload)) {
		t.Fatalf("download Content-Length = %q", got)
	}
	if got := downloadResponse.Header().Get("Content-Disposition"); got != `attachment; filename="e32r28t-0.3.0.bin"` {
		t.Fatalf("download Content-Disposition = %q", got)
	}
	if got := downloadResponse.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("download X-Content-Type-Options = %q", got)
	}
	if location := downloadResponse.Header().Get("Location"); location != "" {
		t.Fatalf("download unexpectedly redirected to %q", location)
	}
	if got := downloadResponse.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("download unexpectedly enabled CORS: %q", got)
	}
}

func TestFirmwareEndpointsRequireDisplayToken(t *testing.T) {
	directory := t.TempDir()
	publishServerFixtureFirmware(t, directory, "0.3.0", []byte("firmware"))
	f := newServerFixture(t, func(cfg *Config) { cfg.FirmwareDir = directory })
	paths := []string{
		"/api/v1/display/firmware/e32r28t/manifest",
		"/api/v1/display/firmware/e32r28t/0.3.0.bin",
	}
	for _, path := range paths {
		for name, token := range map[string]string{"missing": "", "wrong scope": f.agentToken, "invalid": "invalid"} {
			t.Run(path+"/"+name, func(t *testing.T) {
				response := serve(f.handler, http.MethodGet, path, token, nil)
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestFirmwareOnlyServesCurrentValidVersion(t *testing.T) {
	directory := t.TempDir()
	publishServerFixtureFirmware(t, directory, "0.3.0", []byte("old firmware"))
	publishServerFixtureFirmware(t, directory, "0.4.0", []byte("new firmware"))
	f := newServerFixture(t, func(cfg *Config) { cfg.FirmwareDir = directory })

	for _, path := range []string{
		"/api/v1/display/firmware/e32r28t/0.3.0.bin",
		"/api/v1/display/firmware/e32r28t/not-semver.bin",
		"/api/v1/display/firmware/e32r28t/0.4.0.bin.extra",
		"/api/v1/display/firmware/e32r28t/../manifest.json",
	} {
		response := serve(f.handler, http.MethodGet, path, f.displayToken, nil)
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, body = %s", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Location") != "" {
			t.Errorf("GET %s returned a redirect", path)
		}
	}
	response := serve(f.handler, http.MethodPost, "/api/v1/display/firmware/e32r28t/0.4.0.bin", f.displayToken, nil)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST download status = %d; want 405", response.Code)
	}
}

func TestFirmwareMissingAndCorruptResponses(t *testing.T) {
	directory := t.TempDir()
	f := newServerFixture(t, func(cfg *Config) { cfg.FirmwareDir = directory })
	for _, path := range []string{
		"/api/v1/display/firmware/e32r28t/manifest",
		"/api/v1/display/firmware/e32r28t/0.3.0.bin",
	} {
		response := serve(f.handler, http.MethodGet, path, f.displayToken, nil)
		assertFirmwareError(t, response.Code, response.Body.String(), http.StatusNotFound, "firmware_not_found")
	}

	publishServerFixtureFirmware(t, directory, "0.3.0", []byte("original"))
	imagePath := filepath.Join(directory, firmware.ImageName(firmware.BoardE32R28T, "0.3.0"))
	if err := os.WriteFile(imagePath, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := serve(f.handler, http.MethodGet, "/api/v1/display/firmware/e32r28t/0.3.0.bin", f.displayToken, nil)
	assertFirmwareError(t, response.Code, response.Body.String(), http.StatusServiceUnavailable, "firmware_invalid")
	if strings.Contains(response.Body.String(), directory) || strings.Contains(response.Body.String(), imagePath) {
		t.Fatalf("filesystem path leaked in response: %s", response.Body.String())
	}
	if err := os.Remove(imagePath); err != nil {
		t.Fatal(err)
	}
	response = serve(f.handler, http.MethodGet, "/api/v1/display/firmware/e32r28t/0.3.0.bin", f.displayToken, nil)
	assertFirmwareError(t, response.Code, response.Body.String(), http.StatusServiceUnavailable, "firmware_invalid")

	if err := os.WriteFile(filepath.Join(directory, firmware.ManifestName), []byte(`{"schemaVersion":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	response = serve(f.handler, http.MethodGet, "/api/v1/display/firmware/e32r28t/manifest", f.displayToken, nil)
	assertFirmwareError(t, response.Code, response.Body.String(), http.StatusServiceUnavailable, "firmware_invalid")
}

func publishServerFixtureFirmware(t *testing.T, directory, version string, payload []byte) firmware.Manifest {
	t.Helper()
	sourceDirectory := t.TempDir()
	source := filepath.Join(sourceDirectory, "firmware.bin")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := firmware.Publish(context.Background(), firmware.PublishOptions{
		Directory: directory,
		Board:     firmware.BoardE32R28T, Version: version, SourcePath: source,
		PublishedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("firmware.Publish() error = %v", err)
	}
	return manifest
}

func assertFirmwareError(t *testing.T, gotStatus int, body string, wantStatus int, wantCode string) {
	t.Helper()
	if gotStatus != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", gotStatus, wantStatus, body)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode error response: %v (%s)", err, body)
	}
	if payload.Error.Code != wantCode {
		t.Fatalf("error code = %q, want %q", payload.Error.Code, wantCode)
	}
}
