package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/x509roots/fallback/bundle"
)

const (
	releaseAPI  = "https://api.github.com/repos/lihongjie0209/relaycat/releases/latest"
	releaseBase = "https://github.com/lihongjie0209/relaycat/releases/download/"
	maxMetadata = 1 << 20
	maxArchive  = 128 << 20
	maxBinary   = 128 << 20
)

var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$`)

type Options struct {
	Version  string
	Compact  bool
	Standard bool
	Check    bool
	Force    bool
}

type Result struct {
	Current string
	Latest  string
	Asset   string
	Path    string
	Backup  string
	Updated bool
}

func Run(ctx context.Context, current string, opts Options) (Result, error) {
	if opts.Compact && opts.Standard {
		return Result{}, errors.New("--compact and --standard cannot be used together")
	}
	client, err := newHTTPClient()
	if err != nil {
		return Result{}, err
	}
	version := opts.Version
	if version == "" || version == "latest" {
		version, err = latestVersion(ctx, client)
		if err != nil {
			return Result{}, err
		}
	}
	if !releaseTag.MatchString(version) {
		return Result{}, fmt.Errorf("invalid release version %q", version)
	}
	asset, err := assetName(version, runtime.GOOS, runtime.GOARCH, isOpenWrt(), opts)
	if err != nil {
		return Result{}, err
	}
	result := Result{Current: current, Latest: version, Asset: asset}
	if opts.Check || (!opts.Force && strings.TrimPrefix(version, "v") == strings.TrimPrefix(current, "v")) {
		return result, nil
	}
	path, err := os.Executable()
	if err != nil {
		return result, fmt.Errorf("finding current executable: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return result, fmt.Errorf("resolving current executable: %w", err)
	}
	result.Path = path
	backup, err := install(ctx, client, releaseBase+version+"/", asset, path, runtime.GOOS)
	if err != nil {
		return result, err
	}
	result.Backup = backup
	result.Updated = true
	return result, nil
}

func assetName(version, goos, goarch string, openwrt bool, opts Options) (string, error) {
	compact := opts.Compact || (openwrt && !opts.Standard && (goarch == "arm64" || goarch == "mipsle"))
	switch goos {
	case "linux":
		switch goarch {
		case "amd64", "arm64", "mipsle":
		default:
			return "", fmt.Errorf("no Linux release for %s", goarch)
		}
		if compact && goarch == "amd64" {
			return "", errors.New("no compact Linux amd64 release is available")
		}
		suffix := ""
		if compact {
			suffix = "_upx"
		}
		return fmt.Sprintf("relaycat_%s_linux_%s%s.tar.gz", strings.TrimPrefix(version, "v"), goarch, suffix), nil
	case "windows":
		if compact {
			return "", errors.New("no compact Windows release is available")
		}
		switch goarch {
		case "386", "amd64", "arm64":
			return fmt.Sprintf("relaycat_%s_windows_%s.zip", strings.TrimPrefix(version, "v"), goarch), nil
		default:
			return "", fmt.Errorf("no Windows release for %s", goarch)
		}
	default:
		return "", fmt.Errorf("updates are not supported on %s", goos)
	}
}

func isOpenWrt() bool {
	_, err := os.Stat("/etc/openwrt_release")
	return err == nil
}

func newHTTPClient() (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	for root := range bundle.Roots() {
		cert, err := x509.ParseCertificate(root.Certificate)
		if err != nil {
			return nil, fmt.Errorf("parsing embedded root certificate: %w", err)
		}
		if root.Constraint == nil {
			pool.AddCert(cert)
		} else {
			pool.AddCertWithConstraint(cert, root.Constraint)
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	return &http.Client{
		Timeout:   5 * time.Minute,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("refusing a non-HTTPS release redirect")
			}
			if len(via) >= 10 {
				return errors.New("too many release redirects")
			}
			return nil
		},
	}, nil
}

func get(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "relaycat-updater")
	var lastErr error
	for attempt := range 3 {
		resp, err := client.Do(req)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				return resp, nil
			}
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("GET %s returned HTTP %d", url, resp.StatusCode)
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < http.StatusInternalServerError {
				return nil, lastErr
			}
		} else {
			lastErr = err
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, lastErr
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release metadata is too large")
	}
	return data, nil
}

func latestVersion(ctx context.Context, client *http.Client) (string, error) {
	resp, err := get(ctx, client, releaseAPI)
	if err != nil {
		return "", fmt.Errorf("checking latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readLimited(resp.Body, maxMetadata)
	if err != nil {
		return "", err
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(data, &release); err != nil {
		return "", fmt.Errorf("reading release metadata: %w", err)
	}
	if !releaseTag.MatchString(release.TagName) {
		return "", fmt.Errorf("invalid release tag %q", release.TagName)
	}
	return release.TagName, nil
}

func checksumFor(data []byte, asset string) ([sha256.Size]byte, error) {
	var expected [sha256.Size]byte
	found := false
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != asset {
			continue
		}
		if found {
			return expected, fmt.Errorf("duplicate checksum for %s", asset)
		}
		hash, err := hex.DecodeString(fields[0])
		if err != nil || len(hash) != sha256.Size {
			return expected, fmt.Errorf("invalid checksum for %s", asset)
		}
		copy(expected[:], hash)
		found = true
	}
	if !found {
		return expected, fmt.Errorf("no checksum for %s", asset)
	}
	return expected, nil
}

func downloadArchive(ctx context.Context, client *http.Client, base, asset string) (string, error) {
	resp, err := get(ctx, client, base+"checksums.txt")
	if err != nil {
		return "", fmt.Errorf("downloading checksums: %w", err)
	}
	checksums, readErr := readLimited(resp.Body, maxMetadata)
	_ = resp.Body.Close()
	if readErr != nil {
		return "", readErr
	}
	expected, err := checksumFor(checksums, asset)
	if err != nil {
		return "", err
	}
	resp, err = get(ctx, client, base+asset)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", asset, err)
	}
	defer func() { _ = resp.Body.Close() }()
	file, err := os.CreateTemp("", "relaycat-update-archive-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	path := file.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, maxArchive+1))
	if err != nil {
		return "", err
	}
	if n > maxArchive {
		return "", errors.New("release archive is too large")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expected[:])) {
		return "", fmt.Errorf("SHA-256 mismatch for %s", asset)
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	keep = true
	return path, nil
}

func extractBinary(archivePath, asset string, dst io.Writer) error {
	name := "relaycat"
	if strings.HasSuffix(asset, ".zip") {
		name = "relaycat.exe"
		archive, err := zip.OpenReader(archivePath)
		if err != nil {
			return err
		}
		defer func() { _ = archive.Close() }()
		for _, file := range archive.File {
			if file.Name != name {
				continue
			}
			if !file.Mode().IsRegular() || file.UncompressedSize64 > maxBinary {
				return errors.New("invalid relaycat executable in ZIP archive")
			}
			reader, err := file.Open()
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(dst, io.LimitReader(reader, maxBinary+1))
			closeErr := reader.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n > maxBinary {
				return errors.New("relaycat executable is too large")
			}
			return nil
		}
		return fmt.Errorf("%s not found in archive", name)
	}
	file, err := os.Open(archivePath) // #nosec G304 -- path is the private temporary file created by downloadArchive.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	archive := tar.NewReader(gz)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Name != name {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != 0 {
			return errors.New("relaycat executable is not a regular file")
		}
		if header.Size < 1 || header.Size > maxBinary {
			return errors.New("invalid relaycat executable size")
		}
		_, err = io.CopyN(dst, archive, header.Size)
		return err
	}
	return fmt.Errorf("%s not found in archive", name)
}

func install(ctx context.Context, client *http.Client, base, asset, target, goos string) (string, error) {
	archivePath, err := downloadArchive(ctx, client, base, asset)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(archivePath) }()
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("checking current executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("current executable is not a regular file")
	}
	pattern := ".relaycat-update-*"
	if goos == "windows" {
		pattern += ".exe"
	}
	stage, err := os.CreateTemp(filepath.Dir(target), pattern)
	if err != nil {
		return "", fmt.Errorf("creating update beside executable: %w", err)
	}
	stagePath := stage.Name()
	defer func() { _ = os.Remove(stagePath) }()
	if err := extractBinary(archivePath, asset, stage); err != nil {
		_ = stage.Close()
		return "", fmt.Errorf("extracting %s: %w", asset, err)
	}
	if err := stage.Chmod(info.Mode().Perm()); err != nil {
		_ = stage.Close()
		return "", err
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		return "", err
	}
	if err := stage.Close(); err != nil {
		return "", err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// #nosec G204 -- stagePath is the checksum-verified release binary staged beside the current executable.
	if output, err := exec.CommandContext(verifyCtx, stagePath, "version").CombinedOutput(); err != nil {
		return "", fmt.Errorf("new executable failed version check: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if goos == "windows" {
		return replaceWindows(stagePath, target)
	}
	if err := os.Rename(stagePath, target); err != nil {
		return "", fmt.Errorf("replacing executable: %w", err)
	}
	return "", nil
}

func replaceWindows(stage, target string) (string, error) {
	backupFile, err := os.CreateTemp(filepath.Dir(target), ".relaycat-previous-*.exe")
	if err != nil {
		return "", err
	}
	backup := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(backup); err != nil {
		return "", err
	}
	if err := os.Rename(target, backup); err != nil {
		return "", fmt.Errorf("moving running executable aside: %w", err)
	}
	if err := os.Rename(stage, target); err != nil {
		if rollbackErr := os.Rename(backup, target); rollbackErr != nil {
			return "", errors.Join(fmt.Errorf("installing update: %w", err), fmt.Errorf("restoring previous executable: %w", rollbackErr))
		}
		return "", fmt.Errorf("installing update: %w", err)
	}
	// Windows may keep the old executable open until this process exits.
	if err := os.Remove(backup); err == nil {
		backup = ""
	}
	return backup, nil
}
