from huggingface_hub import list_models

for m in list_models(search="tiny-Mixtral", limit=8):
    print(m.id)
print("---")
for m in list_models(search="tiny-MoE", limit=8):
    print(m.id)
