import json
import sys


def main() -> int:
    mode = sys.argv[1]

    if mode == "models":
        data = json.load(sys.stdin)
        for item in data.get("data", []):
            payment = item.get("payment") or {}
            offers = payment.get("offers") or []
            paid = [o for o in offers if o.get("priced")]
            print(f"  {item['id']}")
            print(f"      бесплатно: {payment.get('free_available')}, платных предложений: {len(paid)}")
        return 0

    if mode == "usage":
        data = json.load(sys.stdin)
        if "error" in data:
            print("  ОШИБКА:", data["error"])
            return 1
        usage = data.get("usage") or {}
        tokens = usage.get("completion_tokens") or 0
        print(f"  токенов: {tokens}, вход: {usage.get('prompt_tokens')}, finish: {data['choices'][0].get('finish_reason')}")
        return 0

    return 2


if __name__ == "__main__":
    raise SystemExit(main())
