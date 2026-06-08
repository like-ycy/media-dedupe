from __future__ import annotations

from pathlib import Path

from PIL import Image

from media_dedupe.image_fingerprint import compute_image_phash, read_image_metadata


def test_read_image_metadata_returns_size_and_format(tmp_path: Path) -> None:
    image_path = tmp_path / "photo.png"
    Image.new("RGB", (32, 24), color="red").save(image_path)

    metadata = read_image_metadata(image_path)

    assert metadata.width == 32
    assert metadata.height == 24
    assert metadata.format_name == "PNG"
    assert metadata.is_readable is True


def test_compute_image_phash_is_stable_for_same_file(tmp_path: Path) -> None:
    image_path = tmp_path / "photo.png"
    Image.new("RGB", (32, 24), color="blue").save(image_path)

    first = compute_image_phash(image_path)
    second = compute_image_phash(image_path)

    assert first == second
    assert len(first) > 0


def test_read_image_metadata_rejects_images_over_pixel_limit(
    monkeypatch, tmp_path: Path
) -> None:
    image_path = tmp_path / "large.png"
    Image.new("RGB", (4, 4), color="blue").save(image_path)
    monkeypatch.setattr("media_dedupe.image_fingerprint.MAX_IMAGE_PIXELS", 10)

    metadata = read_image_metadata(image_path)

    assert metadata.is_readable is False
