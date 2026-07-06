from __future__ import annotations

from collections.abc import Callable, Iterable, Iterator
from pathlib import Path

from src.config import (
    IMAGE_EXTENSIONS,
    SKIPPED_DIRECTORY_NAMES,
    VIDEO_EXTENSIONS,
)
from src.models import DiscoveredFile, MediaType

DiscoveryErrorHandler = Callable[[Path, str, str], None]


def classify_media_type(path: Path) -> MediaType | None:
    suffix = path.suffix.lower()
    if suffix in IMAGE_EXTENSIONS:
        return MediaType.IMAGE
    if suffix in VIDEO_EXTENSIONS:
        return MediaType.VIDEO
    return None


def discover_media_files(
    paths: Iterable[Path],
    *,
    recursive: bool,
    on_error: DiscoveryErrorHandler | None = None,
) -> Iterator[DiscoveredFile]:
    seen_real_paths: set[Path] = set()
    for root in sorted(Path(path).expanduser() for path in paths):
        try:
            is_file = root.is_file()
            exists = root.exists()
        except OSError as error:
            _record_discovery_error(on_error, root, "discovery", str(error))
            continue
        if is_file:
            yield from _discover_file(root, seen_real_paths, on_error)
            continue
        if not exists:
            _record_discovery_error(on_error, root, "discovery", "path does not exist")
            continue
        if recursive:
            yield from _walk_directory(root, seen_real_paths, on_error)
        else:
            try:
                children = sorted(root.iterdir())
            except OSError as error:
                _record_discovery_error(on_error, root, "discovery", str(error))
                continue
            for child in children:
                try:
                    is_child_file = child.is_file()
                except OSError as error:
                    _record_discovery_error(on_error, child, "discovery", str(error))
                    continue
                if is_child_file:
                    yield from _discover_file(child, seen_real_paths, on_error)


def _walk_directory(
    root: Path,
    seen_real_paths: set[Path],
    on_error: DiscoveryErrorHandler | None,
) -> Iterator[DiscoveredFile]:
    try:
        children = sorted(root.iterdir())
    except OSError as error:
        _record_discovery_error(on_error, root, "discovery", str(error))
        return
    for child in children:
        try:
            is_symlink_dir = child.is_symlink() and child.is_dir()
            is_dir = child.is_dir()
            is_file = child.is_file()
        except OSError as error:
            _record_discovery_error(on_error, child, "discovery", str(error))
            continue
        if is_symlink_dir:
            continue
        if is_dir:
            if child.name.startswith(".") or child.name in SKIPPED_DIRECTORY_NAMES:
                continue
            yield from _walk_directory(child, seen_real_paths, on_error)
        elif is_file:
            yield from _discover_file(child, seen_real_paths, on_error)


def _discover_file(
    path: Path,
    seen_real_paths: set[Path],
    on_error: DiscoveryErrorHandler | None,
) -> Iterator[DiscoveredFile]:
    media_type = classify_media_type(path)
    if media_type is None:
        return

    try:
        real_path = path.resolve(strict=True)
        stat = path.stat()
    except OSError as error:
        _record_discovery_error(on_error, path, "discovery", str(error))
        return

    if real_path in seen_real_paths:
        return
    seen_real_paths.add(real_path)

    yield DiscoveredFile(
        path=path,
        media_type=media_type,
        extension=path.suffix.lower(),
        size_bytes=stat.st_size,
        mtime_ns=stat.st_mtime_ns,
        inode=stat.st_ino,
        device=stat.st_dev,
    )


def _record_discovery_error(
    on_error: DiscoveryErrorHandler | None,
    path: Path,
    stage: str,
    message: str,
) -> None:
    if on_error is not None:
        on_error(path, stage, message)
