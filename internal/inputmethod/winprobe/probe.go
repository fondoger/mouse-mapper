// Package winprobe reads an existing Windows target thread's exact TSF profile.
// Unknown state is an error, never an invitation to infer a TIP from its HKL.
package winprobe

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	wireSize     = 96
	wireMagic    = 0x50494d4d
	machine386   = 0x014c
	machineAMD64 = 0x8664
	machineARM64 = 0xaa64
)

var (
	ErrAssetsUnavailable = errors.New("native input-method query helpers are not bundled; run go run ./scripts/buildime")
	ErrUnsupported       = errors.New("exact input-method queries are not supported for this target")
	ErrTargetChanged     = errors.New("the input-method query target changed")
)

type Target struct {
	Process uint32
	Thread  uint32
	Window  uintptr
}

// GUID has the Windows in-memory field layout, not a textual UUID byte order.
type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type Profile struct {
	Kind     uint32
	Language uint16
	Class    GUID
	Profile  GUID
	Layout   uint64 // The queried thread's actual GetKeyboardLayout(0) value.
}

type ProbeError struct {
	Status  uint32
	Win32   uint32
	HRESULT uint32
}

func (err *ProbeError) Error() string {
	names := [...]string{
		"success", "invalid arguments", "target changed", "unsupported target",
		"random request creation failed", "shared mapping failed", "query module load failed",
		"target hook refused", "query message timed out or was refused", "target did not respond",
		"target has no existing TSF context", "target profile manager is unavailable",
		"target active profile is unavailable", "target returned an inconsistent profile",
		"query expired", "invalid query response",
	}
	name := "unknown query failure"
	if err.Status < uint32(len(names)) {
		name = names[err.Status]
	}
	return fmt.Sprintf("read foreground input method: %s (status %d, Win32 %d, HRESULT 0x%08X)",
		name, err.Status, err.Win32, err.HRESULT)
}

func architecture(machine uint16) (string, error) {
	switch machine {
	case machine386:
		return "386", nil
	case machineAMD64:
		return "amd64", nil
	case machineARM64:
		return "arm64", nil
	default:
		return "", fmt.Errorf("%w: machine 0x%04X", ErrUnsupported, machine)
	}
}

func resolveArchitecture(reported uint16, exact bool, wow64, native uint16) (uint16, string, error) {
	machine := reported
	if !exact {
		machine = wow64
		if machine == 0 {
			// x64 emulation also reports UNKNOWN/ARM64; native ISA is not proof.
			if native == machineARM64 {
				return 0, "", fmt.Errorf("%w: ambiguous ARM64 process architecture", ErrUnsupported)
			}
			machine = native
		}
	}
	arch, err := architecture(machine)
	return machine, arch, err
}

func validTarget(target Target) bool {
	return target.Process != 0 && target.Thread != 0 && target.Window != 0
}

func decodeGUID(data []byte) GUID {
	guid := GUID{Data1: binary.LittleEndian.Uint32(data), Data2: binary.LittleEndian.Uint16(data[4:]),
		Data3: binary.LittleEndian.Uint16(data[6:])}
	copy(guid.Data4[:], data[8:16])
	return guid
}

func decodeResult(data []byte, target Target, machine uint16) (Profile, error) {
	invalid := func(reason string) (Profile, error) {
		return Profile{}, fmt.Errorf("invalid native input-method query response: %s", reason)
	}
	if !validTarget(target) {
		return invalid("invalid expected target")
	}
	if _, err := architecture(machine); err != nil {
		return Profile{}, err
	}
	if len(data) != wireSize {
		return invalid("wrong record length")
	}
	u16, u32, u64 := binary.LittleEndian.Uint16, binary.LittleEndian.Uint32, binary.LittleEndian.Uint64
	if u32(data) != wireMagic || u16(data[4:]) != 1 || u16(data[6:]) != wireSize {
		return invalid("wrong protocol header")
	}
	if u16(data[34:]) != 0 || u64(data[88:]) != 0 {
		return invalid("nonzero reserved fields")
	}
	if u32(data[20:]) != target.Process || u32(data[24:]) != target.Thread ||
		u64(data[80:]) != uint64(target.Window) || u32(data[36:]) != uint32(machine) {
		return invalid("target or architecture mismatch")
	}
	status, winerr, hresult := u32(data[8:]), u32(data[12:]), u32(data[16:])
	if status > 15 {
		return invalid("unknown status")
	}
	profile := Profile{Kind: u32(data[28:]), Language: u16(data[32:]), Layout: u64(data[40:]),
		Class: decodeGUID(data[48:64]), Profile: decodeGUID(data[64:80])}
	if status != 0 {
		if profile != (Profile{}) {
			return invalid("failed query included profile data")
		}
		return Profile{}, &ProbeError{Status: status, Win32: winerr, HRESULT: hresult}
	}
	if winerr != 0 || hresult != 0 {
		return invalid("success included an error")
	}
	if profile.Language == 0 || profile.Layout == 0 || uint16(profile.Layout) != profile.Language {
		return invalid("missing or inconsistent local keyboard layout")
	}
	upper := profile.Layout >> 32
	if (upper != 0 && upper != 0xffffffff) || (machine == machine386 && upper != 0) {
		return invalid("invalid local keyboard handle width")
	}
	switch profile.Kind {
	case 1:
		if profile.Class == (GUID{}) || profile.Profile == (GUID{}) {
			return invalid("TIP identity is missing")
		}
	case 2:
		if profile.Class != (GUID{}) || profile.Profile != (GUID{}) {
			return invalid("keyboard layout included a TIP identity")
		}
	default:
		return invalid("unknown profile type")
	}
	return profile, nil
}
