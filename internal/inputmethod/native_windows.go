//go:build windows

package inputmethod

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	winProfileTIP            = 1
	winProfileKeyboard       = 2
	winProfileEnabled        = 2
	winProfileSubstituted    = 4
	winActivateForSession    = 0x20000000
	winActivateOtherLanguage = 4
	winInputLanguageRequest  = 0x0050
	winSendAbortIfHung       = 2
	winSendErrorOnExit       = 0x20
	winLayoutRegistry        = `SYSTEM\CurrentControlSet\Control\Keyboard Layouts`
	winSelectionStableTime   = 60 * time.Millisecond
)

var (
	winUser32               = windows.NewLazySystemDLL("user32.dll")
	winOle32                = windows.NewLazySystemDLL("ole32.dll")
	winOleaut32             = windows.NewLazySystemDLL("oleaut32.dll")
	winShlwapi              = windows.NewLazySystemDLL("shlwapi.dll")
	winGetForegroundWindow  = winUser32.NewProc("GetForegroundWindow")
	winGetWindowThread      = winUser32.NewProc("GetWindowThreadProcessId")
	winGetGUIThreadInfo     = winUser32.NewProc("GetGUIThreadInfo")
	winGetKeyboardLayout    = winUser32.NewProc("GetKeyboardLayout")
	winGetKeyboardLayouts   = winUser32.NewProc("GetKeyboardLayoutList")
	winActivateKeyboard     = winUser32.NewProc("ActivateKeyboardLayout")
	winSendMessageTimeout   = winUser32.NewProc("SendMessageTimeoutW")
	winPeekMessage          = winUser32.NewProc("PeekMessageW")
	winTranslateMessage     = winUser32.NewProc("TranslateMessage")
	winDispatchMessage      = winUser32.NewProc("DispatchMessageW")
	winPostQuitMessage      = winUser32.NewProc("PostQuitMessage")
	winCoInitialize         = winOle32.NewProc("CoInitializeEx")
	winCoUninitialize       = winOle32.NewProc("CoUninitialize")
	winCoCreateInstance     = winOle32.NewProc("CoCreateInstance")
	winSysFreeString        = winOleaut32.NewProc("SysFreeString")
	winSysStringLen         = winOleaut32.NewProc("SysStringLen")
	winLoadIndirectString   = winShlwapi.NewProc("SHLoadIndirectString")
	winInputProfilesCLSID   = windows.GUID{Data1: 0x33c53a50, Data2: 0xf456, Data3: 0x4884, Data4: [8]byte{0xb0, 0x49, 0x85, 0xfd, 0x64, 0x3e, 0xcf, 0xed}}
	winProfileManagerIID    = windows.GUID{Data1: 0x71c6e74c, Data2: 0x0f28, Data3: 0x11d8, Data4: [8]byte{0xa8, 0x2a, 0x00, 0x06, 0x5b, 0x84, 0x43, 0x5c}}
	winInputProfilesIID     = windows.GUID{Data1: 0x1f02b6c5, Data2: 0x7842, Data3: 0x4ee6, Data4: [8]byte{0x8a, 0x0b, 0x9a, 0x24, 0x18, 0x3a, 0x95, 0xca}}
	winKeyboardCategoryGUID = windows.GUID{Data1: 0x34745c63, Data2: 0xb2f0, Data3: 0x4784, Data4: [8]byte{0x8b, 0x67, 0x5e, 0x12, 0xc8, 0x70, 0x1a, 0x31}}
)

// The padding follows TF_INPUTPROCESSORPROFILE on both Windows ABIs. Handles
// are process-local; persisted TIP identities use the two GUIDs instead.
type winProfile struct {
	Kind       uint32
	Language   uint16
	_          uint16
	Class      windows.GUID
	Profile    windows.GUID
	Category   windows.GUID
	Substitute uintptr
	Caps       uint32
	Layout     uintptr
	Flags      uint32
}

type winCOM struct{ VTable *[24]uintptr }

// COM may reenter Go while holding these arguments. Unlike SyscallN's keepalive,
// uintptrescapes keeps pointer-backed buffers off the movable goroutine stack.
//
//go:uintptrescapes
func winCOMCall(function uintptr, args ...uintptr) uintptr {
	result, _, _ := syscall.SyscallN(function, args...)
	return result
}

type winThreadMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   struct{ X, Y int32 }
	Private uint32
}

func winPumpThreadMessages() {
	// A COM STA needs to dispatch TSF's queued activation notifications. This
	// reads only our locked worker thread's queue, never another app's messages.
	for range 32 {
		var message winThreadMessage
		found, _, _ := winPeekMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, 1)
		if found == 0 {
			return
		}
		if message.Message == 0x0012 { // Preserve a caller's WM_QUIT.
			winPostQuitMessage.Call(message.WParam)
			return
		}
		winTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		winDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func (obj *winCOM) release() {
	if obj != nil {
		winCOMCall(obj.VTable[2], uintptr(unsafe.Pointer(obj)))
	}
}

func winHRESULT(operation string, result uintptr) error {
	return fmt.Errorf("%s: HRESULT 0x%08X", operation, uint32(result))
}

type winProfiles struct {
	manager      *winCOM
	profiles     *winCOM
	uninitialize bool
}

// The caller must keep this object and all its COM calls on a locked OS thread.
func winOpenProfiles() (*winProfiles, error) {
	result, _, _ := winCoInitialize.Call(0, 2)             // COINIT_APARTMENTTHREADED.
	if int32(result) < 0 && uint32(result) != 0x80010106 { // RPC_E_CHANGED_MODE.
		return nil, winHRESULT("initialize input-method services", result)
	}
	service := &winProfiles{uninitialize: int32(result) >= 0}
	result, _, _ = winCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&winInputProfilesCLSID)), 0, 1,
		uintptr(unsafe.Pointer(&winProfileManagerIID)), uintptr(unsafe.Pointer(&service.manager)),
	)
	if result != 0 || service.manager == nil {
		service.close()
		return nil, winHRESULT("open input-method profiles", result)
	}
	result = winCOMCall(service.manager.VTable[0], uintptr(unsafe.Pointer(service.manager)),
		uintptr(unsafe.Pointer(&winInputProfilesIID)), uintptr(unsafe.Pointer(&service.profiles)))
	if result != 0 || service.profiles == nil {
		service.close()
		return nil, winHRESULT("open input-method descriptions", result)
	}
	return service, nil
}

func (service *winProfiles) close() {
	service.profiles.release()
	service.manager.release()
	if service.uninitialize {
		winCoUninitialize.Call()
	}
}

func (service *winProfiles) enumerate(ctx context.Context) ([]winProfile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var enumerator *winCOM
	// A zero LANGID enumerates every installed language, not just the current one.
	result := winCOMCall(service.manager.VTable[6], uintptr(unsafe.Pointer(service.manager)), 0,
		uintptr(unsafe.Pointer(&enumerator)))
	if result != 0 || enumerator == nil {
		return nil, winHRESULT("enumerate input-method profiles", result)
	}
	defer enumerator.release()
	var profiles []winProfile
	for len(profiles) < 4096 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var profile winProfile
		var fetched uint32
		result = winCOMCall(enumerator.VTable[4], uintptr(unsafe.Pointer(enumerator)), 1,
			uintptr(unsafe.Pointer(&profile)), uintptr(unsafe.Pointer(&fetched)))
		if int32(result) < 0 {
			return nil, winHRESULT("read input-method profile", result)
		}
		if fetched == 0 {
			return profiles, nil
		}
		profiles = append(profiles, profile)
	}
	return nil, errors.New("too many installed input-method profiles")
}

func (service *winProfiles) active() (winProfile, error) {
	var profile winProfile
	result := winCOMCall(service.manager.VTable[10], uintptr(unsafe.Pointer(service.manager)),
		uintptr(unsafe.Pointer(&winKeyboardCategoryGUID)), uintptr(unsafe.Pointer(&profile)))
	if result != 0 {
		return profile, winHRESULT("read active input-method profile", result)
	}
	return profile, nil
}

func (service *winProfiles) description(profile *winProfile) string {
	var text *uint16
	result := winCOMCall(service.profiles.VTable[12], uintptr(unsafe.Pointer(service.profiles)),
		uintptr(unsafe.Pointer(&profile.Class)), uintptr(profile.Language),
		uintptr(unsafe.Pointer(&profile.Profile)), uintptr(unsafe.Pointer(&text)))
	if text == nil {
		return ""
	}
	defer winSysFreeString.Call(uintptr(unsafe.Pointer(text)))
	if result != 0 {
		return ""
	}
	length, _, _ := winSysStringLen.Call(uintptr(unsafe.Pointer(text)))
	if length > 16384 {
		return ""
	}
	return strings.TrimSpace(windows.UTF16ToString(unsafe.Slice(text, int(length))))
}

func (service *winProfiles) activate(profile *winProfile) error {
	result := winCOMCall(service.manager.VTable[3], uintptr(unsafe.Pointer(service.manager)),
		uintptr(profile.Kind), uintptr(profile.Language), uintptr(unsafe.Pointer(&profile.Class)),
		uintptr(unsafe.Pointer(&profile.Profile)), profile.Layout,
		winActivateForSession|winActivateOtherLanguage)
	if result != 0 {
		// S_FALSE means the profile is disabled; it is not successful activation.
		return winHRESULT("activate input-method profile", result)
	}
	return nil
}

func winSelectable(profile winProfile) bool {
	if profile.Flags&winProfileEnabled == 0 || profile.Flags&winProfileSubstituted != 0 {
		return false
	}
	return profile.Kind == winProfileTIP && profile.Category == winKeyboardCategoryGUID ||
		profile.Kind == winProfileKeyboard && profile.Layout != 0
}

func winProfileIdentity(profile winProfile, layoutID string) string {
	if profile.Kind == winProfileTIP {
		return fmt.Sprintf("windows:tip:%04x:%s:%s", profile.Language,
			strings.ToLower(profile.Class.String()), strings.ToLower(profile.Profile.String()))
	}
	return "windows:layout:" + strings.ToLower(layoutID)
}

type winLayoutInfo struct {
	id   string
	name string
}

func winLayoutCatalog() map[uint32]winLayoutInfo {
	result := make(map[uint32]winLayoutInfo)
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, winLayoutRegistry, registry.READ)
	if err != nil {
		return result
	}
	defer key.Close()
	names, _ := key.ReadSubKeyNames(-1)
	for _, name := range names {
		id, err := strconv.ParseUint(name, 16, 32)
		if err != nil {
			continue
		}
		layout, err := registry.OpenKey(key, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		display, _, _ := layout.GetStringValue("Layout Display Name")
		if display != "" {
			display = winResolveIndirectString(display)
		}
		if display == "" {
			display, _, _ = layout.GetStringValue("Layout Text")
		}
		variant, _, _ := layout.GetStringValue("Layout Id")
		layout.Close()
		info := winLayoutInfo{id: strings.ToLower(name), name: display}
		if id>>16 == 0 {
			result[uint32(id)<<16|uint32(id)] = info
		}
		// Legacy IMEs use an E*** HKL; variants use the registry's F*** layout ID.
		if id>>28 == 0xe {
			result[uint32(id)] = info
		}
		if variantID, err := strconv.ParseUint(variant, 16, 12); err == nil {
			result[(0xf000|uint32(variantID))<<16|uint32(id)&0xffff] = info
		}
	}
	return result
}

func winResolveIndirectString(value string) string {
	if !strings.HasPrefix(value, "@") {
		return value
	}
	input, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return ""
	}
	var output [1024]uint16
	result, _, _ := winLoadIndirectString.Call(uintptr(unsafe.Pointer(input)),
		uintptr(unsafe.Pointer(&output[0])), uintptr(len(output)), 0)
	if result != 0 {
		return ""
	}
	return windows.UTF16ToString(output[:])
}

func winLayoutIdentity(layout uintptr, catalog map[uint32]winLayoutInfo) winLayoutInfo {
	if info, found := catalog[uint32(layout)]; found {
		if info.name == "" {
			info.name = "Keyboard layout (" + strings.ToUpper(info.id) + ")"
		}
		return info
	}
	return winLayoutInfo{id: fmt.Sprintf("%08x", uint32(layout)), name: fmt.Sprintf("Keyboard layout (%08X)", uint32(layout))}
}

// List only reads the user's enabled TSF profiles and loaded keyboard layouts.
func List(ctx context.Context) ([]Method, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	service, err := winOpenProfiles()
	if err != nil {
		return nil, err
	}
	defer service.close()
	profiles, err := service.enumerate(ctx)
	if err != nil {
		return nil, err
	}
	catalog := winLayoutCatalog()
	methods := make([]Method, 0, len(profiles))
	seen := make(map[string]bool)
	for _, profile := range profiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !winSelectable(profile) {
			continue
		}
		layout := winLayoutIdentity(profile.Layout, catalog)
		method := Method{ID: winProfileIdentity(profile, layout.id)}
		if seen[method.ID] {
			continue
		}
		if profile.Kind == winProfileTIP {
			method.Name = winFriendlyProfileName(service.description(&profile))
			if method.Name == "" {
				method.Name = "Input method " + profile.Profile.String()
			}
		} else {
			method.Name = layout.name
		}
		if err := ValidateSelection(method.ID, method.Name); err != nil {
			continue
		}
		methods = append(methods, method)
		seen[method.ID] = true
	}
	sort.SliceStable(methods, func(i, j int) bool {
		if methods[i].Name == methods[j].Name {
			return methods[i].ID < methods[j].ID
		}
		return methods[i].Name < methods[j].Name
	})
	return methods, nil
}

func winFriendlyProfileName(name string) string {
	if name == "WeType" {
		return "\u5fae\u4fe1\u8f93\u5165\u6cd5 (WeType)"
	}
	return name
}

type winRect struct{ Left, Top, Right, Bottom int32 }
type winGUIThreadInfo struct {
	Size      uint32
	Flags     uint32
	Active    uintptr
	Focus     uintptr
	Capture   uintptr
	MenuOwner uintptr
	MoveSize  uintptr
	Caret     uintptr
	CaretRect winRect
}

type winFocus struct {
	window  uintptr
	control uintptr
	thread  uint32
	process uint32
}

func CaptureFocus() (Focus, error) {
	window, _, _ := winGetForegroundWindow.Call()
	if window == 0 {
		return nil, ErrFocusChanged
	}
	var process uint32
	thread, _, _ := winGetWindowThread.Call(window, uintptr(unsafe.Pointer(&process)))
	if thread == 0 {
		return nil, ErrFocusChanged
	}
	info := winGUIThreadInfo{Size: uint32(unsafe.Sizeof(winGUIThreadInfo{}))}
	result, _, _ := winGetGUIThreadInfo.Call(thread, uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return nil, ErrFocusChanged
	}
	// Hosted editors can give keyboard focus to a child owned by another thread.
	// Its layout, rather than the top-level frame's layout, receives the shortcut.
	if info.Focus != 0 {
		thread, _, _ = winGetWindowThread.Call(info.Focus, uintptr(unsafe.Pointer(&process)))
		if thread == 0 {
			return nil, ErrFocusChanged
		}
	}
	return &winFocus{window: window, control: info.Focus, thread: uint32(thread), process: process}, nil
}

func (focus *winFocus) Valid() bool {
	current, err := CaptureFocus()
	return err == nil && *focus == *current.(*winFocus)
}

func winSameProfile(left, right winProfile) bool {
	if left.Kind != right.Kind || left.Language != right.Language {
		return false
	}
	if left.Kind == winProfileTIP {
		return left.Class == right.Class && left.Profile == right.Profile
	}
	return uint32(left.Layout) == uint32(right.Layout)
}

func winThreadLayout(thread uint32) uintptr {
	layout, _, _ := winGetKeyboardLayout.Call(uintptr(thread))
	return layout
}

func winLoadedLayouts() []uintptr {
	count, _, _ := winGetKeyboardLayouts.Call(0, 0)
	if count == 0 || count > 4096 {
		return nil
	}
	layouts := make([]uintptr, count)
	count, _, _ = winGetKeyboardLayouts.Call(count, uintptr(unsafe.Pointer(&layouts[0])))
	if int(count) > len(layouts) {
		return nil
	}
	return layouts[:count]
}

func winLanguageLayout(profile winProfile, layouts []uintptr) uintptr {
	if profile.Kind == winProfileKeyboard {
		return profile.Layout
	}
	if profile.Substitute != 0 {
		return profile.Substitute
	}
	for _, layout := range layouts {
		if uint16(layout) == profile.Language {
			return layout
		}
	}
	return 0
}

func (focus *winFocus) requestLayout(ctx context.Context, layout uintptr) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !focus.Valid() {
		return ErrFocusChanged
	}
	window := focus.control
	if window == 0 {
		window = focus.window
	}
	timeout := 150 * time.Millisecond
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return context.DeadlineExceeded
	}
	var response uintptr
	result, _, callErr := winSendMessageTimeout.Call(window, winInputLanguageRequest, 0, layout,
		winSendAbortIfHung|winSendErrorOnExit, uintptr(max(1, timeout.Milliseconds())), uintptr(unsafe.Pointer(&response)))
	if result == 0 {
		if callErr == windows.ERROR_SUCCESS {
			callErr = windows.ERROR_TIMEOUT
		}
		return fmt.Errorf("request the foreground input language: %w", callErr)
	}
	return nil
}

type winSwitchProfiles interface {
	enumerate(context.Context) ([]winProfile, error)
	activate(*winProfile) error
	close()
}

type winSwitchAPI struct {
	valid             func() bool
	foregroundProfile func(context.Context) (winProfile, error)
	openProfiles      func() (winSwitchProfiles, error)
	layoutCatalog     func() map[uint32]winLayoutInfo
	loadedLayouts     func() []uintptr
	threadLayout      func(uint32) uintptr
	activateKeyboard  func(uintptr) error
	requestLayout     func(context.Context, uintptr) error
	pumpMessages      func()
}

func (focus *winFocus) Switch(ctx context.Context, id string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return winSwitchInputMethod(ctx, id, focus.thread, winSwitchAPI{
		valid:             focus.Valid,
		foregroundProfile: func(ctx context.Context) (winProfile, error) { return winReadForegroundProfile(ctx, focus) },
		openProfiles:      func() (winSwitchProfiles, error) { return winOpenProfiles() },
		layoutCatalog:     winLayoutCatalog,
		loadedLayouts:     winLoadedLayouts,
		threadLayout:      winThreadLayout,
		activateKeyboard: func(layout uintptr) error {
			result, _, err := winActivateKeyboard.Call(layout, 0)
			if result == 0 {
				return err
			}
			return nil
		},
		requestLayout: focus.requestLayout,
		pumpMessages:  winPumpThreadMessages,
	})
}

func winSwitchGuard(ctx context.Context, valid func() bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !valid() {
		return ErrFocusChanged
	}
	return ctx.Err()
}

func winValidateForegroundProfile(profile winProfile) error {
	known := profile.Kind == winProfileKeyboard || profile.Kind == winProfileTIP &&
		profile.Class != (windows.GUID{}) && profile.Profile != (windows.GUID{}) && profile.Category == winKeyboardCategoryGUID
	if !known || profile.Layout == 0 || profile.Language == 0 || profile.Language != uint16(profile.Layout) {
		return fmt.Errorf("%w: the foreground input-method identity is unavailable or inconsistent", ErrUnavailable)
	}
	return nil
}

func winForegroundProfileMatches(current, target winProfile) bool {
	return winValidateForegroundProfile(current) == nil && winSameProfile(current, target)
}

func winSwitchInputMethod(ctx context.Context, id string, thread uint32, api winSwitchAPI) error {
	if err := winSwitchGuard(ctx, api.valid); err != nil {
		return err
	}
	if strings.HasPrefix(id, "windows:tip:") {
		current, err := api.foregroundProfile(ctx)
		if guardErr := winSwitchGuard(ctx, api.valid); guardErr != nil {
			return guardErr
		}
		if err != nil {
			return err
		}
		if err := winValidateForegroundProfile(current); err != nil {
			return err
		}
		// Only evidence read inside the original target thread can skip selection.
		// Different TIPs can share an HKL; our worker's profile is not evidence.
		if current.Kind == winProfileTIP && winProfileIdentity(current, "") == id {
			return nil
		}
	}
	service, err := api.openProfiles()
	if err != nil {
		return err
	}
	defer service.close()
	if err := winSwitchGuard(ctx, api.valid); err != nil {
		return err
	}
	profiles, err := service.enumerate(ctx)
	if guardErr := winSwitchGuard(ctx, api.valid); guardErr != nil {
		return guardErr
	}
	if err != nil {
		return err
	}
	var catalog map[uint32]winLayoutInfo
	if strings.HasPrefix(id, "windows:layout:") {
		catalog = api.layoutCatalog()
	}
	var target *winProfile
	for i := range profiles {
		profile := &profiles[i]
		if winSelectable(*profile) && winProfileIdentity(*profile, winLayoutIdentity(profile.Layout, catalog).id) == id {
			target = profile
			break
		}
	}
	if target == nil {
		return ErrUnavailable
	}
	if err := winSwitchGuard(ctx, api.valid); err != nil {
		return err
	}
	if target.Kind == winProfileKeyboard {
		current := api.threadLayout(thread)
		if err := winSwitchGuard(ctx, api.valid); err != nil {
			return err
		}
		if uint32(current) == uint32(target.Layout) {
			return nil
		}
		if err := api.requestLayout(ctx, target.Layout); err != nil {
			return err
		}
		return winWaitForSelection(ctx, api.valid, func() bool {
			return uint32(api.threadLayout(thread)) == uint32(target.Layout)
		})
	}

	// Activate the exact GUID pair for the desktop only after the target-thread
	// check. No ENABLEPROFILE or default-registration APIs are used.
	layout := winLanguageLayout(*target, api.loadedLayouts())
	if layout == 0 {
		return fmt.Errorf("%w: no loaded keyboard layout for the input language", ErrUnavailable)
	}
	previous := api.threadLayout(0)
	if uint16(previous) != target.Language {
		if err := winSwitchGuard(ctx, api.valid); err != nil {
			return err
		}
		if err := api.activateKeyboard(layout); err != nil {
			return fmt.Errorf("prepare input-method worker language: %w", err)
		}
		defer api.activateKeyboard(previous)
	}
	if err := winSwitchGuard(ctx, api.valid); err != nil {
		return err
	}
	// Windows exposes synchronous COM here; cancellation is checked immediately
	// before and after it. Never detach a timed-out call that could switch later.
	if err := service.activate(target); err != nil {
		return err
	}
	if err := winSwitchGuard(ctx, api.valid); err != nil {
		return err
	}
	currentLayout := api.threadLayout(thread)
	if err := winSwitchGuard(ctx, api.valid); err != nil {
		return err
	}
	if uint16(currentLayout) != target.Language {
		if err := api.requestLayout(ctx, layout); err != nil {
			return err
		}
	}
	return winWaitForSelectionResult(ctx, api.valid, func(queryCtx context.Context) (bool, error) {
		api.pumpMessages()
		if err := winSwitchGuard(queryCtx, api.valid); err != nil {
			return false, err
		}
		current, err := api.foregroundProfile(queryCtx)
		if err != nil {
			return false, err
		}
		if err := winValidateForegroundProfile(current); err != nil {
			return false, err
		}
		return winForegroundProfileMatches(current, *target), nil
	})
}

type winSelectionStability struct{ since time.Time }

func (stability *winSelectionStability) ready(matched bool, now time.Time) bool {
	if !matched {
		stability.since = time.Time{}
		return false
	}
	if stability.since.IsZero() {
		stability.since = now
	}
	return now.Sub(stability.since) >= winSelectionStableTime
}

func winWaitForSelection(ctx context.Context, valid func() bool, selected func() bool) error {
	return winWaitForSelectionResult(ctx, valid, func(context.Context) (bool, error) {
		return selected(), nil
	})
}

func winWaitForSelectionResult(ctx context.Context, valid func() bool, selected func(context.Context) (bool, error)) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	// A caller without a deadline must not wait forever for an unresponsive app.
	deadline := time.Now().Add(1500 * time.Millisecond)
	queryCtx, cancelQueries := context.WithDeadline(ctx, deadline)
	defer cancelQueries()
	timeout := time.NewTimer(time.Until(deadline))
	defer timeout.Stop()
	timeoutErr := errors.New("the foreground application did not confirm the selected input method")
	var stability winSelectionStability
	for {
		if err := winSwitchGuard(ctx, valid); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return timeoutErr
		}
		matched, err := selected(queryCtx)
		if guardErr := winSwitchGuard(ctx, valid); guardErr != nil {
			return guardErr
		}
		if err != nil {
			return err
		}
		now := time.Now()
		if !now.Before(deadline) {
			return timeoutErr
		}
		// Keep the selection signals stable while activation notifications settle.
		// This does not establish readiness of vendor-specific voice shortcuts.
		if stability.ready(matched, now) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return timeoutErr
		case <-ticker.C:
		}
	}
}
