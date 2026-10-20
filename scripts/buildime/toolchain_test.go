package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedToolchainMetadata(t *testing.T) {
	for _, host := range []string{"windows/arm64", "windows/amd64", "linux/amd64"} {
		a, ok := toolchainAssets[host]
		if !ok || !strings.HasPrefix(a.Name, "llvm-mingw-"+toolchainVersion+"-ucrt-") || a.Size <= 0 {
			t.Fatalf("invalid asset for %s: %+v", host, a)
		}
		digest, err := hex.DecodeString(a.SHA256)
		if err != nil || len(digest) != sha256.Size {
			t.Errorf("invalid digest for %s", host)
		}
		if a.directory() == a.Name || filepath.Base(a.Name) != a.Name {
			t.Errorf("invalid archive path for %s", host)
		}
	}
}

func TestDownloadArchiveVerifiesSizeHashAndCleansTemporaryFiles(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		chunked bool
		status  int
		valid   bool
	}{
		{name: "valid", payload: "compiler", status: http.StatusOK, valid: true},
		{name: "wrong-hash", payload: "modified", status: http.StatusOK},
		{name: "short", payload: "short", status: http.StatusOK},
		{name: "long", payload: "compiler-excess", status: http.StatusOK},
		{name: "chunked-excess", payload: "compiler-excess", chunked: true, status: http.StatusOK},
		{name: "not-found", status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("User-Agent") != "Mouse-Mapper-native-helper-builder" {
					t.Error("missing downloader user-agent")
				}
				w.WriteHeader(test.status)
				if test.chunked {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write([]byte(test.payload))
			}))
			defer server.Close()
			root := t.TempDir()
			destination := filepath.Join(root, "compiler.zip")
			digest := sha256.Sum256([]byte("compiler"))
			asset := toolchainAsset{Name: "compiler.zip", Size: 8, SHA256: hex.EncodeToString(digest[:])}
			err := downloadArchive(context.Background(), server.Client(), server.URL, destination, asset)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(destination)
				if err != nil || string(data) != "compiler" {
					t.Fatalf("download = %q, %v", data, err)
				}
			} else {
				if err == nil {
					t.Error("unverified download succeeded")
				}
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Errorf("failed download left files: %v, %v", entries, err)
				}
			}
		})
	}
}

func TestDownloadRedirectsStayOnOfficialHTTPSHosts(t *testing.T) {
	client := downloadClient()
	if client.Timeout <= 0 {
		t.Fatal("HTTP client is unbounded")
	}
	for _, test := range []struct {
		url   string
		valid bool
	}{
		{"https://github.com/mstorsjo/llvm-mingw/releases/download/version/file", true},
		{"https://release-assets.githubusercontent.com/file", true},
		{"https://objects.githubusercontent.com/file", true},
		{"http://github.com/file", false},
		{"https://github.com.evil.example/file", false},
		{"https://user:password@github.com/file", false},
		{"https://example.com/file", false},
	} {
		u, err := url.Parse(test.url)
		if err != nil {
			t.Fatal(err)
		}
		err = client.CheckRedirect(&http.Request{URL: u}, nil)
		if (err == nil) != test.valid {
			t.Errorf("redirect %s: %v", test.url, err)
		}
	}
	u, _ := url.Parse("https://github.com/file")
	if err := client.CheckRedirect(&http.Request{URL: u}, make([]*http.Request, 10)); err == nil {
		t.Error("unbounded redirect chain accepted")
	}
}
