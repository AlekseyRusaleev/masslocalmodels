import json
import urllib.request


def main() -> int:
    with urllib.request.urlopen('http://127.0.0.1:3131/api/status', timeout=10) as response:
        status = json.load(response)

    for key in ('available_models', 'models', 'hosted_models', 'model_name', 'model_size_gb', 'is_client', 'is_host'):
        value = status.get(key)
        print(f'{key}: {json.dumps(value, ensure_ascii=False)[:400]}')

    print()
    print('--- кто что обслуживает ---')
    for peer in status.get('peers') or []:
        models = peer.get('models') or peer.get('hosted_models') or []
        names = []
        for item in models:
            if isinstance(item, dict):
                names.append(str(item.get('id') or item.get('model') or item.get('name')))
            else:
                names.append(str(item))
        real = [n for n in names if 'sha256' not in n and 'passwd' not in n and 'ro/ro' not in n]
        print(f"  {peer.get('id')}: узлов={len(peer.get('split_stages') or [])} моделей={len(real)}")
        for name in real[:4]:
            print('     ', name)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
