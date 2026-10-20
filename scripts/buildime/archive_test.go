package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchivePathRejectsTraversalAndWindowsSpecialPaths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"compiler", "compiler/", "compiler/bin/clang.exe", "compiler/lib/a-b_c++.a"} {
		got, err := archivePath(root, "compiler", name)
		if err != nil || !strictlyWithin(got, root) {
			t.Errorf("valid path %q = %q, %v", name, got, err)
		}
	}
	for _, name := range []string{
		"", ".", "..", "../outside", "/compiler/bin/clang", "other/bin/clang", "compiler/../outside",
		"compiler/a/../../outside", "compiler//bin", "compiler/./bin", "C:/compiler/bin", "compiler\\bin\\clang",
		"compiler/bin/file:stream", "compiler/NUL", "compiler/com1.txt", "compiler/LPT9", "compiler/aux.h",
		"compiler/trailing.", "compiler/trailing ", "compiler/\x00file",
	} {
		if _, err := archivePath(root, "compiler", name); err == nil {
			t.Errorf("accepted unsafe path %q", name)
		}
	}
}

func TestZipExtraction(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "compiler.zip")
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	for _, name := range []string{"compiler/bin/clang.exe", "compiler/include/windows.h"} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, archive, data.Bytes())
	extracted := filepath.Join(root, "extract")
	if err := os.Mkdir(extracted, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(archive, extracted, "compiler"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(extracted, "compiler", "bin", "clang.exe"))
	if err != nil || string(got) != "compiler/bin/clang.exe" {
		t.Fatalf("extracted data = %q, %v", got, err)
	}
}

func TestZipRejectsTraversalSymlinksAndDuplicates(t *testing.T) {
	for _, mutation := range []string{"traversal", "symlink", "duplicate", "directory-conflict"} {
		t.Run(mutation, func(t *testing.T) {
			var data bytes.Buffer
			z := zip.NewWriter(&data)
			h := &zip.FileHeader{Name: "compiler/bin/clang.exe", Method: zip.Store}
			switch mutation {
			case "traversal":
				h.Name = "compiler/../../outside"
			case "symlink":
				h.SetMode(os.ModeSymlink | 0777)
			}
			w, err := z.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("contents")); err != nil {
				t.Fatal(err)
			}
			if mutation == "duplicate" || mutation == "directory-conflict" {
				name := h.Name
				if mutation == "directory-conflict" {
					name += "/file"
				}
				if _, err := z.Create(name); err != nil {
					t.Fatal(err)
				}
			}
			if err := z.Close(); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			archive := filepath.Join(root, "compiler.zip")
			mustWrite(t, archive, data.Bytes())
			extracted := filepath.Join(root, "extract")
			if err := os.Mkdir(extracted, 0755); err != nil {
				t.Fatal(err)
			}
			if err := extractZip(archive, extracted, "compiler"); err == nil {
				t.Error("accepted unsafe ZIP")
			}
			if _, err := os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(err) {
				t.Error("ZIP created a file outside its extraction root")
			}
		})
	}
}

func TestTarExtractionAndHardLinks(t *testing.T) {
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	for _, header := range []*tar.Header{
		{Name: "compiler/bin/clang", Typeflag: tar.TypeReg, Mode: 0755, Size: 4},
		{Name: "compiler/bin/clang-wrapper", Typeflag: tar.TypeLink, Linkname: "compiler/bin/clang"},
	} {
		if err := w.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := w.Write([]byte("test")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := extractTar(bytes.NewReader(data.Bytes()), root, "compiler"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "compiler", "bin", "clang-wrapper"))
	if err != nil || string(got) != "test" {
		t.Fatalf("hard-link data = %q, %v", got, err)
	}
}

func TestTarRejectsEscapingLinksAndDeviceFiles(t *testing.T) {
	for _, header := range []*tar.Header{
		{Name: "compiler/bin/escape", Typeflag: tar.TypeSymlink, Linkname: "../../../outside"},
		{Name: "compiler/bin/escape", Typeflag: tar.TypeSymlink, Linkname: "/tmp/outside"},
		{Name: "compiler/bin/escape", Typeflag: tar.TypeLink, Linkname: "../outside"},
		{Name: "compiler/bin/device", Typeflag: tar.TypeChar},
		{Name: "compiler/bin/pipe", Typeflag: tar.TypeFifo},
		{Name: "compiler/../../outside", Typeflag: tar.TypeReg},
	} {
		var data bytes.Buffer
		w := tar.NewWriter(&data)
		if err := w.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractTar(bytes.NewReader(data.Bytes()), t.TempDir(), "compiler"); err == nil {
			t.Errorf("accepted dangerous TAR header %+v", header)
		}
	}
}

func TestTarResolvesValidRelativeSymlinks(t *testing.T) {
	root := t.TempDir()
	target, link := filepath.Join(root, "target"), filepath.Join(root, "test-link")
	mustWrite(t, target, []byte("test"))
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	for _, header := range []*tar.Header{
		{Name: "compiler/lib/clang", Typeflag: tar.TypeReg, Mode: 0755, Size: 4},
		{Name: "compiler/bin/clang", Typeflag: tar.TypeSymlink, Linkname: "../lib/clang"},
	} {
		if err := w.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := w.Write([]byte("test")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractTar(bytes.NewReader(data.Bytes()), root, "compiler"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "compiler", "bin", "clang"))
	if err != nil || string(got) != "test" {
		t.Fatalf("symlink data = %q, %v", got, err)
	}
}

func TestExtractorEnforcesExpansionLimitsAndExactSizes(t *testing.T) {
	for _, e := range []extractor{
		{root: t.TempDir(), top: "compiler", entries: maximumEntries},
		{root: t.TempDir(), top: "compiler", total: maximumExpandedSize},
	} {
		if _, err := e.entry("compiler/file", 1); err == nil {
			t.Error("accepted excessive extraction")
		}
	}
	e := extractor{root: t.TempDir(), top: "compiler"}
	for _, size := range []int64{-1, maximumEntrySize + 1} {
		if _, err := e.entry("compiler/file", size); err == nil {
			t.Errorf("accepted entry size %d", size)
		}
	}
	if err := e.regular(filepath.Join(e.root, "compiler", "file"), 2, 0644, strings.NewReader("1")); err == nil {
		t.Error("accepted a truncated entry")
	}
}
