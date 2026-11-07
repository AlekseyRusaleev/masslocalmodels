#!/usr/bin/env python3
"""Замер инференса через Ollama API: загрузка, TTFT, скорость генерации.

Считает по фактическим полям ответа (load_duration, prompt_eval_*, eval_*),
а не по «на глаз». Печатает JSON, пригодный для записи в документ.

Запуск:
    python scripts/bench-ollama.py [--model qwen3:8b] [--runs 2]
"""

from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.request

BASE = "http://127.0.0.1:11434"

PROMPT = (
    "Ты технический помощник проекта распределённой AI-платформы. "
    "Объясни в пяти предложениях, зачем нужен lease с TTL при выдаче задач "
    "на удалённые GPU-узлы и что произойдёт, если его не использовать."
)


def post(path: str, payload: dict):
    request = urllib.request.Request(
        BASE + path,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
    )
    return urllib.request.urlopen(request, timeout=600)


def unload(model: str) -> None:
    """Выгружает модель из VRAM (keep_alive=0)."""
    try:
        post("/api/chat", {"model": model, "messages": [], "keep_alive": 0}).read()
    except Exception as exc:  # noqa: BLE001
        print(f"unload failed: {exc}", file=sys.stderr)


def bench(model: str, num_ctx: int, num_gpu: int | None) -> dict:
    started = time.perf_counter()
    first_token_at = None
    text_parts: list[str] = []
    final: dict = {}

    options: dict = {"num_ctx": num_ctx, "temperature": 0.3}
    if num_gpu is not None:
        options["num_gpu"] = num_gpu

    response = post(
        "/api/chat",
        {
            "model": model,
            "messages": [{"role": "user", "content": PROMPT}],
            "stream": True,
            "options": options,
        },
    )

    for raw in response:
        line = raw.decode("utf-8").strip()
        if not line:
            continue
        chunk = json.loads(line)
        message = chunk.get("message") or {}
        piece = message.get("content") or ""
        if piece and first_token_at is None:
            first_token_at = time.perf_counter()
        text_parts.append(piece)
        if chunk.get("done"):
            final = chunk

    total = time.perf_counter() - started
    answer = "".join(text_parts).strip()

    def ms(key: str) -> float:
        return round(final.get(key, 0) / 1e6, 1)

    load_ms = ms("load_duration")
    prompt_eval_ms = ms("prompt_eval_duration")
    eval_ms = ms("eval_duration")
    prompt_tokens = final.get("prompt_eval_count", 0)
    eval_tokens = final.get("eval_count", 0)

    ttft = round(first_token_at - started, 3) if first_token_at else None
    gen_tps = round(eval_tokens / (eval_ms / 1000), 2) if eval_ms else None
    prompt_tps = round(prompt_tokens / (prompt_eval_ms / 1000), 2) if prompt_eval_ms else None

    return {
        "model": model,
        "num_ctx": num_ctx,
        "num_gpu": num_gpu if num_gpu is not None else "all",
        "wall_seconds": round(total, 2),
        "ttft_seconds": ttft,
        "load_ms": load_ms,
        "prompt_tokens": prompt_tokens,
        "prompt_ms": prompt_eval_ms,
        "prompt_tokens_per_second": prompt_tps,
        "generated_tokens": eval_tokens,
        "generate_ms": eval_ms,
        "tokens_per_second": gen_tps,
        "answer_preview": answer[:220],
        "answer_chars": len(answer),
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", default="qwen3:8b")
    parser.add_argument("--runs", type=int, default=2)
    parser.add_argument("--num-ctx", type=int, default=4096)
    parser.add_argument("--num-gpu", type=int, default=None, help="сколько слоёв выгрузить на GPU (по умолчанию все)")
    args = parser.parse_args()

    results = []
    for index in range(args.runs):
        if index > 0:
            unload(args.model)
            time.sleep(2)
        label = "cold" if index == 0 else "warm"
        result = bench(args.model, args.num_ctx, args.num_gpu)
        result["load"] = label
        results.append(result)
        print(json.dumps(result, ensure_ascii=False, indent=2), flush=True)

    print(json.dumps({"runs": len(results), "model": args.model}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
