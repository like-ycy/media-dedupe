# 去重召回与安全修正任务书（P0 / P1）

> **读者**：新会话里的实现 Agent。
> **背景**：2026-11-09 与 `qarmin/czkawka`（Rust，35k star）做过一次全面对标，并用 Codex 独立复核过结论。本文只收录**复核后确认成立**的问题，按优先级排列。
> **目标**：补召回（现在会漏报）、修可信度（现在会误导用户删错）。**不重写引擎、不换技术栈、不追 czkawka 的工具广度。**
> **基准**：czkawka 的同类实现在 `/tmp/czkawka_probe`（浅克隆，可能已不存在，需要时重新 `git clone --depth 1 https://github.com/qarmin/czkawka.git /tmp/czkawka_probe`），仅作行为参照，不要移植代码。

---

## 0. 一句话现状

引擎骨架（discovery → exact → image/video/text → group → report → cache → trash）是健康的，问题集中在**候选筛选的三个前置条件**和**结果呈现的可信度**上。这些都不是架构问题，是几十行的判定问题。

核心结论两条：

1. **召回漏在"分组"，不漏在"算法"**：同一张图存成 `.jpg` 和 `.png`、同一段视频存成 `.mp4` 和 `.mkv`、明显缩放的同一张图，都进不了比较阶段。
2. **呈现会误导**：相似组里 ID 最小的文件恒显示 100% 相似；精确副本会让近似关系凭空消失；视频空白帧会让帧位错乱。

---

## 1. 开工前必读

### 1.1 相关文件

| 文件 | 作用 |
|---|---|
| `internal/pipeline/pipeline.go` | 扫描编排，所有分组与组构建都在这里 |
| `internal/candidate/candidate.go` | 候选筛选（尺寸桶 + pHash 树 + 视频桶） |
| `internal/match/match.go` | 视频帧相似度、连通分量 |
| `internal/video/video.go` | 抽帧、帧哈希、缓存 key |
| `internal/cache/cache.go` | SQLite schema 与读写 |
| `internal/updater/updater.go` | 更新检查/下载/校验 |
| `internal/config/config.go` | 扩展名集合、默认阈值 |
| `internal/model/model.go` | ReportGroup / ReportItem / MediaType |
| `frontend/src/App.tsx` | 结果页与组详情，相似度展示 |

### 1.2 硬约束（不要违反）

- **绝不自动删除**；删除路径只走 `internal/ops`，默认回收站。
- **不改 `stable_key` 算法**（`pipeline.go:500-515`）。成员集合变化导致旧组状态失效是可接受的自然结果，不要为了"保住状态"去改 key 规则。
- **不新增第三方依赖**，除非本任务明确需要（本批任务都不需要）。
- **不重构 pipeline 的分阶段结构**，改动限定在指定的函数内。
- **缓存 schema 能不 Migration 就不 Migration**（P1-1 恰好可以复用已有列，见 §4.2）。

### 1.3 验证命令（每次改完都要跑）

```bash
gofmt -l .                     # 应为空
go build ./...
go vet ./...
go test ./...
cd frontend && npx tsc --noEmit && npx prettier --check "src/**/*.{ts,tsx}"
```

> ⚠️ **`.gitignore` 里 `**/*_test.go` 和 `tests/` 是被忽略的**（历史有意为之）。按当前 AGENTS.md 约束，新增本地验证测试不提交，不用 `git add -f` 强制添加，也不要修改 `.gitignore`。

### 1.4 提交规范

首行中文、≤72 字符、格式 `<type>(<scope>): <emoji> <subject>`；次行起写正文（变更 / 原因 / 影响）。不要加 `Co-authored-by` 之类的标记。

---

## 2. 任务总览

| 编号 | 任务 | 优先级 | 主要文件 | 预估改动 | 依赖 |
|---|---|---|---|---|---|
| P0-1 | 取消扩展名隔离，按媒体类型合并候选 | P0 | `pipeline.go` | 小（~40 行） | 无 |
| P0-2 | 精确组的代表文件继续参与近似匹配 | P0 | `pipeline.go`、`model.go` | **中偏大（~120 行，含融合逻辑）** | 建议在 P0-1 之后 |
| P1-1 | 视频帧位对齐 + 最低有效覆盖 | P1 | `video.go`、`match.go`、`cache.go`、`pipeline.go` | 中（~100 行） | 无 |
| P1-2 | 相似度按"相对推荐文件"计算 | P1 | `pipeline.go`、前端 | 小（~30 行 + 前端） | 无 |
| P1-3 | 更新包摘要缺失时拒绝自动安装 | P1 | `updater.go` | 小（~30 行） | 无 |
| P2-1 | 图片候选跨分辨率 | P2 | `candidate.go`、`pipeline.go` | 中 | P0-1 之后 |
| P2-2 | 镜像 / 旋转 90° 不变性 | P2 | `imagehash/`、`candidate.go`、`cache.go` | 中偏大 | P2-1 之后 |

**建议顺序**：P0-1 → P1-2 → P0-2 → P1-1 → P1-3 →（P2 另议）。

理由：P0-1 改动最小、收益最大；P1-2 是纯展示修正，独立且立刻降低误导；P0-2 需要设计取舍，放第三个；P1-1 涉及缓存版本失效，放第四个避免和其他改动混在一起排查。

---

## 3. P0-1 取消扩展名隔离

### 3.1 现象

同一份内容换了扩展名，就永远不参与比较：

- `photo.jpg` 与 `photo.jpeg`（同一张图，只改了扩展名）→ 精确重复不报
- `photo.jpg` 与 `photo.png`（同一张图的不同编码）→ 近似重复不报
- `clip.mp4` 与 `clip.mkv`（同一段视频的两种容器）→ 精确与近似都不报
- 任何扩展名不同的字节完全相同的文件 → 精确重复不报

### 3.2 证据（已逐行核实）

| 位置 | 现状 |
|---|---|
| `pipeline.go:517-543` | `exactBucketKey{size, ext}`，精确匹配只比"同大小 + 同扩展名" |
| `pipeline.go:545-546` | 注释自述：*Exact matching is restricted to files sharing size and extension; other files are never opened in this pass.* |
| `pipeline.go:1003-1014` | `imageSimilarity` 先 `byExtension` 分桶 |
| `pipeline.go:1305-1316` | `videoSimilarity` 先 `byExtension` 分桶 |
| `pipeline.go:357-364` | TXT 也按扩展名分桶 —— **但 `config.TextExtensions` 只有 `.txt`，此桶实际唯一，本任务不动** |
| `config.go:47-59` | 图片 10 种、视频 11 种、文本 1 种扩展名 |

### 3.3 期望行为

候选筛选只按**媒体类型**（image / video / text）分桶，不再按扩展名。扩展名只用于决定"用哪个解码器算哈希"，不参与"能不能比"。

### 3.4 实现方案

1. `exactBucketKey` 改为 `{size int64, media model.MediaType}`（结构体或直接 `map[struct{size int64; media string}]`）。`f.MediaType` 与 `f.Extension` 都已在 `model.DiscoveredFile` 上，直接用。
2. `imageSimilarity`：删掉 `byExtension` 那一层，直接对全部 `imageFacts` 调 `imageSimilarityByExtension`（把该函数改名，例如 `imageSimilarityBucket`）。
3. `videoSimilarity`：同上，直接对全部 `videoFacts` 调一个不带扩展名分组的函数。
4. `imageFacts.extension` / `videoFacts.extension` 字段留着（报告与调试有用），只是不再参与分桶。
5. 注意 HEIC/HEIF：它们在 `collectImages` 阶段就被 `discovery.SupportsPHash` 过滤掉，不会进入 `facts`，行为不变；但精确去重阶段它们会和其他图片一起比较——这是正确的。

**不要**把精确匹配改成"只按 size 分桶"（会跨越 image/video/text 三种类型，虽然结果正确但白白多读文件）。按 `size + mediaType` 是正确粒度。

### 3.5 影响面

- **性能**：精确阶段候选数上升。原先是"同大小 + 同扩展名"，现在是"同大小 + 同类型"。图片库里大量同尺寸文件（如相机连拍转存）会显著增加首轮读取。
  - 缓解：`exactPass` 已有"小于 4 MiB 直接全哈希、否则先算采样指纹"的分层（`pipeline.go:562-700` 一带），保持现状即可；不要为了性能把采样阈值调低。
  - **必须实测**：用 `./testdata`（147 txt / 120 png / 60 mp4）跑一次，记录耗时前后对比。
- **缓存**：无需 schema 变更，无需失效。
- **前端**：无变更。
- **组状态**：可能多出新组，`stable_key` 自然生效，旧状态不继承是对的。

### 3.6 验收标准

新增三个 fixture（放在 `testdata/` 下，注意 `testdata/` 也被 gitignore，测试需能自建临时目录）：

1. `a.jpg` 与 `a.jpeg`：字节完全相同 → 必须出现在**同一个精确重复组**。
2. `b.jpg` 与 `b.png`：同一张原图分别存两种编码、尺寸相同 → 必须出现在**同一个近似图片组**（pHash 距离在阈值内）。
3. `c.mp4` 与 `c.mkv`：同一段视频 remux 成两种容器、时长与分辨率相同 → 必须被**视频阶段纳入候选**（是否最终成组取决于帧哈希，至少不能因为扩展名被提前排除）。

同时保留一个**反向用例**：两个只有扩展名不同、内容完全无关的同尺寸图片，不能成组。

### 3.7 风险与回滚

- 风险：召回变大后，某些原本"安静"的库会冒出大量新组，用户会以为工具变吵了。
  - 对策：在 README「行为说明」里补一句"候选按媒体类型而非扩展名筛选"；不需要前端提示。
- 回滚：改动集中在三个分桶函数，单文件可逆。

---

## 4. P0-2 精确组的代表文件继续参与近似匹配

### 4.1 现象

原本 `A ≈ B`（近似），后来给 A 存了一份字节完全相同的副本 A′：

- 精确阶段发现 `A == A′`，成组；
- A 和 A′ **双双退出**后续所有近似阶段；
- B 变成孤立文件，`A ≈ B` 这条关系凭空消失。

用户视角：多存了一份副本，反而少报了一条重复。

### 4.2 证据（已逐行核实）

| 位置 | 现状 |
|---|---|
| `pipeline.go:748-750` | 精确组的**所有**成员写入 `used[path]` |
| `pipeline.go:924-930` | `collectImages` 用 `usedPaths` 跳过这些文件 |
| `pipeline.go:1093-1099` | `collectVideos` 同样跳过 |
| `pipeline.go:340-342` | TXT 阶段同样跳过 |
| `pipeline.go:562-700` | 精确阶段的哈希/置信度逻辑（改动时不要动这部分） |

### 4.3 期望行为

- 每个精确重复组，**保留一个代表文件**参与后续近似阶段；
- 代表文件命中近似关系时，把它所在精确组的**所有兄弟文件一起并入**该近似组；
- 被并入的精确组**不再单独输出**，避免同一文件在两个组里重复出现；
- 组内仍然保证"至少保留一个文件"的删除安全约束。

### 4.4 推荐设计：以"组"为单位参与近似

不要试图在"文件级"上打补丁（例如给 `usedPaths` 加例外），那会让同一个路径同时出现在两个组里，删除面板会重复计数。正确做法是把每个精确组折叠成一个**代表节点**：

1. **`exactPass` 增加返回值**：除现有 `groups`、`used` 外，再返回
   `reps map[int64]int64`（文件 ID → 所在精确组的代表文件 ID）与
   `groupsByRep map[int64]int64`（代表文件 ID → 精确组在报告里的索引）。
   代表选谁：直接用该组已算出的 `RecommendedFileID`（`pipeline.go:751-769` 一带），没有就用组内第一个。
2. **`usedPaths` 语义改为"跳过非代表成员"**：`collectImages/collectVideos` 与 TXT 阶段改为跳过 `reps` 里"不是代表"的路径。给 `imageFacts` / `videoFacts` 各加一个字段 `exactGroupRep int64`（0 表示不属于精确组）。
3. **`groupsFromComponents` 融合**：构建近似组时，若成员带 `exactGroupRep != 0`，
   - 把该精确组的全部成员展开进本组；
   - 在最终报告里标记该精确组为"已吸收"，输出前过滤掉。
4. **输出前去重**：在 `Scan` 汇总 `allGroups` 的地方加一次"按文件路径去重 + 丢弃已吸收的精确组"，保证一个路径只出现一次。
5. **推荐与删除**：融合后的组仍走 `score.ChooseActions`；精确组内部全是同内容文件，选谁保留都不影响正确性，但**推荐文件应优先沿用精确阶段已经给出的 winner**，避免用户重扫后看到推荐项跳变。

### 4.5 影响面

- **`model.go`**：`ReportGroup` 可能需要一个 `AbsorbedExactGroupIDs []int64` 或在 pipeline 内部维护、不进 model。**优先选后者**，避免动持久化格式。
- **缓存**：`duplicate_groups` / `duplicate_items` 的写入点在 pipeline 之后，融合发生在其之前，因此**不需要 schema 变更**；但要确认融合后 `groupId` 分配不冲突。
- **组状态**：成员集合变了，`stable_key` 不匹配 → 旧 ignored 状态失效，这是预期行为。
- **前端**：若融合后的组里同时存在"精确"与"近似"两种关系，组详情里 `Reasons` 会混杂。**本任务只要求不崩、不重复**，文案优化留到后续。

### 4.6 验收标准

1. `A`、`A′`（字节相同）、`B`（与 A 视觉近似）三个文件同目录扫描：
   - 输出**恰好一个组**，包含 3 个文件；
   - `A` 与 `A′` 在组内标记为精确重复（`Reasons` 体现），`B` 标记为视觉近似；
   - 推荐保留有且只有一个。
2. 删除流程：该组"删除全部非推荐项"后，磁盘至少保留 1 个文件（沿用现有 `ops` 校验）。
3. 重扫一次，组不重复、不消失，`stable_key` 稳定。
4. 只有 `A`、`A′` 没有 `B` 的对照组：仍然只输出一个纯精确组，行为与改动前一致。

### 4.7 风险与回滚

- 这是本批**唯一需要设计判断**的任务。若融合逻辑复杂度失控，允许降级为 **P0-2-minimal**：
  - 只让代表文件参与近似阶段，命中后**不融合**，而是在组详情里给该成员打一个 `alsoInExactGroup` 标记，B 单独成组仍会被丢弃（因不足 2 人）——**这种降级解决不了问题，不要选**。
  - 真正的最小可行版本是：代表参与近似 + 命中后把兄弟文件加进近似组 + 丢弃被吸收的精确组（即 §4.4 的 1、2、3 步，跳过第 4 步的去重改用输出层按 path 去重）。
- 回滚：改动集中在 `exactPass` 返回值、两个 `collect*` 的过滤条件、`groupsFromComponents`。

---

## 5. P1-1 视频帧位对齐与最低有效覆盖

### 5.1 现象

1. 两个视频帧哈希数组按**下标**对齐比较（`match.go:94-116`），但数组是**压紧过的**：空白帧、缺失帧、解码失败帧都被跳过，后面的帧前移。
2. 结果：两条不同的视频，只要各自跳过的帧数不同，下标就错位；相似度要么虚高要么虚低。
3. 更糟的是：只剩 1 帧有效时，`median([]float64{0.97})` = 0.97，直接判为高度相似。**这是静默假阳性**——对一个会删文件的工具，这是最危险的一类 bug。

### 5.2 证据（已逐行核实）

| 位置 | 现状 |
|---|---|
| `video.go:344-370` | `ExtractFramesContext` **已经**用 `""` 占位保留位置（注释也写明了） |
| `video.go:301-338` | `HashFrames` 跳过 `""`、跳过 `IsBlankFrame`、失败帧 `continue`，哈希**紧凑 append** → 位置信息丢失 |
| `video.go:330-334` | 只要 `len(hashes) > 0` 就返回成功 —— **剩 1 帧也当有效结果** |
| `cache.go:90-99` | `perceptual_hashes` **已有 `timestamp_ms` 列** |
| `cache.go:630-637` | 但写入时硬编码 `NULL` |
| `cache.go:641-646` | 读取只返回 `hash_value`，按 `frame_index` 排序 |
| `match.go:94-116` | `VideoHashSimilarity` 按数组下标 ±1 平移取中位数，无最低帧数要求 |

### 5.3 期望行为

- 帧哈希必须带**时间戳**存储与传递，比较按时间戳就近配对，不按下标；
- 有效帧覆盖率不足的视频，不参与判重（宁可漏，不可错）；
- 缓存版本号递增，旧缓存自动失效。

### 5.4 实现方案

1. **定义类型**（放 `internal/video`）：
   ```go
   type FrameHash struct {
       Index       int   // 原始帧位（0-based，供预览对齐）
       TimestampMs int64
       Hash        string
   }
   ```
2. **`HashFrames` 返回 `[]FrameHash`**：遍历 `framePaths` 时用**原始下标**、由 `BuildFrameTimestamps(durationMs, frameCount)` 取对应时间戳；`""` / 解码失败 / 空白帧**不入结果**，但下标保留。签名需要 `meta` 或 `timestamps` 入参（当前只有 `framePaths`），调用点：`video.go:287`（`ComputeFrameHashes`）、`pipeline.go:1148`（`collectVideos`）。
3. **缓存读写**：
   - 新增 `SaveFrameHashes(fileID, hashType string, frames []video.FrameHash)`，写入 `timestamp_ms` 与 `frame_index`（复用现有列，**无 DDL**）；
   - 新增 `LoadFrameHashes(fileID, hashType) ([]video.FrameHash, error)`，`SELECT frame_index, timestamp_ms, hash_value ... ORDER BY timestamp_ms`；
   - 保留旧 `SavePerceptualHashes` / `LoadPerceptualHashes` 给图片用（`hash_type = image_phash`），不要动它们。
   - ⚠️ `internal/cache` 不应 import `internal/video`（会引入 ffmpeg 依赖关系倒挂）。让 `SaveFrameHashes` 接收 `[]struct{Index int; TimestampMs int64; Hash string}` 或三个平行切片，由 pipeline 负责转换。**这点务必先确认 import 方向**。
4. **`match.VideoHashSimilarity` 改为接收 `[]FrameHash`**：
   - 对左视频每一帧，在右视频中找 `|Δt| <= tolerance` 的帧，`tolerance` 取相邻帧间隔的一半（`BuildFrameTimestamps` 等距，直接算 `step/2`）；
   - 相似度取配对结果的**中位数**（保留现有度量，避免阈值体系失配）；
   - 新增最低覆盖要求：**有效配对帧数 ≥ max(3, ceil(0.5 × frameCount))**，否则返回 0。
5. **版本失效**：`frameHashTypePrefix` 从 `video_frame_phash_v2_` 改为 `video_frame_phash_v3_`（`video.go:279`）。这会自动让旧缓存失效并重算，符合预期。
6. **预览对齐**：`framePreviewPath`（`pipeline.go:1203`）与 `copyFramePreviews`（`pipeline.go:1262`）目前用 `framePaths` 下标命名预览图。改为用 `FrameHash.Index`，保证"预览第 N 张"与"哈希第 N 个"指同一时间点。

### 5.5 影响面

- **缓存**：写入内容变化（`timestamp_ms` 从 NULL 变成真实值）、`hash_type` 版本位变化 → 旧缓存自然失效，**不需要 Migration，但会触发一次全量重抽帧**。首次重扫变慢是预期，README 里已描述过这个机制（"首次升级或改变帧数会重新提取视频特征"）。
- **函数签名**：`video.HashFrames`、`video.ComputeFrameHashes`、`match.VideoHashSimilarity` 三处 + 全部调用点与测试。
- **前端**：无变更。
- **报告**：无变更。

### 5.6 验收标准

1. **空白帧错位**：两条内容相同的视频，其中一条前 10% 是纯黑帧、另一条不是 → 改动前相似度虚高但配对错乱，改动后仍应正确判为同一段（或用例允许不判重，但**不能出现"两条不同视频被判重"**）。
2. **单帧假阳性**：一条视频只抽出 1 帧有效（其余空白）→ 必须**不判重**。
3. **时间戳落库**：扫完后 `sqlite3 cache.sqlite "select timestamp_ms from perceptual_hashes where hash_type like 'video_frame_phash_v3_%' limit 5"` 非空。
4. **回归**：`go test ./internal/video/... ./internal/match/... ./internal/cache/...` 全绿；`testdata/` 下 60 个 mp4 扫描结果与改动前的组数差异需要人工过一遍，确认差异都来自"修掉的假阳性"。

### 5.7 风险与回滚

- 风险：最低覆盖要求会让一部分短片/低码率视频不再判重（召回下降）。这是**有意的取舍**：错判删除的代价远高于漏报。
- 回滚：`frameHashTypePrefix` 改回 v2 即可让旧缓存重新生效，但代码里的类型变更需要一并回退。

---

## 6. P1-2 相似度按"相对推荐文件"计算

### 6.1 现象

相似组详情里，成员显示的相似度不是"与推荐保留文件相比有多像"，而是：
- 组里 **fileID 最小**的那个文件，**恒显示 100%**（不管它实际像不像别人）；
- 其余成员显示"与任意一个邻居的最高相似度"；
- 完全没有边的成员，也显示 100%。

用户看到"100% 相似"就会放心删掉别的，这是直接诱发误删的显示 bug。

### 6.2 证据（已逐行核实）

| 位置 | 现状 |
|---|---|
| `pipeline.go:1441-1444` | `if len(comp) > 0 && id == comp[0] { return 1.0 }` |
| `pipeline.go:1383` | `comp` 来自 `match.ConnectedComponents`，内部按 fileID 升序 `sort` |
| `pipeline.go:1445-1461` | 其余成员取"最佳邻边"，`!found` 时也 `return 1.0` |
| `pipeline.go:1419-1439` | `componentConfidence` 只在"存在的边"上取最小值，缺失的边被忽略 → 链式组（A≈B≈C 但 A≉C）仍显示高置信度 |

### 6.3 期望行为

- 成员的 `Similarity` = **该成员与推荐保留文件（`RecommendedFileID`）之间的相似度**；
- 两者之间没有边时，明确表示"无直接相似关系"（返回 0 或前端显示 `—`），而不是 1.0；
- 精确重复组（`GroupExact`）保持 1.0，因为字节相同是事实；
- 组级 `Confidence` 不能用"忽略缺失边"的方式算，链式组要体现最弱环节。

### 6.4 实现方案

1. `itemSimilarity` 签名改为 `itemSimilarity(id, winner int64, edgeSim map[[2]int64]float64) float64`：
   - `id == winner` → 1.0；
   - 有 `edgeSim[EdgeKey(id, winner)]` → 返回该值；
   - 否则 → 返回 -1（或新增 `bool` 返回值），由调用方决定展示为"无直接关系"。
2. `groupsFromComponents` 里把 `winner` 传给 `itemSimilarity`（`winner` 已在 `pipeline.go:1394` 算出，直接用）。
3. `componentConfidence` 改为：对所有成员对检查，**缺失的边按 0 计**，再取最小值；或者改用"每对成员的最低相似度，链式关系单独标注"。**优先选前者**（实现简单、语义明确）。
4. 前端：`Similarity` 为负值时显示 `—`（组详情与结果列表两处）。

### 6.5 影响面

- `model.ReportItem.Similarity` 语义变化 → 报告 JSON/HTML 里的数值会变（同一批文件，数值更保守）。这是修正，不是回归。
- 前端两处展示需要兜底负值。
- 缓存：`duplicate_items` 里存的是写入时的值，重扫后自然更新，**无需 Migration**。

### 6.6 验收标准

1. 构造链式组：`A`≈`B`（0.97）、`B`≈`C`（0.96）、`A`≉`C`。推荐文件为 A 时：
   - A 显示 100%（它是推荐项）；
   - B 显示 0.97；
   - C 显示 `—`（无直接边），**不能显示 0.96 或 100%**；
   - 组级置信度应显著低于 0.96。
2. 精确组：所有成员仍显示 100%。
3. 报告导出（JSON/HTML/TXT）里负值不出现原始 `-1`，需转成 `null` 或省略字段。

---

## 7. P1-3 更新包摘要缺失时拒绝自动安装

### 7.1 现象

下载更新包时，若 release asset 没有 `digest` 字段，代码**只校验文件大小**，然后就可以进入替换自身并重启的流程。走代理下载时风险更高。

### 7.2 证据（已逐行核实，含实测）

| 位置 | 现状 |
|---|---|
| `updater.go:32-37` | `ReleaseAsset.Digest` 来自 GitHub API |
| `updater.go:139` | 赋值 `info.Digest` |
| `updater.go:331-334` | 大小不符即失败 |
| `updater.go:335-340` | **`if expectedDigest != ""`** —— 为空时直接跳过校验 |
| `updater.go:348-360` | `verifyDigest` 只接受 `sha256:` 前缀 |

**实测结论**：`gh api repos/like-ycy/media-dedupe/releases/latest` 显示当前 4 个资产（3 个 zip + `SHA256SUMS.txt`）**全部带 `sha256:` digest**。因此这是**条件性风险**（未来上传方式变化才会触发），不是当前已存在的漏洞。这也决定了它只值 P1，不是 P0。

### 7.3 期望行为

- 有 `digest` → 校验（现状已做）；
- 无 `digest` → 退化为拉取同 release 的 `SHA256SUMS.txt` 校验；
- 两者都拿不到 → **拒绝自动安装**，提示用户手动下载（给出 release 页面链接）。

### 7.4 实现方案

1. `CheckForUpdate` 阶段额外记录 `ChecksumsURL`（在 `Assets` 里找 `name == "SHA256SUMS.txt"` 的 `BrowserDownloadURL`），存入 `UpdateInfo`。
2. `StartDownload` 末尾：
   ```go
   if expectedDigest != "" {
       // 现状
   } else if sumsURL != "" {
       // 下载 SHA256SUMS.txt（注意：走代理时同样加前缀）
       // 按 assetName 查行，格式 "<hex>  <filename>"
   } else {
       return fmt.Errorf("更新包缺少校验信息，已取消自动安装，请手动下载")
   }
   ```
3. `verifyDigest` 增加一个"裸 hex"入口或复用：`SHA256SUMS.txt` 里没有 `sha256:` 前缀。
4. 前端：把该错误原文展示给用户，附 release 页面链接（`internal/appapi` 已有 update 相关 API）。

### 7.5 影响面

- 只影响更新链路，不影响扫描。
- 需要一个能跑的单测：mock 一个 `digest` 为空的 `UpdateInfo`，断言 `StartDownload` 返回错误且**未写 `downloadComplete = true`**。
- 注意 `SHA256SUMS.txt` 本身也是资产，不要把它自己当成可安装包（`assetName` 已有 `.zip` 后缀校验，天然排除）。

### 7.6 验收标准

1. digest 存在 → 正常下载校验通过（回归）。
2. digest 为空、`SHA256SUMS.txt` 可获取且匹配 → 安装继续。
3. digest 为空、`SHA256SUMS.txt` 缺失或校验不匹配 → 明确报错，不进入替换流程。
4. 代理模式下（`useProxy=true`）两个 URL 都加了 `ghfast.top` 前缀。

---

## 8. P2 概要（本批不实现，仅留档）

### 8.1 P2-1 图片候选跨分辨率

**问题**：`candidate.ImageBucketKey` 把宽高量化到 8px，`ImageCandidatesByPHash` 只查同桶与相邻 8 个桶（`candidate.go:11-90`、`146-208`）。4000×3000 与 1920×1440 的同一张图永远不是候选。

**方向**：图片候选**不做尺寸分桶**，全部进 pHash 树检索；只保留一个极宽的兜底（例如面积比 < 1/16 才排除），用于挡住"图标 vs 全屏壁纸"这类无意义比较。

**注意**：不要采纳"面积同数量级"作为主筛选条件——它只是把漏检边界往外挪，本质没解决。czkawka 的做法是不筛尺寸（`similar_images/core.rs:290` 附近），我们照做即可。

**风险**：候选对数量上升，pHash 距离计算量上升。需要实测 3000 张图片规模的耗时。

### 8.2 P2-2 镜像 / 旋转 90° 不变性

**方向**：`imagehash` 除主 pHash 外，额外存 3 个变体（水平镜像、旋转 90°、旋转 270°），或在候选检索时对查询哈希做变体展开。取最小汉明距离。

**代价**：缓存里 `perceptual_hashes` 是"一个 file_id + 多个 hash_type"的结构，天然支持多行；但 `collectImages` 现在只读 `hs[0]`（`pipeline.go:959-966`），需要改成读全部。新增 `hash_type`，无需 DDL。

---

## 9. 明确不做的事

- 不移植 czkawka 的 15 个工具（空文件夹、坏文件、EXIF 清理等）。
- 不改 `stable_key`、不改删除语义、不加自动删除。
- 不换洗哈希算法（保持 SHA-256），不引入 blake3/xxh3 依赖。
- 不做视频"片段包含"匹配（czkawka 的多窗口 + 子片段是独立功能，且涉及"长片含短片不等于短片该删"的产品语义，需单独设计）。
- 不做预哈希 4KB 分桶的性能优化（先修正确性，性能另开任务）。

---

## 10. 总验收清单

改完 P0/P1 后，一次性确认：

```bash
gofmt -l .                                   # 空
go build ./... && go vet ./... && go test ./...
cd frontend && npx tsc --noEmit && npm run build
```

行为侧：

- [x] `.jpg` / `.jpeg` 同字节 → 精确组检出
- [x] `.jpg` / `.png` 同图 → 近似组检出
- [x] `.mp4` / `.mkv` 同源 → 视频阶段纳入候选
- [x] `A` + `A′` + `B` → 单个融合组，推荐项唯一
- [x] 单帧有效视频 → 不判重
- [x] 链式相似组 → 无直接关系的成员不显示 100%
- [x] 更新包无 digest 且无校验和 → 拒绝自动安装
- [x] 重扫两次 → 组不重复、不消失、推荐项稳定
- [x] 删除流程 → 每组至少保留 1 个文件（临时媒体经 App API 实际删除验证，未进行桌面 GUI 手动操作）

---

## 11. 已知遗留（不在本批解决，但要知道）

1. **`VideoBucket` 的时长容差 ±5%** 会漏掉"从长视频剪出的短片"（`candidate.go:306-350`）。这是功能缺失，不是 bug。
2. **简易视频模式采样指纹**可能误判（`confidence=0.99` 是等级不是准确率）。已有 `--verify-sampled-hash` 与桌面开关兜底，本批不动。
3. **TXT 正文近似**的编码嗅探依赖 BOM 与"无效 UTF-8 才试 GB18030"，无 BOM 的 GB18030 文件可能乱码。属独立问题。
4. **`componentConfidence` 的链式标注**在 P1-2 只做了"保守取最小值"，更细的"链式关系可视化"需要 UI 设计。

---

## 12. 2026-11-09 Review 验证记录

审查目标为 `56b657a`，对照基线为其父提交 `64d2523`。修正了以下边界：

- 精确组使用可解码副本参与近似匹配时，若该代表胜出，恢复精确组原推荐项。
- 输出去重移除推荐项后，清除相对旧推荐的相似度；被丢弃的单成员组不占用后续组的路径。
- 项目创建、修改、旧配置加载与全局设置共用抽帧数归一规则，保证配置显示与扫描实际使用一致。
- 修正文档中“全量图片比较”与“换容器仍属字节相同”的不准确说明。

`gofmt -l .` 为空，`go build ./...`、`go vet ./...`、`go test ./...`、前端 `npx tsc --noEmit` 与 `npm run build` 均通过。新增本地回归先复现失败，再验证修复；按 AGENTS.md 不提交这些被忽略的测试文件。额外验证了未知相似度的 JSON/HTML/TXT 导出、无效/缺失校验和拒绝完成下载、代理同时用于更新包与校验和文件。

Prettier 检查仍报告 `src/api.ts`、`src/App.tsx`、`src/main.tsx` 三个文件的既有格式问题；在 `64d2523` 的独立源码快照中复查得到同样结果，本次未做整文件格式重排。

样本为本地 `testdata/` 的 327 个文件（120 PNG、60 MP4、147 TXT），显式使用 `--video-mode precise`、默认 8 帧、并发 1、独立空 SQLite 缓存。以下为单次墙钟耗时，未包含编译，也未清空操作系统文件缓存：

| 指标 | `64d2523` | `56b657a` |
|---|---:|---:|
| 首扫耗时 | 9.88 秒 | 13.90 秒 |
| 精确 / 近似图片 / 近似视频 / 近似 TXT 组 | 75 / 23 / 13 / 23 | 1 / 29 / 15 / 23 |
| 检出成员 | 306 | 317 |
| 扫描错误 | 0 | 0 |

精确代表继续参与抽帧使首扫增加工作量，缓存重扫为 0.10 秒。原有检出成员没有丢失；新增 8 个图片成员、3 个视频成员。15 个视频组均只包含生成器中同一源素材的副本或重编码版本，组数变化来自精确组融合和恢复召回，并非仅来自过滤假阳性。315 条 v3 视频帧哈希均有时间戳。重扫成员、相似度、动作、推荐项及置信度一致；修复后再次扫描也得到相同样本结果。

删除验证仅操作临时生成的三个图片文件：扫描形成单个融合组；请求删除整组被阻止；通过 App API 删除全部非推荐项后，磁盘恰好保留推荐文件。未操作用户样本库中的媒体。
