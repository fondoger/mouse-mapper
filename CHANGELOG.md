# Changelog

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
