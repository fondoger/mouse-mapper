# Mouse Mapper

**让侧键，更顺手。** 给两个小按键，一点新的可能。

使用 [MyGo Native UI](https://github.com/egoist/mygo) 构建的 Windows / macOS 鼠标侧键映射应用。界面、托盘、按键提示与更新窗口全部使用 Go 原生实现，不依赖浏览器、WebView 或 JavaScript 运行时。

[下载](https://github.com/fondoger/mouse-mapper/releases) · [English](#english) · [更新记录](CHANGELOG.md) · [测试清单](docs/testing.md)

![Mouse Mapper 原生主界面](docs/screenshots/mappings.png)

## 功能

- **分别映射前进、后退键。** 支持单个按键、组合键和仅修饰键；按住侧键即可保持按键，松开即释放。
- **即改即存。** 默认后退键复制、前进键粘贴；每个侧键可独立启停，也可一键暂停全部映射并恢复原始行为。
- **安静地常驻后台。** 关闭窗口后继续工作，可隐藏托盘；再次打开应用会找回已有主界面，而不是启动第二个映射进程。
- **随用户登录启动。** 系统级用户启动项，默认关闭；启用后登录时在后台运行。
- **按下，看见。** 可选的桌面右下角短暂按键提示，不抢焦点、不拦截点击，支持调整显示时长。
- **原生自动更新。** MyGo 内置更新器默认每 **7 天**检查一次；支持手动检查、查看说明、稍后提醒和跳过版本。自动下载并安装需用户主动开启。

![原生快捷键编辑器](docs/screenshots/shortcut-editor.png)

## 平台与安装

| 平台 | 安装包 | 输入实现与运行要求 |
| --- | --- | --- |
| Windows 10 / 11，x64 / ARM64 | `Mouse Mapper Setup … amd64.exe` / `… arm64.exe` | `WH_MOUSE_LL` + `SendInput`；不需要 WebView2。普通权限应用不能向高权限窗口或安全桌面注入按键。 |
| macOS 12+，Apple silicon / Intel | 通用 `.dmg`，另提供 `arm64` / `amd64` 包 | `CGEventTap` + `CGEventPost`；需要辅助功能与输入监控授权。 |

从 [Releases](https://github.com/fondoger/mouse-mapper/releases) 下载对应安装包：Windows 运行安装程序；macOS 打开 DMG 并将应用拖入 Applications，然后从 Applications 启动。首次在 macOS 使用时，在「系统设置 → 隐私与安全性」中的「辅助功能」和「输入监控」允许 Mouse Mapper，按应用提示重试或重新启动。

**签名说明：** 更新归档使用 Ed25519 签名，但这与操作系统代码签名是两回事。目前 Windows 安装程序未做代码签名，macOS 使用 ad-hoc 签名，未配置 Developer ID 或 Apple 公证。SmartScreen / Gatekeeper 可能拦截下载的构建；请核对来源与 `SHA256SUMS.txt`，不要全局关闭系统安全保护。macOS 可选择本机从源码构建，或等待已公证的发行版。macOS 更新后也可能需要重新确认辅助功能授权。

部分鼠标厂商软件、游戏、远程桌面或系统快捷键可能先截获输入。CI 覆盖四种系统 / CPU 编译目标、Windows / macOS 单元测试和原生界面渲染；**编译和自动化测试不等于真实鼠标、权限与安装流程均已实机验收**。请参阅[测试清单](docs/testing.md)。

## 日常使用

在「按键映射」点击「更改」，录入组合键或手动选择，再保存。默认复制 / 粘贴使用 Windows 的 `Ctrl` 或 macOS 的 `Command`。录入期间会临时暂停映射，退出编辑后恢复，避免侧键干扰录入。

关闭主窗口不会退出。彻底停止映射请使用应用或托盘菜单的「退出」。隐藏托盘后，从开始菜单、Applications 或应用启动器再次打开 Mouse Mapper 即可恢复主界面。

![偏好设置](docs/screenshots/preferences.png)

## 自动更新

- 使用 MyGo 官方 **`plugins/updater/native`**，包括更新提示、下载进度与重启窗口，整个流程都不需要 WebView。
- 默认开启后台检查，间隔 **7 天**。检查记录跨重启保存；应用运行时才会进行检查，也可随时手动检查。
- 默认不会静默下载安装；可在偏好设置或原生更新窗口中主动开启。更新安装完成后重启生效。
- 从本仓库的公开 GitHub Release 获取对应架构的更新清单，验证归档 SHA-256 的 **Ed25519 签名**后才允许安装。清单携带归档签名，并不是另行签名的 JSON 文件。
- 开发构建、不可写的安装位置不自动更新。Windows 推荐当前用户安装；macOS 应安装在用户有写权限的 Applications 目录，不要直接从 DMG 中运行。

## 本地开发

只需要 **Go 1.27.1+**。MyGo **v0.5.5** 已作为 Go tool 依赖固定，无需全局安装 CLI，无需前端构建链或 C 编译器。

```sh
git clone https://github.com/fondoger/mouse-mapper.git
cd mouse-mapper
go mod download
go tool mygo dev
```

```sh
# 单元测试、原生 UI 交互测试与静态检查
go test ./...
go vet ./...
go build -tags mygo_noinspector ./...

# 当前平台的生产安装包，输出到 build/
go tool mygo build

# Windows x64 / ARM64
go tool mygo build -platform windows/amd64,windows/arm64

# macOS 通用版和单架构版；DMG 打包需在 macOS 上运行
go tool mygo build -platform darwin/universal,darwin/arm64,darwin/amd64
```

MyGo 默认 `CGO_ENABLED=0`。Windows 打包会按需下载并校验 NSIS；首次下载 Go 模块与打包工具需要联网。普通本地构建不需要更新私钥，不提供私钥时 MyGo 会跳过更新归档，只生成应用和安装包。

界面截图由 MyGo 原生渲染测试直接生成，并非网页效果图。设置 `MOUSE_MAPPER_UI_SNAPSHOTS` 为输出目录后，运行 `go test ./internal/desktop -run TestNativeSnapshots -count=1` 可重新生成。更多验收步骤见[测试清单](docs/testing.md)。

## 设置与隐私

| 平台 | 设置目录 |
| --- | --- |
| Windows | `%APPDATA%\Mouse Mapper\` |
| macOS | `~/Library/Application Support/Mouse Mapper/` |

`settings.json` 保存映射和后台偏好，`updater.json` 保存更新偏好、跳过版本与最近检查时间。可通过 `MOUSE_MAPPER_CONFIG_DIR` 指定隔离目录，供测试或便携使用。设置经过校验和原子替换；损坏或更新版本的配置不会被静默覆盖。

应用处理鼠标侧键与物理按键的按下 / 释放状态，防止映射后修饰键卡住；**不记录键入内容，不上传按键数据，没有账户或遥测服务**。自动更新会通过 HTTPS 访问 GitHub 获取版本信息与更新文件，可在设置中关闭后台检查。

「开机自启」实际指当前用户登录时启动：Windows 使用用户 Run 项，macOS 13+ 使用系统登录项，macOS 12 使用 LaunchAgent；macOS 可能需要在系统设置中批准登录项。请在稳定的安装位置开启，移动或卸载应用前先关闭。自启动设置属于系统，不受测试配置目录隔离。

## 自动发布

推送匹配 `mygo.json` 版本的 **`vX.Y.Z` 标签会自动公开发布**，不需要人工批准草稿。务必在推送标签前完成测试和必要的实机验收。

1. 仓库管理员将现有更新私钥保存在 GitHub Actions Secret **`MYGO_UPDATER_PRIVATE_KEY`** 中，公钥保留在 `mygo.json` 的 `updates.publicKey`。私钥不要进入仓库、命令日志或构建产物，并另行安全备份。已经发布应用后，不要随意更换密钥。
2. 修改 `mygo.json` 的版本与 `CHANGELOG.md`，运行测试并提交，然后推送对应版本标签。
3. Actions 验证标签、仓库与密钥匹配，创建暂存草稿；在 Windows 和 macOS runner 上测试、打包，用 `go tool mygo build -upload` 上传安装包和签名更新文件。
4. 最终任务重新下载并验证全部安装包、五个更新通道、归档大小和 Ed25519 签名，生成 `SHA256SUMS.txt` 后**自动公开发布**。普通版本成为 latest；带预发布后缀的版本标记为 prerelease，不替换稳定版更新源。

提供 `windows-amd64`、`windows-arm64`、`darwin-amd64`、`darwin-arm64`、`darwin-universal` 五个更新清单，保证单架构与通用版都能继续更新。失败的流水线只留下未公开草稿；不会自动暴露残缺更新，也不会覆盖已经公开的发行版。尚无旧版更新归档时，不会生成增量更新文件。

## English

Mouse Mapper is a quiet **Windows and macOS** utility built entirely in Go with **MyGo Native UI**. Map the back and forward mouse buttons to a key, shortcut, or held modifier; run in the background, hide the tray icon, launch at login, and optionally show brief, non-interactive shortcut feedback. Reopening the app restores its existing window. There is no webview, JavaScript runtime, or frontend build chain.

**Install:** choose a Windows x64 / ARM64 installer or the universal macOS DMG from [Releases](https://github.com/fondoger/mouse-mapper/releases). macOS 12+ requires Accessibility and Input Monitoring permissions. Windows integrity boundaries and other mouse software can prevent injection. Windows installers are not code-signed; macOS builds are ad-hoc signed, not Developer ID signed or notarized. Update signing does not replace OS code signing. Never disable system-wide security protections to run the app.

**Updates:** the official `plugins/updater/native` plugin checks every **7 days by default**, preserves the last successful check across restarts, and supports manual checks. Automatic download/install is opt-in. Every update archive is verified against the embedded Ed25519 public key. Development builds and unwritable installations do not update themselves. Update checks contact GitHub over HTTPS; mouse mappings and typed content are never uploaded.

**Develop:** install Go 1.27.1+, run `go mod download`, then `go tool mygo dev`. Use `go test ./...`, `go vet ./...`, and `go tool mygo build`. MyGo v0.5.5 is pinned in `go.mod`; no C compiler is required. CI tests both native platforms and cross-compiles all four OS / CPU combinations. See the [manual acceptance checklist](docs/testing.md) for device, permission, startup, overlay and update checks.

**Publish:** pushing a matching `v*` version tag automatically builds, signs update archives, verifies the complete staged release, and makes it public. `MYGO_UPDATER_PRIVATE_KEY` must be configured as an Actions secret; do not rotate the public key after shipping without a migration plan. Stable tags become the latest update source; prereleases do not.

## Credits & License

[MIT](LICENSE) © 2026 fondoger.

Built with [MyGo](https://github.com/egoist/mygo), [purego](https://github.com/ebitengine/purego), [go-text](https://github.com/go-text/typesetting), and Go's `x/sys`, `x/image`, `x/text` packages. The embedded [Manrope](https://github.com/googlefonts/manrope) font is distributed under the **SIL Open Font License 1.1**; system fonts provide appropriate script fallback. Original third-party notices are included in [resources/licenses](resources/licenses) and every packaged application.
