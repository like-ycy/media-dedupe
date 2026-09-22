# media-dedupe

产品名：**文件去重助手**。本机离线去重工具：**Wails 桌面 App**。

扫描目录中的图片/视频/TXT，找出精确重复（SHA-256）、视觉近似重复（pHash / FFmpeg 抽帧多帧 pHash）与正文近似重复，生成可人工审阅的结果。**不会自动删除任何文件**；App 中删除默认移入系统回收站。

支持从 GitHub Releases 检查更新、下载并替换自身后重启。

## 构建

产品只构建桌面 App，不再发布 CLI 工具（仓库中仍保留 `cmd/media-dedupe` 与 `internal/cli` 便于本地调试）。

依赖：Go 1.27+（见 `go.mod`）。视频能力需要系统安装 `ffmpeg` / `ffprobe`。

### 本机构建（macOS）

```bash
# 安装/升级 Wails CLI（可选）
# go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 生产构建（本机架构；产出 build/bin/media-dedupe-app.app）
# 本地测试构建：版本默认为 0.0.0-dev，不会误报成正式版
wails build -o media-dedupe-app

# 若本机也要测“正式版本号”，从当前 Git tag 注入：
wails build -o media-dedupe-app \
  -ldflags "-X media-dedupe/internal/version.Version=$(git describe --tags --exact-match | sed 's/^v//')"

# 开发热重载
wails dev
```

版本号规则：

- `wails build` 本身不会读 Git tag 注入 Go 版本变量
- 正式发布由 GitHub Actions 在打 `v*` tag 后构建，并通过 `-ldflags` 注入版本
- 本地未注入时显示 `v0.0.0-dev`；需要模拟正式版时用上面的 `-ldflags` 命令

App 入口：
- 根目录 `main.go`（供 `wails build`）
- `cmd/app/main.go`（同逻辑入口）

前端源码位于 `frontend/src/`，使用 React + TypeScript + Tailwind CSS + shadcn/ui + Radix；构建产物输出到 `frontend/dist/`，由 `frontend` 包 embed。

```bash
cd frontend
npm ci
npm run build
```

浏览器预览（不连接 Wails，仅用于界面检查）：`npm run dev:mock`。

### 发布构建（GitHub Actions）

打 `v*` tag 后由 Actions 产出，并附带 `SHA256SUMS.txt`：

| 平台 | 架构 | 产物 |
|---|---|---|
| macOS | arm64 / amd64 | `media-dedupe-app_{ver}_darwin_{arch}.zip`（`.app`） |
| Windows | amd64 | `media-dedupe-app_{ver}_windows_amd64.zip`（单文件 exe，无安装包） |

Windows 为便携版：解压后双击 `media-dedupe-app.exe` 即可使用，不生成 NSIS 安装程序。

Release tag 须匹配 `vMAJOR.MINOR.PATCH`。

### 生成本地测试媒体

```bash
# 默认约 3000 个，输出到 ./testdata
go run ./scripts/generate_testdata.go
```

## 桌面 App 使用

1. 启动 App → 项目列表 →「新建项目」
2. 填写名称与扫描目录（可多目录；无效路径会标红）
3. 扫描配置：阈值、图片 pHash、视频 FFmpeg 抽帧、并发
4. 开始扫描 → 进度五阶段（发现 / SHA-256 / pHash / FFmpeg / 分组）
5. 重复结果 → 打开组详情 → 勾选副本 → **移入系统回收站**（需确认）
6. 可忽略组、标记已处理、导出 JSON/HTML/TXT 报告
7. 全局设置：默认删除策略、是否允许永久删除（默认关闭）、FFmpeg 路径
8. 关于与更新：启动后自动检查 GitHub Release；也可手动检查、下载（可选国内加速代理）并重启应用完成更新

### 安全约束

- 从不自动删除；删除必须用户确认
- 默认模式：系统回收站；永久删除需设置开关 + 弹窗勾选「无法恢复」
- 永久删除失败不会静默降级路径之外的逻辑；回收站失败返回明确错误
- 缩略图/帧条仅经本地 `127.0.0.1` mediastore 提供，不暴露任意磁盘路径给 WebView

### 自动更新

- 检查源：GitHub Releases（`like-ycy/media-dedupe`），按当前 OS/Arch 匹配 `media-dedupe-app_{ver}_{goos}_{goarch}.zip`
- 流程：检查 → 下载（进度回调，可选 `ghfast.top` 加速）→ 校验 → 替换自身并重启
- 版本比较基于 `internal/version`；本地未注入 tag 时为 `v0.0.0-dev`，会提示有新版本

### App 数据目录

| OS | 路径 |
|---|---|
| Windows | `%AppData%/media-dedupe` |
| macOS | `~/Library/Application Support/media-dedupe` |
| Linux | `$XDG_DATA_HOME/media-dedupe` 或 `~/.local/share/media-dedupe` |

每项目：`projects/{id}/cache.sqlite`、`thumbs/`、`frames/`、`reports/`

## 行为说明

- **精确重复**：同大小 → SHA-256 全量哈希一致
- **图片近似**：宽高分桶 + pHash 汉明距离 / 相似度阈值
- **视频近似**：时长/宽高预筛 + FFmpeg 多帧 pHash（默认 8 帧）
- **TXT 近似**：正文规范化 + 固定数量指纹候选 + Dice 相似度；支持 UTF-8、UTF-8 BOM、GB18030、UTF-16 BOM
- **增量扫描**：`size+mtime` 未变则复用缓存 hash/pHash/TXT 指纹
- **组状态**：pending / ignored / processed；基于成员路径集合 `stable_key` 在重扫后保留忽略状态
- **推荐保留**：分辨率/大小/时长/编码等启发式质量分，非完整 EXIF 调色评估
- **HEIC/HEIF**：仅精确去重，不做 pHash

## 项目结构

```
main.go               # Wails 桌面入口
cmd/
  app/                # App 入口（与根 main 等价）
  media-dedupe/       # 本地调试用 CLI 入口（不随 Release 发布）
frontend/
  dist/               # 内嵌前端（HTML/CSS/JS + 图标）
  frontend.go         # embed FS
internal/
  appapi/             # Wails 绑定面（唯一 API）
  appmain/            # 启动编排（窗口 + 本地 mediastore）
  project/            # 多项目 CRUD + 设置
  ops/                # 回收站/永久删除/打开路径
  mediastore/         # 缩略图/帧本地服务
  progress/           # 结构化进度事件
  pipeline/           # 扫描编排（context 取消 + 事件）
  cache/              # SQLite + 组状态/删除审计
  discovery/ hashfile/ imagehash/ video/
  textdedupe/         # TXT 正文近似
  candidate/ match/ score/ report/
  config/ model/ version/ fsutil/
  updater/            # GitHub Release 检查/下载/应用更新
  cli/                # 本地调试 CLI 实现（不随 Release 发布）
```

## 测试

```bash
go test ./...
go vet ./...
```

## 设计参考

UI 布局对齐 Stitch 设计稿（项目列表 / 配置 / 进度 / 结果 / 组详情 / 设置），文案使用：SHA-256、pHash、汉明距离、FFmpeg 抽帧、默认回收站。
