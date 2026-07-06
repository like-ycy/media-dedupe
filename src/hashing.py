from __future__ import annotations

import hashlib
from pathlib import Path


def file_sha256(path: Path, *, chunk_size: int = 1024 * 1024) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as file:
        while chunk := file.read(chunk_size):
            digest.update(chunk)
    return digest.hexdigest()


def hamming_distance(left_hex: str, right_hex: str) -> int:
    if len(left_hex) != len(right_hex):
        raise ValueError("hashes must have the same length")
    left = int(left_hex, 16)
    right = int(right_hex, 16)
    return (left ^ right).bit_count()


def hash_similarity(left_hex: str, right_hex: str) -> float:
    bit_count = len(left_hex) * 4
    if bit_count == 0:
        raise ValueError("hashes must not be empty")
    distance = hamming_distance(left_hex, right_hex)
    return max(0.0, 1.0 - (distance / bit_count))
