#!/usr/bin/env bash
# Разбор списка публичных сетей mesh-llm и выбор самой крупной.
set -uo pipefail

cd "$HOME/mesh/mesh-bundle" || exit 1
timeout 120 ./mesh-llm discover --json 2>/dev/null > /tmp/discover.json

python3 <<'PY'
import json

data = json.load(open('/tmp/discover.json'))
meshes = data if isinstance(data, list) else data.get('meshes', [])
print('сетей найдено:', len(meshes))

junk = ('passwd', 'ro/ro', 'x/x', '/dev/zero', 'bogus', 'nonexistent')
best = None
for index, mesh in enumerate(meshes, 1):
    models = mesh.get('models') or mesh.get('serving') or []
    names = []
    if isinstance(models, list):
        for item in models:
            name = item.get('id') or item.get('model') or item.get('name') if isinstance(item, dict) else item
            if name and not any(bad in str(name) for bad in junk) and name not in ('x', 'mesh'):
                names.append(str(name))
    nodes = mesh.get('nodes') or mesh.get('node_count') or 0
    capacity = mesh.get('capacity_gb') or mesh.get('capacity_gb_total') or 0
    token = mesh.get('token') or ''
    print(f'[{index}] узлов={nodes} ёмкость={capacity}ГБ моделей={len(names)} token={token[:24]}…')
    for name in names[:5]:
        print('     ', name)
    if best is None or nodes > best[0]:
        best = (nodes, token, names, index)

if best:
    nodes, token, names, index = best
    print()
    print(f'ВЫБРАНА сеть [{index}]: узлов={nodes}, моделей={len(names)}')
    open('/tmp/mesh-token.txt', 'w').write(token)
    print('токен сохранён в /tmp/mesh-token.txt, длина', len(token))
    for name in names:
        print('  модель:', name)
PY
