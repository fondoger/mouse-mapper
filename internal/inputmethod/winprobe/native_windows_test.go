//go:build windows

package winprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func requireNativeProbeTest(t *testing.T) {
	t.Helper()
	if os.Getenv("MOUSE_MAPPER_NATIVE_PROBE_TEST") != "1" {
		t.Skip("opt in with MOUSE_MAPPER_NATIVE_PROBE_TEST=1; targets are disposable hidden fixture windows only")
	}
	if err := RequireAssets(); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedNativeOutput(t *testing.T) {
	var output boundedOutput
	for range 100 {
		if n, err := output.Write(make([]byte, 1024)); n != 1024 || err != nil {
			t.Fatalf("bounded writer = %d, %v", n, err)
		}
	}
	if len(output.data) != wireSize+1 {
		t.Fatalf("output retained %d bytes", len(output.data))
	}
}

func TestProcessMachineInformationABIAndFallback(t *testing.T) {
	if unsafe.Sizeof(processMachineInformation{}) != 8 {
		t.Fatal("process machine query ABI must have an eight-byte buffer")
	}
	for _, err := range []error{windows.ERROR_INVALID_PARAMETER, windows.ERROR_NOT_SUPPORTED,
		windows.ERROR_INVALID_FUNCTION, windows.ERROR_CALL_NOT_IMPLEMENTED} {
		if !machineInformationUnavailable(err) {
			t.Fatalf("unsupported API error did not permit legacy fallback: %v", err)
		}
	}
	for _, err := range []error{nil, windows.ERROR_ACCESS_DENIED, windows.ERROR_INVALID_HANDLE,
		windows.ERROR_PROC_NOT_FOUND, windows.ERROR_BAD_LENGTH, errors.New("query failed")} {
		if machineInformationUnavailable(err) {
			t.Fatalf("unexpected query error permitted an architecture guess: %v", err)
		}
	}
}

func TestWindowsNativeProbeInvalidArguments(t *testing.T) {
	requireNativeProbeTest(t)
	assets, err := bundledAssets()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := assets.extract(t.TempDir(), runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"0", "0", "0", "1"}, {"1", "2", "3", "1501"}, {"-1", "2", "3", "50"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		command := exec.CommandContext(ctx, paths[0], args...)
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		data, err := command.Output()
		cancel()
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != 1 || len(data) != wireSize ||
			binary.LittleEndian.Uint32(data) != wireMagic || binary.LittleEndian.Uint32(data[8:]) != 1 {
			t.Fatalf("invalid args %v: length=%d, error=%v", args, len(data), err)
		}
	}
}

type fixtureReport struct {
	Target      Target
	Expected    Profile
	Unavailable string
	Error       string
}

func startProbeFixture(t *testing.T, mode string) fixtureReport {
	t.Helper()
	executable := os.Getenv("MOUSE_MAPPER_NATIVE_PROBE_FIXTURE_EXE")
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	command := exec.CommandContext(ctx, executable, "-test.run=^TestNativeProbeFixtureProcess$", "-test.count=1")
	command.Env = append(os.Environ(), "MOUSE_MAPPER_PROBE_FIXTURE="+mode)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	command.WaitDelay = 200 * time.Millisecond
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = input.Write([]byte{'\n'})
		_ = input.Close()
		if err := command.Wait(); err != nil && ctx.Err() == nil {
			t.Errorf("fixture failed to exit cleanly: %v, %s", err, stderr.String())
		}
		cancel()
	})
	var report fixtureReport
	if err := json.NewDecoder(io.LimitReader(output, 4096)).Decode(&report); err != nil {
		t.Fatalf("read isolated fixture report: %v", err)
	}
	if report.Error != "" {
		t.Fatal(report.Error)
	}
	if report.Unavailable != "" {
		t.Skip(report.Unavailable)
	}
	if !validTarget(report.Target) || report.Target.Process != uint32(command.Process.Pid) || report.Target.Process == uint32(os.Getpid()) {
		t.Fatalf("fixture did not identify a separate owned process: %+v", report)
	}
	return report
}

func TestWindowsNativeProbeOwnedWindow(t *testing.T) {
	requireNativeProbeTest(t)
	// Even the extracted executable cache belongs only to this test run.
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Run("no_existing_tsf", func(t *testing.T) {
		fixture := startProbeFixture(t, "no-tsf")
		for range 2 {
			profile, err := Current(context.Background(), fixture.Target)
			var native *ProbeError
			if !errors.As(err, &native) || native.Status != 10 || profile != (Profile{}) {
				t.Fatalf("uninitialized target must stay uninitialized: %+v, %v", profile, err)
			}
		}
		changed := fixture.Target
		changed.Thread++
		if _, err := Current(context.Background(), changed); !errors.Is(err, ErrTargetChanged) {
			t.Fatalf("wrong captured thread = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Current(ctx, fixture.Target); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled read = %v", err)
		}
	})
	t.Run("existing_english_thread", func(t *testing.T) {
		fixture := startProbeFixture(t, "existing-tsf")
		for range 3 {
			profile, err := Current(context.Background(), fixture.Target)
			if err != nil || profile != fixture.Expected {
				t.Fatalf("query did not read the owned thread's exact profile: got %+v, want %+v, error %v", profile, fixture.Expected, err)
			}
		}
	})
	t.Run("unresponsive_owned_thread", func(t *testing.T) {
		fixture := startProbeFixture(t, "hung")
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		started := time.Now()
		profile, err := Current(ctx, fixture.Target)
		var native *ProbeError
		if err == nil || profile != (Profile{}) || time.Since(started) > 2*time.Second {
			t.Fatalf("unresponsive target was not bounded: %+v, %v", profile, err)
		}
		if !errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &native) || (native.Status != 8 && native.Status != 14)) {
			t.Fatalf("unexpected unresponsive error: %v", err)
		}
	})
}

type fixtureCOM struct{ table *[16]uintptr }

func (object *fixtureCOM) release() {
	if object != nil {
		syscall.SyscallN(object.table[2], uintptr(unsafe.Pointer(object)))
	}
}

type fixtureNativeProfile struct {
	Kind       uint32
	Language   uint16
	Padding    uint16
	Class      GUID
	Profile    GUID
	Category   GUID
	Substitute uintptr
	Caps       uint32
	Layout     uintptr
	Flags      uint32
}

type fixtureMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	X       int32
	Y       int32
	Private uint32
}

// This subprocess never shows/focuses a window. TSF setup, when requested,
// affects only its locked thread and only a preexisting English keyboard layout.
func TestNativeProbeFixtureProcess(t *testing.T) {
	mode := os.Getenv("MOUSE_MAPPER_PROBE_FIXTURE")
	if mode == "" {
		return
	}
	if mode != "no-tsf" && mode != "existing-tsf" && mode != "hung" {
		t.Fatal("unknown isolated fixture mode")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	user := windows.NewLazySystemDLL("user32.dll")
	getLayout := user.NewProc("GetKeyboardLayout")
	activateLayout := user.NewProc("ActivateKeyboardLayout")
	report := fixtureReport{}
	var cleanup func()
	if mode == "existing-tsf" {
		var err error
		report.Expected, cleanup, err = setupFixtureTSF(user, getLayout, activateLayout)
		if cleanup != nil {
			defer cleanup()
		}
		if errors.Is(err, ErrUnsupported) {
			report.Unavailable = err.Error()
		} else if err != nil {
			report.Error = err.Error()
		}
		if err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(report)
			return
		}
	}
	class, _ := windows.UTF16PtrFromString("STATIC")
	title, _ := windows.UTF16PtrFromString("Mouse Mapper isolated input-method test")
	window, _, err := user.NewProc("CreateWindowExW").Call(0x08000080, uintptr(unsafe.Pointer(class)),
		uintptr(unsafe.Pointer(title)), 0x80000000, 0, 0, 1, 1, 0, 0, 0, 0)
	if window == 0 {
		report.Error = fmt.Sprintf("create hidden nonactivating fixture window: %v", err)
		_ = json.NewEncoder(os.Stdout).Encode(report)
		return
	}
	defer user.NewProc("DestroyWindow").Call(window)
	thread := windows.GetCurrentThreadId()
	report.Target = Target{Process: uint32(os.Getpid()), Thread: thread, Window: window}
	var stop atomic.Bool
	go func() {
		var one [1]byte
		_, _ = os.Stdin.Read(one[:])
		stop.Store(true)
		user.NewProc("PostThreadMessageW").Call(uintptr(thread), 0x0012, 0, 0)
	}()
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		t.Fatal(err)
	}
	if mode == "hung" {
		for !stop.Load() {
			time.Sleep(10 * time.Millisecond)
		}
		return
	}
	getMessage := user.NewProc("GetMessageW")
	translate := user.NewProc("TranslateMessage")
	dispatch := user.NewProc("DispatchMessageW")
	for {
		var message fixtureMessage
		result, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if result == 0 || int32(result) == -1 {
			return
		}
		translate.Call(uintptr(unsafe.Pointer(&message)))
		dispatch.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func setupFixtureTSF(user *windows.LazyDLL, getLayout, activateLayout *windows.LazyProc) (Profile, func(), error) {
	list := user.NewProc("GetKeyboardLayoutList")
	count, _, _ := list.Call(0, 0)
	if count == 0 || count > 4096 {
		return Profile{}, nil, fmt.Errorf("%w: no loaded keyboard layouts", ErrUnsupported)
	}
	layouts := make([]uintptr, count)
	count, _, _ = list.Call(count, uintptr(unsafe.Pointer(&layouts[0])))
	if count > uintptr(len(layouts)) {
		return Profile{}, nil, errors.New("fixture layout list changed")
	}
	var english uintptr
	for _, layout := range layouts[:count] {
		if uint16(layout)&0x03ff == 9 && uint32(layout)>>28 != 0xe {
			english = layout
			break
		}
	}
	if english == 0 {
		return Profile{}, nil, fmt.Errorf("%w: no existing English keyboard for a noninvasive fixture", ErrUnsupported)
	}
	previous, _, _ := getLayout.Call(0)
	if result, _, err := activateLayout.Call(english, 0); result == 0 {
		return Profile{}, nil, fmt.Errorf("select fixture-thread English layout: %v", err)
	}
	ole := windows.NewLazySystemDLL("ole32.dll")
	initialized := false
	activated := false
	var manager, profiles *fixtureCOM
	cleanup := func() {
		profiles.release()
		if activated {
			syscall.SyscallN(manager.table[4], uintptr(unsafe.Pointer(manager)))
		}
		manager.release()
		if initialized {
			ole.NewProc("CoUninitialize").Call()
		}
		// Restore only after TSF is deactivated, so no previous TIP is activated.
		if previous != 0 {
			activateLayout.Call(previous, 0)
		}
	}
	check := func(operation string, result uintptr) error {
		if result != 0 {
			return fmt.Errorf("%s: HRESULT 0x%08X", operation, uint32(result))
		}
		return nil
	}
	hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if int32(hr) < 0 {
		return Profile{}, cleanup, check("initialize fixture COM", hr)
	}
	initialized = true
	create := ole.NewProc("CoCreateInstance")
	threadClass := windows.GUID{Data1: 0x529a9e6b, Data2: 0x6587, Data3: 0x4f23, Data4: [8]byte{0xab, 0x9e, 0x9c, 0x7d, 0x68, 0x3e, 0x3c, 0x50}}
	threadIID := windows.GUID{Data1: 0x3e90ade3, Data2: 0x7594, Data3: 0x4cb0, Data4: [8]byte{0xbb, 0x58, 0x69, 0x62, 0x8f, 0x5f, 0x45, 0x8c}}
	hr, _, _ = create.Call(uintptr(unsafe.Pointer(&threadClass)), 0, 1, uintptr(unsafe.Pointer(&threadIID)), uintptr(unsafe.Pointer(&manager)))
	if err := check("create fixture thread manager", hr); err != nil || manager == nil {
		return Profile{}, cleanup, fmt.Errorf("create fixture thread manager: %v", err)
	}
	var client uint32
	hr, _, _ = syscall.SyscallN(manager.table[14], uintptr(unsafe.Pointer(manager)), uintptr(unsafe.Pointer(&client)), 0x21)
	if err := check("activate fixture without TIPs or keyboard-layout sync", hr); err != nil {
		return Profile{}, cleanup, err
	}
	activated = true
	profileClass := windows.GUID{Data1: 0x33c53a50, Data2: 0xf456, Data3: 0x4884, Data4: [8]byte{0xb0, 0x49, 0x85, 0xfd, 0x64, 0x3e, 0xcf, 0xed}}
	profileIID := windows.GUID{Data1: 0x71c6e74c, Data2: 0x0f28, Data3: 0x11d8, Data4: [8]byte{0xa8, 0x2a, 0x00, 0x06, 0x5b, 0x84, 0x43, 0x5c}}
	hr, _, _ = create.Call(uintptr(unsafe.Pointer(&profileClass)), 0, 1, uintptr(unsafe.Pointer(&profileIID)), uintptr(unsafe.Pointer(&profiles)))
	if err := check("create fixture profile manager", hr); err != nil || profiles == nil {
		return Profile{}, cleanup, fmt.Errorf("create fixture profile manager: %v", err)
	}
	var empty windows.GUID
	// Type 2 and zero flags select only this test thread's existing English layout.
	hr, _, _ = syscall.SyscallN(profiles.table[3], uintptr(unsafe.Pointer(profiles)), 2, uintptr(uint16(english)),
		uintptr(unsafe.Pointer(&empty)), uintptr(unsafe.Pointer(&empty)), english, 0)
	if err := check("select fixture-thread English profile", hr); err != nil {
		return Profile{}, cleanup, err
	}
	category := windows.GUID{Data1: 0x34745c63, Data2: 0xb2f0, Data3: 0x4784, Data4: [8]byte{0x8b, 0x67, 0x5e, 0x12, 0xc8, 0x70, 0x1a, 0x31}}
	var active fixtureNativeProfile
	hr, _, _ = syscall.SyscallN(profiles.table[10], uintptr(unsafe.Pointer(profiles)), uintptr(unsafe.Pointer(&category)), uintptr(unsafe.Pointer(&active)))
	if err := check("read fixture expected profile", hr); err != nil {
		return Profile{}, cleanup, err
	}
	actual, _, _ := getLayout.Call(0)
	if active.Kind != 2 || active.Language != uint16(english) || uint32(actual) != uint32(english) || uint32(active.Layout) != uint32(english) {
		return Profile{}, cleanup, fmt.Errorf("fixture did not stay on its English keyboard: kind=%d language=%x HKL=%x", active.Kind, active.Language, actual)
	}
	return Profile{Kind: 2, Language: active.Language, Layout: uint64(actual)}, cleanup, nil
}
