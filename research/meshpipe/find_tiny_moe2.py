from huggingface_hub import list_models
for q in ["tiny-random-Mixtral", "tiny-random-Qwen3Moe", "tiny-MixtralForCausalLM"]:
    print("==", q)
    try:
        for m in list_models(search=q, limit=5):
            print(" ", m.id)
    except Exception as exc:
        print("  ошибка:", exc)
