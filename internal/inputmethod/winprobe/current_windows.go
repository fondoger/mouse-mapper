//go:build windows

package winprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxQueryTime = 1500 * time.Millisecond

var getProcessInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessInformation")

type processMachineInformation struct {
	Machine    uint16
	Reserved   uint16
	Attributes uint32
}

func rejectReparsePoint(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("native input-method cache contains a reparse point: %s", path)
	}
	return nil
}

func targetMatches(target Target, process windows.Handle) bool {
	var pid uint32
	tid, err := windows.GetWindowThreadProcessId(windows.HWND(target.Window), &pid)
	if err != nil || tid != target.Thread || pid != target.Process {
		return false
	}
	state, err := windows.WaitForSingleObject(process, 0)
	return err == nil && state == uint32(windows.WAIT_TIMEOUT)
}

func targetArchitecture(process windows.Handle) (uint16, string, error) {
	// ProcessMachineTypeInfo (9) is supported on Windows 11 and distinguishes
	// native ARM64 from x64 emulation, which is not reported as WOW64.
	if err := getProcessInformation.Find(); err == nil {
		var information processMachineInformation
		result, _, callErr := getProcessInformation.Call(uintptr(process), 9,
			uintptr(unsafe.Pointer(&information)), unsafe.Sizeof(information))
		if result != 0 {
			return resolveArchitecture(information.Machine, true, 0, 0)
		}
		if !machineInformationUnavailable(callErr) {
			return 0, "", fmt.Errorf("%w: read process machine type: %v", ErrUnsupported, callErr)
		}
	} else if !errors.Is(err, windows.ERROR_PROC_NOT_FOUND) {
		return 0, "", fmt.Errorf("%w: locate process machine query: %v", ErrUnsupported, err)
	}
	var machine, native uint16
	if err := windows.IsWow64Process2(process, &machine, &native); err != nil {
		return 0, "", fmt.Errorf("%w: determine target architecture: %v", ErrUnsupported, err)
	}
	return resolveArchitecture(0, false, machine, native)
}

func machineInformationUnavailable(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_CALL_NOT_IMPLEMENTED)
}

// Keep verified files open without write/delete sharing until the helper exits.
func lockAsset(path string, item asset) (*os.File, error) {
	if _, err := plainPath(path, false); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_SEQUENTIAL_SCAN, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(handle, &info)
	if err != nil || info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		file.Close()
		return nil, fmt.Errorf("native input-method helper is not a regular locked file: %s", path)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, int64(len(item.data))+1))
	if err != nil || n != int64(len(item.data)) || !bytes.Equal(hash.Sum(nil), item.hash[:]) {
		file.Close()
		return nil, fmt.Errorf("native input-method helper changed before execution: %s", path)
	}
	return file, nil
}

type boundedOutput struct{ data []byte }

func (out *boundedOutput) Write(data []byte) (int, error) {
	remaining := wireSize + 1 - len(out.data)
	if remaining > 0 {
		out.data = append(out.data, data[:min(remaining, len(data))]...)
	}
	return len(data), nil
}

func runProbe(ctx context.Context, path string, target Target, machine uint16) (Profile, error) {
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	remaining := maxQueryTime
	if deadline, ok := ctx.Deadline(); ok {
		remaining = min(remaining, time.Until(deadline))
	}
	if remaining < time.Millisecond {
		return Profile{}, context.DeadlineExceeded
	}
	command := exec.CommandContext(ctx, path, strconv.FormatUint(uint64(target.Process), 10),
		strconv.FormatUint(uint64(target.Thread), 10), strconv.FormatUint(uint64(target.Window), 16),
		strconv.FormatInt(remaining.Milliseconds(), 10))
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	command.WaitDelay = 100 * time.Millisecond
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	runErr := command.Run()
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	profile, err := decodeResult(output.data, target, machine)
	if err != nil {
		var native *ProbeError
		if errors.As(err, &native) {
			return Profile{}, err // Exit 1 still carries the native error record.
		}
		if runErr != nil {
			return Profile{}, fmt.Errorf("run exact input-method query: %w (%v)", runErr, err)
		}
		return Profile{}, err
	}
	if runErr != nil {
		return Profile{}, fmt.Errorf("input-method query failed despite a success record: %w", runErr)
	}
	return profile, nil
}

// Current samples only the captured target. It does not find or focus windows,
// initialize TSF, activate profiles, or fall back to a worker-thread estimate.
func Current(ctx context.Context, target Target) (Profile, error) {
	ctx, cancel := context.WithTimeout(ctx, maxQueryTime)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	if !validTarget(target) {
		return Profile{}, ErrTargetChanged
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, target.Process)
	if err != nil {
		return Profile{}, fmt.Errorf("open input-method query target: %w", err)
	}
	defer windows.CloseHandle(process)
	if !targetMatches(target, process) {
		return Profile{}, ErrTargetChanged
	}
	machine, arch, err := targetArchitecture(process)
	if err != nil {
		return Profile{}, err
	}
	assets, err := bundledAssets()
	if err != nil {
		return Profile{}, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Profile{}, fmt.Errorf("locate native input-method cache: %w", err)
	}
	paths, err := assets.extract(cache, arch)
	if err != nil {
		return Profile{}, err
	}
	for i, path := range paths {
		file, err := lockAsset(path, assets.pairs[arch][i])
		if err != nil {
			return Profile{}, err
		}
		defer file.Close()
	}
	if !targetMatches(target, process) {
		return Profile{}, ErrTargetChanged
	}
	profile, err := runProbe(ctx, paths[0], target, machine)
	if err != nil {
		return Profile{}, err
	}
	if !targetMatches(target, process) {
		return Profile{}, ErrTargetChanged
	}
	return profile, nil
}
