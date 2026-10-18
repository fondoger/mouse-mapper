package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) (configuration, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	config := configuration{Name: "Mouse Mapper", Version: "0.1.0"}
	config.Updates.GitHub = "fondoger/mouse-mapper"
	config.Updates.TagPrefix = "v"
	config.Updates.PublicKey = base64.StdEncoding.EncodeToString(public)
	return config, private
}

func putFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func putManifest(t *testing.T, path string, update manifest) {
	t.Helper()
	data, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, path, data)
}

func addTarget(t *testing.T, config configuration, key ed25519.PrivateKey, directory, target string) manifest {
	t.Helper()
	archiveName := "mouse-mapper-" + config.Version + "-" + target + ".tar.gz"
	data := []byte("fixture archive for " + target)
	putFile(t, filepath.Join(directory, archiveName), data)
	digest := sha256.Sum256(data)
	update := manifest{
		Version: config.Version,
		Date:    time.Now().UTC().Format(time.RFC3339),
		signedFile: signedFile{
			URL:       "https://github.com/" + config.Updates.GitHub + "/releases/download/v" + config.Version + "/" + archiveName,
			Size:      int64(len(data)),
			Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, digest[:])),
		},
	}
	putManifest(t, filepath.Join(directory, "update-"+target+".json"), update)
	putFile(t, filepath.Join(directory, installerName(config, target)), []byte("fixture installer"))
	return update
}

func publicKey(key ed25519.PrivateKey) ed25519.PublicKey {
	return key.Public().(ed25519.PublicKey)
}

func TestVerifyCompleteRelease(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "flat"
		if nested {
			name = "nested"
		}
		t.Run(name, func(t *testing.T) {
			config, key := testConfig(t)
			directory := t.TempDir()
			targets := strings.Split(allTargets, ",")
			for _, target := range targets {
				folder := directory
				if nested {
					folder = filepath.Join(directory, target)
				}
				addTarget(t, config, key, folder, target)
			}
			assets, err := verifyAssets(config, publicKey(key), directory, targets)
			if err != nil || len(assets) != 15 {
				t.Fatalf("assets = %d, error = %v", len(assets), err)
			}
			file := filepath.Join(directory, "SHA256SUMS.txt")
			if err := writeChecksums(file, assets); err != nil {
				t.Fatal(err)
			}
			text, err := os.ReadFile(file)
			if err != nil || strings.Count(string(text), "\n") != 15 {
				t.Fatalf("invalid checksums: %v", err)
			}
		})
	}
}

func TestRejectIncorrectManifest(t *testing.T) {
	cases := map[string]func(*manifest){
		"version":   func(m *manifest) { m.Version = "9.9.9" },
		"size":      func(m *manifest) { m.Size++ },
		"signature": func(m *manifest) { m.Signature = "unsigned" },
		"timestamp": func(m *manifest) { m.Date = "" },
		"host":      func(m *manifest) { m.URL = strings.Replace(m.URL, "github.com", "example.com", 1) },
		"repo":      func(m *manifest) { m.URL = strings.Replace(m.URL, "fondoger", "other", 1) },
		"target":    func(m *manifest) { m.URL = strings.Replace(m.URL, "windows-amd64", "windows-arm64", 1) },
		"query":     func(m *manifest) { m.URL += "?download=1" },
		"traversal": func(m *manifest) { m.URL = strings.Replace(m.URL, "/mouse-mapper-0.1.0", "/../mouse-mapper-0.1.0", 1) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			config, key := testConfig(t)
			directory := t.TempDir()
			update := addTarget(t, config, key, directory, "windows-amd64")
			change(&update)
			putManifest(t, filepath.Join(directory, "update-windows-amd64.json"), update)
			if _, err := verifyAssets(config, publicKey(key), directory, []string{"windows-amd64"}); err == nil {
				t.Fatal("accepted an invalid update manifest")
			}
		})
	}
}

func TestSignedDeltaIsRequiredAndVerified(t *testing.T) {
	config, key := testConfig(t)
	directory := t.TempDir()
	update := addTarget(t, config, key, directory, "windows-amd64")
	name := "mouse-mapper-0.0.9-to-0.1.0-windows-amd64.delta"
	data := []byte("fixture delta")
	digest := sha256.Sum256(data)
	update.Deltas = append(update.Deltas, struct {
		From string `json:"from"`
		signedFile
	}{
		From: "0.0.9",
		signedFile: signedFile{
			URL:       "https://github.com/fondoger/mouse-mapper/releases/download/v0.1.0/" + name,
			Size:      int64(len(data)),
			Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, digest[:])),
		},
	})
	putManifest(t, filepath.Join(directory, "update-windows-amd64.json"), update)
	if _, err := verifyAssets(config, publicKey(key), directory, []string{"windows-amd64"}); err == nil {
		t.Fatal("accepted a manifest whose delta is missing")
	}
	putFile(t, filepath.Join(directory, name), data)
	assets, err := verifyAssets(config, publicKey(key), directory, []string{"windows-amd64"})
	if err != nil || len(assets) != 4 {
		t.Fatalf("delta verification: assets=%d, error=%v", len(assets), err)
	}
	data[0] ^= 1
	putFile(t, filepath.Join(directory, name), data)
	if _, err := verifyAssets(config, publicKey(key), directory, []string{"windows-amd64"}); err == nil {
		t.Fatal("accepted a modified delta with its original size")
	}
}

func TestRejectCorruptArchiveAndMissingInstaller(t *testing.T) {
	config, key := testConfig(t)
	directory := t.TempDir()
	addTarget(t, config, key, directory, "windows-amd64")
	archive := filepath.Join(directory, "mouse-mapper-0.1.0-windows-amd64.tar.gz")
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 1
	putFile(t, archive, data)
	if _, err := verifyAssets(config, publicKey(key), directory, []string{"windows-amd64"}); err == nil {
		t.Fatal("accepted an altered archive with the original size")
	}
	addTarget(t, config, key, directory, "windows-amd64")
	config.Name = "No Installer"
	if _, err := verifyAssets(config, publicKey(key), directory, []string{"windows-amd64"}); err == nil {
		t.Fatal("accepted a release with no expected installer")
	}
}

func TestRejectMissingOrDuplicateTargets(t *testing.T) {
	config, key := testConfig(t)
	directory := t.TempDir()
	addTarget(t, config, key, directory, "windows-amd64")
	for _, targets := range [][]string{
		{"windows-amd64", "windows-arm64"},
		{"windows-amd64", "windows-amd64"},
		{"linux-amd64"},
		{},
	} {
		if _, err := verifyAssets(config, publicKey(key), directory, targets); err == nil {
			t.Fatalf("accepted invalid target set %v", targets)
		}
	}
}

func TestConfigurationAndSigningKey(t *testing.T) {
	config, private := testConfig(t)
	key, err := validateConfig(config, "v0.1.0", "fondoger/mouse-mapper")
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateKey(key, base64.StdEncoding.EncodeToString(private)); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"", "not-a-key", base64.StdEncoding.EncodeToString(make([]byte, 64))} {
		if err := validatePrivateKey(key, secret); err == nil {
			t.Fatal("accepted an absent or invalid private key")
		}
	}
	_, otherKey := testConfig(t)
	if err := validatePrivateKey(key, base64.StdEncoding.EncodeToString(otherKey)); err == nil {
		t.Fatal("accepted a different signing key")
	}
	if _, err := validateConfig(config, "v0.2.0", "fondoger/mouse-mapper"); err == nil {
		t.Fatal("accepted a mismatched tag")
	}
	if _, err := validateConfig(config, "v0.1.0", "other/mouse-mapper"); err == nil {
		t.Fatal("accepted a mismatched repository")
	}
	config.Updates.PrivateKey = "secret.key"
	if _, err := validateConfig(config, "v0.1.0", "fondoger/mouse-mapper"); err == nil {
		t.Fatal("accepted a committed private key path")
	}
}
