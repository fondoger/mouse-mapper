# Manual acceptance checklist

The application and updater use MyGo Native UI, not a browser. Automated tests cover mapping state, settings, native UI interactions, rendering and signed release assets. Cross-compilation does **not** prove that a physical mouse, OS permissions, startup registration or application installation works on another machine.

Record the OS version, CPU architecture, mouse model, display scale, app version and tested cases. Do not report untested cases as passing. Complete required acceptance tests **before pushing a version tag**: tag workflows publish successful builds automatically.

## Safe setup

- Use a normal text editor and a disposable document or browser tab. Avoid passwords, payment flows, shells containing commands and other sensitive targets.
- Point `MOUSE_MAPPER_CONFIG_DIR` at a fresh test directory to isolate mapping, window and updater preferences. Leave launch-at-login off except during its explicit test; startup registrations belong to the OS, not the test directory.
- Do not disable system-wide protections, automatically elevate the app or bypass permission checks. Keep the main window visible until reopening and quitting are verified.
- Test the installed production package, not just `go tool mygo dev` or renderer snapshots.

## Core mapping

- [ ] Map back to a harmless key such as `F6`; the foreground application receives the mapped key.
- [ ] Map forward to an appropriate combination such as `Ctrl+Shift+P` or `Command+Shift+P`; all modifiers and the final key work.
- [ ] Holding the side button keeps the mapped keys down; releasing it releases them. Test a modifier-only mapping while dragging.
- [ ] A mapped click does not also cause browser back / forward. Disabled mappings retain the original side-button behavior.
- [ ] Rapid alternating clicks, both side buttons together, and physically held modifiers do not leave keys stuck or corrupt reference counts.
- [ ] Globally pause and resume; both original behavior and saved mappings return at the appropriate time.
- [ ] Change a mapping while a side button is held; it releases safely and does not activate the new shortcut mid-press.
- [ ] Restart; mapping settings and enable states persist. Invalid shortcuts do not replace valid saved values.
- [ ] Open the native shortcut editor; mapping is suspended. Recording uses the physical key position, Escape cancels, and manual selection handles keys that the window manager would consume.
- [ ] Successful save, cancellation and recording timeout all restore the mapper's previous enabled state.

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

- [ ] The optional shortcut overlay appears near the bottom-right of the usable desktop while another application stays focused.
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
- [ ] Verify macOS 13+ login item registration and approval in System Settings; macOS 12 uses a LaunchAgent.
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
go test ./...
go vet ./...
go build -tags mygo_noinspector ./...
go run ./scripts/releasecheck -tag v0.1.1 -repository fondoger/mouse-mapper
```

The last example must use the version currently in `mygo.json`. To render native snapshots, set `MOUSE_MAPPER_UI_SNAPSHOTS` to an output directory and run:

```sh
go test ./internal/desktop -run TestNativeSnapshots -count=1
```

For the opt-in Windows input integration test, set `MOUSE_MAPPER_NATIVE_TEST=1` and run `go test ./internal/input -run TestWindowsNativeHookAndSendInput -count=1`. Its test-owned keyboard hook consumes the tagged F24 events; it does not send unconsumed shortcuts or change the foreground window. The CI manual-dispatch option `native_input` is off by default and skips this smoke test on runners without an interactive desktop.

- [ ] `mygo.json`, `CHANGELOG.md`, the tag and installed application version agree.
- [ ] `MYGO_UPDATER_PRIVATE_KEY` is present in Actions secrets, matches the committed public key, and never appears in logs or artifacts.
- [ ] Windows and macOS jobs pass native tests and produce every intended package.
- [ ] Before publishing, the final workflow verifies all five manifests, their version/repository/target, each archive/delta size and Ed25519 signature, and each expected installer.
- [ ] Validate `SHA256SUMS.txt` against downloaded assets. Check that licenses ship inside the application resources.
- [ ] Confirm the successful workflow makes the complete release public automatically, while failed builds leave only an inaccessible-to-updaters draft.
- [ ] Document actual hardware coverage and known limitations. Signed update archives are not the same as OS-signed or notarized installers.
