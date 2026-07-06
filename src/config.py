from __future__ import annotations

DEFAULT_SIMILARITY_THRESHOLD = 0.80
DEFAULT_WORKERS = 4
DEFAULT_CACHE_PATH = ".media-dedupe/cache.sqlite"

IMAGE_EXTENSIONS = frozenset(
    {
        ".jpg",
        ".jpeg",
        ".png",
        ".webp",
        ".heic",
        ".heif",
        ".tiff",
        ".bmp",
        ".gif",
    }
)

VIDEO_EXTENSIONS = frozenset(
    {
        ".mp4",
        ".mov",
        ".mkv",
        ".avi",
        ".webm",
        ".m4v",
        ".flv",
        ".wmv",
        ".mpeg",
        ".mpg",
    }
)

SKIPPED_DIRECTORY_NAMES = frozenset({".git", ".venv", "__pycache__", ".DS_Store"})
