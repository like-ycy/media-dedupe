# 打包与目录契约

> 本文件在四个仓库（media-dedupe / video_cut / video_library / grabber）中各存一份，内容逐字相同。
> 改动请四仓一起改 —— 不同步的契约比没有契约更糟：它只会在某个仓库的 CI 里安静地过期。

## 1. 仓库骨架

根目录只放组织性目录，每种语言一个子树。两个模板，B 是 A 的超集：

```
<repo>/
├── go/                 # Go 模块根 = Wails 项目根（go.mod、wails.json 都在这层）
│   ├── main.go         # GUI 入口（wails build 要求 main 包在这层）
│   ├── cmd/            # 辅助命令：CLI 入口、manifestgen、数据生成器
│   ├── internal/       # 私有实现，按领域分包（各项目自定）
│   ├── frontend/       # 前端工程
│   ├── build/          # Wails 资源（appicon、darwin/、windows/）
│   ├── tests/          # 跨包集成测试（需要真实媒体样本的）
│   └── wails.json
├── python/             # 仅模板 B：Python 项目根
│   ├── pyproject.toml  uv.lock  .python-version
│   ├── src/<pkg>/
│   ├── tests/
│   ├── packaging/      # pyinstaller.spec + entry.py
│   └── build/pyi/      # PyInstaller work/dist（不提交）
├── tools/              # 构建与打包脚本（只放脚本，不放二进制、不放产物）
├── third_party/        # 仅模板 B：随包分发的第三方二进制 <name>/<os>-<arch>/
├── docs/
├── dist/               # 组装目录与归档（不提交）
└── .github/workflows/release.yml
```

`go/build/` 是**源文件**目录（图标、plist、manifest），只忽略 `go/build/bin/`。

## 2. tools/config.env

四个脚本共用一份配置，是唯一需要按仓库改动的文件。

| 键 | 含义 |
| --- | --- |
| `SLUG` | 归档名前缀，小写。`<SLUG>_<ver>_<os>_<arch>.zip` |
| `PRODUCT` | 分发目录名与 macOS `.app` 名（= `wails.json` 的 `name`） |
| `EXE` | 可执行文件名（= `wails.json` 的 `outputfilename`，不含 `.exe`） |
| `GO_MODULE` | Go 模块路径，用于 `-ldflags -X <GO_MODULE>/internal/version.Version`；留空表示该仓库没有 version 注入点 |
| `HAS_PYTHON` | `1` = 模板 B，`0` = 模板 A |
| `PY_PACKAGE` | Python 源码包名（`python/src/<PY_PACKAGE>`） |
| `PY_BUILD_NAME` | PyInstaller onedir 产出目录名（`python/build/pyi/dist/<PY_BUILD_NAME>`） |
| `PY_COMPONENT` | 上述产物在 `tools/` 下的组件目录名 |
| `COMPONENTS` | 需单独发布的组件（`<c>_<ver>_<os>_<arch>.zip`），空格分隔 |
| `BUNDLED` | 只随整包分发、不单独发布的组件（如上例的 ffprobe） |

## 3. 脚本入口

四个仓库的外部行为一致，参数语义一致：

```
tools/build.sh   [--full] [--no-python] [--package-only] [-v X.Y.Z]
tools/package.sh [--full] [--legacy-flat] [-v X.Y.Z]
```

Windows 用 `tools\build.ps1` / `tools\package.ps1`，参数 `-Full` / `-NoPython` / `-PackageOnly` / `-Version`。

`build.sh` 固定四步，顺序不可换：

1. Python 组件（模板 B）：`uv sync` + `pyinstaller packaging/pyinstaller.spec`
2. `tools/component.sh`（若存在）：跑产物取自述版本 → 生成组件 zip 到 `dist/release/`
3. `tools/prebuild.sh`（若存在）：编译前代码生成，典型是「把本平台组件清单烘进 App」
4. `wails build`

随后调用 `package.sh`：先打**标准包**（此时 `dist/<PRODUCT>/` 里只有 App），再拷组件，`--full` 时打**整包**。
所以一次 `--full` 构建同时产出标准包与整包 —— 标准包就是「拷组件之前的那棵树」，不需要额外临时目录。

`tools/component.sh` / `tools/prebuild.sh` 是各仓库自己的钩子（命名与清单生成跟产物咬得太紧，塞进公共脚本会让四份分叉）；`*.ps1` 一一对应。

## 4. 分发目录契约

Go 侧从可执行文件向上探测 `tools/<component>/<exe>`，所以这个布局是**运行时契约**，不是审美：

```
dist/<PRODUCT>/                 # macOS
├── <PRODUCT>.app/
└── tools/<component>/<exe>     # 目录型组件含 _internal/

dist/<PRODUCT>/                 # Windows
├── <EXE>.exe
└── tools/<component>/<exe>
```

- 组件一律进同名子目录，即使只有单个文件。
- `.app` 内部**不放** `tools/`：塞进 bundle 会让 codesign 把整棵树当嵌套代码校验。
- 标准包没有 `tools/`，整包才有；两侧都要断言。

## 5. 归档命名

```
<SLUG>_<version>_<os>_<arch>.zip          # 标准包
<SLUG>_<version>_full_<os>_<arch>.zip     # 整包（仅模板 B）
<component>_<version>_<os>_<arch>.zip     # 组件包（仅模板 B）
manifest.json                             # 组件清单（仅模板 B）
SHA256SUMS.txt
```

- `<os>` = **GOOS**（`darwin` / `windows`）。不要写 `macos`：更新器按 `runtime.GOOS` 拼后缀匹配资产，改名会让老版本的自更新静默失效。
- `<arch>` = GOARCH（`arm64` / `amd64`）。
- `<version>` 不带 `v`；`v` 只出现在 tag 与 Release 标题。
- 格式一律 `.zip`：macOS 用 `zip -qry -X`（`-y` 保符号链接），Windows 用 `Compress-Archive`。
- `--full` 构建同时产出标准包与整包（模板 B）；模板 A 只有一个归档，`--full` 等价于标准包。

归档内部：

```
# 模板 A：解压即可执行，不套目录
media-dedupe_0.8.3_darwin_arm64.zip
└── media-dedupe.app/
jabgrab… 之类 Windows 包
└── <EXE>.exe

# 模板 B：解压得到一个目录，标准包与整包只差 tools/
videolib_0.4.0_full_darwin_arm64.zip
└── VideoLib/{VideoLib.app,tools/scraper/}
```

## 6. CI 骨架

`release.yml` 只做四件事，构建逻辑一律不重复写在 YAML 里：

1. 解析版本（`git describe --tags --exact-match`，需 `fetch-depth: 0`）
2. 调 `tools/build.sh`（Windows 调 `tools\build.ps1`）
3. 断言产物（归档存在、顶层条目唯一、可执行文件在预期位置、组件真跑一次 `version`）
4. 传 artifact → release job 合并、生成 `SHA256SUMS.txt`、发布

## 7. 同步策略

仓库不合并、不共享 Go 模块（四仓独立发版）。共享的是：本文件与 `tools/{common.sh,config.env,build.*,package.*}`。
改这几个文件时四仓一起改；一眼能看出的差异只允许出现在 `config.env` 与 `tools/component.*`。
