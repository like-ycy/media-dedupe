# media-dedupe APP 改造方案（Wails）

> **读者**：新会话里的实现 Agent（或人类工程师）。  
> **目标**：在 **不重写去重引擎** 的前提下，把 CLI 工具改造成 Windows 优先的桌面 App（Wails v2）。  
> **设计稿**：以 `/Users/wang/Downloads/stitch_media_deduplication_desktop_app/` 的 HTML 为 UI 基线（已评审通过）。  
> **约束**：本机运行；默认删除进系统回收站；**从不自动删除**。

---

## 0. 一页结论

| 项 | 决定 |
|---|---|
| 引擎 | **复用** `internal/{discovery,hashfile,imagehash,video,candidate,match,score,pipeline,cache}` |
| CLI | **保留** `cmd/media-dedupe` 作调试入口；产品入口改为 App |
| UI | Wails v2 + 前端（建议 Vite + 原生 TS/JS，或你熟悉的轻量栈）；视觉照 Stitch |
| 新能力 | 多项目、结构化进度、组状态（忽略/已处理）、回收站/永久删除、目录持久化、缩略图与帧条可读服务 |
| 不做 | 断点续扫、像素差对比、并排同步播放、NVDEC、云同步、自动清空 |

---

## 1. 现状盘点（改造起点）

### 1.1 已有且应复用

| 包 | 职责 | App 用法 |
|---|---|---|
| `internal/discovery` | 遍历目录、分类图/视频 | 扫描第一阶段 |
| `internal/hashfile` | SHA-256、pHash 汉明距离/相似度 | 精确 + 近似度量 |
| `internal/imagehash` | 解码、pHash、缩略图 | 图片特征 + 缩略图 |
| `internal/video` | ffprobe/ffmpeg、抽帧、帧 pHash、封面 | 视频特征 + 帧条素材 |
| `internal/candidate` | 分桶候选、阈值→最大汉明距离 | 近似预筛 |
| `internal/match` | 连通分量、视频元数据候选 | 分组 |
| `internal/score` | 质量分、keep/cleanup/review | 推荐保留 |
| `internal/pipeline` | 编排整次扫描 | App 后台扫描核心 |
| `internal/cache` | SQLite 特征与报告缓存 | 每项目库或全局库扩展 |
| `internal/report` | text/json/html 报告 | 导出功能复用 JSON/HTML |

### 1.2 CLI 行为（产品上要被 App 替代的）

- `scan [paths...]`：扫目录 → 打报告文件；**不删文件**
- `report`：从 cache 重渲上次结果
- `cache info|clear`
- `doctor`：ffmpeg/缓存目录检查

### 1.3 关键缺口（必须新做）

| 缺口 | 说明 |
|---|---|
| 多项目 | 当前单一 `cache.sqlite`；无项目 CRUD |
| 结构化进度 | `OnProgress func(string)` 仅字符串 |
| 组处理状态 | 组只有扫描结果；无「已忽略/已删除/已处理」 |
| 删除 | 无回收站/永久删除；无删除记录 |
| 目录列表配置 | 只有当次 CLI args |
| 前端 + Wails 壳 | 完全没有 |
| 读本地文件给 WebView | 缩略图/帧需 `asset server` 或 base64/自定义协议 |
| 取消扫描 | pipeline 无 context 取消 |
| pHash 距离展示 | 有 `Similarity`（0..1）与 `HammingDistance`；UI 要映射成「汉明距离 D」 |

### 1.4 引擎事实（UI 禁止说错）

- 精确重复：**SHA-256**
- 图片近似：**pHash** + 汉明距离 / 相似度阈值（默认 `0.80`）
- 视频近似：**多帧 pHash** + 时长/宽高预筛
- 推荐保留：`score`（分辨率/大小/时长/编码等启发式），**不是**完整 EXIF 调色评估
- HEIC/HEIF：**仅精确**，不做 pHash（`config.PHashSkippedExtensions`）
- 缓存：`size+mtime` 未变则复用 hash/pHash

---

## 2. 目标架构

```text
┌─────────────────────────────────────────────────────────┐
│  Wails App (cmd/app)                                     │
│  frontend/  (Stitch 设计 → HTML/CSS/TS)                  │
│    项目列表 / 配置 / 进度 / 结果 / 组详情 / 删除 / 设置     │
└───────────────┬─────────────────────────────────────────┘
                │ Wails bindings + Events
┌───────────────▼─────────────────────────────────────────┐
│  internal/appapi     对 Web 的 Go API（唯一绑定面）        │
│  internal/project    多项目 CRUD、配置、路径               │
│  internal/ops        删除到回收站 / 永久删除 / 打开文件夹   │
│  internal/progress   结构化进度事件                        │
│  internal/mediastore 缩略图/帧的 file:// 或 app:// 服务    │
└───────────────┬─────────────────────────────────────────┘
                │
┌───────────────▼─────────────────────────────────────────┐
│  既有引擎 internal/pipeline + cache + …（尽量少改）        │
│  每个项目：data dir 下独立 cache.sqlite + thumbs/ + frames/│
└─────────────────────────────────────────────────────────┘
```

### 2.1 推荐目录树（新增/调整）

```text
cmd/
  media-dedupe/          # 保留 CLI
  app/                   # 新：Wails 入口 main.go
internal/
  appapi/                # 新：绑定给前端的方法 + DTO
  project/               # 新：项目模型与存储
  ops/                   # 新：删除/回收站/打开路径
  progress/              # 新：进度事件结构
  mediastore/            # 新：媒体静态资源服务
  pipeline/              # 改：context 取消 + 事件回调
  cache/                 # 改：组状态、可选 project 维度
  config/                # 改：App 数据根目录（Windows 友好）
  cli/                   # 微调：继续可用
frontend/                # 新：Vite + TS（或 Vue/React，二选一）
  src/ ...
  index.html
docs/
  app-implementation-plan.md   # 本文
```

**技术选型建议（若未指定则按此）**：

- Go **1.22+**（当前 go.mod 为 1.27.1，保持）
- **Wails v2**（`github.com/wailsapp/wails/v2`）
- 前端：**Vite + TypeScript + 原生组件**（少依赖，贴近 Stitch HTML）；不用重型 UI 框架也可
- 状态：前端简单 store（或 pinia，若用 Vue）
- Windows 打包：`wails build -platform windows/amd64`

---

## 3. 数据模型

### 3.1 应用级配置（全局）

文件：`{AppData}/media-dedupe/settings.json`（Windows 示例）  
`config` 包提供路径助手，按 `GOOS` 选根目录。

```json
{
  "defaultThreshold": 0.8,
  "defaultIncludeVideos": true,
  "defaultFrameCount": 8,
  "defaultWorkers": 2,
  "defaultVideoWorkers": 1,
  "defaultDeleteMode": "recycle",   // recycle | permanent
  "allowPermanentDelete": false,
  "ffmpegPath": "",                 // 空 = PATH
  "ffprobePath": ""
}
```

### 3.2 项目（新）

文件：`{AppData}/media-dedupe/projects.json` **或** 全局 `app.sqlite` 表 `projects`（二选一，推荐 sqlite）。

```json
{
  "id": "prj_xxx",
  "name": "2024 家庭照片",
  "createdAt": "…",
  "updatedAt": "…",
  "paths": ["D:\\Photos\\2024"],
  "recursive": true,
  "threshold": 0.8,
  "includeImages": true,
  "includeVideos": true,
  "frameCount": 8,
  "workers": 2,
  "videoWorkers": 1,
  "enableThumbs": true,
  "lastScanAt": null,
  "lastSummary": {
    "filesSeen": 0,
    "groups": 0,
    "reclaimableBytes": 0,
    "exactGroups": 0,
    "similarGroups": 0
  }
}
```

每项目数据目录：

```text
{AppData}/media-dedupe/projects/{id}/
  cache.sqlite      # 复用现有 schema + 迁移
  thumbs/           # fileID.jpg
  frames/           # 可选：帧条图 fileID_frameN.jpg
```

### 3.3 缓存 schema 增量（在现有 cache 上 ALTER/新增表）

**必须新增：**

```sql
-- 组级处理状态（扫描后可更新；rescan 时策略见下）
ALTER TABLE duplicate_groups ADD COLUMN status TEXT NOT NULL DEFAULT 'pending';
-- pending | ignored | processed
ALTER TABLE duplicate_groups ADD COLUMN ignored_at TEXT;
ALTER TABLE duplicate_groups ADD COLUMN processed_at TEXT;

-- 可选：删除审计（简化版，非 AES）
CREATE TABLE IF NOT EXISTS delete_ops (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at TEXT NOT NULL,
  mode TEXT NOT NULL,              -- recycle | permanent
  group_id INTEGER,
  file_id INTEGER,
  path TEXT NOT NULL,
  ok INTEGER NOT NULL,
  message TEXT
);

-- 可选：全局最近目录（也可放 settings.json）
CREATE TABLE IF NOT EXISTS recent_dirs (
  path TEXT PRIMARY KEY,
  last_used_at TEXT NOT NULL
);
```

**组 ID 稳定性（重要）**：  
现 pipeline 每次扫描按顺序重编 `GroupID`。App 要在 **同一项目** 内让「忽略/已处理」在 **文件集合未变** 时仍有效，建议：

1. **扫描结束后**按「组内成员 file path 排序后的 hash」生成 `stable_key`  
2. 状态表用 `stable_key` 或 `(scan_run_id 外的 content key)`，不要只依赖自增 ID  
3. 或：全量重扫后，用成员路径集合匹配迁移 status；匹配失败则重置为 pending  

实现取一种即可，文档要求：**用户忽略一组后，除非该组文件变化，重新扫描不应静默丢掉忽略状态。**

### 3.4 模型扩展（`internal/model`）

```go
type GroupStatus string
const (
  GroupPending   GroupStatus = "pending"
  GroupIgnored   GroupStatus = "ignored"
  GroupProcessed GroupStatus = "processed"
)

// ReportGroup 增加：
//   Status GroupStatus
//   StableKey string
// ReportItem 增加（展示用，可从 cache 装载）：
//   Width, Height int
//   DurationMs int64
//   MediaType MediaType
//   MediaHashHex string  // 主 pHash 或空
```

---

## 4. Pipeline 改造（最小侵入）

### 4.1 `pipeline.Options` 扩展

```go
type Options struct {
  // …既有字段…
  Ctx          context.Context
  OnEvent      func(ev progress.Event)  // 结构化；OnProgress 可保留兼容 CLI
  FrameDir     string                   // 视频帧缩略图输出目录（可空=不存帧图）
  CancelCheck  func() bool              // 或直接用 Ctx
}
```

### 4.2 取消

- 全程检查 `opts.Ctx.Done()`：在 discover 循环、exact 哈希 worker、image/video worker 批次之间
- CLI 不传 Ctx 时用 `context.Background()`
- 取消后：已写入 cache 的特征 **保留**；不 Replace 最终 groups 或 Replace 已完成部分均可，但 UI 要显示「已取消，缓存已保留」

### 4.3 进度事件（`internal/progress`）

```go
type Stage string
const (
  StageDiscover Stage = "discover"
  StageExact    Stage = "exact"
  StageImage    Stage = "image"
  StageVideo    Stage = "video"
  StageMatch    Stage = "match"   // 可并入各 similarity 段末尾
  StageThumb    Stage = "thumb"
  StageDone     Stage = "done"
  StageCanceled Stage = "canceled"
)

type Event struct {
  Stage     Stage   `json:"stage"`
  Phase     string  `json:"phase"`           // running|done|skipped|error
  Message   string  `json:"message"`
  Current   int     `json:"current"`
  Total     int     `json:"total"`
  Percent   float64 `json:"percent"`         // 0-100 全局近似
  CurrentFile string `json:"currentFile"`
  FoundExact  int   `json:"foundExact"`
  FoundSimilar int  `json:"foundSimilar"`
  FilesSeen  int    `json:"filesSeen"`
  Errors     int    `json:"errors"`
}
```

Wails 侧：`runtime.EventsEmit(ctx, "scan:progress", ev)`。

**阶段与设计稿对齐**：

1. 发现文件 → `discover`  
2. 精确哈希 SHA-256 → `exact`  
3. 图片感知哈希 pHash → `image`  
4. 视频抽帧 → `video`（关视频或无 ffmpeg → `skipped`）  
5. 分组可嵌在 image/video 尾或单独 `match`  
6. 缩略图 → `thumb`

### 4.4 帧条素材

设计要求视频组显示 8 帧小图。现 `ComputeFrameHashes` 只在 tmp 删除。改造：

- 计算时同时把帧写到 `opts.FrameDir/{fileID}_{i}.jpg`（或在 `ExtractFrame` 后复制）
- 路径写入 cache 新表或 `perceptual_hashes` 旁路表 `frame_previews`
- UI 组详情通过 mediastore 读

封面：已有 `video.ExtractThumbFrame` / `imgutil.ThumbnailFromPath` → `thumbs/{fileID}.jpg`。

---

## 5. 删除与安全（`internal/ops`）

### 5.1 模式

| 模式 | 行为 |
|---|---|
| `recycle`（默认） | 移入系统回收站 |
| `permanent` | `os.Remove`；仅当 settings `allowPermanentDelete` 且用户确认 |

### 5.2 回收站实现

- **Windows**：调用 shell（建议 `github.com/hymkor/golang-i18n/...` 不用；用成熟的 `github.com/…` 中  
  **推荐**：`Exec` PowerShell  
  `Microsoft.VisualBasic.FileIO.FileSystem::DeleteFile`  
  或 COM `SHFileOperation` / `IFileOperation`。  
  实现阶段选一个可测方案；失败时返回明确错误，**禁止静默改 permanent**。
- **macOS**：`osascript` → Finder trash，或 `github.com/…` 库；开发机 mac 上要能跑通回收站。
- Linux：可暂时 permanent + 警告，或 XDG trash（MVP 可降级）。

### 5.3 API

```go
type DeleteRequest struct {
  Mode string   // recycle|permanent
  Paths []string
  GroupIDs []int64 // 可选审计
}
type DeleteResult struct {
  Succeeded []string
  Failed    []FailedItem // Path, Message
}
```

规则：

1. 拒绝删除：不存在、在保护路径前缀（可选：`C:\Windows` 等，MVP 可后置）  
2. 组内必须仍保留至少一个文件不被本批删除（前端保证 + 后端可二次校验）  
3. 成功后：对相关组 `status=processed`，写 `delete_ops`  
4. 失败（如占用）：返回 EBUSY 等 message；部分成功要如实返回  

**禁止**：未勾选文件自动删除；绕过确认弹窗。

---

## 6. Wails 绑定面（`internal/appapi`）

所有前端只调这里；**不要**把 pipeline 内部结构直接暴露。

### 6.1 DTO 原则

- JSON 字段 `snake_case` 或与前端约定一致（建议 **camelCase** 对齐常见前端）
- 路径保持绝对路径原样
- 相似度：同时给 `similarity`（0..1）与 `hammingDistance`（int，若可算）

```go
// 近似展示映射
// 64-bit pHash: distance = round((1-sim)*64)
// 或直接 hashfile.HammingDistance(hexA, hexB)
```

### 6.2 方法清单（实现时全做）

| 方法 | 说明 |
|---|---|
| `AppReady() AppInfo` | 版本、ffmpeg/ffprobe、累计释放（可选） |
| `ListProjects() []ProjectDTO` | 项目列表 + 摘要 |
| `CreateProject(input) ProjectDTO` | 新建 |
| `UpdateProject(id, patch) ProjectDTO` | 改名/路径/参数 |
| `DeleteProject(id) error` | 删项目数据目录；**不删媒体** |
| `GetProject(id) ProjectDTO` | 详情 |
| `CheckPaths(paths []string) []PathStatus` | 存在性/是否目录 |
| `StartScan(projectID string) error` | 后台 goroutine 扫描 |
| `CancelScan(projectID string) error` | 取消 |
| `GetScanStatus(projectID string) ScanStatusDTO` | 当前阶段/是否运行 |
| `ListGroups(projectID, filter) []GroupListDTO` | 列表页 |
| `GetGroup(projectID, groupID) GroupDetailDTO` | 组详情（含 members） |
| `SetGroupStatus(projectID, groupID, status) error` | ignore/unignore |
| `SetRecommended(projectID, groupID, fileID) error` | 反转推荐保留（写回 items Action） |
| `DeleteFiles(req DeleteRequest) DeleteResultDTO` | 删除 |
| `ExportReport(projectID, format, scope) string` | 返回写出的文件路径；复用 report |
| `GetSettings() SettingsDTO` / `SaveSettings(SettingsDTO)` | 全局设置 |
| `OpenPath(path string) error` | 系统默认程序打开 |
| `RevealInFolder(path string) error` | 资源管理器定位 |
| `CopyToClipboard(text string) error` | 复制路径 |
| `ListRecentDirs() []string` | 最近目录 |
| `GetThumbURL(fileID int64) string` | mediastore URL |
| `GetFrameURLs(fileID int64) []string` | 视频帧条 URL |

### 6.3 事件

| 事件名 | payload |
|---|---|
| `scan:progress` | `progress.Event` |
| `scan:finished` | `{projectId, groups, filesSeen, errors, durationMs}` |
| `scan:canceled` | `{projectId}` |
| `scan:error` | `{projectId, message}` |
| `files:deleted` | `{paths, mode}` |

### 6.4 并发

- 每项目最多一个活跃 scan；全局可限 1 个（MVP 更简单）
- `appapi` 持有 `map[projectID]*scanState{cancel, running}`

---

## 7. 前端（对照 Stitch）

### 7.1 设计稿映射（以内容为准，目录编号可能乱）

| 页面 | 参考 HTML（desktop_app） | 路由/视图 |
|---|---|---|
| 项目列表 | `*_项目列表`（有「打开项目」） | `/projects` |
| 扫描配置 | 「扫描配置」 | `/projects/:id/config` |
| 扫描进度 | 「扫描执行中」 | `/projects/:id/progress` |
| 完成摘要 | 「扫描完成，发现 N」 | `/projects/:id/summary`（或并入 results 顶栏） |
| 结果列表 | 「重复结果列表」 | `/projects/:id/results` |
| 图片组详情 | 「视觉近似照片组」/推荐保留 | `/projects/:id/groups/:gid` |
| 视频组详情 | 「视觉近似视频组」+ 抽帧 | 同路由，按 type 分支 |
| 删除确认 | 「确认删除选中的重复媒体文件？」 | Modal |
| 导出 | 「导出去重清单」 | Modal 或 `/projects/:id/export` |
| 设置 | 「全局设置」 | `/settings` |
| 空态 | 「空态与异常」四态 | 各视图 empty state |

### 7.2 导航

固定左侧：项目列表 / 扫描配置 / 扫描进度 / 重复结果 / 全局设置。  
窗口：**Windows 右上角**三键（Wails 用系统标题栏或自绘均可；开发在 mac 上用系统栏即可）。

### 7.3 UI 文案硬约束

- 算法：**SHA-256、pHash、汉明距离、FFmpeg 抽帧**
- 删除默认：**移入系统回收站**
- 永久删除：设置开启 + 弹窗勾选「无法恢复」
- 禁止：MD5、xxHash、dHash、NVDEC、断点续扫、扇区擦除、假相似度百分比（除非明确公式）

### 7.4 组详情数据

成员行需展示：

- 缩略图 URL、路径、大小、修改时间、分辨率/时长  
- 推荐保留徽章（`recommended_file_id`）  
- 已勾选待删  
- 近似成员：`hammingDistance`（相对推荐保留）  
- 视频：封面 + `GetFrameURLs`

### 7.5 本地媒体加载

Wails：

```go
// main.go
assetServer: wails.Options{
  AssetServer: wails.AssetServerOptions{
    Assets: assets, // 若内嵌
  },
}
// 自定义：
// runtime URL 仅允许项目 thumbs/frames 白名单目录，防路径穿越
```

**禁止**用 `file://` 随意读用户全盘；只暴露应用 cache 下 thumbs/frames。  
大图预览可先用大缩略图；「系统看图」走 `OpenPath(原始文件)`。

---

## 8. 实现阶段（新会话按此顺序做）

### Phase 0 — 工程骨架（0.5–1 天）

1. `go get github.com/wailsapp/wails/v2`  
2. `cmd/app/main.go` + 最小窗口 + 空白页  
3. `frontend/` Vite TS；`wails dev` 能跑  
4. `internal/config`：增加 App 数据根目录（Windows `AppData`，mac `~/Library/Application Support` 或 Caches）  
5. 不碰引擎逻辑  

**验收**：`wails dev` 打开空白壳；`go build ./...` 通过。

### Phase 1 — 项目与设置（1 天）

1. `internal/project`：CRUD + 磁盘持久化  
2. `internal/appapi`：`ListProjects/Create/Update/Delete/Get`、`GetSettings/SaveSettings`  
3. 前端：项目列表页（对照设计 `_2`）+ 新建/空态  
4. 最近目录  

**验收**：能新建项目、改路径列表、重启仍在。

### Phase 2 — 扫描与进度（1.5–2 天）

1. `pipeline`：`Ctx` + `OnEvent`；CLI 兼容  
2. 帧图落盘（若时间紧可 Phase 2 先只做图片，视频帧条 Phase 3）  
3. `StartScan/CancelScan/GetScanStatus` + Events  
4. 前端：配置页、进度页（阶段条、当前文件、取消）  
5. ffmpeg 检测：`video.Available()` + 可选自定义路径（设置页）  

**验收**：扫真实目录，进度更新；取消后 cache 保留；无 ffmpeg 时视频阶段 skipped。

### Phase 3 — 结果与组详情（2 天）

1. `ListGroups/GetGroup`（从 `LoadReportGroups` + 补 metadata）  
2. `SetGroupStatus`、`SetRecommended`（更新 DB actions 与 recommended_file_id）  
3. 缩略图 URL 服务  
4. 前端：结果列表筛选/排序/批量勾选；图片组详情；视频组详情+帧条  
5. 完成摘要可简化进结果页顶栏  

**验收**：设计核心闭环走通：列表 → 组详情 → 勾选。

### Phase 4 — 删除与导出（1 天）

1. `internal/ops` 回收站 + permanent  
2. `DeleteFiles` + 强确认（前端 modal 对照设计）  
3. 组 `processed` 状态  
4. `ExportReport`：JSON/HTML/TXT（复用 `report`）  

**验收**：回收站可还原（至少 Windows/mac 实测）；失败有错误列表。

### Phase 5 — 设置/空态/打磨（0.5–1 天）

1. 设置页全文案与 ffmpeg 警告  
2. 空态：无项目、无重复、目录失效、无 ffmpeg  
3. Windows 打包脚本、图标  
4. README 增加 App 使用说明  

**验收**：`docs` §自检 + 主路径手测清单全过。

---

## 9. 对既有代码的具体修改清单

| 文件 | 改动 | 侵入性 |
|---|---|---|
| `internal/pipeline/pipeline.go` | Ctx、事件、FrameDir、取消检查 | 中；保持 CLI 默认行为 |
| `internal/model/model.go` | GroupStatus、StableKey、展示字段 | 小 |
| `internal/cache/cache.go` | status/stable_key/delete_ops/recent_dirs；Load 时带 status | 中；注意迁移 |
| `internal/config/config.go` | AppData 根、项目子目录 | 小 |
| `internal/video/video.go` | 可选：导出帧预览到指定目录（或 pipeline 内调用 ExtractFrame） | 小 |
| `internal/cli/cli.go` | 可选：Ctx 透传；行为不变 | 小 |
| `internal/report/*` | 可选：导出 JSON 增加 stable_key/status | 小 |
| **新包** appapi/project/ops/progress/mediastore | 全新 | — |
| **新** cmd/app, frontend/ | 全新 | — |

**不要**在第一版重写 discovery/hash/match/score；有 bug 再修。

---

## 10. 主路径用户流（实现验收剧本）

1. 启动 App → 项目列表空态 →「新建项目」  
2. 输入名称，添加 2 个目录（含 1 个失效路径演示红条）  
3. 设置：阈值 0.8、开视频、抽帧 8  
4. 开始扫描 → 进度五阶段 → 完成  
5. 进入结果列表 → 筛选近似 / 精确  
6. 打开一组图片 → 看推荐保留与汉明距离 → 勾选 2 个副本  
7. 移入回收站 → 确认弹窗 → 成功 toast；组变为已处理  
8. 打开视频组 → 帧条 → 只勾选转码副本 → 删除  
9. 忽略一组 → 重新扫描 → 该组忽略仍在（若稳定键逻辑正确）  
10. 导出 JSON 到桌面  
11. 设置里关永久删除默认；开 ffmpeg 路径检测  

---

## 11. 测试与质量

### 11.1 自动化（引擎回归）

```bash
go test ./...
go vet ./...
# CLI 冒烟
go build -o bin/media-dedupe ./cmd/media-dedupe
./bin/media-dedupe doctor
./bin/media-dedupe scan testdata --format text --no-video
```

### 11.2 新增单测（建议）

- `project` CRUD 与 stable_key 匹配忽略状态  
- `ops` 在无真实回收站时：permanent + 路径校验单测；回收站用集成/手动  
- `appapi` 过滤 ignored 的 ListGroups  
- `progress` 百分比单调、取消后 Phase canceled  

### 11.3 手动矩阵

| 环境 | 项 |
|---|---|
| Windows 10/11 | 回收站、资源管理器定位、打包 exe |
| macOS 开发机 | 回收站、dev 运行 |
| 无 ffmpeg | 视频关闭、图片仍可用 |
| 大目录 | 取消、缓存二次加速 |
| HEIC | 仅精确组，无 pHash 近似 |

---

## 12. 风险与决策记录

| 风险 | 对策 |
|---|---|
| 回收站跨平台 | 抽象 `Trash(path) error`；分 OS 文件；失败不静默 |
| 组 ID 每次扫描变化 | StableKey / 路径集合迁移 |
| WebView 安全 | 只暴露 thumbs/frames；OpenPath 用系统调用 |
| pipeline 改坏 CLI | 保持 OnProgress 字符串兼容；`go test` + CLI 扫 testdata |
| 设计过密 | MVP 可先舒适密度；不阻塞功能 |
| 单写 SQLite | 项目库 `MaxOpenConns(1)` 已有；避免 UI 直接写 DB，只走 appapi |

---

## 13. 非目标（明确不做，防范围膨胀）

- 应用内视频双窗同步播放、像素 diff  
- 改阈值「仅重匹配不重扫」（有缓存重扫已够快）  
- 暂停/断点续扫 UI  
- 云同步、多用户  
- 自动「一键清空所有重复」  
- HEIC 感知哈希（引擎限制）  

---

## 14. 新会话开工指令（可直接粘贴）

```text
请按 docs/app-implementation-plan.md 实现 media-dedupe 的 Wails 桌面 App。

硬性要求：
1. 复用 internal 现有去重引擎，不要重写 discovery/hash/pHash/match/score 逻辑。
2. 按文档 Phase 0→5 顺序实施；每阶段可编译、可运行、可验证。
3. UI 文案与数据字段对齐文档 §1.4 与 §7.3（SHA-256、pHash、汉明距离、FFmpeg 抽帧、默认回收站）。
4. 删除必须：默认回收站 + 用户确认；永久删除需设置开关。
5. 完成后：go test ./...、wails build（或 dev 冒烟）、并写简短更新到 README。
6. 设计视觉参考 /Users/wang/Downloads/stitch_media_deduplication_desktop_app/ 的 HTML；布局优先，装饰可简化。
7. 不要扩展 §13 非目标功能。

当前仓库：/Users/wang/myself_project/media-dedupe
CLI 入口保留：cmd/media-dedupe
App 入口新建：cmd/app
```

---

## 15. 附录：字段映射速查

| 设计/UI | Go 数据来源 |
|---|---|
| 精确重复 | `ReportGroup.GroupType == exact` |
| 近似图片 | `similar_image` |
| 近似视频 | `similar_video` |
| 推荐保留 | `RecommendedFileID` / item `keep_recommended` |
| 待清理 | item `cleanup_candidate` 或用户勾选 |
| 可释放空间 | sum(非保留成员 size) |
| 汉明距离 | `hashfile.HammingDistance` 或 `(1-Similarity)*64` |
| 缩略图 | `ThumbPath` / thumbs/{fileID}.jpg |
| 帧条 | FrameDir / GetFrameURLs |
| 文件失败列表 | `Result.Errors` / `cache.LoadErrors` |
| FFmpeg | `video.Available()` |

---

*文档版本：1.0 · 对应设计评审：分页任务书通过 + 补丁通过。*  
*实现完成后应在本文件底部追加「实现偏差记录」，供后续会话对齐。*

---

## 实现偏差记录（2026-09-15）

- 前端采用 **纯 HTML/CSS/JS**（`frontend/dist` + embed），未引入 Vite/TS；布局对齐 Stitch 稿，装饰简化。
- Wails 入口：根 `main.go` + `cmd/app`，共享 `internal/appmain`。
- 缩略图/帧条通过本机 `127.0.0.1` HTTP + base64 path token 提供，仅白名单项目 thumbs/frames 目录。
- 组状态迁移使用 `duplicate_groups.stable_key`（成员路径排序后 SHA-256）。
- 回收站：macOS `osascript` Finder（失败回退 `~/.Trash`）；Windows PowerShell `Microsoft.VisualBasic.FileIO`；Linux MVP 为 permanent 并返回明确警告。
- 永久删除依赖 `settings.allowPermanentDelete`；前端二次确认。
- CLI 行为保持兼容（OnProgress 字符串仍在；OnEvent/Ctx 为可选）。
