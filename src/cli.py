from __future__ import annotations

import argparse
import shutil
from collections.abc import Sequence
from pathlib import Path

from rich.console import Console

from src.cache import Cache
from src.config import (
    DEFAULT_CACHE_PATH,
    DEFAULT_SIMILARITY_THRESHOLD,
    DEFAULT_WORKERS,
)
from src.models import ReportError, ReportGroup
from src.pipeline import load_last_report_groups, scan_paths
from src.reports import (
    render_html_report,
    render_json_report,
    render_text_report,
)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="media-dedupe")
    subparsers = parser.add_subparsers(dest="command", required=True)

    scan = subparsers.add_parser("scan", help="scan directories for duplicate media")
    scan.add_argument("paths", nargs="+")
    scan.add_argument("--similarity", type=float, default=DEFAULT_SIMILARITY_THRESHOLD)
    scan.add_argument("--workers", type=_positive_int, default=DEFAULT_WORKERS)
    scan.add_argument("--cache", default=DEFAULT_CACHE_PATH)
    scan.add_argument("--output")
    scan.add_argument("--format", choices=("text", "json", "html"), default="text")
    scan.add_argument("--no-video", action="store_true")
    scan.add_argument("--no-image", action="store_true")
    scan.add_argument(
        "--recursive", action=argparse.BooleanOptionalAction, default=True
    )

    report = subparsers.add_parser("report", help="render the last scan report")
    report.add_argument("--cache", default=DEFAULT_CACHE_PATH)
    report.add_argument("--format", choices=("text", "json", "html"), default="text")
    report.add_argument("--output")

    cache = subparsers.add_parser("cache", help="inspect or clear cache")
    cache_subparsers = cache.add_subparsers(dest="cache_command", required=True)
    cache_info = cache_subparsers.add_parser("info", help="show cache information")
    cache_info.add_argument("--cache", default=argparse.SUPPRESS)
    cache_clear = cache_subparsers.add_parser("clear", help="clear cache database")
    cache_clear.add_argument("--cache", default=argparse.SUPPRESS)
    cache.add_argument("--cache", default=DEFAULT_CACHE_PATH)

    subparsers.add_parser("doctor", help="check runtime dependencies")
    return parser


def run(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    console = Console(width=10_000, soft_wrap=True)

    if args.command == "doctor":
        return _run_doctor(console)

    if args.command == "scan":
        groups = scan_paths(
            [Path(path) for path in args.paths],
            cache_path=Path(args.cache),
            similarity_threshold=args.similarity,
            recursive=args.recursive,
            include_images=not args.no_image,
            include_videos=not args.no_video,
            workers=args.workers,
        )
        errors = Cache(Path(args.cache)).load_latest_errors()
        rendered = _render_groups(groups, args.format, errors)
        _write_report_if_requested(rendered, args.format, args.output)
        console.print(rendered, end="", markup=False)
        return 0

    if args.command == "report":
        cache = Cache(Path(args.cache))
        groups = load_last_report_groups(cache)
        rendered = _render_groups(groups, args.format, cache.load_latest_errors())
        _write_report_if_requested(rendered, args.format, args.output)
        console.print(rendered, end="", markup=False)
        return 0

    if args.command == "cache":
        cache_path = Path(args.cache)
        if args.cache_command == "info":
            console.print(f"cache path: {cache_path}", markup=False)
            console.print(f"exists: {cache_path.exists()}", markup=False)
            return 0
        if args.cache_command == "clear":
            if not _is_safe_cache_path(cache_path):
                console.print(
                    f"refusing to clear non-cache file: {cache_path}", markup=False
                )
                return 1
            if cache_path.exists():
                cache_path.unlink()
            console.print(f"cleared cache: {cache_path}", markup=False)
            return 0

    parser.error(f"unsupported command: {args.command}")
    return 2


def _run_doctor(console: Console) -> int:
    console.print("media-dedupe doctor")
    ffmpeg = shutil.which("ffmpeg")
    ffprobe = shutil.which("ffprobe")
    console.print(f"ffmpeg: {ffmpeg or 'missing'}")
    console.print(f"ffprobe: {ffprobe or 'missing'}")
    if ffmpeg is None or ffprobe is None:
        console.print("Install ffmpeg on macOS with: brew install ffmpeg")
    return 0


def _render_groups(
    groups: list[ReportGroup],
    report_format: str,
    errors: list[ReportError] | None = None,
) -> str:
    if report_format == "json":
        return render_json_report(groups, errors=errors)
    if report_format == "html":
        return render_html_report(groups, errors=errors)
    return render_text_report(groups, errors=errors)


def _write_report_if_requested(
    content: str, report_format: str, output: str | None
) -> None:
    if output is None:
        return
    output_path = Path(output)
    if output_path.suffix:
        output_path.parent.mkdir(parents=True, exist_ok=True)
        report_path = output_path
    else:
        output_path.mkdir(parents=True, exist_ok=True)
        report_path = output_path / f"media-dedupe-report.{report_format}"
    report_path.write_text(content, encoding="utf-8")


def _is_safe_cache_path(cache_path: Path) -> bool:
    return cache_path.name in {"cache.sqlite", "fingerprints.db"} or (
        cache_path.parent.name == ".media-dedupe"
        and cache_path.suffix in {".sqlite", ".db"}
    )


def _positive_int(value: str) -> int:
    parsed = int(value)
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be at least 1")
    return parsed


def main() -> None:
    raise SystemExit(run())
