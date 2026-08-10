// Package firmware validates, publishes, and opens firmware images used by
// the embedded quota display. Published manifests are the final commit point:
// readers either observe the previous manifest or a complete new image.
package firmware

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	SchemaVersion = 1
	BoardE32R28T  = "e32r28t"
	ManifestName  = "manifest.json"

	// The E32R28T OTA partition is 0x1e0000 bytes. CI keeps an additional
	// build-time reserve, while the publisher enforces the physical slot limit.
	MaxImageSize    int64 = 0x1e0000
	maxManifestSize       = 4 << 10
)

var (
	ErrNotFound        = errors.New("firmware not found")
	ErrInvalidManifest = errors.New("invalid firmware manifest")
	ErrInvalidVersion  = errors.New("invalid firmware version")
	ErrVersionNotNewer = errors.New("firmware version is not newer")
	ErrImageTooLarge   = errors.New("firmware image exceeds OTA slot")
	ErrHashMismatch    = errors.New("firmware SHA-256 mismatch")
)

// Manifest is the stable JSON contract consumed by the E32R28T display.
type Manifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	Board         string    `json:"board"`
	Version       string    `json:"version"`
	PublishedAt   time.Time `json:"publishedAt"`
	SizeBytes     int64     `json:"sizeBytes"`
	SHA256        string    `json:"sha256"`
}

// PublishOptions describes one local, administrator-initiated publication.
type PublishOptions struct {
	Directory   string
	Board       string
	Version     string
	SourcePath  string
	PublishedAt time.Time
}

// Version is a strict three-component semantic version without prerelease or
// build metadata. OTA intentionally accepts a smaller grammar than SemVer 2.0.
type Version struct {
	Major uint64
	Minor uint64
	Patch uint64
}

func ParseVersion(value string) (Version, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%w: must be MAJOR.MINOR.PATCH", ErrInvalidVersion)
	}
	values := make([]uint64, 3)
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return Version{}, fmt.Errorf("%w: components must be canonical decimal integers", ErrInvalidVersion)
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return Version{}, fmt.Errorf("%w: must not contain prerelease or build metadata", ErrInvalidVersion)
			}
		}
		parsed, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return Version{}, fmt.Errorf("%w: component is out of range", ErrInvalidVersion)
		}
		values[i] = parsed
	}
	return Version{Major: values[0], Minor: values[1], Patch: values[2]}, nil
}

func (v Version) Compare(other Version) int {
	left := [...]uint64{v.Major, v.Minor, v.Patch}
	right := [...]uint64{other.Major, other.Minor, other.Patch}
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schemaVersion %d", ErrInvalidManifest, manifest.SchemaVersion)
	}
	if manifest.Board != BoardE32R28T {
		return fmt.Errorf("%w: unsupported board %q", ErrInvalidManifest, manifest.Board)
	}
	if _, err := ParseVersion(manifest.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if manifest.PublishedAt.IsZero() {
		return fmt.Errorf("%w: publishedAt is required", ErrInvalidManifest)
	}
	if manifest.SizeBytes <= 0 || manifest.SizeBytes > MaxImageSize {
		return fmt.Errorf("%w: sizeBytes is outside the OTA slot", ErrInvalidManifest)
	}
	if !validSHA256(manifest.SHA256) {
		return fmt.Errorf("%w: sha256 must be 64 lowercase hexadecimal characters", ErrInvalidManifest)
	}
	return nil
}

func ImageName(board, version string) string {
	return board + "-" + version + ".bin"
}

// LoadManifest reads and validates the current committed manifest without
// loading a firmware image into memory.
func LoadManifest(directory string) (Manifest, error) {
	if strings.TrimSpace(directory) == "" {
		return Manifest{}, ErrNotFound
	}
	file, err := os.Open(filepath.Join(directory, ManifestName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, ErrNotFound
		}
		return Manifest{}, fmt.Errorf("open manifest: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxManifestSize {
		return Manifest{}, fmt.Errorf("%w: manifest file has an invalid type or size", ErrInvalidManifest)
	}

	var manifest Manifest
	decoder := json.NewDecoder(io.LimitReader(file, maxManifestSize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode: %v", ErrInvalidManifest, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("%w: trailing data", ErrInvalidManifest)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// OpenVerifiedImage opens the image named by the current manifest, verifies
// its exact size and SHA-256 using bounded memory, and rewinds the same file
// descriptor for streaming. Only the current manifest version is downloadable.
func OpenVerifiedImage(ctx context.Context, directory, version string) (*os.File, Manifest, error) {
	if _, err := ParseVersion(version); err != nil {
		return nil, Manifest{}, ErrNotFound
	}
	manifest, err := LoadManifest(directory)
	if err != nil {
		return nil, Manifest{}, err
	}
	if manifest.Version != version {
		return nil, Manifest{}, ErrNotFound
	}
	file, err := os.Open(filepath.Join(directory, ImageName(manifest.Board, manifest.Version)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, Manifest{}, fmt.Errorf("%w: manifest image is missing", ErrInvalidManifest)
		}
		return nil, Manifest{}, fmt.Errorf("open firmware image: %w", err)
	}
	fail := func(err error) (*os.File, Manifest, error) {
		_ = file.Close()
		return nil, Manifest{}, err
	}
	info, err := file.Stat()
	if err != nil {
		return fail(fmt.Errorf("inspect firmware image: %w", err))
	}
	if !info.Mode().IsRegular() || info.Size() != manifest.SizeBytes {
		return fail(fmt.Errorf("%w: image size does not match manifest", ErrInvalidManifest))
	}
	digest, size, err := hashReader(ctx, file)
	if err != nil {
		return fail(fmt.Errorf("verify firmware image: %w", err))
	}
	if size != manifest.SizeBytes || subtle.ConstantTimeCompare([]byte(digest), []byte(manifest.SHA256)) != 1 {
		return fail(ErrHashMismatch)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("rewind firmware image: %w", err))
	}
	return file, manifest, nil
}

// Publish validates and atomically commits a firmware image. The image is
// installed before the manifest; therefore an interrupted publication cannot
// make readers observe a manifest that points at a partial file.
func Publish(ctx context.Context, options PublishOptions) (Manifest, error) {
	if strings.TrimSpace(options.Directory) == "" {
		return Manifest{}, errors.New("firmware directory is required")
	}
	if options.Board != BoardE32R28T {
		return Manifest{}, fmt.Errorf("unsupported board %q (want %s)", options.Board, BoardE32R28T)
	}
	version, err := ParseVersion(options.Version)
	if err != nil {
		return Manifest{}, err
	}
	if strings.TrimSpace(options.SourcePath) == "" {
		return Manifest{}, errors.New("firmware file is required")
	}
	if options.PublishedAt.IsZero() {
		options.PublishedAt = time.Now()
	}
	options.PublishedAt = options.PublishedAt.UTC().Truncate(time.Second)

	if err := os.MkdirAll(options.Directory, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create firmware directory: %w", err)
	}
	if err := os.Chmod(options.Directory, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("secure firmware directory: %w", err)
	}
	publishLock, err := acquirePublishLock(options.Directory)
	if err != nil {
		return Manifest{}, fmt.Errorf("lock firmware directory: %w", err)
	}
	defer func() { _ = publishLock.Close() }()

	// Keep the version check and both commit renames under one cross-process
	// lock. Otherwise two administrators could validate against the same old
	// manifest, then commit out of order or interleave image/manifest files.
	if current, loadErr := LoadManifest(options.Directory); loadErr == nil {
		currentVersion, parseErr := ParseVersion(current.Version)
		if parseErr != nil {
			return Manifest{}, parseErr
		}
		if version.Compare(currentVersion) <= 0 {
			return Manifest{}, fmt.Errorf("%w: current is %s", ErrVersionNotNewer, current.Version)
		}
	} else if !errors.Is(loadErr, ErrNotFound) {
		return Manifest{}, loadErr
	}

	source, err := os.Open(options.SourcePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open firmware file: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect firmware file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Manifest{}, errors.New("firmware file must be a regular file")
	}
	if info.Size() <= 0 {
		return Manifest{}, errors.New("firmware file is empty")
	}
	if info.Size() > MaxImageSize {
		return Manifest{}, ErrImageTooLarge
	}

	targetName := ImageName(options.Board, options.Version)
	targetPath := filepath.Join(options.Directory, targetName)
	if _, err := os.Lstat(targetPath); err == nil {
		return Manifest{}, fmt.Errorf("firmware version file already exists: %s", targetName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("inspect firmware target: %w", err)
	}

	imageTemp, err := os.CreateTemp(options.Directory, ".image-*.tmp")
	if err != nil {
		return Manifest{}, fmt.Errorf("create firmware staging file: %w", err)
	}
	imageTempPath := imageTemp.Name()
	imageInstalled := false
	publicationCommitted := false
	defer func() {
		_ = imageTemp.Close()
		_ = os.Remove(imageTempPath)
		if imageInstalled && !publicationCommitted {
			if os.Remove(targetPath) == nil {
				_ = syncDirectory(options.Directory)
			}
		}
	}()
	if err := imageTemp.Chmod(0o600); err != nil {
		return Manifest{}, fmt.Errorf("secure firmware staging file: %w", err)
	}

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(imageTemp, hasher), io.LimitReader(source, MaxImageSize+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("copy firmware image: %w", err)
	}
	if written > MaxImageSize {
		return Manifest{}, ErrImageTooLarge
	}
	if written <= 0 || written != info.Size() {
		return Manifest{}, errors.New("firmware file changed while publishing")
	}
	stagedDigest := hex.EncodeToString(hasher.Sum(nil))
	if err := imageTemp.Sync(); err != nil {
		return Manifest{}, fmt.Errorf("sync firmware staging file: %w", err)
	}
	if _, err := imageTemp.Seek(0, io.SeekStart); err != nil {
		return Manifest{}, fmt.Errorf("rewind firmware staging file: %w", err)
	}
	verifiedDigest, verifiedSize, err := hashReader(ctx, imageTemp)
	if err != nil {
		return Manifest{}, fmt.Errorf("verify firmware staging file: %w", err)
	}
	if verifiedSize != written || subtle.ConstantTimeCompare([]byte(verifiedDigest), []byte(stagedDigest)) != 1 {
		return Manifest{}, ErrHashMismatch
	}
	if err := imageTemp.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close firmware staging file: %w", err)
	}

	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		Board:         options.Board,
		Version:       options.Version,
		PublishedAt:   options.PublishedAt,
		SizeBytes:     written,
		SHA256:        stagedDigest,
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, fmt.Errorf("encode firmware manifest: %w", err)
	}
	manifestPayload = append(manifestPayload, '\n')
	manifestTemp, err := os.CreateTemp(options.Directory, ".manifest-*.tmp")
	if err != nil {
		return Manifest{}, fmt.Errorf("create manifest staging file: %w", err)
	}
	manifestTempPath := manifestTemp.Name()
	defer func() {
		_ = manifestTemp.Close()
		_ = os.Remove(manifestTempPath)
	}()
	if err := manifestTemp.Chmod(0o600); err != nil {
		return Manifest{}, fmt.Errorf("secure manifest staging file: %w", err)
	}
	if _, err := manifestTemp.Write(manifestPayload); err != nil {
		return Manifest{}, fmt.Errorf("write manifest staging file: %w", err)
	}
	if err := manifestTemp.Sync(); err != nil {
		return Manifest{}, fmt.Errorf("sync manifest staging file: %w", err)
	}
	if err := manifestTemp.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close manifest staging file: %w", err)
	}

	if err := os.Rename(imageTempPath, targetPath); err != nil {
		return Manifest{}, fmt.Errorf("install firmware image: %w", err)
	}
	imageInstalled = true
	if err := syncDirectory(options.Directory); err != nil {
		return Manifest{}, fmt.Errorf("sync installed firmware image: %w", err)
	}
	if err := atomicReplace(manifestTempPath, filepath.Join(options.Directory, ManifestName)); err != nil {
		return Manifest{}, fmt.Errorf("commit firmware manifest: %w", err)
	}
	publicationCommitted = true
	if err := syncDirectory(options.Directory); err != nil {
		return Manifest{}, fmt.Errorf("sync committed firmware manifest: %w", err)
	}
	return manifest, nil
}

func hashReader(ctx context.Context, reader io.Reader) (string, int64, error) {
	hasher := sha256.New()
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			if _, writeErr := hasher.Write(buffer[:count]); writeErr != nil {
				return "", 0, writeErr
			}
			total += int64(count)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", 0, err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), total, nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
