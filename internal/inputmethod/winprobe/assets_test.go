package winprobe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
)

func sampleBundle(t *testing.T) (fstest.MapFS, manifest) {
	t.Helper()
	files := fstest.MapFS{}
	metadata := manifest{Schema: 1, Toolchain: "test", Sources: map[string]string{},
		Artifacts: make(map[string]artifactMetadata)}
	for _, machine := range []uint16{machine386, machineAMD64, machineARM64} {
		arch, _ := architecture(machine)
		for _, name := range []string{"ime-probe.exe", "ime-hook-" + arch + ".dll"} {
			path := arch + "/" + name
			data := []byte("test-only nonexecutable fixture: " + path)
			hash := sha256.Sum256(data)
			files["bin/"+path] = &fstest.MapFile{Data: data}
			metadata.Artifacts[path] = artifactMetadata{SHA256: hex.EncodeToString(hash[:]),
				Size: int64(len(data)), Machine: machine}
		}
	}
	setManifest(t, files, metadata)
	return files, metadata
}

func setManifest(t *testing.T, files fstest.MapFS, metadata manifest) {
	t.Helper()
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	files["bin/manifest.json"] = &fstest.MapFile{Data: data}
}

func TestEmbeddedProbeAssets(t *testing.T) {
	err := RequireAssets()
	if errors.Is(err, ErrAssetsUnavailable) && os.Getenv("MOUSE_MAPPER_REQUIRE_NATIVE_PROBE_ASSETS") != "1" {
		t.Skip("source-only build; release builds require MOUSE_MAPPER_REQUIRE_NATIVE_PROBE_ASSETS=1")
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestBundleRequiresAllMatchingArtifactHashes(t *testing.T) {
	if _, err := readBundle(fstest.MapFS{}); !errors.Is(err, ErrAssetsUnavailable) {
		t.Fatalf("missing manifest = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(fstest.MapFS, *manifest)
	}{
		{"schema", func(_ fstest.MapFS, m *manifest) { m.Schema++ }},
		{"missing_entry", func(_ fstest.MapFS, m *manifest) { delete(m.Artifacts, "arm64/ime-probe.exe") }},
		{"extra_entry", func(_ fstest.MapFS, m *manifest) { m.Artifacts["other"] = artifactMetadata{} }},
		{"path_escape", func(_ fstest.MapFS, m *manifest) {
			m.Artifacts["../../other.exe"] = m.Artifacts["amd64/ime-probe.exe"]
			delete(m.Artifacts, "amd64/ime-probe.exe")
		}},
		{"wrong_machine", func(_ fstest.MapFS, m *manifest) {
			value := m.Artifacts["amd64/ime-probe.exe"]
			value.Machine = machineARM64
			m.Artifacts["amd64/ime-probe.exe"] = value
		}},
		{"size", func(_ fstest.MapFS, m *manifest) {
			value := m.Artifacts["amd64/ime-probe.exe"]
			value.Size++
			m.Artifacts["amd64/ime-probe.exe"] = value
		}},
		{"digest", func(_ fstest.MapFS, m *manifest) {
			value := m.Artifacts["amd64/ime-probe.exe"]
			value.SHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			m.Artifacts["amd64/ime-probe.exe"] = value
		}},
		{"missing_file", func(files fstest.MapFS, _ *manifest) { delete(files, "bin/386/ime-hook-386.dll") }},
		{"changed_file", func(files fstest.MapFS, _ *manifest) { files["bin/arm64/ime-probe.exe"].Data[0]++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			files, metadata := sampleBundle(t)
			test.mutate(files, &metadata)
			setManifest(t, files, metadata)
			if _, err := readBundle(files); err == nil {
				t.Fatal("accepted invalid bundle")
			}
		})
	}
	files, _ := sampleBundle(t)
	files["bin/manifest.json"].Data = append(files["bin/manifest.json"].Data, []byte("{}")...)
	if _, err := readBundle(files); err == nil {
		t.Fatal("accepted trailing manifest data")
	}
}

func TestExtractionPublishesIdenticalBytesWithoutOverwrite(t *testing.T) {
	files, _ := sampleBundle(t)
	assets, err := readBundle(files)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	paths, err := assets.extract(cache, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if repeated, err := assets.extract(cache, "arm64"); err != nil || repeated != paths {
		t.Fatalf("same-content extraction = %v, %v", repeated, err)
	}
	corrupt := []byte("do not overwrite this unexpected file")
	if err := os.WriteFile(paths[0], corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := assets.extract(cache, "arm64"); err == nil {
		t.Fatal("silently replaced mismatching content")
	}
	unchanged, err := os.ReadFile(paths[0])
	if err != nil || string(unchanged) != string(corrupt) {
		t.Fatalf("unexpected file was modified: %q, %v", unchanged, err)
	}
	if _, err := assets.extract("relative-cache", "arm64"); err == nil {
		t.Fatal("accepted relative cache")
	}
	if _, err := assets.extract(cache, "untrusted/../arch"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("untrusted architecture = %v", err)
	}
}

func TestConcurrentExtractionDoesNotExposePartialFiles(t *testing.T) {
	files, _ := sampleBundle(t)
	assets, err := readBundle(files)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	var workers sync.WaitGroup
	errors := make(chan error, 12)
	for range 12 {
		workers.Go(func() {
			_, err := assets.extract(cache, "amd64")
			errors <- err
		})
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Error(err)
		}
	}
	paths, err := assets.extract(cache, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(paths[0]))
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary files were left behind: %v, %v", entries, err)
	}
}

func TestExtractionRejectsDirectoryOrSymlinkInFileSlot(t *testing.T) {
	for _, mode := range []string{"directory", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			files, _ := sampleBundle(t)
			assets, err := readBundle(files)
			if err != nil {
				t.Fatal(err)
			}
			cache := t.TempDir()
			directory := filepath.Join(cache, "Mouse Mapper", "native-input", assets.id, "386")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(directory, "ime-probe.exe")
			if mode == "directory" {
				err = os.Mkdir(destination, 0o700)
			} else {
				reference := filepath.Join(cache, "reference")
				if err := os.WriteFile(reference, assets.pairs["386"][0].data, 0o600); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(reference, destination)
				if err != nil {
					t.Skipf("test host cannot create a symlink: %v", err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := assets.extract(cache, "386"); err == nil {
				t.Fatal("accepted an indirect or non-regular helper")
			}
		})
	}
}

func TestExtractionRejectsSymlinkedDirectory(t *testing.T) {
	files, _ := sampleBundle(t)
	assets, err := readBundle(files)
	if err != nil {
		t.Fatal(err)
	}
	cache, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cache, "Mouse Mapper")); err != nil {
		t.Skipf("test host cannot create directory symlinks: %v", err)
	}
	if _, err := assets.extract(cache, "amd64"); err == nil {
		t.Fatal("followed symlinked cache directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote outside cache: %v, %v", entries, err)
	}
}
