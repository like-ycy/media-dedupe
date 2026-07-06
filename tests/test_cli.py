from __future__ import annotations

from pathlib import Path

import pytest
from PIL import Image

from src import cli
from src.cli import build_parser, run
from src.models import ReportGroup, VideoMetadata


def test_parser_accepts_scan_command() -> None:
    parser = build_parser()
    args = parser.parse_args(["scan", "~/Pictures", "~/Movies", "--format", "json"])

    assert args.command == "scan"
    assert args.paths == ["~/Pictures", "~/Movies"]
    assert args.format == "json"


def test_parser_rejects_non_positive_workers() -> None:
    parser = build_parser()

    with pytest.raises(SystemExit):
        parser.parse_args(["scan", "~/Pictures", "--workers", "0"])


def test_run_doctor_returns_success(capsys: pytest.CaptureFixture[str]) -> None:
    exit_code = run(["doctor"])

    captured = capsys.readouterr()
    assert exit_code == 0
    assert "media-dedupe doctor" in captured.out


def test_run_scan_outputs_json(
    capsys: pytest.CaptureFixture[str], tmp_path: Path
) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())

    exit_code = run(
        [
            "scan",
            str(tmp_path),
            "--cache",
            str(tmp_path / "cache.sqlite"),
            "--format",
            "json",
        ]
    )

    captured = capsys.readouterr()
    assert exit_code == 0
    assert '"type": "exact"' in captured.out


def test_run_scan_respects_no_image_flag(
    capsys: pytest.CaptureFixture[str], tmp_path: Path
) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())

    exit_code = run(
        [
            "scan",
            str(tmp_path),
            "--cache",
            str(tmp_path / "cache.sqlite"),
            "--format",
            "json",
            "--no-image",
        ]
    )

    captured = capsys.readouterr()
    assert exit_code == 0
    assert '"groups": []' in captured.out


def test_run_scan_passes_workers_to_pipeline(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    captured_workers: list[int] = []

    def fake_scan_paths(
        paths: list[Path],
        *,
        cache_path: Path,
        similarity_threshold: float,
        recursive: bool,
        include_images: bool = True,
        include_videos: bool = True,
        workers: int,
    ) -> list[ReportGroup]:
        captured_workers.append(workers)
        return []

    monkeypatch.setattr(cli, "scan_paths", fake_scan_paths)

    exit_code = run(["scan", str(tmp_path), "--workers", "3"])

    assert exit_code == 0
    assert captured_workers == [3]


def test_run_scan_writes_json_report_to_output_directory(tmp_path: Path) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    output_dir = tmp_path / "reports"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())

    exit_code = run(
        [
            "scan",
            str(tmp_path),
            "--cache",
            str(tmp_path / "cache.sqlite"),
            "--format",
            "json",
            "--output",
            str(output_dir),
        ]
    )

    report_path = output_dir / "media-dedupe-report.json"
    assert exit_code == 0
    assert report_path.exists()
    assert '"type": "exact"' in report_path.read_text(encoding="utf-8")


def test_run_scan_outputs_html_when_requested(
    capsys: pytest.CaptureFixture[str], tmp_path: Path
) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())

    exit_code = run(
        [
            "scan",
            str(tmp_path),
            "--cache",
            str(tmp_path / "cache.sqlite"),
            "--format",
            "html",
        ]
    )

    captured = capsys.readouterr()
    assert exit_code == 0
    assert "<html" in captured.out
    assert "exact" in captured.out


def test_cache_info_accepts_cache_argument_after_subcommand(
    capsys: pytest.CaptureFixture[str], tmp_path: Path
) -> None:
    cache_path = tmp_path / "cache.sqlite"
    cache_path.write_bytes(b"sqlite")

    exit_code = run(["cache", "info", "--cache", str(cache_path)])

    captured = capsys.readouterr()
    assert exit_code == 0
    assert str(cache_path) in captured.out


def test_cache_info_accepts_cache_argument_before_subcommand(
    capsys: pytest.CaptureFixture[str], tmp_path: Path
) -> None:
    cache_path = tmp_path / "cache.sqlite"
    cache_path.write_bytes(b"sqlite")

    exit_code = run(["cache", "--cache", str(cache_path), "info"])

    captured = capsys.readouterr()
    assert exit_code == 0
    assert str(cache_path) in captured.out


def test_cache_clear_accepts_cache_argument_before_subcommand(tmp_path: Path) -> None:
    cache_path = tmp_path / "cache.sqlite"
    cache_path.write_bytes(b"sqlite")

    exit_code = run(["cache", "--cache", str(cache_path), "clear"])

    assert exit_code == 0
    assert not cache_path.exists()


def test_run_scan_respects_no_video_flag(
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
) -> None:
    first = tmp_path / "clip_a.mp4"
    second = tmp_path / "clip_b.mp4"
    first.write_bytes(b"video-a")
    second.write_bytes(b"video-b")

    monkeypatch.setattr(
        "src.pipeline.ffprobe_metadata",
        lambda path: VideoMetadata(
            10_000, 1920, 1080, 30.0, 4_000_000, "h264", True, True
        ),
    )
    monkeypatch.setattr(
        "src.pipeline.compute_video_frame_hashes",
        lambda path, *, metadata=None: ["ffff", "ff0f", "f0ff"],
    )

    exit_code = run(
        [
            "scan",
            str(tmp_path),
            "--cache",
            str(tmp_path / "cache.sqlite"),
            "--format",
            "json",
            "--no-video",
        ]
    )

    captured = capsys.readouterr()
    assert exit_code == 0
    assert '"groups": []' in captured.out


def test_run_scan_writes_report_to_explicit_file_path(tmp_path: Path) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    report_path = tmp_path / "reports" / "latest.json"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())

    exit_code = run(
        [
            "scan",
            str(tmp_path),
            "--cache",
            str(tmp_path / "cache.sqlite"),
            "--format",
            "json",
            "--output",
            str(report_path),
        ]
    )

    assert exit_code == 0
    assert report_path.exists()
    assert '"type": "exact"' in report_path.read_text(encoding="utf-8")


def test_run_report_outputs_persisted_groups(
    capsys: pytest.CaptureFixture[str], tmp_path: Path
) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    cache_path = tmp_path / "cache.sqlite"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())
    assert run(["scan", str(tmp_path), "--cache", str(cache_path)]) == 0

    exit_code = run(["report", "--cache", str(cache_path), "--format", "json"])

    captured = capsys.readouterr()
    assert exit_code == 0
    assert '"type": "exact"' in captured.out
    assert str(first) in captured.out


def test_cache_clear_refuses_non_sqlite_cache_file(
    capsys: pytest.CaptureFixture[str], tmp_path: Path
) -> None:
    important_file = tmp_path / "important.txt"
    important_file.write_text("do not delete", encoding="utf-8")

    exit_code = run(["cache", "clear", "--cache", str(important_file)])

    captured = capsys.readouterr()
    assert exit_code == 1
    assert important_file.exists()
    assert "refusing" in captured.out.lower()


def test_cache_clear_refuses_arbitrary_sqlite_file(
    capsys: pytest.CaptureFixture[str], tmp_path: Path
) -> None:
    important_file = tmp_path / "important.sqlite"
    important_file.write_text("do not delete", encoding="utf-8")

    exit_code = run(["cache", "clear", "--cache", str(important_file)])

    captured = capsys.readouterr()
    assert exit_code == 1
    assert important_file.exists()
    assert "refusing" in captured.out.lower()
