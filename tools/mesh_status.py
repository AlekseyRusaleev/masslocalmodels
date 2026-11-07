import json
import urllib.request


def main() -> int:
    with open('/tmp/pub2.json', encoding='utf-8') as handle:
        answer = json.load(handle)
    print('модель в ответе:', answer.get('model'))
    print('id ответа:', answer.get('id'))
    text = (answer['choices'][0]['message'].get('content') or '').replace('\n', ' ')
    print('текст:', text[:160])

    try:
        with urllib.request.urlopen('http://127.0.0.1:3131/api/status', timeout=5) as response:
            status = json.load(response)
    except Exception as error:  # noqa: BLE001 - статус может быть недоступен
        print('статус недоступен:', error)
        return 0

    print()
    print('node_state:', status.get('node_state'), '| llama_ready:', status.get('llama_ready'))
    print('ключи статуса:', ', '.join(sorted(status.keys()))[:300])
    peers = status.get('peers') or []
    print('узлов в сети:', len(peers))
    for peer in peers[:3]:
        print('  peer:', json.dumps(peer, ensure_ascii=False)[:260])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
