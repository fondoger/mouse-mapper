# Changelog

## 0.1.0 - 2026-10-19

- Native Go / MyGo interface for Windows and macOS; no webview or JavaScript runtime.
- Map back and forward mouse buttons to a key, shortcut, or held modifier.
- Tray background operation, optional hidden tray, single-instance reopening, and user-level launch at login.
- Optional non-activating shortcut feedback in the bottom-right corner.
- MyGo's native updater checks every 7 days by default and verifies signed update archives before installation. Automatic downloads/install are opt-in.
- Automated tagged releases for Windows x64 / ARM64 and macOS Intel / Apple silicon / Universal, with update feeds for every package variant.

Windows builds are not code-signed. macOS builds use ad-hoc signing and are not Developer ID signed or notarized. OS security prompts and input permission requirements still apply. Build/test coverage is not a claim of real-mouse acceptance on every platform.
