# media-dedupe

Go 命令行工具：扫描目录中的图片/视频，找出精确重复与视觉近似重复，生成可人工审阅的报告。**不会删除或修改任何媒体文件。**

## 构建

```bash
go build -ldflags="-s -w" -o bin/media-dedupe ./cmd/media-dedupe
```

依赖：Go 1.22+。视频能力需要系统安装 `ffmpeg` / `ffprobe`。

## 使用

```bash
# 环境检查
./bin/media-dedupe doctor

# 扫描（默认 HTML 报告 + 缩略图）
./bin/media-dedupe scan ~/Pictures ~/Movies

# 仅图片、文本输出到终端
./bin/media-dedupe scan ~/Pictures --format text --no-video

# JSON 报告
./bin/media-dedupe scan ~/Pictures --format json --output reports/

# 重新渲染上次结果（不重扫）
./bin/media-dedupe report --format html

# 缓存
./bin/media-dedupe cache info
./bin/media-dedupe cache clear
```

### 常用参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--similarity` | 0.80 | 相似阈值 |
| `--workers` | 2 | 图片/hash 并发（机械盘建议保持较低） |
| `--video-workers` | 1 | 抽帧并发 |
| `--frames` | 8 | 视频抽帧数 |
| `--cache` | `~/Library/Caches/media-dedupe/cache.sqlite` | SQLite 缓存 |
| `--format` | html | `text` \| `json` \| `html` |
| `--output` | 缓存目录 `reports/` | 文件或目录 |
| `--no-thumbs` | false | 跳过缩略图 |

## 行为说明

- **精确重复**：同文件大小 → SHA-256 相同。
- **图片近似**：宽高分桶 + pHash 汉明距离。
- **视频近似**：时长/宽高比预筛 + 多帧 pHash 中位数（跳过黑帧）。
- **增量扫描**：`size+mtime` 未变则复用缓存中的 hash/pHash，不重新解码。
- **推荐策略**：质量分最高为 `keep_recommended`，分差过小则整组 `review_required`。
- HEIC/HEIF 第一版仅做精确去重，不做感知哈希。

## 项目结构

```
cmd/media-dedupe/     入口
internal/
  cli/                cobra 命令
  discovery/          目录遍历
  hashfile/           SHA-256、汉明距离
  imagehash/          解码、pHash、缩略图
  video/              ffmpeg/ffprobe
  candidate/          分桶候选
  match/              相似度、并查集
  score/              质量分
  cache/              SQLite
  report/             text/json/html
  pipeline/           编排
```
