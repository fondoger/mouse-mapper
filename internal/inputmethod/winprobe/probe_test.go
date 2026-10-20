package winprobe

import (
	"encoding/binary"
	"errors"
	"testing"
)

var sampleTarget = Target{Process: 123, Thread: 456, Window: 0x23456}

func TestResolveProcessArchitecture(t *testing.T) {
	for _, test := range []struct {
		name                  string
		reported              uint16
		exact                 bool
		wow64, native, wanted uint16
		arch                  string
	}{
		{"x64_emulation_is_not_wow64", machineAMD64, true, 0, machineARM64, machineAMD64, "amd64"},
		{"native_arm64", machineARM64, true, 0, machineARM64, machineARM64, "arm64"},
		{"exact_x86", machine386, true, machine386, machineARM64, machine386, "386"},
		{"unknown_exact_never_falls_back", 0, true, machine386, machineAMD64, 0, ""},
		{"unsupported_exact_never_falls_back", 0xa641, true, 0, machineAMD64, 0, ""},
		{"legacy_x64", 0, false, 0, machineAMD64, machineAMD64, "amd64"},
		{"legacy_native_x86", 0, false, 0, machine386, machine386, "386"},
		{"legacy_x86_on_arm64", 0, false, machine386, machineARM64, machine386, "386"},
		{"legacy_x86_on_x64", 0, false, machine386, machineAMD64, machine386, "386"},
		{"ambiguous_arm64_is_not_assumed", 0, false, 0, machineARM64, 0, ""},
		{"missing_legacy_machine", 0, false, 0, 0, 0, ""},
		{"unsupported_legacy_machine", 0, false, 0x1c4, machineARM64, 0, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			machine, arch, err := resolveArchitecture(test.reported, test.exact, test.wow64, test.native)
			if test.arch == "" {
				if !errors.Is(err, ErrUnsupported) || arch != "" {
					t.Fatalf("ambiguous process architecture = %04x, %q, %v", machine, arch, err)
				}
			} else if err != nil || machine != test.wanted || arch != test.arch {
				t.Fatalf("process architecture = %04x, %q, %v", machine, arch, err)
			}
		})
	}
}

func sampleRecord(machine uint16) []byte {
	data := make([]byte, wireSize)
	u16, u32, u64 := binary.LittleEndian.PutUint16, binary.LittleEndian.PutUint32, binary.LittleEndian.PutUint64
	u32(data, wireMagic)
	u16(data[4:], 1)
	u16(data[6:], wireSize)
	u32(data[20:], sampleTarget.Process)
	u32(data[24:], sampleTarget.Thread)
	u32(data[28:], 1)
	u16(data[32:], 0x0804)
	u32(data[36:], uint32(machine))
	u64(data[40:], 0x08040804)
	for i := 48; i < 80; i++ {
		data[i] = byte(i)
	}
	u64(data[80:], uint64(sampleTarget.Window))
	return data
}

func TestDecodeExactTIPAndKeyboardProfile(t *testing.T) {
	for _, machine := range []uint16{machine386, machineAMD64, machineARM64} {
		data := sampleRecord(machine)
		profile, err := decodeResult(data, sampleTarget, machine)
		if err != nil {
			t.Fatal(err)
		}
		if profile.Kind != 1 || profile.Language != 0x0804 || profile.Layout != 0x08040804 ||
			profile.Class.Data1 != 0x33323130 || profile.Class.Data2 != 0x3534 ||
			profile.Profile.Data3 != 0x4746 || profile.Profile.Data4[7] != 79 {
			t.Fatalf("profile does not preserve native identity: %+v", profile)
		}
		binary.LittleEndian.PutUint32(data[28:], 2)
		clear(data[48:80])
		profile, err = decodeResult(data, sampleTarget, machine)
		if err != nil || profile.Kind != 2 || profile.Layout != 0x08040804 || profile.Class != (GUID{}) {
			t.Fatalf("keyboard profile = %+v, %v", profile, err)
		}
	}
}

func TestDecodeRejectsMalformedOrAmbiguousRecords(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"empty", func([]byte) []byte { return nil }},
		{"short", func(data []byte) []byte { return data[:95] }},
		{"trailing", func(data []byte) []byte { return append(data, 0) }},
		{"magic", func(data []byte) []byte { data[0]++; return data }},
		{"version", func(data []byte) []byte { data[4]++; return data }},
		{"size", func(data []byte) []byte { data[6]--; return data }},
		{"status", func(data []byte) []byte { data[8] = 16; return data }},
		{"success_winerr", func(data []byte) []byte { data[12] = 5; return data }},
		{"success_hresult", func(data []byte) []byte { data[16] = 1; return data }},
		{"pid", func(data []byte) []byte { data[20]++; return data }},
		{"tid", func(data []byte) []byte { data[24]++; return data }},
		{"hwnd", func(data []byte) []byte { data[80]++; return data }},
		{"machine", func(data []byte) []byte { data[36]++; return data }},
		{"reserved16", func(data []byte) []byte { data[34] = 1; return data }},
		{"reserved64", func(data []byte) []byte { data[95] = 1; return data }},
		{"kind_zero", func(data []byte) []byte { data[28] = 0; return data }},
		{"kind_unknown", func(data []byte) []byte { data[28] = 3; return data }},
		{"language_zero", func(data []byte) []byte { clear(data[32:34]); return data }},
		{"layout_zero", func(data []byte) []byte { clear(data[40:48]); return data }},
		{"layout_language", func(data []byte) []byte { data[40]++; return data }},
		{"layout_pointer", func(data []byte) []byte { data[45] = 1; return data }},
		{"class_zero", func(data []byte) []byte { clear(data[48:64]); return data }},
		{"profile_zero", func(data []byte) []byte { clear(data[64:80]); return data }},
		{"layout_with_tip_guid", func(data []byte) []byte { data[28] = 2; return data }},
		{"failure_with_profile", func(data []byte) []byte { data[8] = 10; return data }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if profile, err := decodeResult(test.mutate(sampleRecord(machineAMD64)), sampleTarget, machineAMD64); err == nil || profile != (Profile{}) {
				t.Fatalf("accepted malformed result: %+v, %v", profile, err)
			}
		})
	}
}

func TestDecodeHandlesNativeFailureAndHandleWidth(t *testing.T) {
	data := sampleRecord(machineAMD64)
	clear(data[28:36])
	clear(data[40:80])
	data[8] = 10
	binary.LittleEndian.PutUint32(data[16:], 0x800401f0)
	_, err := decodeResult(data, sampleTarget, machineAMD64)
	var native *ProbeError
	if !errors.As(err, &native) || native.Status != 10 || native.HRESULT != 0x800401f0 {
		t.Fatalf("native failure = %v", err)
	}
	data = sampleRecord(machineAMD64)
	binary.LittleEndian.PutUint64(data[40:], 0xfffffffff0200804)
	if _, err := decodeResult(data, sampleTarget, machineAMD64); err != nil {
		t.Fatalf("sign-extended HKL should be preserved: %v", err)
	}
	binary.LittleEndian.PutUint32(data[36:], machine386)
	if _, err := decodeResult(data, sampleTarget, machine386); err == nil {
		t.Fatal("32-bit helper accepted a 64-bit HKL")
	}
	if _, err := decodeResult(sampleRecord(machineAMD64), Target{}, machineAMD64); err == nil {
		t.Fatal("accepted an empty expected target")
	}
	for _, machine := range []uint16{0, 0xa641, 0x1c4, 0xffff} {
		if _, err := architecture(machine); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unknown machine %x = %v", machine, err)
		}
	}
	_ = (&ProbeError{Status: ^uint32(0)}).Error()
}

func FuzzDecodeResult(f *testing.F) {
	f.Add(sampleRecord(machineAMD64))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = decodeResult(data, sampleTarget, machineAMD64)
	})
}
