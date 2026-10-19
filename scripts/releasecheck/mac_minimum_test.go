package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const (
	macFixtureAMD64 = 0x01000007
	macFixtureARM64 = 0x0100000c
	macFixture13    = 13 << 16
)

type macFixtureEntry struct {
	name string
	data []byte
	kind byte
}

func macFixtureArchive(t *testing.T, entries ...macFixtureEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	tr := tar.NewWriter(z)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Typeflag: kind, Mode: 0o755, Size: int64(len(entry.data))}
		if kind == tar.TypeSymlink {
			header.Linkname = "elsewhere"
		}
		if err := tr.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func macFixturePlist(minimum string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>Mouse Mapper</string>
<key>LSMinimumSystemVersion</key><string>%s</string>
</dict></plist>`, minimum))
}

func macFixtureBundle(t *testing.T, name, minimum string, program []byte) []byte {
	t.Helper()
	prefix := name + ".app/Contents/"
	// The executable deliberately precedes its plist in the stream.
	return macFixtureArchive(t,
		macFixtureEntry{name: prefix + "MacOS/Mouse Mapper", data: program},
		macFixtureEntry{name: prefix + "Info.plist", data: macFixturePlist(minimum)},
	)
}

func macFixtureBuildVersion(minimum uint32) []byte {
	var command []byte
	for _, word := range []uint32{0x32, 24, 1, minimum, minimum, 0} {
		command = binary.LittleEndian.AppendUint32(command, word)
	}
	return command
}

func macFixtureMachO(cpu uint32, commands ...[]byte) []byte {
	var data []byte
	size := 0
	for _, command := range commands {
		size += len(command)
	}
	for _, word := range []uint32{0xfeedfacf, cpu, 0, 2, uint32(len(commands)), uint32(size), 0, 0} {
		data = binary.LittleEndian.AppendUint32(data, word)
	}
	for _, command := range commands {
		data = append(data, command...)
	}
	return data
}

func macFixtureUniversal(slices ...[]byte) []byte {
	var data []byte
	data = binary.BigEndian.AppendUint32(data, 0xcafebabe)
	data = binary.BigEndian.AppendUint32(data, uint32(len(slices)))
	offset := uint32(8 + 20*len(slices))
	for _, slice := range slices {
		for _, word := range []uint32{binary.LittleEndian.Uint32(slice[4:8]), 0, offset, uint32(len(slice)), 0} {
			data = binary.BigEndian.AppendUint32(data, word)
		}
		offset += uint32(len(slice))
	}
	for _, slice := range slices {
		data = append(data, slice...)
	}
	return data
}

func TestMacMinimumMatchesEverySliceAndConfiguration(t *testing.T) {
	arm13 := macFixtureMachO(macFixtureARM64, macFixtureBuildVersion(macFixture13))
	x6413 := macFixtureMachO(macFixtureAMD64, macFixtureBuildVersion(macFixture13))
	arm14 := macFixtureMachO(macFixtureARM64, macFixtureBuildVersion(14<<16))
	for _, tt := range []struct {
		name, declared, configured string
		program                    []byte
		want                       string
	}{
		{name: "thin", declared: "13.0", configured: "13.0", program: arm13},
		{name: "numeric equality", declared: "13.0.0", configured: "13", program: arm13},
		{name: "conservative minimum", declared: "14.0", configured: "14.0", program: arm13},
		{name: "universal", declared: "13.0", configured: "13.0", program: macFixtureUniversal(x6413, arm13)},
		{name: "advertised too low", declared: "12.0", configured: "12.0", program: arm13, want: "12.0.0 is below Mach-O requirement 13.0.0"},
		{name: "wrong configuration", declared: "13.0", configured: "12.0", program: arm13, want: "does not match configured"},
		{name: "second universal slice too high", declared: "13.0", configured: "13.0", program: macFixtureUniversal(x6413, arm14), want: "below Mach-O requirement 14.0.0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config, _ := testConfig(t)
			config.MacOS.MinimumSystemVersion = tt.configured
			archive := filepath.Join(t.TempDir(), "mac.tar.gz")
			putFile(t, archive, macFixtureBundle(t, config.Name, tt.declared, tt.program))
			err := verifyMacMinimum(config, archive)
			if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("wanted %q, got %v", tt.want, err)
			}
		})
	}
}

func TestMacVersionParsing(t *testing.T) {
	for _, text := range []string{"13", "13.0", "13.0.0"} {
		version, err := parseMacVersion(text)
		if err != nil || version != macFixture13 {
			t.Fatalf("%q = %x, %v", text, version, err)
		}
	}
	for _, text := range []string{"", "0.1", " 13", "13 ", "+13", "13.+1", "13.-1", "13..1", "13.0.0.0", "65536", "13.256", "13.0.256", "13.x"} {
		if _, err := parseMacVersion(text); err == nil {
			t.Fatalf("accepted invalid macOS version %q", text)
		}
	}
	version, err := parseMacVersion("10.15.7")
	if err != nil || macVersionString(version) != "10.15.7" {
		t.Fatalf("patch version lost: %x, %v", version, err)
	}
}

func TestMacPlistRejectsMissingAndMalformedMetadata(t *testing.T) {
	valid := string(macFixturePlist("13.0"))
	minimum := "<key>LSMinimumSystemVersion</key><string>13.0</string>"
	for name, data := range map[string]string{
		"empty":                     "",
		"malformed XML":             "<plist><dict>",
		"missing dictionary":        "<plist/>",
		"missing minimum":           strings.Replace(valid, minimum, "", 1),
		"nonstring minimum":         strings.Replace(valid, "<string>13.0</string>", "<integer>13</integer>", 1),
		"empty minimum":             strings.Replace(valid, "13.0", "", 1),
		"nonnumeric minimum":        strings.Replace(valid, "13.0", "Ventura", 1),
		"duplicate minimum":         strings.Replace(valid, minimum, minimum+minimum, 1),
		"nested minimum":            strings.Replace(valid, "<string>13.0</string>", "<string><string>13.0</string></string>", 1),
		"missing executable":        strings.Replace(valid, "<key>CFBundleExecutable</key><string>Mouse Mapper</string>", "", 1),
		"empty executable":          strings.Replace(valid, "Mouse Mapper", "", 1),
		"executable traversal":      strings.Replace(valid, "Mouse Mapper", "../Other", 1),
		"executable backslash":      strings.Replace(valid, "Mouse Mapper", `MacOS\Other`, 1),
		"executable dot":            strings.Replace(valid, "Mouse Mapper", ".", 1),
		"odd dictionary":            strings.Replace(valid, "</dict>", "<key>unpaired</key></dict>", 1),
		"multiple roots":            valid + "<plist><dict/></plist>",
		"unexpected trailing text":  valid + "garbage",
		"per-architecture override": strings.Replace(valid, "</dict>", "<key>LSMinimumSystemVersionByArchitecture</key><dict/></dict>", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := readMacPlist([]byte(data)); err == nil {
				t.Fatal("accepted malformed plist metadata")
			}
		})
	}
}

func TestMacBinaryMinimumCommands(t *testing.T) {
	var legacy []byte
	for _, word := range []uint32{0x24, 16, 10<<16 | 15<<8 | 7, 13 << 16} {
		legacy = binary.LittleEndian.AppendUint32(legacy, word)
	}
	minimum, err := macBinaryMinimum(macFixtureMachO(macFixtureAMD64, legacy))
	if err != nil || macVersionString(minimum) != "10.15.7" {
		t.Fatalf("legacy minimum = %x, error = %v", minimum, err)
	}
	short := macFixtureBuildVersion(macFixture13)[:16]
	binary.LittleEndian.PutUint32(short[4:8], uint32(len(short)))
	wrongPlatform := macFixtureBuildVersion(macFixture13)
	binary.LittleEndian.PutUint32(wrongPlatform[8:12], 2)
	wrongToolCount := macFixtureBuildVersion(macFixture13)
	binary.LittleEndian.PutUint32(wrongToolCount[20:24], 1)
	tooManySlices := make([]byte, 8)
	binary.BigEndian.PutUint32(tooManySlices[:4], 0xcafebabe)
	binary.BigEndian.PutUint32(tooManySlices[4:], 17)
	valid := macFixtureBuildVersion(macFixture13)
	for name, program := range map[string][]byte{
		"empty":              nil,
		"truncated":          {0xcf, 0xfa, 0xed, 0xfe},
		"missing minimum":    macFixtureMachO(macFixtureAMD64),
		"short command":      macFixtureMachO(macFixtureAMD64, short),
		"wrong platform":     macFixtureMachO(macFixtureAMD64, wrongPlatform),
		"wrong tool count":   macFixtureMachO(macFixtureAMD64, wrongToolCount),
		"zero version":       macFixtureMachO(macFixtureAMD64, macFixtureBuildVersion(0)),
		"duplicate minimum":  macFixtureMachO(macFixtureAMD64, valid, valid),
		"conflicting legacy": macFixtureMachO(macFixtureAMD64, valid, legacy),
		"missing fat slice":  macFixtureUniversal(macFixtureMachO(macFixtureAMD64, valid), macFixtureMachO(macFixtureARM64)),
		"too many slices":    tooManySlices,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := macBinaryMinimum(program); err == nil {
				t.Fatal("accepted malformed Mach-O metadata")
			}
		})
	}
}

func TestMacArchiveRejectsMissingAndDuplicateFiles(t *testing.T) {
	config, _ := testConfig(t)
	prefix := config.Name + ".app/Contents/"
	plist := macFixtureEntry{name: prefix + "Info.plist", data: macFixturePlist("13.0")}
	program := macFixtureEntry{name: prefix + "MacOS/Mouse Mapper", data: macFixtureMachO(macFixtureARM64, macFixtureBuildVersion(macFixture13))}
	for name, entries := range map[string][]macFixtureEntry{
		"missing plist":        {program},
		"missing executable":   {plist},
		"duplicate plist":      {plist, program, plist},
		"duplicate executable": {plist, program, program},
		"aliased plist":        {plist, program, {name: prefix + "MacOS/../Info.plist", data: macFixturePlist("12.0")}},
		"aliased executable":   {plist, program, {name: prefix + "MacOS/./Mouse Mapper", data: program.data}},
		"symlink plist":        {{name: plist.name, kind: tar.TypeSymlink}, program},
		"symlink executable":   {plist, {name: program.name, kind: tar.TypeSymlink}},
		"empty plist":          {{name: plist.name}, program},
		"empty executable":     {plist, {name: program.name}},
	} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "mac.tar.gz")
			putFile(t, archive, macFixtureArchive(t, entries...))
			if err := verifyMacMinimum(config, archive); err == nil {
				t.Fatal("accepted invalid archive entries")
			}
		})
	}
}

func TestMacArchiveChecksGzipTrailer(t *testing.T) {
	config, _ := testConfig(t)
	valid := macFixtureBundle(t, config.Name, "13.0", macFixtureMachO(macFixtureARM64, macFixtureBuildVersion(macFixture13)))
	badCRC := bytes.Clone(valid)
	badCRC[len(badCRC)-8] ^= 1
	for name, data := range map[string][]byte{"truncated trailer": valid[:len(valid)-8], "bad CRC": badCRC} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "mac.tar.gz")
			putFile(t, archive, data)
			if err := verifyMacMinimum(config, archive); err == nil {
				t.Fatal("tar EOF hid an invalid gzip trailer")
			}
		})
	}
}

func TestMacArchiveEntryLimits(t *testing.T) {
	for _, tt := range []struct {
		name string
		size int64
	}{
		{name: "Info.plist", size: maxMacPlistBytes + 1},
		{name: "MacOS/Mouse Mapper", size: maxMacBinaryBytes + 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var data bytes.Buffer
			tr := tar.NewWriter(&data)
			if err := tr.WriteHeader(&tar.Header{Name: "Mouse Mapper.app/Contents/" + tt.name, Mode: 0o755, Size: tt.size}); err != nil {
				t.Fatal(err)
			}
			// Oversized entries must fail from their headers, before reading data.
			_, err := readMacBundle(&data, "Mouse Mapper.app/Contents/")
			if err == nil || !strings.Contains(err.Error(), "size limit") {
				t.Fatalf("wanted size-limit failure, got %v", err)
			}
		})
	}
	var data bytes.Buffer
	tr := tar.NewWriter(&data)
	for i := 0; i <= maxMacEntries; i++ {
		if err := tr.WriteHeader(&tar.Header{Name: fmt.Sprintf("resource-%d", i), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readMacBundle(&data, "Mouse Mapper.app/Contents/"); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("wanted entry-count failure, got %v", err)
	}
}

func TestSignedMacArchiveStillRequiresCompatibleMinimum(t *testing.T) {
	config, key := testConfig(t)
	config.MacOS.MinimumSystemVersion = "12.0"
	directory := t.TempDir()
	update := addTarget(t, config, key, directory, "darwin-arm64")
	data := macFixtureBundle(t, config.Name, "12.0", macFixtureMachO(macFixtureARM64, macFixtureBuildVersion(macFixture13)))
	archiveName := "mouse-mapper-" + config.Version + "-darwin-arm64.tar.gz"
	putFile(t, filepath.Join(directory, archiveName), data)
	digest := sha256.Sum256(data)
	update.Size = int64(len(data))
	update.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, digest[:]))
	putManifest(t, filepath.Join(directory, "update-darwin-arm64.json"), update)
	if _, err := verifyAssets(config, publicKey(key), directory, []string{"darwin-arm64"}); err == nil || !strings.Contains(err.Error(), "below Mach-O requirement") {
		t.Fatalf("wanted incompatibility after successful signature check, got %v", err)
	}
}
