package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseVersionStrictThreePartCanonical(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"0.0.0", "0.3.0", "12.34.56", "4294967295.0.1"} {
		if _, err := ParseVersion(value); err != nil {
			t.Errorf("ParseVersion(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{
		"", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03",
		"v1.2.3", "1.2.3-beta", "1.2.3+build", "-1.2.3", "4294967296.0.0",
	} {
		if _, err := ParseVersion(value); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("ParseVersion(%q) error = %v; want ErrInvalidVersion", value, err)
		}
	}

	one, _ := ParseVersion("1.2.3")
	same, _ := ParseVersion("1.2.3")
	older, _ := ParseVersion("1.2.2")
	newer, _ := ParseVersion("2.0.0")
	if one.Compare(same) != 0 || one.Compare(older) <= 0 || one.Compare(newer) >= 0 {
		t.Fatal("Version.Compare returned an invalid ordering")
	}
}

func TestManifestValidation(t *testing.T) {
	t.Parallel()
	valid := Manifest{
		SchemaVersion: SchemaVersion,
		Board:         BoardE32R28T,
		Version:       "0.3.0",
		PublishedAt:   time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		SizeBytes:     123,
		SHA256:        strings.Repeat("a", 64),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid manifest error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "schema", mutate: func(m *Manifest) { m.SchemaVersion++ }},
		{name: "board", mutate: func(m *Manifest) { m.Board = "other" }},
		{name: "version", mutate: func(m *Manifest) { m.Version = "v0.3.0" }},
		{name: "published at", mutate: func(m *Manifest) { m.PublishedAt = time.Time{} }},
		{name: "empty", mutate: func(m *Manifest) { m.SizeBytes = 0 }},
		{name: "too large", mutate: func(m *Manifest) { m.SizeBytes = MaxImageSize + 1 }},
		{name: "short hash", mutate: func(m *Manifest) { m.SHA256 = "abcd" }},
		{name: "uppercase hash", mutate: func(m *Manifest) { m.SHA256 = strings.Repeat("A", 64) }},
		{name: "non hex hash", mutate: func(m *Manifest) { m.SHA256 = strings.Repeat("z", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := valid
			test.mutate(&manifest)
			if err := manifest.Validate(); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Validate() error = %v; want ErrInvalidManifest", err)
			}
		})
	}
}

func TestPublishLoadAndOpenVerifiedImage(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	source := writeImage(t, t.TempDir(), "firmware.bin", []byte("valid firmware payload"))
	publishedAt := time.Date(2026, 8, 9, 12, 34, 56, 789, time.FixedZone("fixture", 8*60*60))

	manifest, err := Publish(context.Background(), PublishOptions{
		Directory: directory, Board: BoardE32R28T, Version: "0.3.0",
		SourcePath: source, PublishedAt: publishedAt,
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	digest := sha256.Sum256([]byte("valid firmware payload"))
	if manifest.SchemaVersion != 1 || manifest.Board != BoardE32R28T || manifest.Version != "0.3.0" ||
		manifest.SizeBytes != int64(len("valid firmware payload")) || manifest.SHA256 != hex.EncodeToString(digest[:]) ||
		!manifest.PublishedAt.Equal(publishedAt.UTC().Truncate(time.Second)) || manifest.PublishedAt.Nanosecond() != 0 || manifest.PublishedAt.Location() != time.UTC {
		t.Fatalf("manifest = %+v", manifest)
	}

	loaded, err := LoadManifest(directory)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if loaded != manifest {
		t.Fatalf("loaded manifest = %+v; want %+v", loaded, manifest)
	}
	file, opened, err := OpenVerifiedImage(context.Background(), directory, "0.3.0")
	if err != nil {
		t.Fatalf("OpenVerifiedImage() error = %v", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "valid firmware payload" || opened != manifest {
		t.Fatalf("opened manifest/payload mismatch: %+v %q", opened, payload)
	}

	for _, path := range []string{
		filepath.Join(directory, ManifestName),
		filepath.Join(directory, ImageName(BoardE32R28T, "0.3.0")),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("%s permissions = %o; want 600", filepath.Base(path), info.Mode().Perm())
		}
	}
	if info, err := os.Stat(directory); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("firmware directory permissions = %o; want 700", info.Mode().Perm())
	}
}

func TestConcurrentPublishCannotRollBackOrInterleave(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sourceThree := writeImage(t, t.TempDir(), "three.bin", []byte("version three"))
	sourceFour := writeImage(t, t.TempDir(), "four.bin", []byte("version four"))
	start := make(chan struct{})
	type result struct {
		version string
		err     error
	}
	results := make(chan result, 2)
	for _, item := range []struct {
		version string
		source  string
	}{{"0.3.0", sourceThree}, {"0.4.0", sourceFour}} {
		item := item
		go func() {
			<-start
			_, err := Publish(context.Background(), PublishOptions{
				Directory: directory, Board: BoardE32R28T,
				Version: item.version, SourcePath: item.source,
			})
			results <- result{version: item.version, err: err}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil && !errors.Is(result.err, ErrVersionNotNewer) {
			t.Fatalf("concurrent Publish(%s) error = %v", result.version, result.err)
		}
	}

	manifest, err := LoadManifest(directory)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "0.4.0" {
		t.Fatalf("concurrent publication rolled back to %s", manifest.Version)
	}
	file, _, err := OpenVerifiedImage(context.Background(), directory, "0.4.0")
	if err != nil {
		t.Fatalf("final image/manifest mismatch: %v", err)
	}
	file.Close()
}

func TestPublishRequiresStrictlyNewerAndDoesNotOverwrite(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	source := writeImage(t, t.TempDir(), "firmware.bin", []byte("first"))
	if _, err := Publish(context.Background(), PublishOptions{
		Directory: directory, Board: BoardE32R28T, Version: "1.2.3", SourcePath: source,
	}); err != nil {
		t.Fatal(err)
	}
	manifestBefore, err := os.ReadFile(filepath.Join(directory, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1.2.3", "1.2.2", "0.99.99"} {
		if _, err := Publish(context.Background(), PublishOptions{
			Directory: directory, Board: BoardE32R28T, Version: version, SourcePath: source,
		}); !errors.Is(err, ErrVersionNotNewer) {
			t.Errorf("Publish(%s) error = %v; want ErrVersionNotNewer", version, err)
		}
	}
	manifestAfter, err := os.ReadFile(filepath.Join(directory, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestAfter) != string(manifestBefore) {
		t.Fatal("failed publication changed the current manifest")
	}

	second := writeImage(t, t.TempDir(), "firmware.bin", []byte("second"))
	if _, err := Publish(context.Background(), PublishOptions{
		Directory: directory, Board: BoardE32R28T, Version: "1.3.0", SourcePath: second,
	}); err != nil {
		t.Fatalf("newer Publish() error = %v", err)
	}
	if _, _, err := OpenVerifiedImage(context.Background(), directory, "1.2.3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old image remained publicly selectable: %v", err)
	}
}

func TestPublishRejectsBadBoardEmptyAndOversize(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	valid := writeImage(t, t.TempDir(), "valid.bin", []byte("x"))
	if _, err := Publish(context.Background(), PublishOptions{
		Directory: directory, Board: "other", Version: "0.3.0", SourcePath: valid,
	}); err == nil {
		t.Fatal("Publish() accepted an unsupported board")
	}
	empty := writeImage(t, t.TempDir(), "empty.bin", nil)
	if _, err := Publish(context.Background(), PublishOptions{
		Directory: directory, Board: BoardE32R28T, Version: "0.3.0", SourcePath: empty,
	}); err == nil {
		t.Fatal("Publish() accepted an empty image")
	}
	oversize := filepath.Join(t.TempDir(), "oversize.bin")
	file, err := os.Create(oversize)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxImageSize + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(context.Background(), PublishOptions{
		Directory: directory, Board: BoardE32R28T, Version: "0.3.0", SourcePath: oversize,
	}); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("oversize Publish() error = %v; want ErrImageTooLarge", err)
	}
	if _, err := os.Stat(filepath.Join(directory, ManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid publication created a manifest: %v", err)
	}
}

func TestLoadManifestRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	base := `{"schemaVersion":1,"board":"e32r28t","version":"0.3.0","publishedAt":"2026-08-09T12:00:00Z","sizeBytes":1,"sha256":"` + strings.Repeat("a", 64) + `"}`
	for name, payload := range map[string]string{
		"unknown":  strings.TrimSuffix(base, "}") + `,"unexpected":true}`,
		"trailing": base + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(directory, ManifestName), []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadManifest(directory); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("LoadManifest() error = %v; want ErrInvalidManifest", err)
			}
		})
	}
}

func TestOpenVerifiedImageRejectsCorruptionAndCancellation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	source := writeImage(t, t.TempDir(), "firmware.bin", []byte("original"))
	if _, err := Publish(context.Background(), PublishOptions{
		Directory: directory, Board: BoardE32R28T, Version: "0.3.0", SourcePath: source,
	}); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(directory, ImageName(BoardE32R28T, "0.3.0"))
	if err := os.WriteFile(imagePath, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenVerifiedImage(context.Background(), directory, "0.3.0"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("corrupt image error = %v; want ErrHashMismatch", err)
	}

	// Restore exact bytes, then verify cancellation is checked during hashing.
	if err := os.WriteFile(imagePath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := OpenVerifiedImage(ctx, directory, "0.3.0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled verification error = %v; want context.Canceled", err)
	}
}

func TestManifestJSONContract(t *testing.T) {
	t.Parallel()
	manifest := Manifest{
		SchemaVersion: 1, Board: BoardE32R28T, Version: "0.3.0",
		PublishedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		SizeBytes:   1377328, SHA256: strings.Repeat("a", 64),
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schemaVersion":1,"board":"e32r28t","version":"0.3.0","publishedAt":"2026-08-09T12:00:00Z","sizeBytes":1377328,"sha256":"` + strings.Repeat("a", 64) + `"}`
	if string(payload) != want {
		t.Fatalf("manifest JSON = %s; want %s", payload, want)
	}
}

func writeImage(t *testing.T, directory, name string, payload []byte) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
