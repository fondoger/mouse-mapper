// buildime builds the small, no-CRT Windows IME query helpers without enabling
// cgo in the Go application. Its compiler stays inside the workspace cache.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const toolchainVersion = "20261006"

var targetArchitectures = []string{"amd64", "arm64", "386"}

type artifactRecord struct {
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Machine uint16 `json:"machine"`
}

type manifest struct {
	Schema    int                       `json:"schema"`
	Toolchain string                    `json:"toolchain"`
	Sources   map[string]string         `json:"sources"`
	Artifacts map[string]artifactRecord `json:"artifacts"`
}

type options struct {
	root        string
	toolchain   string
	arch        string
	prepareOnly bool
	verifyOnly  bool
}

func main() {
	var opts options
	flag.StringVar(&opts.root, "root", ".", "repository root")
	flag.StringVar(&opts.toolchain, "toolchain", "", "use a trusted llvm-mingw directory instead of the pinned download")
	flag.StringVar(&opts.arch, "arch", strings.Join(targetArchitectures, ","), "comma-separated Windows helper architectures")
	flag.BoolVar(&opts.prepareOnly, "prepare-only", false, "download and verify the compiler without building helpers")
	flag.BoolVar(&opts.verifyOnly, "verify-only", false, "verify existing helpers, source hashes and PE metadata without a compiler")
	flag.Parse()
	if err := run(context.Background(), opts); err != nil {
		fmt.Fprintln(os.Stderr, "buildime:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) error {
	if opts.prepareOnly && opts.verifyOnly {
		return errors.New("-prepare-only and -verify-only cannot be combined")
	}
	root, err := filepath.Abs(opts.root)
	if err != nil {
		return err
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !strings.HasPrefix(string(mod), "module github.com/fondoger/mouse-mapper\n") && !strings.HasPrefix(string(mod), "module github.com/fondoger/mouse-mapper\r\n") {
		return fmt.Errorf("-root must be the Mouse Mapper repository: %s", root)
	}
	architectures, err := parseArchitectures(opts.arch)
	if err != nil {
		return err
	}
	packageDir := filepath.Join(root, "internal", "inputmethod", "winprobe")
	if opts.verifyOnly {
		return verifyManifest(packageDir, architectures)
	}
	compilerRoot := opts.toolchain
	compilerIdentity := "custom"
	if compilerRoot == "" {
		compilerRoot, err = prepareToolchain(ctx, filepath.Join(root, ".mygo", "toolchains"), runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return err
		}
		compilerIdentity = "llvm-mingw-" + toolchainVersion
	} else {
		compilerRoot, err = filepath.Abs(compilerRoot)
		if err != nil {
			return err
		}
	}
	if err := validateCompilers(compilerRoot, runtime.GOOS, architectures); err != nil {
		return err
	}
	fmt.Println("Compiler:", compilerRoot)
	if opts.prepareOnly {
		return nil
	}
	sources, err := hashSources(packageDir)
	if err != nil {
		return err
	}
	cache := filepath.Join(root, ".mygo")
	if err := os.MkdirAll(cache, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(cache, "ime-build-")
	if err != nil {
		return err
	}
	defer removeWithin(stage, cache)
	result := manifest{Schema: 1, Toolchain: compilerIdentity, Sources: sources, Artifacts: make(map[string]artifactRecord)}
	for _, arch := range architectures {
		for _, dll := range []bool{false, true} {
			name := artifactName(arch, dll)
			out := filepath.Join(stage, arch, name)
			if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
				return err
			}
			compiler := compilerPath(compilerRoot, runtime.GOOS, arch)
			args := compilerArguments(packageDir, arch, dll, out)
			buildCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			cmd := exec.CommandContext(buildCtx, compiler, args...)
			cmd.Dir = packageDir
			cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=0")
			output, buildErr := cmd.CombinedOutput()
			cancel()
			if buildErr != nil {
				return fmt.Errorf("compile %s/%s: %w\n%s", arch, name, buildErr, output)
			}
			record, err := inspectArtifact(out, arch, dll)
			if err != nil {
				return err
			}
			result.Artifacts[arch+"/"+name] = record
			fmt.Printf("Built %s/%s (%d bytes, SHA256 %s)\n", arch, name, record.Size, record.SHA256)
		}
	}
	// Compile and inspect every requested target before replacing any embedded file.
	for name := range result.Artifacts {
		if err := atomicCopy(filepath.Join(stage, filepath.FromSlash(name)), filepath.Join(packageDir, "bin", filepath.FromSlash(name))); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(packageDir, "bin", "manifest.json"), append(data, '\n'), 0644); err != nil {
		return err
	}
	return verifyManifest(packageDir, architectures)
}

func parseArchitectures(list string) ([]string, error) {
	var result []string
	for _, value := range strings.Split(list, ",") {
		arch := strings.TrimSpace(value)
		if !slices.Contains(targetArchitectures, arch) {
			return nil, fmt.Errorf("unsupported Windows architecture %q", arch)
		}
		if slices.Contains(result, arch) {
			return nil, fmt.Errorf("duplicate Windows architecture %q", arch)
		}
		result = append(result, arch)
	}
	return result, nil
}

func artifactName(arch string, dll bool) string {
	if dll {
		return "ime-hook-" + arch + ".dll"
	}
	return "ime-probe.exe"
}

func compilerPath(root, hostOS, arch string) string {
	triple := map[string]string{"amd64": "x86_64", "arm64": "aarch64", "386": "i686"}[arch]
	name := triple + "-w64-mingw32-clang"
	if hostOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, "bin", name)
}

func validateCompilers(root, hostOS string, architectures []string) error {
	for _, arch := range architectures {
		path := compilerPath(root, hostOS, arch)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("compiler is missing: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("compiler is not a regular file: %s", path)
		}
	}
	return nil
}

func compilerArguments(packageDir, arch string, dll bool, output string) []string {
	source, entry := "probe.c", "mm_probe_entry"
	if dll {
		source, entry = "hook.c", "DllMain"
	}
	if arch == "386" {
		entry = "_" + entry
		if dll {
			entry += "@12"
		}
	}
	args := []string{
		"-std=c11", "-Os", "-Wall", "-Wextra", "-Werror", "-ffreestanding", "-fno-builtin",
		"-fno-stack-protector", "-ffunction-sections", "-fdata-sections", "-fno-ident", "-nostdlib",
		"-DUNICODE", "-D_UNICODE", "-D_WIN32_WINNT=0x0A00",
		"-DMM_ARCH=\"" + arch + "\"", "-Wl,--entry," + entry,
		"-Wl,--subsystem,windows", "-Wl,--no-insert-timestamp", "-Wl,--dynamicbase", "-Wl,--nxcompat",
		"-Wl,--gc-sections", "-Wl,--strip-all",
	}
	if arch != "386" {
		args = append(args, "-Wl,--high-entropy-va")
	}
	if dll {
		args = append(args, "-shared", "-Wl,--kill-at")
	}
	return append(args, filepath.Join(packageDir, "native", source), "-o", output, "-lkernel32", "-luser32", "-lole32")
}

func hashSources(packageDir string) (map[string]string, error) {
	entries, err := os.ReadDir(filepath.Join(packageDir, "native"))
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".c" && filepath.Ext(entry.Name()) != ".h" {
			continue
		}
		name := "native/" + entry.Name()
		sum, _, err := hashFile(filepath.Join(packageDir, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		result[name] = sum
	}
	for _, required := range []string{"native/probe.c", "native/hook.c", "native/protocol.h"} {
		if result[required] == "" {
			return nil, fmt.Errorf("missing helper source %s", required)
		}
	}
	return result, nil
}

func verifyManifest(packageDir string, architectures []string) error {
	data, err := os.ReadFile(filepath.Join(packageDir, "bin", "manifest.json"))
	if err != nil {
		return err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m.Schema != 1 || m.Toolchain == "" {
		return errors.New("unsupported or incomplete helper manifest")
	}
	sources, err := hashSources(packageDir)
	if err != nil {
		return err
	}
	if len(sources) != len(m.Sources) {
		return errors.New("helper sources changed; run go run ./scripts/buildime again")
	}
	for name, digest := range sources {
		if m.Sources[name] != digest {
			return fmt.Errorf("helper source %s changed; run go run ./scripts/buildime again", name)
		}
	}
	for _, arch := range architectures {
		for _, dll := range []bool{false, true} {
			name := arch + "/" + artifactName(arch, dll)
			expected, ok := m.Artifacts[name]
			if !ok {
				return fmt.Errorf("helper %s is not in the build manifest", name)
			}
			actual, err := inspectArtifact(filepath.Join(packageDir, "bin", filepath.FromSlash(name)), arch, dll)
			if err != nil {
				return err
			}
			if actual != expected {
				return fmt.Errorf("helper %s differs from its build manifest", name)
			}
		}
	}
	fmt.Println("Verified helper sources, hashes, PE targets, exports and system-only imports.")
	return nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func atomicCopy(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return atomicWrite(destination, data, 0644)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".buildime-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func removeWithin(path, parent string) error {
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !strictlyWithin(resolvedPath, resolvedParent) {
		return fmt.Errorf("refusing to remove a directory outside its staging parent: %s", path)
	}
	return os.RemoveAll(resolvedPath)
}

func strictlyWithin(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
