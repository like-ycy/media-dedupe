from __future__ import annotations

import os
from pathlib import Path

import pytest

from src.discovery import discover_media_files
from src.models import MediaType


def test_discover_media_files_finds_supported_media(tmp_path: Path) -> None:
    image = tmp_path / "photo.JPG"
    video = tmp_path / "clip.mp4"
    text = tmp_path / "notes.txt"
    image.write_bytes(b"image")
    video.write_bytes(b"video")
    text.write_text("notes", encoding="utf-8")

    discovered = list(discover_media_files([tmp_path], recursive=True))

    assert [item.path for item in discovered] == [video, image]
    assert [item.media_type for item in discovered] == [
        MediaType.VIDEO,
        MediaType.IMAGE,
    ]


def test_discover_media_files_skips_hidden_and_venv_directories(tmp_path: Path) -> None:
    visible = tmp_path / "visible.png"
    hidden_dir = tmp_path / ".hidden"
    venv_dir = tmp_path / ".venv"
    hidden_dir.mkdir()
    venv_dir.mkdir()
    visible.write_bytes(b"visible")
    (hidden_dir / "secret.png").write_bytes(b"secret")
    (venv_dir / "library.jpg").write_bytes(b"library")

    discovered = list(discover_media_files([tmp_path], recursive=True))

    assert [item.path for item in discovered] == [visible]


def test_discover_media_files_does_not_follow_directory_symlinks(
    tmp_path: Path,
) -> None:
    outside = tmp_path / "outside"
    root = tmp_path / "root"
    outside.mkdir()
    root.mkdir()
    (outside / "outside.png").write_bytes(b"outside")
    (root / "inside.png").write_bytes(b"inside")
    symlink = root / "linked-outside"
    try:
        os.symlink(outside, symlink, target_is_directory=True)
    except OSError:
        pytest.skip("directory symlinks are not supported on this platform")

    discovered = list(discover_media_files([root], recursive=True))

    assert [item.path for item in discovered] == [root / "inside.png"]
