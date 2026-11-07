"""Добавляет BOM в начало PowerShell-скриптов.

Зачем: Windows PowerShell 5.1 без BOM читает файл как ANSI, и кириллица в
скрипте ломает разбор (ошибки вида «Unexpected token»). UTF-8 с BOM читают
правильно и 5.1, и PowerShell 7.

    python tools/ps1_bom.py scripts/*.ps1
"""

import sys
from pathlib import Path

BOM = b"\xef\xbb\xbf"


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: ps1_bom.py <file.ps1> [...]", file=sys.stderr)
        return 2

    changed = 0
    for item in sys.argv[1:]:
        path = Path(item)
        data = path.read_bytes()
        if data.startswith(BOM):
            continue
        text = data.decode("utf-8")
        path.write_bytes(BOM + text.encode("utf-8"))
        changed += 1
        print(f"bom: {path}")

    print(f"готово, файлов изменено: {changed}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
