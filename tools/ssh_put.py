"""Кладёт локальный файл на сервер по SFTP.

Зачем: раньше бинарник заливался через base64 в heredoc, это медленно и
раздувает поток в четыре раза. SFTP проще и быстрее.

    python tools/ssh_put.py <локальный> <удалённый>

Параметры подключения берутся из окружения: PVE_HOST, PVE_PORT, PVE_USER, PVE_PASS.
"""

import os
import sys
from pathlib import Path

import paramiko


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: ssh_put.py <local> <remote>", file=sys.stderr)
        return 2

    local = Path(sys.argv[1])
    remote = sys.argv[2]
    if not local.is_file():
        print(f"нет файла: {local}", file=sys.stderr)
        return 2

    host = os.environ["PVE_HOST"]
    port = int(os.environ.get("PVE_PORT", "22"))
    user = os.environ["PVE_USER"]
    password = os.environ["PVE_PASS"]

    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(host, port=port, username=user, password=password, timeout=30)
    try:
        sftp = client.open_sftp()
        try:
            sftp.put(str(local), remote, confirm=True)
            size = sftp.stat(remote).st_size
        finally:
            sftp.close()
    finally:
        client.close()

    print(f"ok: {local.name} -> {remote} ({size} байт)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
