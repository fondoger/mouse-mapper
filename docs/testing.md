# Manual acceptance checklist

The application and updater use MyGo Native UI, not a browser. Automated tests cover mapping state, input-method preparation with fake backends, settings, native UI interactions, rendering and signed release assets. Cross-compilation does **not** prove that a physical mouse, OS permissions, input-method switching, vendor voice activation, startup registration or application installation works on another machine.

Record the OS version, CPU architecture, mouse model, display scale, app version and tested cases. Do not report untested cases as passing. Complete required acceptance tests **before pushing a version tag**: tag workflows publish successful builds automatically.

## Safe setup

- Use a normal text editor and a disposable document or browser tab. Avoid passwords, payment flows, shells containing commands and other sensitive targets.
- Point `MOUSE_MAPPER_CONFIG_DIR` at a fresh test directory to isolate mapping, window and updater preferences. Leave launch-at-login off except during its explicit test; startup registrations belong to the OS, not the test directory.
- Do not disable system-wide protections, automatically elevate the app or bypass permission checks. Keep the main window visible until reopening and quitting are verified.
- Test the installed production package, not just `go tool mygo dev` or renderer snapshots.

## Core mapping

- [ ] Map back to a harmless key such as `F6`; the foreground application receives the mapped key.
- [ ] Map forward to an appropriate combination such as `Ctrl+Shift+P` or `Command+Shift+P`; all modifiers and the final key work.
- [ ] With long press disabled, mouse-down immediately presses the mapped keys; holding keeps them down and mouse-up releases them. Test a modifier-only mapping while dragging.
- [ ] A mapped click does not also cause browser back / forward. Disabled mappings retain the original side-button behavior.
- [ ] Mapped down/up events remain consumed after input-method capture/queue errors or denied/partial key injection; errors never fall back to navigation. Extra duplicate ups do not generate output or navigation, and pausing restores new original clicks.
- [ ] On Windows, mouse-software-generated XBUTTON events are mapped and consumed, including mixed hardware/injected down/up pairs. Separately emitted browser commands, keyboard shortcuts or raw-device input require checking the mouse software; do not claim the side-button hook blocks those distinct paths.
- [ ] Rapid alternating clicks, both side buttons together, and physically held modifiers do not leave keys stuck or corrupt reference counts.
- [ ] Globally pause and resume; both original behavior and saved mappings return at the appropriate time.
- [ ] Change a mapping while a side button is held; it releases safely and does not activate the new shortcut mid-press.
- [ ] Restart; mapping settings and enable states persist. Invalid shortcuts do not replace valid saved values.
- [ ] Open the native shortcut editor; mapping is suspended. Recording uses the physical key position, Escape cancels, and manual selection handles keys that the window manager would consume.
- [ ] Record only `Ctrl+Shift+Win` (or `Control+Shift+Command` on macOS), release the modifiers in all six orders, then save and reopen: the full chord remains. After all keys are up, record another modifier-only chord. Separate non-overlapping modifier presses must not accumulate. `Ctrl+C`, release C while holding Ctrl, then press V must become `Ctrl+V`; releasing modifiers or auto-repeat from a previously held key must not replace it.
- [ ] Successful save, cancellation and recording timeout all restore the mapper's previous enabled state.

## Optional long press

- [ ] Open either side-button editor and enable long press. Configure short Enter, long Escape using the manual picker, and a 500 ms threshold. Save/reopen/restart preserves both actions; Escape pressed during recording still cancels the editor.
- [ ] Before release, a press shorter than 500 ms sends nothing. Releasing it sends exactly one complete Enter tap and no Escape. The original side-button down/up must both remain consumed.
- [ ] Hold until the threshold: Escape fires once immediately without waiting for mouse-up. Continue holding for several seconds, then release: there is no repeat and no Enter. Repeat around the exact threshold boundary and with a busy event loop.
- [ ] Give Back and Forward different thresholds and independent actions. Overlapping presses and physically held/shared modifiers leave no stuck keys or duplicate actions.
- [ ] Configure different input methods for short and long actions. Only the decided action prepares its chosen input method; the short action must not switch an IME on initial mouse-down. Each action is a complete tap, including release while its IME preparation is pending.
- [ ] Change focus, pause, edit settings, cancel recording or quit while a timer/preparation is pending. No stale action should reach a newly focused target or replay after restart. An already consumed press stays consumed through release.
- [ ] Accept 100 and 5000 ms thresholds, reject blank/non-numeric/out-of-range values, and preserve saved settings on rejection or Cancel. Switching the editor between actions must not mix shortcut/IME drafts.
- [ ] Disable long press and save: ordinary immediate-on-down, hold-until-up mapping returns. Old settings with no long-press field remain unchanged.
- [ ] At the minimum window size, inspect both action tabs, manual key entry, input-method discovery errors and long names. Threshold input and Save/Cancel remain reachable without overlapping.

## Input method before shortcut

Use an installed, enabled input method and a disposable document. These are explicit manual checks: do not switch a user's IME or enable a microphone as an unattended test. Record the input-method product/version, exact system selection, vendor-configured hotkey and target application.

- [ ] Existing settings and new bindings default to no switching. Selecting the no-switch option keeps ordinary mapping behavior and the current input method.
- [ ] The editor lists only installed, enabled, selectable input methods. Refresh after an OS input-source change; WeType has a localized WeChat Input Method label on Windows, and other official names remain intact.
- [ ] Select different input methods for back and forward; save/restart and confirm each button retains its own exact target and shortcut.
- [ ] From a different input language, press the side button and confirm the selected input method is ready before the mapped key arrives.
- [ ] Also switch between two IMEs sharing one language, such as Doubao and WeChat. A Chinese language/HKL match must not be mistaken for the correct vendor profile.
- [ ] While the requested input method is already active, repeated presses send the shortcut with **zero selection/activation calls**, not merely an idempotent reselection. Fake-backend tests enforce this API boundary; observing an unchanged tray icon alone does not prove it.
- [ ] On Windows with per-application input preferences, distinguish the captured keyboard-focus thread's exact TSF profile from the mapper worker's profile, language/HKL and a cached prior selection. A same-HKL WeChat/Doubao mismatch must still switch to the configured target.
- [ ] With an unavailable TSF context, blocked target hook, unsupported target or query timeout, report an error and skip the action without guessing or activating. Ordinary mappings with no input-method selection should still work.
- [ ] On Windows ARM64, use accurate `ProcessMachineTypeInfo` results to distinguish native ARM64, x64-emulated and x86-emulated targets. Older Windows 10 ARM64 systems without that API must reject ambiguous legacy results instead of guessing an architecture; ordinary mappings remain available.
- [ ] For voice input, enable it in the vendor application, grant microphone permission deliberately, and map the exact hotkey shown in that product's settings. Confirm its voice UI starts only after selection; do not assume a universal voice shortcut.
- [ ] Quickly release the mouse button during switching: successful preparation delivers one complete tap, not a lost click or a stuck modifier. Also check holding, alternating buttons and physically held modifiers.
- [ ] Using a disposable unavailable selection or controlled backend failure, verify a missing/disabled IME or timeout produces an error and does not send the shortcut to the wrong input method.
- [ ] Change foreground applications while preparation is pending; the delayed shortcut is canceled rather than delivered to the new application. On Windows also change the focused control. macOS checks the foreground application, not window changes within the same app.
- [ ] Pause, edit the binding, begin recording or quit while switching is pending; pending shortcut delivery is canceled and previously held keys are released. An input-method change that already happened is not automatically rolled back.
- [ ] The chosen input method stays selected after completion, while OS default-input preferences remain unchanged. On Windows test both system-wide and per-application input preferences: exact TSF selection applies to the desktop session, and other windows may follow it.
- [ ] Preview shows feedback without selecting an input method, starting vendor voice input, or injecting any real keys.

## Background and startup

- [ ] Closing the main window leaves mapping active. The tray can show the window, pause/resume, check for updates and quit.
- [ ] Hide the tray, close the main window, then open the installed application again. Its existing window appears; there is no duplicate process or input hook.
- [ ] Repeated rapid launches still leave one instance. Show the tray again and verify the menu returns.
- [ ] Quit explicitly; all held keys are released, mappings stop, and no overlay remains.
- [ ] From a stable installation location, enable launch at login and verify its user-level registration in system settings.
- [ ] Log out and in; mapping starts in the background without opening the main window. Tray visibility follows the saved preference.
- [ ] Open the application normally after login; the main window appears. Disable autostart, repeat login, and confirm it no longer starts.
- [ ] Make the test settings directory read-only or deny startup registration; the app explains the failure and does not pretend the change succeeded.
- [ ] With the app stopped, put malformed JSON or a newer schema in the disposable settings file. It is preserved and mapping is safely paused. Restore a valid file to recover.

## Overlay and native UI

- [ ] The optional shortcut overlay appears near the bottom-right of the usable desktop on the pointer's display, not in the application's own corner, while another application stays focused.
- [ ] A common shortcut is approximately 112 x 38 DIP, with only one line of text. Long shortcuts grow horizontally without clipping or adding a large empty panel.
- [ ] On Windows, rounded corners show the underlying desktop with no solid black rectangular backing. Check the actual composed window, not only an offscreen UI snapshot, with both normal GPU rendering and `MYGO_GPU=0` software rendering.
- [ ] It never steals typing focus, intercepts mouse clicks, reserves a taskbar entry or sends real keys when previewed.
- [ ] Rapid presses refresh feedback predictably; it always disappears. Turning feedback off does not disable actual mapping.
- [ ] Check different durations, multiple displays, 100% and 150%+ scaling, and non-default taskbar / dock positions.
- [ ] Check light/dark desktop backgrounds, keyboard navigation, small windows and Chinese text fallback. No browser or WebView process is needed for the main view, editor, overlay or updater.

## Windows

- [ ] Verify installation, Start menu shortcut, uninstall entry and user settings retention on Windows 10/11.
- [ ] Test physical side buttons in ordinary editors and browsers with no elevation.
- [ ] Confirm elevated applications and the secure desktop are treated as limitations, not as working injection targets.
- [ ] Test Windows ARM64 on actual ARM64 hardware before claiming it is runtime-verified; x64 CI compilation is insufficient.
- [ ] Treat SmartScreen signing prompts separately from input function. Do not auto-elevate or globally disable SmartScreen.

## macOS

- [ ] Launch the installed `.app` without Accessibility / Input Monitoring permission; mapping stays inactive and the native UI explains how to grant the required permissions.
- [ ] Grant both permissions and retry or relaunch. Verify both physical buttons and the overlay without focus theft.
- [ ] Revoke permission during use; input fails safely. Restore permission and verify recovery.
- [ ] Verify login item registration and approval in System Settings on supported macOS 13+ installations.
- [ ] Test both Apple silicon and Intel hardware before marking both runtime-verified. Check that a universal package runs on each.
- [ ] Report Gatekeeper, code-signing and notarization status accurately. Recheck Accessibility authorization after replacing the app with an update.

## Built-in updater

- [ ] Preferences show automatic checks enabled by default, a seven-day interval, and automatic download/install disabled by default.
- [ ] Manual Check for Updates opens the official native updater window, with no webview. Up-to-date, offline and error states are useful.
- [ ] A successful check persists in `updater.json`; restarting does not cause an immediate redundant automatic check before seven days.
- [ ] Disabling background checks does not disable manual checking. Opting into automatic downloads is an explicit user choice.
- [ ] Development builds and unwritable installations explain that updates are unavailable and do not repeatedly request them in the background.
- [ ] Using a controlled prior signed build, publish a newer signed version and verify notes, progress, skip/remind options, install and relaunch. Back up disposable settings first.
- [ ] A modified update archive fails signature verification and does not replace the installed app. A missing or invalid update feed does not break mapping.
- [ ] Verify Windows x64 / ARM64 and macOS Intel / Apple silicon / universal packages fetch their own exact `update-<target>.json` feed.
- [ ] Confirm stable releases become the latest update source; prereleases do not replace it.

## Automated and release checks

```sh
# Before Windows builds/tests; supported hosts: Windows x64/ARM64 or Linux x64.
# Skip this step for macOS-only builds.
go run ./scripts/buildime
go run ./scripts/buildime -verify-only
go test ./...
go vet ./...
go build -tags mygo_noinspector ./...
go run ./scripts/releasecheck -tag v0.3.0 -repository fondoger/mouse-mapper
```

`buildime` downloads a pinned, SHA-256-verified LLVM-MinGW toolchain into the ignored workspace cache and compiles the six small no-CRT query assets. It does not execute the compiled helpers. Windows CI/release tests set `MOUSE_MAPPER_REQUIRE_NATIVE_PROBE_ASSETS=1` so missing or corrupt embedded assets fail the build; `-verify-only` additionally checks current C sources, PE architecture/exports/imports and hashes. Windows jobs also set `MOUSE_MAPPER_NATIVE_PROBE_TEST=1` to run the isolated, non-activating target-thread query fixture described below; this gate does not require the optional input-hook dispatch. macOS jobs do not download or run a C compiler. The application remains `CGO_ENABLED=0`.

The releasecheck example must use the version currently in `mygo.json`. To render native snapshots, set `MOUSE_MAPPER_UI_SNAPSHOTS` to an output directory and run:

```sh
go test ./internal/desktop -run '^TestNative(Snapshots|LongPressSnapshots)$' -count=1
```

For the opt-in Windows input integration test, set `MOUSE_MAPPER_NATIVE_TEST=1` and run `go test ./internal/input -run TestWindowsNativeHookAndSendInput -count=1`. It verifies that queued output executes on the actual input-hook thread. Its test-owned keyboard hook consumes the tagged F24 events; it does not send unconsumed shortcuts or change the foreground window. The CI manual-dispatch option `native_input` is off by default and skips this smoke test on runners without an interactive desktop.

For guarded Windows side-button suppression, set `MOUSE_MAPPER_NATIVE_MOUSE_TEST=1` and run:

```sh
go test ./internal/input -run '^TestWindowsNative(SideButtonsAreSuppressed|LongPressUsesHookThread)$' -v -count=1
```

An independent guard is installed before the test mapper. It consumes every privately marked test XBUTTON and tagged F23/F24 event, including events missed by a broken mapper; real user events remain untouched. No target window is opened or focused. The long-press cases use a Valid-only fake focus (no real focus/IME query) and verify that both buttons emit a short tap only on release, or one long tap on the actual hook thread before release, without repeats or an extra short action. Run these probes serially across processes because they share a private test marker; a named-object lease rejects concurrent mouse probes. This verifies the native hook/injected-event path, not a physical mouse or a vendor's separately emitted browser/keyboard command.

For the exact Windows target-thread query, first build the helpers, then set `MOUSE_MAPPER_NATIVE_PROBE_TEST=1` and run:

```sh
go test ./internal/inputmethod/winprobe -run TestWindowsNativeProbeOwnedWindow -v -count=1
```

The fixture uses only a disposable, hidden, non-activating test window. It never focuses that window, queries the user's real foreground application, selects a third-party input method, changes session/default input preferences or starts vendor voice input. Any controlled layout choice is restricted to an already-loaded built-in English layout on the fixture's own thread. Safety preconditions must hold or the fixture stops rather than weakening them. The production query module itself does not initialize TSF or call activation APIs. Restore test environment variables afterward. This fixture is not live WeChat/Doubao activation acceptance.

For **read-only** Windows IME discovery, set `MOUSE_MAPPER_IME_DISCOVERY_TEST=1` and run:

```sh
go test ./internal/inputmethod -run TestWindowsReadOnlyInputMethodDiscovery -v -count=1
```

This enumerates profiles/descriptions and reads current selection/layout metadata. It does not activate a profile, inject keys, move focus or change the registry. A successful run is **not** a live switch or voice-input test. Restore the environment variable after testing.

For actual Windows overlay composition, use an interactive desktop, set `MOUSE_MAPPER_NATIVE_OVERLAY_TEST=1`, and run:

```sh
go test ./internal/desktop -run TestNativeOverlayCompositionAndPlacement -v -count=1
```

Repeat with `MYGO_GPU=0`, then restore both environment variables. The test briefly shows only test-owned, non-activating windows; it never starts the input service. It checks real native bounds, composed corner/background pixels, focus and click-through behavior. The screenshot is cropped to its own opaque backdrop and overlay; set `MOUSE_MAPPER_OVERLAY_SNAPSHOT` for a chosen output path, otherwise it uses the test's temporary directory. Offscreen renderer snapshots alone cannot detect this Windows black-background regression.

### Recorded 0.3.0 coverage

- Windows ARM64 and an x64-emulated process on the same ARM64 host: full Go package tests, `go vet` and production-tag compilation pass. The full package suite also passes `go test -race ./...` in the x64-emulated process, including forced stack-growth/GC tests for Windows COM argument stability. Windows/macOS arm64/amd64 cross-builds and target-specific vet pass; MyGo's native-UI lifetime vet passes.
- Deterministic timer tests cover 499/500 ms decisions, late/early timer delivery, one-shot long actions before release, independent buttons, shared modifiers, focus loss, errors, queued IME work and lifecycle cancellation.
- Windows fake-backend tests assert **zero activation, enumeration, layout requests or worker-layout changes** when the exact target is already selected; they also cover different TIPs sharing an HKL, a manual change between triggers, query failures and focus/cancellation guards.
- Guarded real Windows hook tests pass on ARM64 and an x64-emulated process, including short-on-release and one-shot long-before-release on both side buttons. Every synthetic XBUTTON/F23/F24 remains inside the independent test sink; no browser navigation or vendor voice shortcut is intentionally delivered to a user window.
- Exact target-thread queries pass the full 3 x 3 bridge/target-process architecture matrix on Windows ARM64 (native ARM64, x64 emulation and x86 emulation). Each pair covers a target without an existing TSF manager, accurate reads of an existing built-in English profile, and an unresponsive target. No live foreground application or third-party input method is selected by these fixtures.
- Native UI tests cover independent short/long keys and IMEs, threshold editing/bounds, cancellation/failed-save isolation, modifier-only recording and manual Escape. Generated views are visually checked at the 880 x 640 minimum size, with pinned Save/Cancel.
- **Still requires manual acceptance:** physical-device long presses and vendor-generated navigation, live WeChat/Doubao switching/voice activation, protected applications, macOS runtime input permissions and actual Intel/Apple silicon hardware. Emulated x64 testing is not separate x64 hardware acceptance.

### Recorded 0.2.0 coverage

- Windows ARM64 and an x64-emulated process on Windows ARM64: read-only native discovery found the actual installed WeChat and Doubao TSF profiles; native structure/COM ABI unit tests pass.
- Fake-backend tests cover exact-source selection, readiness, failures, cancellation, focus changes, shortcut ordering and short-click preservation without changing the host's input method.
- Windows dispatcher regression tests cover unlocked waits, queue cancellation, session replacement, lifecycle cleanup and stale completion rejection without native key injection. The opt-in test-owned F24 sink also passes on Windows ARM64 with real hook-thread dispatch.
- Windows ARM64: the actual composed native overlay passes placement, rounded-corner, no-focus and click-through checks under GPU and software rendering.
- **Not yet manually accepted:** live IME switching and vendor voice activation, physical mouse behavior with pre-shortcut switching, and macOS input-method runtime behavior. Also retain all platform/hardware gaps listed above; emulated x64 testing is not a claim of testing separate x64 hardware.

### Release gate

- [ ] `mygo.json`, `CHANGELOG.md`, the tag and installed application version agree.
- [ ] `MYGO_UPDATER_PRIVATE_KEY` is present in Actions secrets, matches the committed public key, and never appears in logs or artifacts.
- [ ] Windows and macOS jobs pass native tests and produce every intended package.
- [ ] Windows builds contain current arm64/amd64/386 query executables and DLLs; all six artifact hashes, PE identities and current source hashes pass the pinned builder's verification gate. The 386 query helper supports legacy target applications, not a 32-bit app installer.
- [ ] Before publishing, the final workflow verifies all five manifests, their version/repository/target, each archive/delta size and Ed25519 signature, and each expected installer. For macOS it also checks the configured minimum OS, Info.plist and every Mach-O slice; a plist must not promise a system older than its binaries support.
- [ ] Validate `SHA256SUMS.txt` against downloaded assets. Check that licenses ship inside the application resources.
- [ ] Confirm the successful workflow makes the complete release public automatically, while failed builds leave only an inaccessible-to-updaters draft.
- [ ] Document actual hardware coverage and known limitations. Signed update archives are not the same as OS-signed or notarized installers.
