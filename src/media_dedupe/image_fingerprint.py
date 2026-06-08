from __future__ import annotations

from pathlib import Path

import imagehash
from PIL import Image, UnidentifiedImageError

from media_dedupe.models import ImageMetadata

MAX_IMAGE_PIXELS = 100_000_000
Image.MAX_IMAGE_PIXELS = MAX_IMAGE_PIXELS


def read_image_metadata(path: Path) -> ImageMetadata:
    try:
        with Image.open(path) as image:
            width, height = image.size
            if width * height > MAX_IMAGE_PIXELS:
                return _unreadable_image_metadata()
            orientation = image.getexif().get(274)
            return ImageMetadata(
                width=width,
                height=height,
                format_name=image.format or path.suffix.lstrip(".").upper(),
                orientation=int(orientation) if orientation is not None else None,
                is_readable=True,
            )
    except OSError, UnidentifiedImageError, Image.DecompressionBombError:
        return _unreadable_image_metadata()


def compute_image_phash(path: Path, *, hash_size: int = 8) -> str:
    with Image.open(path) as image:
        width, height = image.size
        if width * height > MAX_IMAGE_PIXELS:
            raise OSError("image exceeds pixel limit")
        return str(imagehash.phash(image, hash_size=hash_size))


def _unreadable_image_metadata() -> ImageMetadata:
    return ImageMetadata(
        width=0, height=0, format_name="", orientation=None, is_readable=False
    )
