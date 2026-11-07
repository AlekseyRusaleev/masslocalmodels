#!/usr/bin/env bash
# Список релизных архивов mesh-llm для Linux x86_64 и их размеры.
set -euo pipefail

json_file=/tmp/mesh-release.json
curl -fsSL https://api.github.com/repos/Mesh-LLM/mesh-llm/releases/latest -o "$json_file"

python3 - "$json_file" <<'PY'
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
print("релиз:", data.get("tag_name"))
for asset in data.get("assets", []):
    name = asset["name"]
    if "x86_64-unknown-linux-gnu" in name:
        print(f"{asset['size'] / 1024 / 1024:8.1f} MB  {name}")
PY
