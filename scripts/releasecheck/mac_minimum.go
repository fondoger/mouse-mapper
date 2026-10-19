package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"debug/macho"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

const (
	maxMacArchiveBytes = 256 << 20
	maxMacPlistBytes   = 1 << 20
	maxMacBinaryBytes  = 128 << 20
	maxMacEntries      = 4096
)

func verifyMacMinimum(config configuration, archive string) error {
	configured, err := parseMacVersion(config.MacOS.MinimumSystemVersion)
	if err != nil {
		return fmt.Errorf("macos.minimumSystemVersion: %w", err)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	compressed := &io.LimitedReader{R: f, N: maxMacArchiveBytes + 1}
	z, err := gzip.NewReader(compressed)
	if err != nil {
		return fmt.Errorf("macOS archive gzip: %w", err)
	}
	defer z.Close()
	expanded := &io.LimitedReader{R: z, N: maxMacArchiveBytes + 1}
	files, err := readMacBundle(expanded, config.Name+".app/Contents/")
	if err == nil {
		// tar EOF precedes gzip EOF: finish the stream to check its CRC and size.
		_, err = io.Copy(io.Discard, expanded)
	}
	if compressed.N == 0 || expanded.N == 0 {
		return errors.New("macOS archive exceeds the 256 MiB compressed or expanded limit")
	}
	if err != nil {
		return fmt.Errorf("macOS archive: %w", err)
	}
	plist, ok := files["Info.plist"]
	if !ok {
		return errors.New("macOS archive is missing Info.plist")
	}
	minimum, executable, err := readMacPlist(plist)
	if err != nil {
		return fmt.Errorf("Info.plist: %w", err)
	}
	if minimum != configured {
		return fmt.Errorf("Info.plist minimum macOS %s does not match configured %s", macVersionString(minimum), macVersionString(configured))
	}
	program, ok := files["MacOS/"+executable]
	if !ok {
		return fmt.Errorf("macOS archive is missing executable %q", executable)
	}
	required, err := macBinaryMinimum(program)
	if err != nil {
		return fmt.Errorf("executable %q: %w", executable, err)
	}
	if minimum < required {
		return fmt.Errorf("Info.plist minimum macOS %s is below Mach-O requirement %s", macVersionString(minimum), macVersionString(required))
	}
	return nil
}

func readMacBundle(r io.Reader, prefix string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	var retained int64
	tr := tar.NewReader(r)
	for count := 0; ; count++ {
		header, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if count >= maxMacEntries {
			return nil, errors.New("too many macOS archive entries")
		}
		clean := path.Clean(header.Name)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "\\") {
			return nil, fmt.Errorf("unsafe macOS archive path %q", header.Name)
		}
		name, ok := strings.CutPrefix(clean, prefix)
		if !ok {
			continue
		}
		limit := int64(maxMacPlistBytes)
		if name != "Info.plist" {
			base, ok := strings.CutPrefix(name, "MacOS/")
			if !ok || base == "" || strings.Contains(base, "/") {
				continue
			}
			limit = maxMacBinaryBytes
		}
		if _, exists := files[name]; exists {
			return nil, fmt.Errorf("duplicate macOS archive entry %q", name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("macOS archive entry %q must be a regular file", name)
		}
		if header.Size <= 0 || header.Size > limit {
			return nil, fmt.Errorf("macOS archive entry %q is empty or exceeds its size limit", name)
		}
		if header.Size > maxMacArchiveBytes-retained {
			return nil, errors.New("macOS archive files exceed the 256 MiB aggregate size limit")
		}
		retained += header.Size
		files[name], err = io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
	}
}

type plistNode struct {
	XMLName  xml.Name
	Text     string      `xml:",chardata"`
	Children []plistNode `xml:",any"`
}

func readMacPlist(data []byte) (uint32, string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var root plistNode
	seenRoot := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, "", err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if seenRoot {
				return 0, "", errors.New("multiple XML roots")
			}
			if err := decoder.DecodeElement(&root, &token); err != nil {
				return 0, "", err
			}
			seenRoot = true
		case xml.CharData:
			if strings.TrimSpace(string(token)) != "" {
				return 0, "", errors.New("unexpected XML text")
			}
		}
	}
	if root.XMLName.Local != "plist" || strings.TrimSpace(root.Text) != "" || len(root.Children) != 1 {
		return 0, "", errors.New("expected one plist dictionary")
	}
	dict := root.Children[0]
	if dict.XMLName.Local != "dict" || strings.TrimSpace(dict.Text) != "" || len(dict.Children)%2 != 0 {
		return 0, "", errors.New("invalid plist dictionary")
	}
	values := map[string]plistNode{}
	for i := 0; i < len(dict.Children); i += 2 {
		key := dict.Children[i]
		if key.XMLName.Local != "key" || len(key.Children) != 0 || key.Text == "" {
			return 0, "", errors.New("invalid plist key")
		}
		if _, exists := values[key.Text]; exists {
			return 0, "", fmt.Errorf("duplicate plist key %q", key.Text)
		}
		values[key.Text] = dict.Children[i+1]
	}
	if _, exists := values["LSMinimumSystemVersionByArchitecture"]; exists {
		return 0, "", errors.New("per-architecture minimum system versions are not supported by this release check")
	}
	for _, key := range []string{"LSMinimumSystemVersion", "CFBundleExecutable"} {
		value, exists := values[key]
		if !exists || value.XMLName.Local != "string" || len(value.Children) != 0 || value.Text == "" {
			return 0, "", fmt.Errorf("%s must be a nonempty string", key)
		}
	}
	minimum, err := parseMacVersion(values["LSMinimumSystemVersion"].Text)
	if err != nil {
		return 0, "", fmt.Errorf("LSMinimumSystemVersion: %w", err)
	}
	executable := values["CFBundleExecutable"].Text
	if strings.TrimSpace(executable) == "" || executable == "." || executable == ".." || strings.ContainsAny(executable, "/\\\x00\r\n") {
		return 0, "", errors.New("CFBundleExecutable must be a safe basename")
	}
	return minimum, executable, nil
}

func parseMacVersion(text string) (uint32, error) {
	parts := strings.Split(text, ".")
	if len(parts) > 3 {
		return 0, fmt.Errorf("invalid macOS version %q", text)
	}
	var values [3]uint32
	for i, part := range parts {
		bits := 8
		if i == 0 {
			bits = 16
		}
		value, err := strconv.ParseUint(part, 10, bits)
		if err != nil || strings.HasPrefix(part, "+") {
			return 0, fmt.Errorf("invalid macOS version %q", text)
		}
		values[i] = uint32(value)
	}
	if values[0] == 0 {
		return 0, fmt.Errorf("invalid macOS version %q", text)
	}
	return values[0]<<16 | values[1]<<8 | values[2], nil
}

func macVersionString(version uint32) string {
	return fmt.Sprintf("%d.%d.%d", version>>16, version>>8&255, version&255)
}

func macBinaryMinimum(data []byte) (uint32, error) {
	if len(data) >= 8 && binary.BigEndian.Uint32(data[:4]) == macho.MagicFat && binary.BigEndian.Uint32(data[4:8]) > 16 {
		return 0, errors.New("too many Mach-O slices")
	}
	fat, err := macho.NewFatFile(bytes.NewReader(data))
	if errors.Is(err, macho.ErrNotFat) {
		thin, err := macho.NewFile(bytes.NewReader(data))
		if err != nil {
			return 0, err
		}
		return macSliceMinimum(thin)
	}
	if err != nil {
		return 0, err
	}
	var required uint32
	for i, arch := range fat.Arches {
		minimum, err := macSliceMinimum(arch.File)
		if err != nil {
			return 0, fmt.Errorf("Mach-O slice %d (%s): %w", i, arch.Cpu, err)
		}
		required = max(required, minimum)
	}
	return required, nil
}

func macSliceMinimum(file *macho.File) (uint32, error) {
	var minimum uint32
	for _, load := range file.Loads {
		raw := load.Raw()
		var version uint32
		switch file.ByteOrder.Uint32(raw[:4]) {
		case 0x32: // LC_BUILD_VERSION
			if len(raw) < 24 || uint64(len(raw)) != 24+8*uint64(file.ByteOrder.Uint32(raw[20:24])) {
				return 0, errors.New("malformed LC_BUILD_VERSION")
			}
			if file.ByteOrder.Uint32(raw[8:12]) != 1 {
				return 0, errors.New("Mach-O deployment platform is not macOS")
			}
			version = file.ByteOrder.Uint32(raw[12:16])
		case 0x24: // LC_VERSION_MIN_MACOSX
			if len(raw) != 16 {
				return 0, errors.New("malformed LC_VERSION_MIN_MACOSX")
			}
			version = file.ByteOrder.Uint32(raw[8:12])
		default:
			continue
		}
		if minimum != 0 || version>>16 == 0 {
			return 0, errors.New("duplicate or invalid Mach-O minimum system version")
		}
		minimum = version
	}
	if minimum == 0 {
		return 0, errors.New("missing Mach-O macOS minimum-version load command")
	}
	return minimum, nil
}
