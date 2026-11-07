#!/usr/bin/env python3
"""Контроль: сравнивает генерацию с KV-кэшем и без него на одной модели.

Если последовательности расходятся уже здесь, то расхождение прототипа с
базлайном — это не ошибка передачи активаций, а обычная разница округлений
bf16 при двух разных порядках вычислений. Без такого контроля легко начать
искать баг там, где его нет.
"""

import sys

import torch
from transformers import AutoModelForCausalLM, AutoTokenizer

model_name = sys.argv[1] if len(sys.argv) > 1 else "Qwen/Qwen3-0.6B"
prompt = sys.argv[2] if len(sys.argv) > 2 else "Столица Франции — это"
steps = int(sys.argv[3]) if len(sys.argv) > 3 else 8

tokenizer = AutoTokenizer.from_pretrained(model_name)
model = AutoModelForCausalLM.from_pretrained(model_name, dtype=torch.bfloat16).eval()
device = "cuda" if torch.cuda.is_available() else "cpu"
model.to(device)


def greedy(use_cache: bool) -> list[int]:
    ids = tokenizer(prompt, return_tensors="pt")["input_ids"].to(device)
    tokens: list[int] = []
    cache = None
    with torch.no_grad():
        for _ in range(steps):
            if use_cache and cache is not None:
                position = ids.shape[1] - 1
                out = model(
                    input_ids=ids[:, -1:],
                    past_key_values=cache,
                    use_cache=True,
                    cache_position=torch.tensor([position], device=device),
                )
            else:
                out = model(input_ids=ids, use_cache=use_cache)
            if use_cache:
                cache = out.past_key_values
            next_token = int(torch.argmax(out.logits[0, -1, :]))
            tokens.append(next_token)
            ids = torch.cat([ids, torch.tensor([[next_token]], device=device)], dim=1)
    return tokens


cached = greedy(True)
plain = greedy(False)
print("с кэшем:  ", cached)
print("без кэша: ", plain)
print("совпали:", cached == plain)
if cached != plain:
    first = next(i for i, (a, b) in enumerate(zip(cached, plain)) if a != b)
    print("первое расхождение на токене", first + 1)
