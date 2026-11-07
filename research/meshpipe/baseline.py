#!/usr/bin/env python3
"""Базлайн без разрезания: та же модель, тот же промпт, тот же greedy-декод.

Нужен, чтобы отличить дефект пайплайна от слабости маленькой модели: если
базлайн даёт связный текст, а разрез — нет, значит ошибка в передаче активаций
или позиций, а не в модели.
"""

import sys

import torch
from transformers import AutoModelForCausalLM, AutoTokenizer

model_name = sys.argv[1] if len(sys.argv) > 1 else "Qwen/Qwen3-0.6B"
prompt = sys.argv[2] if len(sys.argv) > 2 else "Столица Франции — это"
max_new = int(sys.argv[3]) if len(sys.argv) > 3 else 24

tokenizer = AutoTokenizer.from_pretrained(model_name)
model = AutoModelForCausalLM.from_pretrained(model_name, dtype=torch.bfloat16).eval()
device = "cuda" if torch.cuda.is_available() else "cpu"
model.to(device)

ids = tokenizer(prompt, return_tensors="pt")["input_ids"].to(device)

with torch.no_grad():
    out = model.generate(ids, max_new_tokens=max_new, do_sample=False,
                         pad_token_id=tokenizer.eos_token_id)

print("базлайн:", repr(tokenizer.decode(out[0][ids.shape[1]:], skip_special_tokens=True)))
