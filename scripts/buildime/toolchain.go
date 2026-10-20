package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type toolchainAsset struct {
	Name   string
	SHA256 string
	Size   int64
}

// Pins come from the official release's assets[].digest and assets[].size:
// https://api.github.com/repos/mstorsjo/llvm-mingw/releases/tags/20261006
var toolchainAssets = map[string]toolchainAsset{
	"windows/arm64": {
		Name: "llvm-mingw-20261006-ucrt-aarch64.zip", Size: 185727081,
		SHA256: "9a835d5179c9f3a5a783c11a7f14062a249b4765dd11add64a66786864e82ab2",
	},
	"windows/amd64": {
		Name: "llvm-mingw-20261006-ucrt-x86_64.zip", Size: 190836485,
		SHA256: "317492c456aa27ee607a5919f1d2d38dcdc1112516a24d0bf4b00d078f52d17a",
	},
	"linux/amd64": {
		Name: "llvm-mingw-20261006-ucrt-ubuntu-22.04-x86_64.tar.xz", Size: 84003044,
		SHA256: "5f9c6ed95b2d4bdb2869a488c5fd5857fbdabcf288a0aa3eb1da43f6a08d8ab4",
	},
}

func (a toolchainAsset) directory() string {
	return strings.TrimSuffix(strings.TrimSuffix(a.Name, ".tar.xz"), ".zip")
}

func prepareToolchain(ctx context.Context, cache, hostOS, hostArch string) (string, error) {
	a, ok := toolchainAssets[hostOS+"/"+hostArch]
	if !ok {
		return "", fmt.Errorf("no pinned compiler for %s/%s; build on Windows amd64/arm64 or Linux amd64, or supply -toolchain", hostOS, hostArch)
	}
	if err := os.MkdirAll(cache, 0755); err != nil {
		return "", err
	}
	root := filepath.Join(cache, a.directory())
	receipt := filepath.Join(root, ".mouse-mapper-toolchain-sha256")
	if data, err := os.ReadFile(receipt); err == nil && strings.TrimSpace(string(data)) == a.SHA256 {
		if err := validateCompilers(root, hostOS, targetArchitectures); err != nil {
			return "", err
		}
		return root, nil
	}
	if _, err := os.Lstat(root); err == nil {
		return "", fmt.Errorf("compiler directory exists without its verified receipt; move it aside before retrying: %s", root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	archive := filepath.Join(cache, a.Name)
	if _, err := os.Stat(archive); errors.Is(err, os.ErrNotExist) {
		fmt.Printf("Downloading pinned llvm-mingw %s for %s/%s (%.1f MB)...\n", toolchainVersion, hostOS, hostArch, float64(a.Size)/1e6)
		url := "https://github.com/mstorsjo/llvm-mingw/releases/download/" + toolchainVersion + "/" + a.Name
		if err := downloadArchive(ctx, downloadClient(), url, archive, a); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	digest, size, err := hashFile(archive)
	if err != nil {
		return "", err
	}
	if digest != a.SHA256 || size != a.Size {
		return "", fmt.Errorf("cached compiler archive does not match the pinned SHA256 and size; remove only this archive and retry: %s", archive)
	}
	stage, err := os.MkdirTemp(cache, ".extract-")
	if err != nil {
		return "", err
	}
	defer removeWithin(stage, cache)
	fmt.Println("Extracting verified compiler into the workspace cache...")
	if strings.HasSuffix(a.Name, ".zip") {
		err = extractZip(archive, stage, a.directory())
	} else {
		err = extractXZ(ctx, archive, stage, a.directory())
	}
	if err != nil {
		return "", err
	}
	extractedRoot := filepath.Join(stage, a.directory())
	if err := validateCompilers(extractedRoot, hostOS, targetArchitectures); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(extractedRoot, filepath.Base(receipt)), []byte(a.SHA256+"\n"), 0644); err != nil {
		return "", err
	}
	if err := os.Rename(extractedRoot, root); err != nil {
		return "", err
	}
	return root, nil
}

func downloadClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 45 * time.Second
	transport.TLSHandshakeTimeout = 20 * time.Second
	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many compiler download redirects")
			}
			switch req.URL.Hostname() {
			case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			default:
				return errors.New("compiler download redirected outside the official GitHub asset hosts")
			}
			if req.URL.Scheme != "https" || req.URL.User != nil {
				return errors.New("compiler download redirect must use HTTPS without credentials")
			}
			return nil
		},
	}
}

func downloadArchive(ctx context.Context, client *http.Client, url, destination string, asset toolchainAsset) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mouse-Mapper-native-helper-builder")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download compiler: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download compiler: HTTP %d", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != asset.Size {
		return fmt.Errorf("compiler download size is %d, expected %d", response.ContentLength, asset.Size)
	}
	f, err := os.CreateTemp(filepath.Dir(destination), ".download-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(response.Body, asset.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != asset.Size || hex.EncodeToString(h.Sum(nil)) != asset.SHA256 {
		return errors.New("compiler download does not match its pinned size and SHA256; nothing was extracted")
	}
	return os.Rename(f.Name(), destination)
}
