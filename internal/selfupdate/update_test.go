package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, os, arch string
		openwrt        bool
		opts           Options
		want           string
	}{
		{name: "Linux amd64", os: "linux", arch: "amd64", want: "relaycat_0.3.8_linux_amd64.tar.gz"},
		{name: "OpenWrt MIPS", os: "linux", arch: "mipsle", openwrt: true, want: "relaycat_0.3.8_linux_mipsle_upx.tar.gz"},
		{name: "OpenWrt ARM64", os: "linux", arch: "arm64", openwrt: true, want: "relaycat_0.3.8_linux_arm64_upx.tar.gz"},
		{name: "standard override", os: "linux", arch: "arm64", openwrt: true, opts: Options{Standard: true}, want: "relaycat_0.3.8_linux_arm64.tar.gz"},
		{name: "compact override", os: "linux", arch: "arm64", opts: Options{Compact: true}, want: "relaycat_0.3.8_linux_arm64_upx.tar.gz"},
		{name: "Windows x86", os: "windows", arch: "386", want: "relaycat_0.3.8_windows_386.zip"},
		{name: "Windows ARM64", os: "windows", arch: "arm64", want: "relaycat_0.3.8_windows_arm64.zip"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := assetName("v0.3.8", test.os, test.arch, test.openwrt, test.opts)
			if err != nil || got != test.want {
				t.Fatalf("assetName = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	if _, err := assetName("v0.3.8", "linux", "amd64", false, Options{Compact: true}); err == nil {
		t.Fatal("compact amd64 unexpectedly supported")
	}
}

func TestChecksumFor(t *testing.T) {
	t.Parallel()
	asset := "relaycat_0.3.8_linux_amd64.tar.gz"
	hash := strings.Repeat("a", 64)
	got, err := checksumFor([]byte(hash+"  "+asset+"\n"), asset)
	if err != nil || hex.EncodeToString(got[:]) != hash {
		t.Fatalf("checksumFor = %x, %v", got, err)
	}
	if _, err := checksumFor([]byte(hash+"  "+asset+"\n"+hash+"  "+asset+"\n"), asset); err == nil {
		t.Fatal("duplicate checksum accepted")
	}
	if _, err := checksumFor([]byte("invalid  "+asset+"\n"), asset); err == nil {
		t.Fatal("invalid checksum accepted")
	}
}

func TestInstallVerifiesBeforeReplacing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell executable fixture")
	}
	t.Parallel()
	const asset = "relaycat_9.9.9_linux_amd64.tar.gz"
	newBinary := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo 'relaycat 9.9.9'; fi\n")
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "relaycat", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(newBinary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(newBinary); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	correct := sha256.Sum256(archive.Bytes())

	for _, test := range []struct {
		name    string
		badHash bool
	}{
		{name: "valid archive"},
		{name: "tampered archive", badHash: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			target := filepath.Join(dir, "relaycat")
			oldBinary := []byte("#!/bin/sh\necho 'relaycat 0.0.1'\n")
			if err := os.WriteFile(target, oldBinary, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, 0o755); err != nil { // #nosec G302 -- the test fixture is an executable shell script.
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/checksums.txt":
					hash := correct
					if test.badHash {
						hash = sha256.Sum256([]byte("different archive"))
					}
					_, _ = fmt.Fprintf(w, "%x  %s\n", hash, asset)
				case "/" + asset:
					_, _ = w.Write(archive.Bytes())
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			_, err := install(context.Background(), server.Client(), server.URL+"/", asset, target, "linux")
			if (err != nil) != test.badHash {
				t.Fatalf("install error = %v; want error %v", err, test.badHash)
			}
			got, err := os.ReadFile(target) // #nosec G304 -- target is a test file in t.TempDir.
			if err != nil {
				t.Fatal(err)
			}
			want := newBinary
			if test.badHash {
				want = oldBinary
			}
			if !bytes.Equal(got, want) {
				t.Fatal("installed executable contents do not match expected value")
			}
		})
	}
}
