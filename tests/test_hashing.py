from __future__ import annotations

from pathlib import Path

from media_dedupe.hashing import file_sha256, hamming_distance, hash_similarity


def test_file_sha256_matches_identical_files(tmp_path: Path) -> None:
    first = tmp_path / "a.bin"
    second = tmp_path / "b.bin"
    first.write_bytes(b"same content")
    second.write_bytes(b"same content")

    assert file_sha256(first) == file_sha256(second)


def test_hamming_distance_counts_different_bits() -> None:
    assert hamming_distance("0f", "00") == 4


def test_hash_similarity_returns_one_for_identical_hashes() -> None:
    assert hash_similarity("ffff", "ffff") == 1.0
    assert hash_similarity("ffff", "0000") == 0.0
