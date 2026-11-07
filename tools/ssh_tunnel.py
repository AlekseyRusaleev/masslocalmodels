#!/usr/bin/env python3
"""ssh_tunnel.py — локальный TCP-форвардер через SSH без sshpass.

Зачем: RPC-нода llama.cpp не имеет ни TLS, ни авторизации, поэтому её нельзя
публиковать в интернет. Туннель поднимается по SSH, порт на сервере слушает
только 127.0.0.1, а наружу смотрит только localhost на клиенте.

Использование:
    export PVE_HOST=... PVE_PORT=22 PVE_USER=root PVE_PASS=...
    python tools/ssh_tunnel.py --local-port 60053 --remote-port 50053

Работает до Ctrl+C. Пароль берётся из окружения и нигде не сохраняется.
"""

from __future__ import annotations

import argparse
import os
import socket
import sys
import threading

import paramiko


def pump(source: socket.socket | paramiko.Channel, target: socket.socket | paramiko.Channel) -> None:
    """Перекачивает байты в одну сторону до закрытия любой из сторон."""
    try:
        while True:
            data = source.recv(32768)
            if not data:
                break
            target.sendall(data)
    except Exception:  # noqa: BLE001
        pass
    finally:
        try:
            target.shutdown(socket.SHUT_WR)
        except Exception:  # noqa: BLE001
            pass


def handle(conn: socket.socket, transport: paramiko.Transport, remote_host: str, remote_port: int) -> None:
    try:
        channel = transport.open_channel(
            "direct-tcpip",
            (remote_host, remote_port),
            conn.getpeername(),
        )
    except Exception as exc:  # noqa: BLE001
        print(f"tunnel: cannot open channel: {exc}", file=sys.stderr)
        conn.close()
        return

    threading.Thread(target=pump, args=(conn, channel), daemon=True).start()
    threading.Thread(target=pump, args=(channel, conn), daemon=True).start()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--local-port", type=int, default=60053)
    parser.add_argument("--remote-host", default="127.0.0.1")
    parser.add_argument("--remote-port", type=int, default=50053)
    parser.add_argument("--bind", default="127.0.0.1")
    args = parser.parse_args()

    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(
        os.environ["PVE_HOST"],
        port=int(os.environ.get("PVE_PORT", "22")),
        username=os.environ.get("PVE_USER", "root"),
        password=os.environ["PVE_PASS"],
        timeout=20,
        banner_timeout=40,
        auth_timeout=40,
        look_for_keys=False,
        allow_agent=False,
    )
    transport = client.get_transport()
    if transport is None:
        print("tunnel: no transport", file=sys.stderr)
        return 1

    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((args.bind, args.local_port))
    server.listen(16)

    print(
        f"tunnel: {args.bind}:{args.local_port} -> {args.remote_host}:{args.remote_port} "
        f"via {os.environ['PVE_HOST']}",
        flush=True,
    )

    try:
        while True:
            conn, _ = server.accept()
            handle(conn, transport, args.remote_host, args.remote_port)
    except KeyboardInterrupt:
        pass
    finally:
        server.close()
        client.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
