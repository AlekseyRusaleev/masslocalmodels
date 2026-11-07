#!/usr/bin/env python3
"""Сверка верности пайплайна: одинаковые ли токены у базлайна и у разрезанной модели.

Выход модели-малыша сам по себе вырожденный, поэтому «красивость» текста ничего
не доказывает. Доказательство — совпадение токенов на первых шагах: если разрез
даёт те же токены, что и обычная генерация, активации и позиции передаются
верно, и расхождение дальше — это уже bf16-шум, а не ошибка схемы.
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

ids = tokenizer(prompt, return_tensors="pt")["input_ids"].to(device)
tokens: list[int] = []

with torch.no_grad():
    for _ in range(steps):
        out = model(ids)
        next_token = int(torch.argmax(out.logits[0, -1, :]))
        tokens.append(next_token)
        ids = torch.cat([ids, torch.tensor([[next_token]], device=device)], dim=1)

print("токены:", tokens)
print("текст:", repr(tokenizer.decode(tokens, skip_special_tokens=True)))
