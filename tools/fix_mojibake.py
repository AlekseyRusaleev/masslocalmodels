"""Починка двойной перекодировки UTF-8 → CP1251 в текстовых файлах.

Зачем: PowerShell 5.1 `Get-Content -Raw` читает UTF-8 как ANSI (CP1251), и
после обратной записи кириллица превращается в «РџСЂРёРІРµС‚». Обратное
преобразование (текст → CP1251 байты → UTF-8) возвращает исходник.

    python tools/fix_mojibake.py internal/worker/mesh.go
"""

import sys
from pathlib import Path

MARKERS = ("Рџ", "Р°", "Рµ", "РЅ", "СЃ", "вЂ")


def looks_broken(text: str) -> bool:
    return any(marker in text for marker in MARKERS)


def repair(text: str) -> str | None:
    try:
        return text.encode("cp1251").decode("utf-8")
    except (UnicodeEncodeError, UnicodeDecodeError):
        return None


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: fix_mojibake.py <file> [...]", file=sys.stderr)
        return 2

    for item in sys.argv[1:]:
        path = Path(item)
        raw = path.read_bytes()
        try:
            text = raw.decode("utf-8")
        except UnicodeDecodeError:
            print(f"skip {path}: не UTF-8")
            continue
        if not looks_broken(text):
            print(f"ok   {path}: перекодировки нет")
            continue
        fixed = repair(text)
        if fixed is None:
            print(f"skip {path}: обратное преобразование невозможно")
            continue
        if looks_broken(fixed):
            print(f"skip {path}: после починки остались маркеры")
            continue
        path.write_bytes(fixed.encode("utf-8"))
        print(f"fix  {path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
