package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"media-dedupe/internal/cache"
	"media-dedupe/internal/config"
	"media-dedupe/internal/fsutil"
	"media-dedupe/internal/model"
	"media-dedupe/internal/pipeline"
	"media-dedupe/internal/report"
	"media-dedupe/internal/video"
)

func NewRootCmd() *cobra.Command {
	var (
		cachePath      string
		similarity     float64
		workers        int
		videoWorkers   int
		textWorkers    int
		frames         int
		textSimilarity float64
		output         string
		format         string
		noImage        bool
		noVideo        bool
		noText         bool
		recursive      bool
		noThumbs       bool
		thumbDir       string
	)

	root := &cobra.Command{
		Use:   "media-dedupe",
		Short: "Find exact and near-duplicate images/videos/TXT locally",
		Long: "Local CLI that scans directories for exact and near-duplicate media and text.\n" +
			"It never deletes files; it only writes cache and reports.",
	}

	scan := &cobra.Command{
		Use:   "scan [paths...]",
		Short: "Scan directories for duplicate media",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !noImage && !noVideo {
				// both default on
			}
			includeImage := !noImage
			includeVideo := !noVideo
			includeText := !noText
			if noImage && noVideo && noText {
				return fmt.Errorf("cannot disable all media types")
			}

			cachePath = fsutil.ExpandPath(cachePath)

			outPath, err := resolveOutputPath(format, output, config.DefaultReportDir())
			if err != nil {
				return err
			}
			if thumbDir == "" {
				// Keep thumbs next to HTML so file:// relative paths work.
				if format == "html" {
					thumbDir = filepath.Join(filepath.Dir(outPath), "thumbs")
				} else {
					thumbDir = config.DefaultThumbDir()
				}
			}
			thumbDir = fsutil.ExpandPath(thumbDir)

			opts := pipeline.Options{
				Paths:         args,
				CachePath:     cachePath,
				ThumbDir:      thumbDir,
				Threshold:     similarity,
				Recursive:     recursive,
				IncludeImages: includeImage,
				IncludeVideos: includeVideo,
				IncludeTexts:  includeText,
				TextThreshold: textSimilarity,
				TextWorkers:   textWorkers,
				Workers:       workers,
				VideoWorkers:  videoWorkers,
				FrameCount:    frames,
				EnableThumbs:  !noThumbs && format == "html",
				OnProgress: func(msg string) {
					fmt.Fprintln(os.Stderr, "[media-dedupe]", msg)
				},
			}

			result, err := pipeline.Scan(opts)
			if err != nil {
				return err
			}

			var content string
			switch format {
			case "text":
				content = report.RenderText(result.Groups, result.Errors, result.Meta)
				if output == "" {
					fmt.Print(content)
					return nil
				}
			case "json":
				content, err = report.RenderJSON(result.Groups, result.Errors, result.Meta)
				if err != nil {
					return err
				}
			case "html":
				content = report.RenderHTML(result.Groups, result.Errors, result.Meta, filepath.Dir(outPath))
			default:
				return fmt.Errorf("unsupported format: %s", format)
			}

			if err := report.WriteFile(outPath, content); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "[media-dedupe] wrote %s\n", outPath)
			if format == "text" {
				fmt.Print(content)
			}
			return nil
		},
	}

	scan.Flags().StringVar(&cachePath, "cache", config.DefaultCachePath(), "sqlite cache path")
	scan.Flags().Float64Var(&similarity, "similarity", config.DefaultSimilarityThreshold, "similarity threshold 0..1")
	scan.Flags().IntVar(&workers, "workers", config.DefaultWorkers, "image/hash workers (HDD-friendly default)")
	scan.Flags().IntVar(&videoWorkers, "video-workers", config.DefaultVideoWorkers, "video frame extraction workers")
	scan.Flags().IntVar(&textWorkers, "text-workers", config.DefaultTextWorkers, "text workers")
	scan.Flags().IntVar(&frames, "frames", config.DefaultFrameCount, "video frames to sample")
	scan.Flags().Float64Var(&textSimilarity, "text-similarity", config.DefaultTextSimilarityThreshold, "text similarity threshold 0..1")
	scan.Flags().StringVar(&output, "output", "", "output file or directory")
	scan.Flags().StringVar(&format, "format", "html", "output format: text|json|html")
	scan.Flags().BoolVar(&noImage, "no-image", false, "skip images")
	scan.Flags().BoolVar(&noVideo, "no-video", false, "skip videos")
	scan.Flags().BoolVar(&noText, "no-text", false, "skip txt files")
	scan.Flags().BoolVar(&recursive, "recursive", true, "recurse into subdirectories")
	scan.Flags().BoolVar(&noThumbs, "no-thumbs", false, "skip thumbnail generation")
	scan.Flags().StringVar(&thumbDir, "thumb-dir", "", "thumbnail directory (default cache dir/thumbs)")

	reportCmd := &cobra.Command{
		Use:   "report",
		Short: "Render the last persisted scan report",
		RunE: func(cmd *cobra.Command, args []string) error {
			cachePath = fsutil.ExpandPath(cachePath)
			c, err := cache.Open(cachePath)
			if err != nil {
				return err
			}
			defer c.Close()
			groups, err := c.LoadReportGroups()
			if err != nil {
				return err
			}
			runID, ok, err := c.LatestScanRunID()
			if err != nil {
				return err
			}
			reportErrs := mustLoadErrors(c, runID, ok)
			meta := resultMetaFromCache(c, runID, ok, similarity, cachePath)

			outPath, err := resolveOutputPath(format, output, config.DefaultReportDir())
			if err != nil {
				return err
			}
			var content string
			switch format {
			case "text":
				content = report.RenderText(groups, reportErrs, meta)
				if output == "" {
					fmt.Print(content)
					return nil
				}
			case "json":
				content, err = report.RenderJSON(groups, reportErrs, meta)
				if err != nil {
					return err
				}
			case "html":
				content = report.RenderHTML(groups, reportErrs, meta, filepath.Dir(outPath))
			default:
				return fmt.Errorf("unsupported format: %s", format)
			}
			if err := report.WriteFile(outPath, content); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "[media-dedupe] wrote %s\n", outPath)
			return nil
		},
	}
	reportCmd.Flags().StringVar(&cachePath, "cache", config.DefaultCachePath(), "sqlite cache path")
	reportCmd.Flags().StringVar(&output, "output", "", "output file or directory")
	reportCmd.Flags().StringVar(&format, "format", "html", "output format: text|json|html")

	cacheCmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect or clear cache",
	}
	cacheInfo := &cobra.Command{
		Use:   "info",
		Short: "Show cache information",
		RunE: func(cmd *cobra.Command, args []string) error {
			cachePath = fsutil.ExpandPath(cachePath)
			fmt.Printf("cache path: %s\n", cachePath)
			fmt.Printf("exists: %v\n", fsutil.FileExists(cachePath))
			if !fsutil.FileExists(cachePath) {
				return nil
			}
			c, err := cache.Open(cachePath)
			if err != nil {
				return err
			}
			defer c.Close()
			info, err := c.Info()
			if err != nil {
				return err
			}
			for _, k := range []string{"files", "file_hashes", "media_metadata", "perceptual_hashes", "text_facts", "duplicate_groups", "duplicate_items", "scan_runs", "errors"} {
				fmt.Printf("%s: %d\n", k, info[k])
			}
			return nil
		},
	}
	cacheClear := &cobra.Command{
		Use:   "clear",
		Short: "Clear cache database",
		RunE: func(cmd *cobra.Command, args []string) error {
			cachePath = fsutil.ExpandPath(cachePath)
			if !fsutil.FileExists(cachePath) {
				fmt.Println("cache does not exist, nothing to clear")
				return nil
			}
			c, err := cache.Open(cachePath)
			if err != nil {
				return err
			}
			if err := c.Clear(); err != nil {
				c.Close()
				return err
			}
			c.Close()
			fmt.Printf("cleared cache at %s\n", cachePath)
			return nil
		},
	}
	cacheCmd.PersistentFlags().StringVar(&cachePath, "cache", config.DefaultCachePath(), "sqlite cache path")
	cacheCmd.AddCommand(cacheInfo, cacheClear)

	doctor := &cobra.Command{
		Use:   "doctor",
		Short: "Check runtime dependencies",
		RunE: func(cmd *cobra.Command, args []string) error {
			ok := true
			fmt.Printf("go binary: %s\n", os.Args[0])
			cacheDir := config.DefaultCacheDir()
			if err := fsutil.EnsureDir(cacheDir); err != nil {
				fmt.Printf("cache dir: FAIL (%s): %v\n", cacheDir, err)
				ok = false
			} else {
				fmt.Printf("cache dir: OK (%s)\n", cacheDir)
			}
			ffmpeg, ffprobe := video.Available()
			fmt.Printf("ffmpeg: %s\n", pathStatus(ffmpeg))
			fmt.Printf("ffprobe: %s\n", pathStatus(ffprobe))
			if !ffmpeg || !ffprobe {
				fmt.Println("note: video similarity requires ffmpeg and ffprobe on PATH or common install locations")
			}
			if err := exec.Command("go", "version").Run(); err == nil {
				// informational only
			}
			if !ok {
				return fmt.Errorf("doctor found problems")
			}
			return nil
		},
	}

	root.AddCommand(scan, reportCmd, cacheCmd, doctor)
	return root
}

func Execute() {
	root := NewRootCmd()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func pathStatus(ok bool) string {
	if ok {
		return "OK"
	}
	return "MISSING"
}

func resolveOutputPath(format, output, defaultDir string) (string, error) {
	if output == "" {
		if err := os.MkdirAll(defaultDir, 0o755); err != nil {
			return "", err
		}
		switch format {
		case "json":
			return filepath.Join(defaultDir, "latest.json"), nil
		case "html":
			return filepath.Join(defaultDir, "latest.html"), nil
		default:
			return filepath.Join(defaultDir, "latest.txt"), nil
		}
	}
	output = fsutil.ExpandPath(output)
	// Treat as directory when it exists as one, ends with separator,
	// or has no report-file extension.
	ext := strings.ToLower(filepath.Ext(output))
	isReportFile := ext == ".json" || ext == ".html" || ext == ".txt" || ext == ".htm"
	if !isReportFile {
		if err := os.MkdirAll(output, 0o755); err != nil {
			return "", err
		}
		switch format {
		case "json":
			return filepath.Join(output, "latest.json"), nil
		case "html":
			return filepath.Join(output, "latest.html"), nil
		default:
			return filepath.Join(output, "latest.txt"), nil
		}
	}
	return output, nil
}

func mustLoadErrors(c *cache.Cache, runID int64, ok bool) []model.ReportError {
	if !ok {
		return nil
	}
	errs, err := c.LoadErrors(&runID)
	if err != nil {
		return nil
	}
	return errs
}

func resultMetaFromCache(c *cache.Cache, runID int64, ok bool, threshold float64, cachePath string) model.ScanMeta {
	meta := model.ScanMeta{
		CachePath: cachePath,
		Threshold: threshold,
	}
	if !ok {
		return meta
	}
	// lightweight: files_seen from scan_runs
	var seen, failed int
	_ = c.DB().QueryRow(`SELECT files_seen, files_failed FROM scan_runs WHERE id = ?`, runID).Scan(&seen, &failed)
	meta.FilesSeen = seen
	meta.FilesFailed = failed
	return meta
}
