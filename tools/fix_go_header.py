"""Заменяет шапку Go-файла на корректный UTF-8 текст.

Зачем: часть файлов ранее прошла через PowerShell Set-Content и получила
двойную перекодировку (кириллица превратилась в «Р РѕРјР°РЅРґР°»).
Скрипт читает файл как UTF-8, отбрасывает ведущий блок комментариев и
вставляет новый, записывая результат в UTF-8 без BOM и с LF.

    python tools/fix_go_header.py <файл> <файл-с-шапкой.md>

Шапка во втором файле должна начинаться с «// ».
"""

import sys
from pathlib import Path


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: fix_go_header.py <target.go> <header.md>", file=sys.stderr)
        return 2

    target = Path(sys.argv[1])
    header = Path(sys.argv[2]).read_text(encoding="utf-8").rstrip("\n")

    lines = target.read_text(encoding="utf-8").split("\n")

    # Ищем первую строку, которая не комментарий и не пустая.
    start = 0
    while start < len(lines) and (lines[start].startswith("//") or not lines[start].strip()):
        start += 1

    body = "\n".join(lines[start:])
    target.write_text(header + "\n" + body if body else header + "\n", encoding="utf-8", newline="\n")
    print(f"ok: {target} (шапка {start} строк)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
