#!/usr/bin/env python3
"""Run a command on a remote host over SSH (password auth) and print its output.

Credentials come from environment variables so they are never stored in the repo:
  PVE_HOST, PVE_PORT, PVE_USER, PVE_PASS

Usage:
  python tools/ssh_run.py "pveversion"
  python tools/ssh_run.py -f path/to/local_script.sh
  echo "pveversion" | python tools/ssh_run.py -
"""

from __future__ import annotations

import os
import sys

import paramiko


for _stream in (sys.stdout, sys.stderr, sys.stdin):
    try:
        _stream.reconfigure(encoding="utf-8", errors="replace")
    except (AttributeError, ValueError):
        pass


def read_command() -> str:
    args = sys.argv[1:]
    if not args:
        raise SystemExit("usage: ssh_run.py <command> | -f <file> | -")
    if args[0] == "-f":
        with open(args[1], encoding="utf-8") as fh:
            return fh.read()
    if args[0] == "-":
        return sys.stdin.read()
    return " ".join(args)


def main() -> int:
    host = os.environ["PVE_HOST"]
    port = int(os.environ.get("PVE_PORT", "22"))
    user = os.environ.get("PVE_USER", "root")
    password = os.environ["PVE_PASS"]
    command = read_command().lstrip("\ufeff")

    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(
        host,
        port=port,
        username=user,
        password=password,
        timeout=25,
        banner_timeout=40,
        auth_timeout=40,
        look_for_keys=False,
        allow_agent=False,
    )
    try:
        stdin, stdout, stderr = client.exec_command(command, timeout=300)
        stdin.close()
        out = stdout.read().decode("utf-8", "replace")
        err = stderr.read().decode("utf-8", "replace")
        rc = stdout.channel.recv_exit_status()
    finally:
        client.close()

    sys.stdout.write(out)
    if err.strip():
        sys.stdout.write("\n--- stderr ---\n" + err)
    sys.stdout.write(f"\n[exit: {rc}]\n")
    return 0 if rc == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
