package report

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"media-dedupe/internal/model"
)

// JSONReport is the machine-readable payload.
type JSONReport struct {
	Scan   model.ScanMeta      `json:"scan"`
	Groups []jsonGroup         `json:"groups"`
	Errors []model.ReportError `json:"errors"`
}

type jsonGroup struct {
	ID              int64      `json:"id"`
	Type            string     `json:"type"`
	Confidence      float64    `json:"confidence"`
	RecommendedKeep string     `json:"recommended_keep"`
	Items           []jsonItem `json:"items"`
}

type jsonItem struct {
	FileID       int64    `json:"file_id"`
	Path         string   `json:"path"`
	Action       string   `json:"action"`
	Similarity   float64  `json:"similarity"`
	QualityScore float64  `json:"quality_score"`
	SizeBytes    int64    `json:"size_bytes"`
	Thumb        string   `json:"thumb,omitempty"`
	Reasons      []string `json:"reasons"`
}

func RenderJSON(groups []model.ReportGroup, errs []model.ReportError, meta model.ScanMeta) (string, error) {
	if errs == nil {
		errs = []model.ReportError{}
	}
	payload := JSONReport{
		Scan:   meta,
		Errors: errs,
	}
	for _, g := range groups {
		rec := ""
		var items []jsonItem
		for _, it := range g.Items {
			if it.FileID == g.RecommendedFileID {
				rec = it.Path
			}
			items = append(items, jsonItem{
				FileID:       it.FileID,
				Path:         it.Path,
				Action:       string(it.Action),
				Similarity:   it.Similarity,
				QualityScore: it.QualityScore,
				SizeBytes:    it.SizeBytes,
				Thumb:        it.ThumbPath,
				Reasons:      it.Reasons,
			})
		}
		payload.Groups = append(payload.Groups, jsonGroup{
			ID:              g.GroupID,
			Type:            string(g.GroupType),
			Confidence:      g.Confidence,
			RecommendedKeep: rec,
			Items:           items,
		})
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

func RenderText(groups []model.ReportGroup, errs []model.ReportError, meta model.ScanMeta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Scanned %d files (%d failed), %d duplicate groups, threshold=%.2f\n",
		meta.FilesSeen, meta.FilesFailed, len(groups), meta.Threshold)
	var cleanupBytes int64
	for _, g := range groups {
		fmt.Fprintf(&b, "\nGroup #%d %s confidence=%.2f\n", g.GroupID, g.GroupType, g.Confidence)
		for _, it := range g.Items {
			marker := " "
			switch it.Action {
			case model.ActionKeep:
				marker = "+"
			case model.ActionCleanup:
				marker = "-"
				cleanupBytes += it.SizeBytes
			case model.ActionReview:
				marker = "?"
			}
			fmt.Fprintf(&b, "  %s %s  sim=%.2f quality=%.2f size=%d\n",
				marker, it.Path, it.Similarity, it.QualityScore, it.SizeBytes)
		}
	}
	if cleanupBytes > 0 {
		fmt.Fprintf(&b, "\nCleanup candidate total size: %s\n", humanBytes(cleanupBytes))
	}
	if len(errs) > 0 {
		b.WriteString("\nErrors:\n")
		for _, e := range errs {
			fmt.Fprintf(&b, "  %s [%s] %s\n", e.Path, e.Stage, e.Message)
		}
	}
	return b.String()
}

func RenderHTML(groups []model.ReportGroup, errs []model.ReportError, meta model.ScanMeta, thumbRel string) string {
	var b strings.Builder
	var cleanupBytes int64
	keepCount, cleanupCount := 0, 0
	for _, g := range groups {
		for _, it := range g.Items {
			switch it.Action {
			case model.ActionKeep:
				keepCount++
			case model.ActionCleanup:
				cleanupCount++
				cleanupBytes += it.SizeBytes
			}
		}
	}

	b.WriteString(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Media Dedupe Report</title>
<style>
:root { --bg:#0f1115; --card:#1a1d24; --ink:#e8eaed; --muted:#9aa0a6; --keep:#34a853; --clean:#ea4335; --review:#fbbc04; --line:#2a2f3a; }
* { box-sizing:border-box; }
body { margin:0; font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; background:var(--bg); color:var(--ink); padding:24px; }
h1 { font-size:22px; margin:0 0 8px; }
.summary { color:var(--muted); margin-bottom:24px; }
.summary strong { color:var(--ink); }
.group { background:var(--card); border:1px solid var(--line); border-radius:12px; padding:16px; margin-bottom:16px; }
.group h2 { margin:0 0 12px; font-size:16px; }
.badge { display:inline-block; font-size:11px; padding:2px 8px; border-radius:999px; margin-left:8px; vertical-align:middle; }
.badge.exact { background:#1a73e8; }
.badge.similar_image { background:#a142f4; }
.badge.similar_video { background:#ff6d00; }
.row { display:flex; gap:12px; flex-wrap:wrap; }
.item { flex:1 1 220px; min-width:200px; border:1px solid var(--line); border-radius:10px; overflow:hidden; background:#12151b; }
.item img { width:100%; aspect-ratio:4/3; object-fit:cover; display:block; background:#000; }
.item .meta { padding:10px; }
.item .path { word-break:break-all; font-size:12px; color:var(--muted); }
.item .scores { margin-top:6px; font-size:12px; }
.action { display:inline-block; font-size:11px; font-weight:600; padding:2px 6px; border-radius:4px; margin-bottom:6px; }
.action.keep_recommended { background:rgba(52,168,83,.2); color:var(--keep); }
.action.cleanup_candidate { background:rgba(234,67,53,.2); color:var(--clean); }
.action.review_required { background:rgba(251,188,4,.15); color:var(--review); }
.errors table { width:100%; border-collapse:collapse; font-size:12px; }
.errors th,.errors td { border-bottom:1px solid var(--line); text-align:left; padding:6px; }
.muted { color:var(--muted); }
</style>
</head>
<body>
<h1>Media Dedupe Report</h1>
<div class="summary">
  扫描 <strong>` + fmt.Sprint(meta.FilesSeen) + `</strong> 个文件，
  失败 <strong>` + fmt.Sprint(meta.FilesFailed) + `</strong>，
  重复组 <strong>` + fmt.Sprint(len(groups)) + `</strong>，
  建议清理 <strong>` + fmt.Sprint(cleanupCount) + `</strong> 个（约 ` + humanBytes(cleanupBytes) + `），
  阈值 <strong>` + fmt.Sprintf("%.2f", meta.Threshold) + `</strong>
</div>
`)

	for _, g := range groups {
		fmt.Fprintf(&b, `<section class="group"><h2>Group #%d %s<span class="badge %s">%s</span> confidence=%.2f</h2><div class="row">`,
			g.GroupID, html.EscapeString(string(g.GroupType)),
			html.EscapeString(string(g.GroupType)), html.EscapeString(string(g.GroupType)),
			g.Confidence)
		for _, it := range g.Items {
			thumb := ""
			if it.ThumbPath != "" {
				src := it.ThumbPath
				if thumbRel != "" {
					if rel, err := filepath.Rel(thumbRel, it.ThumbPath); err == nil {
						src = rel
					}
				}
				thumb = fmt.Sprintf(`<img loading="lazy" src="%s" alt="">`, html.EscapeString(filepath.ToSlash(src)))
			} else {
				thumb = `<div class="muted" style="aspect-ratio:4/3;display:flex;align-items:center;justify-content:center">无预览</div>`
			}
			fmt.Fprintf(&b, `<div class="item">%s<div class="meta">
<div class="action %s">%s</div>
<div class="scores">sim=%.2f · quality=%.2f · %s</div>
<div class="path">%s</div>
<div class="muted" style="margin-top:4px">%s</div>
</div></div>`,
				thumb,
				html.EscapeString(string(it.Action)), html.EscapeString(string(it.Action)),
				it.Similarity, it.QualityScore, humanBytes(it.SizeBytes),
				html.EscapeString(it.Path),
				html.EscapeString(strings.Join(it.Reasons, "; ")),
			)
		}
		b.WriteString(`</div></section>`)
	}

	if len(errs) > 0 {
		b.WriteString(`<section class="group errors"><h2>Errors</h2><table><tr><th>Path</th><th>Stage</th><th>Message</th></tr>`)
		for _, e := range errs {
			fmt.Fprintf(&b, `<tr><td>%s</td><td>%s</td><td>%s</td></tr>`,
				html.EscapeString(e.Path), html.EscapeString(e.Stage), html.EscapeString(e.Message))
		}
		b.WriteString(`</table></section>`)
	}

	b.WriteString(`</body></html>`)
	return b.String()
}

// WriteFile writes content to path, creating parent dirs.
func WriteFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
