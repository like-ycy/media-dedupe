from __future__ import annotations

from pathlib import Path

from PIL import Image

from media_dedupe.cache import Cache
from media_dedupe.pipeline import load_last_report_groups, scan_paths


def test_scan_persists_groups_for_report_command(tmp_path: Path) -> None:
    first = tmp_path / "a.png"
    second = tmp_path / "b.png"
    Image.new("RGB", (16, 16), color="red").save(first)
    second.write_bytes(first.read_bytes())
    cache_path = tmp_path / "cache.sqlite"

    scan_paths(
        [tmp_path], cache_path=cache_path, similarity_threshold=0.80, recursive=True
    )
    groups = load_last_report_groups(Cache(cache_path))

    assert len(groups) == 1
    assert groups[0].confidence == 1.0
    assert len(groups[0].items) == 2
    assert groups[0].recommended_file_id in {item.file_id for item in groups[0].items}
    assert {item.action.value for item in groups[0].items} == {
        "keep_recommended",
        "cleanup_candidate",
    }
    assert all(item.reasons for item in groups[0].items)
