"""Shared filesystem identity, independent of storage and Git execution."""

import hashlib
from pathlib import Path


def digest_path(value: str) -> str:
    return hashlib.sha256(value.encode()).hexdigest()[:32]


def name_part(value: str) -> str:
    name = "".join(
        chr(c) if 97 <= c <= 122 or 48 <= c <= 57 or c in b"_.~" else f"%{c:02X}"
        for c in value.encode()
    )
    if name in ("", ".", ".."):
        name = name.replace(".", "%2E") or "%00"
    return name if len(name) <= 180 else name[:120] + "--" + digest_path(value)


def repository_key(forge: str, path: str) -> str:
    name = "--".join(name_part(p) for p in (forge, *path.split("/")))
    return name if len(name) <= 240 else name[:160] + "--" + digest_path(forge + "/" + path)


def local_key(common: str) -> str:
    directory = Path(common)
    label = directory.parent.name if directory.name == ".git" else directory.name
    return f"local--{name_part(label)}--{hashlib.sha256(common.encode()).hexdigest()[:16]}"
