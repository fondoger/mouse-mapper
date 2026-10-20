package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseArchitectures(t *testing.T) {
	got, err := parseArchitectures("amd64, arm64,386")
	if err != nil || !slices.Equal(got, targetArchitectures) {
		t.Fatalf("architectures = %v, %v", got, err)
	}
	for _, invalid := range []string{"", "x86_64", "arm64,amd64,arm64", "amd64,", "../amd64"} {
		if _, err := parseArchitectures(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func TestCompilerArgumentsHaveExplicitNoCRTEntryPoints(t *testing.T) {
	for _, arch := range targetArchitectures {
		for _, dll := range []bool{false, true} {
			args := compilerArguments("sources", arch, dll, "output")
			for _, expected := range []string{"-nostdlib", "-ffreestanding", "-fno-builtin", "-fno-stack-protector", "-Wl,--no-insert-timestamp", "-Wl,--subsystem,windows", "-Wl,--dynamicbase", "-Wl,--nxcompat"} {
				if !slices.Contains(args, expected) {
					t.Errorf("%s dll=%v misses %s", arch, dll, expected)
				}
			}
			entry := "mm_probe_entry"
			if dll {
				entry = "DllMain"
			}
			if arch == "386" {
				entry = "_" + entry
				if dll {
					entry += "@12"
				}
			}
			if !slices.Contains(args, "-Wl,--entry,"+entry) {
				t.Errorf("incorrect %s DLL=%v entry", arch, dll)
			}
			if slices.Contains(args, "-shared") != dll || slices.Contains(args, "-Wl,--kill-at") != dll {
				t.Error("DLL flags do not match output")
			}
			for _, arg := range args {
				if strings.HasPrefix(arg, "-l") && !slices.Contains([]string{"-lkernel32", "-luser32", "-lole32"}, arg) {
					t.Errorf("unexpected library %s", arg)
				}
			}
		}
	}
}

func TestCompilerPathsAndUniqueDLLNames(t *testing.T) {
	seen := make(map[string]bool)
	for _, arch := range targetArchitectures {
		path := compilerPath("compiler", "windows", arch)
		if !strings.HasSuffix(path, "-w64-mingw32-clang.exe") {
			t.Errorf("Windows compiler = %s", path)
		}
		if strings.HasSuffix(compilerPath("compiler", "linux", arch), ".exe") {
			t.Error("Linux compiler has an exe suffix")
		}
		name := artifactName(arch, true)
		if seen[name] {
			t.Error("hook DLL basenames must be distinct across architectures")
		}
		seen[name] = true
	}
}

func TestPEValidationRejectsWrongABIAndRuntimeDependencies(t *testing.T) {
	for _, arch := range targetArchitectures {
		for _, dll := range []bool{false, true} {
			data := fixturePE(t, arch, dll)
			f, err := pe.NewFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if err := validatePE(f, arch, dll); err != nil {
				t.Fatalf("valid %s dll=%v rejected: %v", arch, dll, err)
			}
			if err := validatePE(f, "unknown", dll); err == nil {
				t.Error("accepted an unknown architecture")
			}
			if err := validatePE(f, arch, !dll); err == nil {
				t.Error("accepted wrong artifact type")
			}
			f.TimeDateStamp = 1
			if err := validatePE(f, arch, dll); err == nil {
				t.Error("accepted a non-reproducible timestamp")
			}
		}
	}
	for _, test := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"crt", func(data []byte) { copy(data[0x380:], "msvcrt.dll\x00\x00\x00") }},
		{"side-loadable-system-library", func(data []byte) { copy(data[0x380:], "bcrypt.dll\x00\x00\x00") }},
		{"unknown-library", func(data []byte) { copy(data[0x380:], "unknown.dll\x00\x00") }},
		{"outside-import-name", func(data []byte) { binary.LittleEndian.PutUint32(data[0x30c:], 0xfffffff0) }},
		{"decorated-export", func(data []byte) { copy(data[0x450:], "_MouseMapperProbeHook@12\x00") }},
		{"invalid-export-ordinal", func(data []byte) { binary.LittleEndian.PutUint16(data[0x444:], 10) }},
		{"forwarded-export", func(data []byte) { binary.LittleEndian.PutUint32(data[0x448:], 0x1250) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := fixturePE(t, "amd64", true)
			test.mutate(data)
			f, err := pe.NewFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if err := validatePE(f, "amd64", true); err == nil {
				t.Error("accepted invalid PE")
			}
		})
	}
}

func TestPEValidationRejectsMissingHardeningAndDelayedImports(t *testing.T) {
	for _, mutation := range []string{"console", "nx", "aslr", "high-entropy", "entry", "delay-import", "wrong-machine"} {
		t.Run(mutation, func(t *testing.T) {
			f, err := pe.NewFile(bytes.NewReader(fixturePE(t, "amd64", false)))
			if err != nil {
				t.Fatal(err)
			}
			h := f.OptionalHeader.(*pe.OptionalHeader64)
			switch mutation {
			case "console":
				h.Subsystem = pe.IMAGE_SUBSYSTEM_WINDOWS_CUI
			case "nx":
				h.DllCharacteristics &^= pe.IMAGE_DLLCHARACTERISTICS_NX_COMPAT
			case "aslr":
				h.DllCharacteristics &^= pe.IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE
			case "high-entropy":
				h.DllCharacteristics &^= pe.IMAGE_DLLCHARACTERISTICS_HIGH_ENTROPY_VA
			case "entry":
				h.AddressOfEntryPoint = 0
			case "delay-import":
				h.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_DELAY_IMPORT] = pe.DataDirectory{VirtualAddress: 0x1100, Size: 20}
			case "wrong-machine":
				f.Machine = pe.IMAGE_FILE_MACHINE_ARM64
			}
			if err := validatePE(f, "amd64", false); err == nil {
				t.Error("accepted invalid PE")
			}
		})
	}
}

func TestManifestDetectsStaleSourcesAndArtifacts(t *testing.T) {
	for _, mutation := range []string{"none", "source", "new-header", "binary", "missing-target", "schema"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			m := fixtureManifest(t, root)
			switch mutation {
			case "source":
				mustWrite(t, filepath.Join(root, "native", "probe.c"), []byte("changed"))
			case "new-header":
				mustWrite(t, filepath.Join(root, "native", "extra.h"), []byte("new"))
			case "binary":
				data := fixturePE(t, "amd64", false)
				data[0x200] ^= 1
				mustWrite(t, filepath.Join(root, "bin", "amd64", "ime-probe.exe"), data)
			case "missing-target":
				delete(m.Artifacts, "386/ime-probe.exe")
			case "schema":
				m.Schema = 99
			}
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(root, "bin", "manifest.json"), data)
			err = verifyManifest(root, targetArchitectures)
			if mutation == "none" && err != nil {
				t.Fatal(err)
			}
			if mutation != "none" && err == nil {
				t.Errorf("did not detect %s", mutation)
			}
		})
	}
}

func TestAtomicWriteReplacesPreviousFileAndLeavesNoTemporaryFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "file")
	for _, value := range []string{"first", "second"} {
		if err := atomicWrite(path, []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != value {
			t.Fatalf("read = %q, %v", data, err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remain: %v, %v", entries, err)
	}
}

func TestRemoveWithinNeverRemovesItsParentOrOutsideDirectory(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	for _, invalid := range []string{root, outside} {
		if err := removeWithin(invalid, root); err == nil {
			t.Errorf("accepted unsafe deletion %s", invalid)
		}
		if _, err := os.Stat(invalid); err != nil {
			t.Errorf("unsafe directory was removed: %v", err)
		}
	}
	stage := filepath.Join(root, "owned")
	mustWrite(t, filepath.Join(stage, "output"), []byte("temporary"))
	if err := removeWithin(stage, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("owned staging directory was not removed")
	}
}

func fixtureManifest(t *testing.T, root string) manifest {
	t.Helper()
	for _, name := range []string{"probe.c", "hook.c", "protocol.h"} {
		mustWrite(t, filepath.Join(root, "native", name), []byte("source for "+name))
	}
	sources, err := hashSources(root)
	if err != nil {
		t.Fatal(err)
	}
	m := manifest{Schema: 1, Toolchain: "llvm-mingw-" + toolchainVersion, Sources: sources, Artifacts: make(map[string]artifactRecord)}
	for _, arch := range targetArchitectures {
		for _, dll := range []bool{false, true} {
			name := arch + "/" + artifactName(arch, dll)
			path := filepath.Join(root, "bin", filepath.FromSlash(name))
			mustWrite(t, path, fixturePE(t, arch, dll))
			record, err := inspectArtifact(path, arch, dll)
			if err != nil {
				t.Fatal(err)
			}
			m.Artifacts[name] = record
		}
	}
	return m
}

func fixturePE(t *testing.T, arch string, dll bool) []byte {
	t.Helper()
	var data bytes.Buffer
	dos := make([]byte, 0x80)
	dos[0], dos[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(dos[0x3c:], 0x80)
	data.Write(dos)
	data.WriteString("PE\x00\x00")
	header := pe.FileHeader{Machine: machineByArchitecture[arch], NumberOfSections: 1, Characteristics: pe.IMAGE_FILE_EXECUTABLE_IMAGE}
	if dll {
		header.Characteristics |= pe.IMAGE_FILE_DLL
	}
	var directories [16]pe.DataDirectory
	directories[pe.IMAGE_DIRECTORY_ENTRY_IMPORT] = pe.DataDirectory{VirtualAddress: 0x1100, Size: 40}
	if dll {
		directories[pe.IMAGE_DIRECTORY_ENTRY_EXPORT] = pe.DataDirectory{VirtualAddress: 0x1200, Size: 0x100}
	}
	var optional any
	if arch == "386" {
		optional = pe.OptionalHeader32{
			Magic: 0x10b, AddressOfEntryPoint: 0x1000, SectionAlignment: 0x1000, FileAlignment: 0x200,
			Subsystem: pe.IMAGE_SUBSYSTEM_WINDOWS_GUI, NumberOfRvaAndSizes: 16, DataDirectory: directories,
			DllCharacteristics: pe.IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE | pe.IMAGE_DLLCHARACTERISTICS_NX_COMPAT,
		}
	} else {
		optional = pe.OptionalHeader64{
			Magic: 0x20b, AddressOfEntryPoint: 0x1000, SectionAlignment: 0x1000, FileAlignment: 0x200,
			Subsystem: pe.IMAGE_SUBSYSTEM_WINDOWS_GUI, NumberOfRvaAndSizes: 16, DataDirectory: directories,
			DllCharacteristics: pe.IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE | pe.IMAGE_DLLCHARACTERISTICS_NX_COMPAT | pe.IMAGE_DLLCHARACTERISTICS_HIGH_ENTROPY_VA,
		}
	}
	header.SizeOfOptionalHeader = uint16(binary.Size(optional))
	for _, value := range []any{header, optional, pe.SectionHeader32{
		Name: [8]uint8{'.', 't', 'e', 'x', 't'}, VirtualSize: 0x600, VirtualAddress: 0x1000,
		SizeOfRawData: 0x600, PointerToRawData: 0x200, Characteristics: pe.IMAGE_SCN_MEM_EXECUTE | pe.IMAGE_SCN_MEM_READ | pe.IMAGE_SCN_CNT_CODE,
	}} {
		if err := binary.Write(&data, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	if data.Len() > 0x200 {
		t.Fatal("fixture headers overlap the section")
	}
	data.Write(make([]byte, 0x800-data.Len()))
	result := data.Bytes()
	result[0x200] = 0xc3
	binary.LittleEndian.PutUint32(result[0x300:], 0x11a0)
	binary.LittleEndian.PutUint32(result[0x30c:], 0x1180)
	binary.LittleEndian.PutUint32(result[0x310:], 0x11b0)
	copy(result[0x380:], "KERNEL32.dll\x00")
	if dll {
		binary.LittleEndian.PutUint32(result[0x414:], 1)
		binary.LittleEndian.PutUint32(result[0x418:], 1)
		binary.LittleEndian.PutUint32(result[0x41c:], 0x1248)
		binary.LittleEndian.PutUint32(result[0x420:], 0x1240)
		binary.LittleEndian.PutUint32(result[0x424:], 0x1244)
		binary.LittleEndian.PutUint32(result[0x440:], 0x1250)
		binary.LittleEndian.PutUint32(result[0x448:], 0x1000)
		copy(result[0x450:], "MouseMapperProbeHook\x00")
	}
	return result
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}
