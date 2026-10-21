# Changelog

## 0.4.0 - 2026-10-21

- Show single-click and long-press actions together as two rows, each with its own click-to-record shortcut field and optional input-method selector. Remove the long-press enable switch, action tabs, manual picker and shortcut presets.
- Leave the optional long shortcut empty by default. Recording a shortcut enables it; clearing it disables long press without changing the single-click action. Keep the configurable 100-5000 ms threshold.
- Add an opt-in "hold until side-button release" checkbox. At the threshold, the long chord is pressed once and stays held until mouse-up; the default remains one complete tap. Preserve key ownership, IME readiness, delayed-timer release and lifecycle cleanup. Existing settings default to tap mode.
- Retain recordable Escape, modifier-only chords, and exact-current-input-method selection skipping. Generic modifiers continue to send the left-hand native keys.
- Updated sources and regression cases without running local builds, tests or screenshot checks, as requested. Tagged GitHub Actions retain their existing automatic build/release gates.

## 0.3.1 - 2026-10-21

- Replace the automatically armed recording panel with a compact shortcut field. Opening the editor or changing actions/modes never starts capture; explicitly activating the field shows the recording prompt before listening. Existing keys remain intact if no new shortcut is entered.
- Record Escape, Enter and Tab as shortcut keys while the field is armed, instead of dismissing the editor, submitting it or navigating away. Keep explicit Cancel/close controls and idle Escape dismissal. Keyboard users can focus the field with Tab and activate it with Enter or Space.
- Stop capture when clicking elsewhere, editing another control or leaving the application window; returning never silently re-arms it. Reset held-key tracking between sessions while retaining complete modifier-only chords regardless of release order.
- Cover activation, blur, keyboard accessibility and special-key save behavior for both side buttons and single/long actions with native UI regression tests and Windows/macOS-label renderer snapshots. Preserve the exact-current-input-method no-switch behavior and all existing mapping/update defaults.

## 0.3.0 - 2026-10-20

- Add an optional long-press action for each side button, with an independent shortcut, input method and 100-5000 ms threshold (default 500 ms). Without long press, retain immediate key-down and hold-until-release behavior. With it, a short release sends one complete tap; reaching the threshold immediately sends one long-action tap, never repeats and never also sends the short action on release.
- Add a compact Native UI editor for single/long actions, numeric threshold validation, isolated drafts and atomic Save/Cancel. Existing settings remain compatible and do not enable long press automatically.
- Skip input-method selection entirely when the captured target already uses the exact requested profile. Windows now reads the actual focus thread's existing TSF identity using small matching-architecture native query helpers; it never infers WeChat/Doubao identity from a shared HKL, another process's profile or a cached prior selection. macOS retains its exact TIS identity check.
- Fail closed when the current Windows input-method identity cannot be read: report the error and skip the shortcut, without blindly activating a profile. Native queries do not initialize or activate a target's TSF manager. Build and verify all helper architectures from source with a pinned, hash-checked LLVM-MinGW toolchain in CI and Windows releases; the Go application remains cgo-free.
- Keep Windows COM argument buffers stable across native callbacks and Go stack growth; exercise the ABI with forced stack-growth/GC and race-instrumented regression tests.
- Consume mapped side-button down/up events even when focus capture, input-method preparation or key injection fails. Handle mouse-software-injected XBUTTON events and duplicate releases instead of letting them fall through to original navigation. Separately generated browser commands, keyboard shortcuts and raw-device input remain distinct vendor/application paths.
- Cancel pending long actions and input-method work on pause, configuration changes, stop or native failure; preserve original focus, shared-key ownership and one-shot threshold decisions under delayed timer/dispatch delivery.
- Keep compact bottom-right feedback, modifier-only recording, tray behavior, seven-day update checks and macOS 13+ requirements unchanged. Physical mouse/vendor voice behavior still requires device-level acceptance; automated tests do not silently change the user's live input method or installed application.

## 0.2.1 - 2026-10-19

- Correct the declared minimum macOS version to 13.0 to match the actual Mach-O deployment target produced by Go 1.27. Earlier packages declared 12.0 in their Info.plist, but that did not make those binaries compatible with macOS 12.
- Add a release gate that compares the configured minimum macOS version, application Info.plist and every Mach-O slice, including universal packages, before publication.
- Retain all 0.2.0 recording, compact bottom-right feedback and optional input-method features. Windows behavior is unchanged.

## 0.2.0 - 2026-10-19

- Move shortcut feedback to the bottom-right of the pointer's display work area, not the application window's top-left; respect monitor origins, usable bounds and Windows DPI.
- Reduce feedback to one line, approximately 112 x 38 DIP for a common shortcut, expanding only as needed for longer combinations.
- Fix the black square behind Windows feedback with a native rounded window region; retain non-activating, click-through behavior. Verify actual desktop composition with both GPU and software rendering.
- Add optional per-button input-method selection before shortcut delivery, with an installed/enabled system list, refresh, saved selections and a default of no switching.
- Keep the editor's Save/Cancel actions accessible at the minimum window size, including manual key selection, long input-method names and discovery errors.
- Preserve modifier-only recordings such as Ctrl+Shift+Win/Command while keys are released; allow subsequent chords without accumulating non-overlapping modifiers or letting auto-repeat replace a newer recording.
- Select exact Windows TSF profiles and macOS TIS input sources. Repeated selection does not cycle input methods, and distinct same-language IMEs remain distinct. Label Windows WeType as WeChat Input Method in the Chinese UI.
- Confirm stable selection before sending the shortcut. Keep switching work outside native hooks, cancel pending actions on pause/settings changes/quit, skip delivery on failure or foreground-app change, and preserve short clicks while a switch completes. Selection stability does not guarantee that a vendor's voice service is ready.
- Dispatch Windows output and lifecycle cleanup on the input-hook thread without waiting under the engine mutex; cancel queued work on shutdown and prevent old tasks replaying after restart.
- Cancel input preparation before the UI event loop starts shutting down, so queued macOS input-source callbacks cannot replay a shortcut during exit.
- Keep the selected input method active afterward without changing OS default-input preferences. Windows selection applies to the desktop session; macOS retains the existing Accessibility and Input Monitoring requirements.
- Keep preview non-interactive: no real shortcuts or input-method switches. Native signed auto-updates retain their seven-day default check interval and opt-in automatic download/install.

Read-only discovery found the installed WeChat and Doubao profiles on Windows ARM64 and in an x64-emulated test process; fake-backend tests cover switching/order/cancellation logic. Live IME switching, vendor voice activation, physical mouse behavior with the new option, and macOS input-method runtime behavior still require manual acceptance. Windows installers remain unsigned; macOS builds remain ad-hoc signed and unnotarized.

## 0.1.1 - 2026-10-19

- First public release of the native Windows and macOS app, with all features listed in 0.1.0.
- Verify GitHub's normalized installer filenames as well as local build filenames; fail safely if both exist.
- Publish SHA-256 checksums for the exact asset names users download.
- Keep signed native auto-updates on a seven-day default interval, with automatic downloads/install opt-in.

The 0.1.0 release remained an unpublished staging draft when the final asset-name check failed; no incomplete update was published. OS code-signing and macOS permission caveats below still apply.

## 0.1.0 - 2026-10-19

- Native Go / MyGo interface for Windows and macOS; no webview or JavaScript runtime.
- Map back and forward mouse buttons to a key, shortcut, or held modifier.
- Tray background operation, optional hidden tray, single-instance reopening, and user-level launch at login.
- Optional non-activating shortcut feedback in the bottom-right corner.
- MyGo's native updater checks every 7 days by default and verifies signed update archives before installation. Automatic downloads/install are opt-in.
- Automated tagged releases for Windows x64 / ARM64 and macOS Intel / Apple silicon / Universal, with update feeds for every package variant.

Windows builds are not code-signed. macOS builds use ad-hoc signing and are not Developer ID signed or notarized. OS security prompts and input permission requirements still apply. Build/test coverage is not a claim of real-mouse acceptance on every platform.
