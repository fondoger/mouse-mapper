package main

import (
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
)

var machineByArchitecture = map[string]uint16{
	"amd64": pe.IMAGE_FILE_MACHINE_AMD64,
	"arm64": pe.IMAGE_FILE_MACHINE_ARM64,
	"386":   pe.IMAGE_FILE_MACHINE_I386,
}

// Static dependencies must be KnownDLLs; other system DLLs need explicit
// system-directory loading to avoid unverified cache-directory side loading.
var allowedImports = map[string]bool{
	"kernel32.dll": true,
	"user32.dll":   true,
	"ole32.dll":    true,
}

func inspectArtifact(path, arch string, dll bool) (artifactRecord, error) {
	var empty artifactRecord
	info, err := os.Stat(path)
	if err != nil {
		return empty, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4<<20 {
		return empty, fmt.Errorf("helper is not a small regular PE file: %s", path)
	}
	f, err := pe.Open(path)
	if err != nil {
		return empty, fmt.Errorf("read helper PE %s: %w", path, err)
	}
	defer f.Close()
	if err := validatePE(f, arch, dll); err != nil {
		return empty, fmt.Errorf("invalid helper %s: %w", path, err)
	}
	digest, size, err := hashFile(path)
	if err != nil {
		return empty, err
	}
	return artifactRecord{SHA256: digest, Size: size, Machine: f.Machine}, nil
}

func validatePE(f *pe.File, arch string, dll bool) error {
	expected, ok := machineByArchitecture[arch]
	if !ok || f.Machine != expected {
		return fmt.Errorf("machine %#x does not match %s", f.Machine, arch)
	}
	if (f.Characteristics&pe.IMAGE_FILE_DLL != 0) != dll || f.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 {
		return errors.New("PE executable/DLL characteristics do not match the artifact")
	}
	if f.TimeDateStamp != 0 {
		return errors.New("PE timestamp is not deterministic")
	}
	var subsystem, flags uint16
	var entry uint32
	var directories [16]pe.DataDirectory
	switch header := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		if arch == "386" {
			return errors.New("32-bit helper has a 64-bit optional header")
		}
		subsystem, flags, entry, directories = header.Subsystem, header.DllCharacteristics, header.AddressOfEntryPoint, header.DataDirectory
	case *pe.OptionalHeader32:
		if arch != "386" {
			return errors.New("64-bit helper has a 32-bit optional header")
		}
		subsystem, flags, entry, directories = header.Subsystem, header.DllCharacteristics, header.AddressOfEntryPoint, header.DataDirectory
	default:
		return errors.New("PE optional header is missing")
	}
	if subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI || entry == 0 {
		return errors.New("helper must use the GUI subsystem and an explicit native entry point")
	}
	required := uint16(pe.IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE | pe.IMAGE_DLLCHARACTERISTICS_NX_COMPAT)
	if arch != "386" {
		required |= pe.IMAGE_DLLCHARACTERISTICS_HIGH_ENTROPY_VA
	}
	if flags&required != required {
		return errors.New("helper is missing ASLR/NX hardening")
	}
	if d := directories[pe.IMAGE_DIRECTORY_ENTRY_DELAY_IMPORT]; d.VirtualAddress != 0 || d.Size != 0 {
		return errors.New("helper must not have delayed imports")
	}
	libraries, err := importedLibraries(f, directories[pe.IMAGE_DIRECTORY_ENTRY_IMPORT])
	if err != nil {
		return err
	}
	if len(libraries) == 0 {
		return errors.New("helper has no Windows system imports")
	}
	for _, name := range libraries {
		if !allowedImports[strings.ToLower(name)] {
			return fmt.Errorf("non-system or CRT dependency %q", name)
		}
	}
	if dll {
		return requireExport(f, directories[pe.IMAGE_DIRECTORY_ENTRY_EXPORT], "MouseMapperProbeHook")
	}
	return nil
}

// debug/pe.ImportedLibraries is currently a stub. Read descriptors directly so
// a successful build cannot silently acquire a redistributed CRT dependency.
func importedLibraries(f *pe.File, directory pe.DataDirectory) ([]string, error) {
	if directory.VirtualAddress == 0 || directory.Size < 20 || directory.Size > 1<<20 {
		return nil, errors.New("invalid PE import directory")
	}
	data, err := peData(f, directory.VirtualAddress, directory.Size)
	if err != nil {
		return nil, err
	}
	var libraries []string
	for len(data) >= 20 && len(libraries) < 256 {
		var nonzero byte
		for _, b := range data[:20] {
			nonzero |= b
		}
		if nonzero == 0 {
			return libraries, nil
		}
		name, err := peString(f, binary.LittleEndian.Uint32(data[12:16]))
		if err != nil {
			return nil, err
		}
		libraries = append(libraries, name)
		data = data[20:]
	}
	return nil, errors.New("unterminated PE import directory")
}

func requireExport(f *pe.File, directory pe.DataDirectory, wanted string) error {
	if directory.VirtualAddress == 0 || directory.Size < 40 {
		return errors.New("DLL export directory is missing")
	}
	d, err := peData(f, directory.VirtualAddress, 40)
	if err != nil {
		return err
	}
	count := binary.LittleEndian.Uint32(d[24:28])
	functionCount := binary.LittleEndian.Uint32(d[20:24])
	if count == 0 || count > 1024 || functionCount == 0 || functionCount > 1024 {
		return errors.New("invalid DLL export count")
	}
	names, err := peData(f, binary.LittleEndian.Uint32(d[32:36]), count*4)
	if err != nil {
		return err
	}
	ordinals, err := peData(f, binary.LittleEndian.Uint32(d[36:40]), count*2)
	if err != nil {
		return err
	}
	functions, err := peData(f, binary.LittleEndian.Uint32(d[28:32]), functionCount*4)
	if err != nil {
		return err
	}
	for i := uint32(0); i < count; i++ {
		name, err := peString(f, binary.LittleEndian.Uint32(names[i*4:]))
		if err != nil {
			return err
		}
		if name != wanted {
			continue
		}
		ordinal := uint32(binary.LittleEndian.Uint16(ordinals[i*2:]))
		if ordinal >= functionCount {
			return errors.New("DLL hook export ordinal is invalid")
		}
		rva := binary.LittleEndian.Uint32(functions[ordinal*4:])
		if uint64(rva) >= uint64(directory.VirtualAddress) && uint64(rva) < uint64(directory.VirtualAddress)+uint64(directory.Size) {
			return errors.New("DLL hook export must not forward to another library")
		}
		for _, section := range f.Sections {
			if rva >= section.VirtualAddress && uint64(rva-section.VirtualAddress) < uint64(section.Size) && section.Characteristics&pe.IMAGE_SCN_MEM_EXECUTE != 0 {
				return nil
			}
		}
		return errors.New("DLL hook export does not point to executable code")
	}
	return fmt.Errorf("DLL must export undecorated %s", wanted)
}

func peData(f *pe.File, rva, size uint32) ([]byte, error) {
	for _, section := range f.Sections {
		if rva < section.VirtualAddress {
			continue
		}
		offset := uint64(rva - section.VirtualAddress)
		if offset >= uint64(section.Size) || offset+uint64(size) > uint64(section.Size) {
			continue
		}
		data, err := section.Data()
		if err != nil {
			return nil, err
		}
		if offset+uint64(size) > uint64(len(data)) {
			return nil, errors.New("truncated PE section")
		}
		return data[offset : offset+uint64(size)], nil
	}
	return nil, fmt.Errorf("PE RVA %#x (+%d) is outside file-backed sections", rva, size)
}

func peString(f *pe.File, rva uint32) (string, error) {
	var value []byte
	for i := uint32(0); i < 512; i++ {
		if rva+i < rva {
			break
		}
		data, err := peData(f, rva+i, 1)
		if err != nil {
			return "", err
		}
		if data[0] == 0 {
			if len(value) == 0 {
				return "", errors.New("empty PE name")
			}
			return string(value), nil
		}
		value = append(value, data[0])
	}
	return "", errors.New("unterminated PE name")
}
