package winprobe

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed bin
var embedded embed.FS

const maxAssetSize = 8 << 20

type artifactMetadata struct {
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Machine uint16 `json:"machine"`
}

type manifest struct {
	Schema    int                         `json:"schema"`
	Toolchain string                      `json:"toolchain"`
	Sources   map[string]string           `json:"sources"`
	Artifacts map[string]artifactMetadata `json:"artifacts"`
}

type asset struct {
	name string
	data []byte
	hash [sha256.Size]byte
}

type bundle struct {
	id    string
	pairs map[string][2]asset
}

var bundledAssets = sync.OnceValues(func() (*bundle, error) { return readBundle(embedded) })

// RequireAssets is the release gate; source-only developer builds fail closed
// when an exact native query is requested rather than silently approximating it.
func RequireAssets() error {
	_, err := bundledAssets()
	return err
}

func readBundle(files fs.FS) (*bundle, error) {
	data, err := fs.ReadFile(files, "bin/manifest.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrAssetsUnavailable
	}
	if err != nil {
		return nil, fmt.Errorf("read native input-method manifest: %w", err)
	}
	if len(data) > 64<<10 {
		return nil, errors.New("native input-method manifest is too large")
	}
	var metadata manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return nil, fmt.Errorf("decode native input-method manifest: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || metadata.Schema != 1 || len(metadata.Artifacts) != 6 {
		return nil, errors.New("invalid native input-method manifest schema")
	}
	digest := sha256.Sum256(data)
	result := &bundle{id: hex.EncodeToString(digest[:]), pairs: make(map[string][2]asset)}
	for _, machine := range []uint16{machine386, machineAMD64, machineARM64} {
		arch, _ := architecture(machine)
		var pair [2]asset
		for index, name := range []string{"ime-probe.exe", "ime-hook-" + arch + ".dll"} {
			key := arch + "/" + name
			info, found := metadata.Artifacts[key]
			if !found || info.Size <= 0 || info.Size > maxAssetSize || info.Machine != machine ||
				len(info.SHA256) != sha256.Size*2 || info.SHA256 != strings.ToLower(info.SHA256) {
				return nil, fmt.Errorf("invalid native input-method artifact metadata: %s", key)
			}
			want, err := hex.DecodeString(info.SHA256)
			if err != nil {
				return nil, fmt.Errorf("invalid native input-method digest: %s", key)
			}
			content, err := fs.ReadFile(files, "bin/"+key)
			if err != nil {
				return nil, fmt.Errorf("read native input-method artifact %s: %w", key, err)
			}
			hash := sha256.Sum256(content)
			if int64(len(content)) != info.Size || !bytes.Equal(hash[:], want) {
				return nil, fmt.Errorf("native input-method artifact digest mismatch: %s", key)
			}
			pair[index] = asset{name: name, data: content, hash: hash}
		}
		result.pairs[arch] = pair
	}
	return result, nil
}

func plainPath(path string, wantDirectory bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != wantDirectory ||
		(!wantDirectory && !info.Mode().IsRegular()) {
		return nil, fmt.Errorf("native input-method cache path is not a plain file or directory: %s", path)
	}
	if err := rejectReparsePoint(path); err != nil {
		return nil, err
	}
	return info, nil
}

func ensureDirectory(path string) error {
	parent := filepath.Dir(path)
	if parent == path {
		_, err := plainPath(path, true)
		return err
	}
	if err := ensureDirectory(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	_, err := plainPath(path, true)
	return err
}

func verifyFile(path string, item asset) error {
	info, err := plainPath(path, false)
	if err != nil {
		return err
	}
	if info.Size() != int64(len(item.data)) {
		return fmt.Errorf("native input-method cache content differs: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, int64(len(item.data))+1))
	if err != nil {
		return err
	}
	if n != int64(len(item.data)) || !bytes.Equal(hash.Sum(nil), item.hash[:]) {
		return fmt.Errorf("native input-method cache content differs: %s", path)
	}
	return nil
}

func publishAsset(directory string, item asset) (string, error) {
	destination := filepath.Join(directory, item.name)
	if _, err := os.Lstat(destination); err == nil {
		return destination, verifyFile(destination, item)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	temporary, err := os.CreateTemp(directory, ".ime-asset-*")
	if err != nil {
		return "", err
	}
	name := temporary.Name()
	defer os.Remove(name)
	defer temporary.Close()
	if _, err := temporary.Write(item.data); err != nil {
		return "", err
	}
	if err := temporary.Chmod(0o700); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	// Link is an atomic, non-replacing publication. A racing writer must publish
	// the same verified bytes; existing mismatches are never removed or replaced.
	if err := os.Link(name, destination); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	if err := verifyFile(destination, item); err != nil {
		return "", err
	}
	return destination, nil
}

func (assets *bundle) extract(cache, arch string) ([2]string, error) {
	var paths [2]string
	pair, ok := assets.pairs[arch]
	if !ok {
		return paths, ErrUnsupported
	}
	if !filepath.IsAbs(cache) {
		return paths, errors.New("native input-method cache must be absolute")
	}
	directory := filepath.Join(cache, "Mouse Mapper", "native-input", assets.id, arch)
	if err := ensureDirectory(directory); err != nil {
		return paths, fmt.Errorf("prepare native input-method cache: %w", err)
	}
	for i, item := range pair {
		path, err := publishAsset(directory, item)
		if err != nil {
			return [2]string{}, fmt.Errorf("extract native input-method helper: %w", err)
		}
		paths[i] = path
	}
	return paths, nil
}
