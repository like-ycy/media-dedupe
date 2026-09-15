# media-dedupe

本机离线媒体去重工具：**CLI** + **Wails 桌面 App**。

扫描目录中的图片/视频，找出精确重复（SHA-256）与视觉近似重复（pHash / FFmpeg 抽帧多帧 pHash），生成可人工审阅的结果。**不会自动删除任何文件**；App 中删除默认移入系统回收站。

## 构建

### CLI

```bash
go build -ldflags="-s -w" -o bin/media-dedupe ./cmd/media-dedupe
```

依赖：Go 1.22+。视频能力需要系统安装 `ffmpeg` / `ffprobe`。

### 桌面 App（Wails v2）

```bash
# 安装/升级 Wails CLI（可选）
# go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 生产构建（macOS 会产出 build/bin/media-dedupe.app）
wails build -o media-dedupe-app

# 开发热重载
wails dev
```

App 入口：
- 根目录 `main.go`（供 `wails build`）
- `cmd/app/main.go`（同逻辑入口）

前端静态资源：`frontend/dist/`（`index.html` + `app.css` + `app.js`），由 `frontend` 包 embed。

### Windows 交叉编译示例

```bash
wails build -platform windows/amd64 -o media-dedupe-app.exe
```

## CLI 使用

```bash
./bin/media-dedupe doctor
./bin/media-dedupe scan ~/Pictures ~/Movies
./bin/media-dedupe scan ./testdata/sample --format text --no-video
./bin/media-dedupe report --format html
./bin/media-dedupe cache info
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `--similarity` | 0.80 | 相似阈值（pHash / 视频） |
| `--workers` | 2 | 图片/hash 并发 |
| `--video-workers` | 1 | 抽帧并发 |
| `--frames` | 8 | 视频抽帧数 |
| `--format` | html | `text` \| `json` \| `html` |
| `--no-thumbs` | false | 跳过缩略图 |

## 桌面 App 使用

1. 启动 App → 项目列表 →「新建项目」
2. 填写名称与扫描目录（可多目录；无效路径会标红）
3. 扫描配置：阈值、图片 pHash、视频 FFmpeg 抽帧、并发
4. 开始扫描 → 进度五阶段（发现 / SHA-256 / pHash / FFmpeg / 分组）
5. 重复结果 → 打开组详情 → 勾选副本 → **移入系统回收站**（需确认）
6. 可忽略组、标记已处理、导出 JSON/HTML/TXT 报告
7. 全局设置：默认删除策略、是否允许永久删除（默认关闭）、FFmpeg 路径

### 安全约束

- 从不自动删除；删除必须用户确认
- 默认模式：系统回收站；永久删除需设置开关 + 弹窗勾选「无法恢复」
- 永久删除失败不会静默降级路径之外的逻辑；回收站失败返回明确错误
- 缩略图/帧条仅经本地 `127.0.0.1` mediastore 提供，不暴露任意磁盘路径给 WebView

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
- **增量扫描**：`size+mtime` 未变则复用缓存 hash/pHash
- **组状态**：pending / ignored / processed；基于成员路径集合 `stable_key` 在重扫后保留忽略状态
- **推荐保留**：分辨率/大小/时长/编码等启发式质量分，非完整 EXIF 调色评估
- **HEIC/HEIF**：仅精确去重，不做 pHash

## 项目结构

```
main.go               # Wails 桌面入口
cmd/
  media-dedupe/       # CLI 入口
  app/                # App 入口（与根 main 等价）
frontend/
  dist/               # 内嵌前端（HTML/CSS/JS）
  frontend.go         # embed FS
internal/
  appapi/             # Wails 绑定面（唯一 API）
  appmain/            # 启动编排
  project/            # 多项目 CRUD + 设置
  ops/                # 回收站/永久删除/打开路径
  mediastore/         # 缩略图/帧本地服务
  progress/           # 结构化进度事件
  pipeline/           # 扫描编排（context 取消 + 事件）
  cache/              # SQLite + 组状态/删除审计
  discovery/ hashfile/ imagehash/ video/
  candidate/ match/ score/ report/ cli/ config/ model/
```

## 测试

```bash
go test ./...
go vet ./...
```

## 设计参考

UI 布局对齐 Stitch 设计稿（项目列表 / 配置 / 进度 / 结果 / 组详情 / 设置），文案使用：SHA-256、pHash、汉明距离、FFmpeg 抽帧、默认回收站。
