# Native input-method query assets

`go run ./scripts/buildime` builds the three architecture-specific helper EXEs
and hook DLLs with a pinned LLVM-MinGW toolchain and writes `manifest.json`.
Generated binaries and their manifest are not tracked. They are embedded in
Windows application builds; release verification requires all six artifacts.

The helpers only query an existing target-thread TSF context. They never
initialize a target's COM/TSF services or activate an input method. Missing,
unsupported, protected, unresponsive, and changed targets fail closed.

The bridge and helper prefer `GetProcessInformation(ProcessMachineTypeInfo)`
to select an exact instruction set. Unlike x86, x64 emulation on ARM64 is not
WOW64, so `IsWow64Process2` alone cannot distinguish it from native ARM64.
Older systems may use the legacy query only when the newer information class
is unavailable; ambiguous ARM64 results are rejected rather than guessed.
