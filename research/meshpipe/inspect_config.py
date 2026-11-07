from transformers import AutoConfig

c = AutoConfig.from_pretrained("Qwen/Qwen3-0.6B")
print("layers", c.num_hidden_layers, "hidden", c.hidden_size, "heads", c.num_attention_heads)
print("head_dim", getattr(c, "head_dim", None))
print("use_sliding_window", getattr(c, "use_sliding_window", None))
print("sliding_window", getattr(c, "sliding_window", None))
print("layer_types", getattr(c, "layer_types", None))
