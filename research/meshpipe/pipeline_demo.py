#!/usr/bin/env python3
"""Прототип пайплайна слоёв: модель разрезана между двумя процессами.

Зачем. Проверить схему, которая заменит llama.cpp RPC: каждый узел держит свои
блоки трансформера и свой KV-кэш, между узлами идут только активации
(килобайты на токен), а не веса (гигабайты). Тогда холодный старт перестаёт
зависеть от скорости отдачи координатора: каждая машина качает свои слои сама.

Запуск (два процесса; в прототипе — на одной машине):

    python pipeline_demo.py second --port 31330 --model Qwen/Qwen3-0.6B --split 14
    python pipeline_demo.py first  --peer 127.0.0.1:31330 --model Qwen/Qwen3-0.6B --split 14

Протокол намеренно простой: [uint32 длина][int64 id][uint32 ранг][uint32 форма]
[данные bf16]. Без pickle и без torch.distributed.

Почему блоки выполняет forward самой модели, а не ручной цикл: в современных
transformers слой требует не только position_ids, но и attention_mask и
position_embeddings, посчитанные на уровне модели. Своя реализация цикла уже
один раз дала неверные ротари-эмбеддинги; переиспользование кода библиотеки
убирает целый класс таких ошибок.

KV-кэш не передаётся между узлами: каждый кэширует свои слои (так же у Petals).
Иначе на каждый токен по сети поехали бы мегабайты вместо килобайт.

Позиции передаются как position_ids. В transformers 5 аргумент cache_position
этот forward не читает, а длину кэша берёт у слоя 0. Поэтому после разреза
layer_idx снова нумеруется с нуля: иначе вторая стадия пишет ключи в слой
с исходным номером, слой 0 остаётся пустым, и каждый новый токен получает
позицию 0.
"""

from __future__ import annotations

import argparse
import json
import socket
import struct
import sys
import tempfile
import time
from pathlib import Path
from typing import Iterable

import torch
from huggingface_hub import hf_hub_download, snapshot_download
from safetensors import safe_open
from safetensors.torch import save_file
from transformers import AutoConfig, AutoModelForCausalLM, AutoTokenizer, DynamicCache
from transformers.modeling_rope_utils import ROPE_INIT_FUNCTIONS

DTYPE = torch.bfloat16


# ---------- транспорт ----------

def send_tensor(conn: socket.socket, tensor: torch.Tensor) -> None:
    """Отправляет тензор: заголовок с формой, затем байты bf16."""
    # numpy не умеет bfloat16, а reinterpret-каст ломается на мелких формах:
    # передаём float32. Для активаций это лишние десятки килобайт на токен.
    payload = tensor.to(torch.float32).contiguous().cpu().numpy().tobytes()
    shape = tensor.shape
    # В длину входит всё: заголовок, форма и данные. Иначе получатель ждёт
    # данные нулевой длины и падает на разборе.
    header_size = 16 + 4 * len(shape)
    header = struct.pack("<IqI", header_size + len(payload), 0, len(shape))
    header += struct.pack(f"<{len(shape)}I", *shape)
    conn.sendall(header)
    conn.sendall(payload)


def recv_exact(conn: socket.socket, size: int) -> bytes:
    """Читает ровно size байт."""
    chunks = bytearray()
    while len(chunks) < size:
        chunk = conn.recv(size - len(chunks))
        if not chunk:
            raise ConnectionError("соединение закрыто")
        chunks.extend(chunk)
    return bytes(chunks)


def recv_tensor(conn: socket.socket) -> torch.Tensor:
    """Принимает тензор, отправленный send_tensor."""
    head = recv_exact(conn, 16)
    total, _message_id, rank = struct.unpack("<IqI", head)
    shape = struct.unpack(f"<{rank}I", recv_exact(conn, 4 * rank))
    payload = recv_exact(conn, total - 16 - 4 * rank)
    tensor = torch.frombuffer(bytearray(payload), dtype=torch.float32).reshape(shape)
    return tensor.clone()


# ---------- модель ----------

def rss_bytes() -> int:
    """Текущая резидентная память процесса. На Windows поля нет — вернётся 0."""
    try:
        for line in Path("/proc/self/status").read_text(encoding="utf-8").splitlines():
            if line.startswith("VmRSS:"):
                return int(line.split()[1]) * 1024
    except OSError:
        return 0
    return 0


def checkpoint_files(model_name: str) -> list[Path]:
    """Файлы safetensors модели. Скачивание только недостающего, без чтения тензоров."""
    # model*.safetensors — шарды из индекса. Шаблон *.safetensors захватил бы
    # ещё original/model.safetensors, полную вторую копию весов.
    folder = Path(snapshot_download(
        repo_id=model_name,
        allow_patterns=["model*.safetensors", "*.safetensors.index.json"],
    ))
    index = folder / "model.safetensors.index.json"
    if index.exists():
        weight_map = json.loads(index.read_text(encoding="utf-8"))["weight_map"]
        names = sorted(set(weight_map.values()))
        return [folder / name for name in names]
    files = sorted(folder.glob("*.safetensors"))
    if not files:
        raise SystemExit(f"в {folder} нет safetensors")
    return files


def tensor_nbytes(handle, key: str) -> int:
    """Размер тензора по заголовку, без чтения данных."""
    view = handle.get_slice(key)
    shape = view.get_shape()
    dtype = str(view.get_dtype()).split(".")[-1]
    width = {"F64": 8, "F32": 4, "F16": 2, "BF16": 2, "I64": 8, "I32": 4, "I16": 2, "I8": 1, "U8": 1, "BOOL": 1}.get(dtype, 2)
    size = width
    for dim in shape:
        size *= dim
    return size


def destination_key(key: str, first: int, last: int, role: str, has_lm_head: bool) -> str | None:
    """Имя параметра в срезе. Чужие блоки возвращают None и в память не читаются."""
    if key.startswith("model.layers."):
        index_text, dot, tail = key[len("model.layers."):].partition(".")
        if not dot:
            return None
        index = int(index_text)
        if first <= index < last:
            return f"model.layers.{index - first}.{tail}"
        return None
    if key == "model.embed_tokens.weight" and role == "first":
        return key
    if key == "model.norm.weight" and role == "second":
        return key
    if key == "lm_head.weight" and role == "second":
        return key
    # Голова часто связана с эмбеддингом и в файле отдельно не лежит.
    if key == "model.embed_tokens.weight" and role == "second" and not has_lm_head:
        return "lm_head.weight"
    return None


def is_mxfp4(config) -> bool:
    """Чекпойнт GPT-OSS хранит экспертов блоками MXFP4, а не плотными bf16."""
    quant = getattr(config, "quantization_config", None)
    if quant is None:
        return False
    method = quant.get("quant_method") if isinstance(quant, dict) else getattr(quant, "quant_method", None)
    return method == "mxfp4"


class PackedGptOssExperts(torch.nn.Module):
    """Эксперты GPT-OSS из блоков MXFP4. Распаковывается только выбранный эксперт."""

    def __init__(self, config):
        super().__init__()
        self.num_experts = config.num_local_experts
        self.alpha = config.swiglu_alpha
        self.limit = config.swiglu_limit
        self.compute_device = "cpu"
        self._sources: dict[str, tuple[Path, str]] = {}
        self._handles: dict[Path, object] = {}
        self._cache_key: tuple[int, ...] | None = None
        self._cache_gate: torch.Tensor | None = None
        self._cache_down: torch.Tensor | None = None

    def bind(self, name: str, path: Path, key: str) -> None:
        """Запоминает, где лежат блоки. В память попадёт только выбранный эксперт."""
        self._sources[name] = (path, key)

    def _view(self, name: str):
        path, key = self._sources[name]
        handle = self._handles.get(path)
        if handle is None:
            handle = safe_open(path, framework="pt", device="cpu")
            self._handles[path] = handle
        return handle.get_slice(key)

    def _packed(self, name: str, indices: list[int]) -> torch.Tensor:
        """Читает с диска только перечисленные эксперты и склеивает их по первой оси."""
        view = self._view(name)
        parts = [view[index:index + 1] for index in indices]
        return torch.cat(parts, dim=0)

    def _dequant_on(self, blocks_name: str, scales_name: str, indices: list[int], device: torch.device) -> torch.Tensor:
        from transformers.integrations.mxfp4 import convert_moe_packed_tensors

        parts = []
        for start in range(0, len(indices), 4):
            chunk = indices[start:start + 4]
            weight = convert_moe_packed_tensors(
                self._packed(blocks_name, chunk).to(device),
                self._packed(scales_name, chunk).to(device),
            )
            # Библиотечная распаковка при нехватке памяти молча уходит на CPU.
            if weight.device.type != device.type:
                raise torch.OutOfMemoryError("распаковка ушла с видеокарты")
            parts.append(weight)
        return parts[0] if len(parts) == 1 else torch.cat(parts, dim=0)

    def _expert_device(self, hidden_states: torch.Tensor) -> torch.device:
        if hidden_states.is_cuda:
            return hidden_states.device
        if torch.cuda.is_available():
            return torch.device("cuda")
        return torch.device("cpu")

    def _apply_gate(self, gate_up: torch.Tensor) -> torch.Tensor:
        gate, up = gate_up[..., ::2], gate_up[..., 1::2]
        gate = gate.clamp(min=None, max=self.limit)
        up = up.clamp(min=-self.limit, max=self.limit)
        glu = gate * torch.sigmoid(gate * self.alpha)
        return (up + 1) * glu

    def forward(self, hidden_states: torch.Tensor, router_indices=None, routing_weights=None) -> torch.Tensor:
        out_device = hidden_states.device
        # Выбор эксперта остаётся на CPU: то же соответствие токен→эксперт,
        # без one_hot на видеокарте. Распаковка пачки — там, где она быстрее.
        indices = router_indices.detach().to("cpu")
        weights = routing_weights.detach().to("cpu")
        chosen: list[int] = []
        groups: list[torch.Tensor] = []
        for expert in range(self.num_experts):
            hits = (indices == expert).nonzero(as_tuple=False)
            if hits.numel() == 0:
                continue
            chosen.append(expert)
            groups.append(hits)
        device = self._expert_device(hidden_states)
        if not chosen:
            return torch.zeros(hidden_states.shape, dtype=hidden_states.dtype, device=out_device)
        key = tuple(chosen)
        cached = (
            self._cache_key == key
            and self._cache_gate is not None
            and self._cache_gate.device == device
        )
        try:
            if cached:
                gate, down = self._cache_gate, self._cache_down
            else:
                gate = self._dequant_on("gate_up_proj_blocks", "gate_up_proj_scales", chosen, device)
                down = self._dequant_on("down_proj_blocks", "down_proj_scales", chosen, device)
                self._remember(key, gate, down, device)
        except torch.OutOfMemoryError:
            device = torch.device("cpu")
            self._cache_key = None
            self._cache_gate = None
            self._cache_down = None
            if torch.cuda.is_available():
                torch.cuda.empty_cache()
            gate = self._dequant_on("gate_up_proj_blocks", "gate_up_proj_scales", chosen, device)
            down = self._dequant_on("down_proj_blocks", "down_proj_scales", chosen, device)
        if not getattr(PackedGptOssExperts, "_announced", False):
            print(f"[experts] распаковка на {device}", flush=True)
            PackedGptOssExperts._announced = True
        next_states = torch.zeros(hidden_states.shape, dtype=hidden_states.dtype, device=device)
        picked = torch.tensor(chosen, device=self.gate_up_proj_bias.device)
        gate_bias = self.gate_up_proj_bias[picked].to(device=device, dtype=hidden_states.dtype)
        down_bias = self.down_proj_bias[picked].to(device=device, dtype=hidden_states.dtype)
        for local, hits in enumerate(groups):
            token_idx = hits[:, 0]
            slot = hits[:, 1]
            current = hidden_states[token_idx.cpu() if hidden_states.device.type == "cpu" else token_idx.to(hidden_states.device)]
            current = current.to(device)
            mixed = current @ gate[local] + gate_bias[local]
            out = self._apply_gate(mixed) @ down[local] + down_bias[local]
            scale = weights[token_idx, slot].to(device=device, dtype=out.dtype)
            next_states.index_add_(0, token_idx.to(device), out * scale[:, None])
        return next_states.to(out_device)

    def _remember(self, key: tuple[int, ...], gate: torch.Tensor, down: torch.Tensor, device: torch.device) -> None:
        """Держит последнюю четвёрку экспертов слоя. Повтор на следующем токене не распаковывается."""
        self._cache_gate = None
        self._cache_down = None
        self._cache_key = None
        if device.type != "cuda" or len(key) > 4:
            return
        if torch.cuda.memory_allocated() > 1_200_000_000:
            return
        self._cache_key = key
        self._cache_gate = gate
        self._cache_down = down


def drop_dense_experts(model) -> None:
    """Убирает плотные матрицы экспертов до переноса на устройство.

    Иначе to_empty выделит bf16 на каждый эксперт. Для одного слоя GPT-OSS-20B
    это больше гигабайта, хотя в файле тот же слой — упакованные блоки.
    """
    config = model.config
    for layer in model.model.layers:
        layer.mlp.experts = PackedGptOssExperts(config)


def slice_catalog(files: list[Path], first: int, last: int, role: str) -> tuple[dict[str, tuple[Path, str]], int, list[str]]:
    """Какие тензоры входят в срез. Чужие ключи в результат не попадают."""
    catalog: list[tuple[str, Path]] = []
    file_bytes = 0
    for path in files:
        with safe_open(path, framework="pt") as handle:
            for key in handle.keys():
                catalog.append((key, path))
                file_bytes += tensor_nbytes(handle, key)
    has_lm_head = any(key == "lm_head.weight" for key, _ in catalog)
    wanted: dict[str, tuple[Path, str]] = {}
    for key, path in catalog:
        dest = destination_key(key, first, last, role, has_lm_head)
        if dest is not None:
            wanted[dest] = (path, key)
    return wanted, file_bytes, [key for _, key in wanted.values()]


def read_slice_tensors(files: list[Path], first: int, last: int, role: str, device: str) -> tuple[dict[str, torch.Tensor], int, int, list[str]]:
    """Читает из safetensors только тензоры среза."""
    wanted, file_bytes, source_keys = slice_catalog(files, first, last, role)
    by_file: dict[Path, list[tuple[str, str]]] = {}
    for dest, (path, key) in wanted.items():
        by_file.setdefault(path, []).append((dest, key))
    tensors: dict[str, torch.Tensor] = {}
    loaded_bytes = 0
    for path, items in by_file.items():
        with safe_open(path, framework="pt", device=device) as handle:
            for dest, key in items:
                tensor = handle.get_tensor(key)
                tensors[dest] = tensor
                loaded_bytes += tensor.numel() * tensor.element_size()
    return tensors, loaded_bytes, file_bytes, source_keys


def assign_parameter(model, name: str, tensor: torch.Tensor) -> None:
    """Кладёт тензор параметром туда, куда указывает полное имя."""
    module_name, _, param_name = name.rpartition(".")
    module = model.get_submodule(module_name)
    module.register_parameter(param_name, torch.nn.Parameter(tensor, requires_grad=False))


def revive_rope(model, device: str) -> None:
    """Частоты RoPE не хранятся в файле. После to_empty их надо посчитать заново."""
    rope = model.model.rotary_emb
    init = ROPE_INIT_FUNCTIONS.get(rope.rope_type, type(rope).compute_default_rope_parameters)
    inv_freq, scale = init(model.config)
    rope.inv_freq = torch.nn.Buffer(inv_freq.to(device=device), persistent=False)
    rope.original_inv_freq = torch.nn.Buffer(inv_freq.detach().clone().to(device=device), persistent=False)
    rope.attention_scaling = scale


def load_slice(model_name: str, split: int, role: str, device: str | None = None):
    """Собирает срез и читает из safetensors только его тензоры.

    Модель целиком не материализуется: число слоёв в конфиге уменьшается до
    среза ещё до создания модулей. Пик памяти — свой срез, не весь файл.
    """
    tokenizer = AutoTokenizer.from_pretrained(model_name)
    config = AutoConfig.from_pretrained(model_name)
    total = config.num_hidden_layers
    if not (0 < split < total):
        raise SystemExit(f"split={split} вне диапазона 0..{total}")
    first, last = (0, split) if role == "first" else (split, total)

    original_types = list(getattr(config, "layer_types", []) or [])
    config.num_hidden_layers = last - first
    if original_types:
        config.layer_types = original_types[first:last]

    if device is None or device == "auto":
        device = "cuda" if torch.cuda.is_available() else "cpu"
    elif device == "cuda" and not torch.cuda.is_available():
        raise SystemExit("запрошена видеокарта, но она недоступна")
    print(f"[{role}] собираю срез на {device}", flush=True)

    rss_before = rss_bytes()
    if device == "cuda":
        torch.cuda.reset_peak_memory_stats()

    packed = is_mxfp4(config)
    with torch.device("meta"):
        model = AutoModelForCausalLM.from_config(config, dtype=DTYPE)
    model.eval()
    if packed:
        drop_dense_experts(model)
    if role == "first":
        model.lm_head = None
        model.model.norm = torch.nn.Identity()
    else:
        model.model.embed_tokens = None
    # Крупнее ~0.5 ГиБ одним куском CUDA на этой WSL не выделяет, хотя
    # свободны гигабайты. Эмбеддинг и голова остаются в оперативке.
    model.to_empty(device="cpu")
    revive_rope(model, "cpu")
    print(f"[{role}] пустые веса на cpu, RSS {rss_bytes()/2**30:.2f} GiB", flush=True)

    files = checkpoint_files(model_name)
    wanted, file_bytes, _source_keys = slice_catalog(files, first, last, role)
    params = dict(model.named_parameters())
    missing = sorted(set(params) - set(wanted))
    if missing:
        raise SystemExit(f"[{role}] в файле нет весов: {', '.join(missing[:8])}")
    loaded_bytes = 0
    streamed_bytes = 0
    by_file: dict[Path, list[tuple[str, str]]] = {}
    for dest, (path, key) in wanted.items():
        by_file.setdefault(path, []).append((dest, key))
    for path, items in by_file.items():
        print(f"[{role}] читаю {path.name}, тензоров {len(items)}", flush=True)
        # safetensors прямо в CUDA на этой машине роняет драйвер WSL.
        # Читаем в оперативку и переносим по одному тензору.
        with safe_open(path, framework="pt", device="cpu") as handle:
            for dest, key in items:
                packed = key.endswith("_blocks") or key.endswith("_scales")
                if packed:
                    module_name, _, param_name = dest.rpartition(".")
                    model.get_submodule(module_name).bind(param_name, path, key)
                    streamed_bytes += tensor_nbytes(handle, key)
                    continue
                tensor = handle.get_tensor(key)
                nbytes = tensor.numel() * tensor.element_size()
                if nbytes >= 64 * 2**20:
                    print(f"[{role}] {dest} {nbytes/2**20:.0f} MiB", flush=True)
                if dest in params:
                    with torch.no_grad():
                        params[dest].copy_(tensor.to(dtype=params[dest].dtype))
                else:
                    assign_parameter(model, dest, tensor.detach())
                loaded_bytes += tensor.numel() * tensor.element_size()
                del tensor
    if device != "cpu":
        with torch.no_grad():
            for name, param in model.named_parameters():
                if name.endswith("embed_tokens.weight") or name.endswith("lm_head.weight"):
                    continue
                param.data = param.data.to(device)
        revive_rope(model, device)
    for module in model.modules():
        if isinstance(module, PackedGptOssExperts):
            module.compute_device = device

    live = sum(p.numel() * p.element_size() for p in model.parameters())
    cuda_peak = torch.cuda.max_memory_allocated() if device == "cuda" else 0
    load_slice.last_stats = {
        "loaded": loaded_bytes,
        "live": live,
        "file_bytes": file_bytes,
        "cuda_peak": cuda_peak,
        "rss_delta": max(0, rss_bytes() - rss_before),
    }
    print(f"[{role}] блоки [{first}:{last}] из {total}, "
          f"в памяти {live/2**30:.2f} GiB, эксперты с диска {streamed_bytes/2**30:.2f} GiB, "
          f"файл {file_bytes/2**30:.2f} GiB, пик CUDA {cuda_peak/2**30:.2f} GiB", flush=True)
    print(f"[{role}] устройство {device}", flush=True)
    return tokenizer, model, device


def forward_slice(model, hidden: torch.Tensor, cache: DynamicCache,
                  positions: torch.Tensor) -> torch.Tensor:
    """Прогон среза модели: свои блоки, свой кэш, абсолютные позиции токенов."""
    if positions.ndim == 1:
        positions = positions.unsqueeze(0)
    out = model.model(
        inputs_embeds=hidden,
        past_key_values=cache,
        use_cache=True,
        position_ids=positions,
    )
    return out.last_hidden_state


def new_cache(model) -> DynamicCache:
    """Кэш ровно на слои этого среза, включая скользящее окно, если оно есть."""
    return DynamicCache(config=model.config)


def run_first(args: argparse.Namespace) -> None:
    tokenizer, model, device = load_slice(args.model, args.split, "first", args.device)
    cache = new_cache(model)

    host, port = args.peer.split(":")
    conn = socket.create_connection((host, int(port)), timeout=600)
    conn.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

    embed = model.model.embed_tokens
    ids = tokenizer(args.prompt, return_tensors="pt")["input_ids"].to(embed.weight.device)
    prompt_len = ids.shape[1]
    print(f"[first] промпт {args.prompt!r}: {prompt_len} токенов", flush=True)

    generated: list[int] = []
    started = time.time()

    with torch.no_grad():
        hidden = embed(ids).to(device)
        positions = torch.arange(prompt_len, device=device)
        print("[first] прогон промпта", flush=True)
        hidden = forward_slice(model, hidden, cache, positions)
        print("[first] промпт посчитан", flush=True)
        send_tensor(conn, hidden)

        logits = recv_tensor(conn).to(device=device, dtype=torch.float32)
        next_token = int(torch.argmax(logits[0, -1, :]))
        generated.append(next_token)

        position = prompt_len
        while len(generated) < args.max_new_tokens and next_token != tokenizer.eos_token_id:
            # Пришёл байт (или несколько) от стадии — начинаем новый токен.
            token_ids = torch.tensor([[next_token]], device=embed.weight.device)
            hidden = embed(token_ids).to(device)
            positions = torch.tensor([position], device=device)
            hidden = forward_slice(model, hidden, cache, positions)
            send_tensor(conn, hidden)

            logits = recv_tensor(conn).to(device=device, dtype=torch.float32)
            next_token = int(torch.argmax(logits[0, -1, :]))
            generated.append(next_token)
            position += 1

    elapsed = time.time() - started
    text = tokenizer.decode(generated, skip_special_tokens=True)
    print(f"[first] токены: {generated}", flush=True)
    print(f"[first] ответ: {text!r}", flush=True)
    print(f"[first] токенов {len(generated)} за {elapsed:.1f} с → "
          f"{len(generated)/max(elapsed, 1e-6):.2f} tok/s", flush=True)
    conn.close()


def run_second(args: argparse.Namespace) -> None:
    _tokenizer, model, device = load_slice(args.model, args.split, "second", args.device)

    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(("0.0.0.0", args.port))
    server.listen(1)
    print(f"[second] слушаю порт {args.port}", flush=True)

    while True:
        conn, addr = server.accept()
        conn.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        print(f"[second] подключился {addr}", flush=True)
        cache = new_cache(model)
        position = 0
        try:
            with torch.no_grad():
                while True:
                    hidden = recv_tensor(conn).to(device=device, dtype=DTYPE)
                    length = hidden.shape[1]
                    positions = torch.arange(position, position + length, device=device)
                    position += length

                    hidden = forward_slice(model, hidden, cache, positions)
                    logits = model.lm_head(hidden.to(model.lm_head.weight.device))
                    send_tensor(conn, logits)
        except (ConnectionError, struct.error):
            print("[second] клиент отключился", flush=True)
        finally:
            conn.close()


def greedy_tokens(model, tokenizer, prompt: str, steps: int, device: str, use_cache: bool) -> list[int]:
    """Жадная генерация целой модели. С кэшем — тот же инкрементальный путь, что у разреза."""
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


def split_tokens(model_name: str, split: int, prompt: str, steps: int) -> list[int]:
    """Те же шаги, что у двух процессов, но в одном процессе и с тем же float32-проводом."""
    tokenizer, first, device = load_slice(model_name, split, "first")
    _, second, device_second = load_slice(model_name, split, "second")
    cache_first = new_cache(first)
    cache_second = new_cache(second)

    ids = tokenizer(prompt, return_tensors="pt")["input_ids"].to(device)
    generated: list[int] = []
    with torch.no_grad():
        hidden = first.model.embed_tokens(ids)
        positions = torch.arange(ids.shape[1], device=device)
        hidden = forward_slice(first, hidden, cache_first, positions)
        hidden = recv_tensor_from(send_tensor_bytes(hidden)).to(device=device_second, dtype=DTYPE)
        hidden = forward_slice(second, hidden, cache_second, positions.to(device_second))
        logits = recv_tensor_from(send_tensor_bytes(second.lm_head(hidden))).to(device=device, dtype=torch.float32)
        next_token = int(torch.argmax(logits[0, -1, :]))
        generated.append(next_token)

        position = ids.shape[1]
        while len(generated) < steps:
            token_ids = torch.tensor([[next_token]], device=device)
            hidden = first.model.embed_tokens(token_ids)
            positions = torch.tensor([position], device=device)
            hidden = forward_slice(first, hidden, cache_first, positions)
            hidden = recv_tensor_from(send_tensor_bytes(hidden)).to(device=device_second, dtype=DTYPE)
            hidden = forward_slice(second, hidden, cache_second, positions.to(device_second))
            logits = recv_tensor_from(send_tensor_bytes(second.lm_head(hidden))).to(device=device, dtype=torch.float32)
            next_token = int(torch.argmax(logits[0, -1, :]))
            generated.append(next_token)
            position += 1
    return generated


def send_tensor_bytes(tensor: torch.Tensor) -> bytes:
    """Те же байты, что уходят в сокет: float32 и форма."""
    payload = tensor.detach().to(torch.float32).contiguous().cpu().numpy().tobytes()
    shape = tensor.shape
    header_size = 16 + 4 * len(shape)
    header = struct.pack("<IqI", header_size + len(payload), 0, len(shape))
    header += struct.pack(f"<{len(shape)}I", *shape)
    return header + payload


def recv_tensor_from(blob: bytes) -> torch.Tensor:
    """Разбор кадра send_tensor без сокета."""
    total, _message_id, rank = struct.unpack("<IqI", blob[:16])
    shape = struct.unpack(f"<{rank}I", blob[16:16 + 4 * rank])
    payload = blob[16 + 4 * rank:total]
    tensor = torch.frombuffer(bytearray(payload), dtype=torch.float32).reshape(shape)
    return tensor.clone()


def run_check(args: argparse.Namespace) -> int:
    """Сверяет разрезанную модель с целой. Ненулевой код — токены разошлись."""
    tokenizer = AutoTokenizer.from_pretrained(args.model)
    full = AutoModelForCausalLM.from_pretrained(args.model, dtype=DTYPE).eval()
    device = "cuda" if torch.cuda.is_available() else "cpu"
    full.to(device)
    cached = greedy_tokens(full, tokenizer, args.prompt, args.max_new_tokens, device, True)
    plain = greedy_tokens(full, tokenizer, args.prompt, args.max_new_tokens, device, False)
    del full
    if device == "cuda":
        torch.cuda.empty_cache()

    got = split_tokens(args.model, args.split, args.prompt, args.max_new_tokens)
    print(f"[check] кэш целой модели: {cached}", flush=True)
    print(f"[check] без кэша:         {plain}", flush=True)
    print(f"[check] разрез:           {got}", flush=True)
    if plain != cached:
        limit = min(len(plain), len(cached))
        first = next((i for i in range(limit) if plain[i] != cached[i]), limit)
        print(f"[check] целая модель сама расходится с кэшем на токене {first + 1}", flush=True)
    if got != cached:
        limit = min(len(got), len(cached))
        first = next((i for i in range(limit) if got[i] != cached[i]), limit)
        print(f"[check] разрез разошёлся с кэшем на токене {first + 1}", flush=True)
        return 1
    print(f"[check] разрез совпал с кэшем целой модели, токенов {len(cached)}", flush=True)
    return 0


def run_mxfp4_selftest(_args: argparse.Namespace) -> int:
    """Чужие блоки не читаются, плотный эксперт одного слоя не выделяется."""
    layer0 = torch.arange(64, dtype=torch.uint8).reshape(2, 4, 8)
    layer1 = torch.arange(64, 128, dtype=torch.uint8).reshape(2, 4, 8)
    scales0 = torch.ones(2, 4, dtype=torch.uint8)
    scales1 = torch.full((2, 4), 2, dtype=torch.uint8)
    with tempfile.TemporaryDirectory() as folder:
        path = Path(folder) / "model.safetensors"
        save_file({
            "model.layers.0.mlp.experts.gate_up_proj_blocks": layer0,
            "model.layers.0.mlp.experts.gate_up_proj_scales": scales0,
            "model.layers.0.self_attn.q_proj.weight": torch.ones(2, 2),
            "model.layers.1.mlp.experts.gate_up_proj_blocks": layer1,
            "model.layers.1.mlp.experts.gate_up_proj_scales": scales1,
            "model.layers.1.self_attn.q_proj.weight": torch.full((2, 2), 2.0),
            "model.embed_tokens.weight": torch.ones(3, 4),
            "model.norm.weight": torch.ones(4),
            "lm_head.weight": torch.zeros(3, 4),
        }, str(path))
        tensors, loaded, file_bytes, sources = read_slice_tensors([path], 0, 1, "first", "cpu")
        foreign = [key for key in sources if ".layers.1." in key or key == "lm_head.weight" or key == "model.norm.weight"]
        if foreign:
            print(f"[mxfp4] прочитаны чужие ключи: {foreign}", flush=True)
            return 1
        if loaded >= file_bytes:
            print(f"[mxfp4] прочитано {loaded} из файла {file_bytes}", flush=True)
            return 1
        got = tensors["model.layers.0.mlp.experts.gate_up_proj_blocks"]
        if not torch.equal(got, layer0):
            print("[mxfp4] блоки своего слоя не совпали", flush=True)
            return 1
        second, loaded_second, _, sources_second = read_slice_tensors([path], 1, 2, "second", "cpu")
        if any(".layers.0." in key or key == "model.embed_tokens.weight" for key in sources_second):
            print(f"[mxfp4] вторая стадия прочитала чужое: {sources_second}", flush=True)
            return 1
        if "lm_head.weight" not in second:
            print("[mxfp4] у второй стадии нет головы", flush=True)
            return 1
        print(f"[mxfp4] свой срез {loaded} байт из файла {file_bytes}, вторая стадия {loaded_second}", flush=True)

    from transformers.integrations.mxfp4 import convert_moe_packed_tensors
    experts, rows, groups, pack = 2, 4, 2, 2
    blocks = torch.randint(0, 256, (experts, rows, groups, pack), dtype=torch.uint8)
    scales = torch.randint(100, 140, (experts, rows, groups), dtype=torch.uint8)
    whole = convert_moe_packed_tensors(blocks, scales)
    one = convert_moe_packed_tensors(blocks[1:2], scales[1:2])[0]
    if not torch.equal(whole[1], one):
        print("[mxfp4] распаковка одного эксперта не совпала с полной", flush=True)
        return 1
    print("[mxfp4] распаковка одного эксперта совпала", flush=True)

    config = AutoConfig.from_pretrained("openai/gpt-oss-20b")
    config.num_hidden_layers = 1
    if getattr(config, "layer_types", None):
        config.layer_types = list(config.layer_types[:1])
    with torch.device("meta"):
        model = AutoModelForCausalLM.from_config(config, dtype=DTYPE)
    drop_dense_experts(model)
    model.to_empty(device="cpu")
    expert_bytes = sum(
        param.numel() * param.element_size()
        for name, param in model.named_parameters()
        if ".experts." in name
    )
    # Плотный gate_up одного слоя GPT-OSS-20B — около 1 ГиБ. Его быть не должно.
    if expert_bytes != 0:
        print(f"[mxfp4] эксперты всё ещё занимают {expert_bytes/2**20:.0f} МиБ", flush=True)
        return 1
    print("[mxfp4] плотные эксперты не выделены", flush=True)

    index_path = Path(hf_hub_download("openai/gpt-oss-20b", "model.safetensors.index.json"))
    index = json.loads(index_path.read_text(encoding="utf-8"))
    weight_map = index["weight_map"]
    total = index.get("metadata", {}).get("total_size", 0)
    first_keys = []
    for key in weight_map:
        if destination_key(key, 0, 12, "first", True) is not None:
            first_keys.append(key)
    blocks = [key for key in weight_map if key.endswith("_blocks")]
    blocks_kept = [key for key in blocks if ".layers." in key and int(key.split(".layers.")[1].split(".")[0]) < 12]
    print(f"[mxfp4] индекс GPT-OSS-20B: файл {total/2**30:.2f} ГиБ, "
          f"блоки слоёв 0..12: {len(blocks_kept)} из {len(blocks)}, "
          f"ключи первой стадии: {len(first_keys)} из {len(weight_map)}", flush=True)
    if len(blocks_kept) != len(blocks) // 2:
        print("[mxfp4] срез блоков не равен половине слоёв", flush=True)
        return 1
    return 0


def run_footprint(args: argparse.Namespace) -> int:
    """Один срез в чистом процессе. Пик не должен доходить до размера всего файла."""
    load_slice(args.model, args.split, args.half, args.device)
    stats = load_slice.last_stats
    live = stats["live"]
    file_bytes = stats["file_bytes"]
    cuda_peak = stats["cuda_peak"]
    if live >= file_bytes * 0.9:
        print(f"[footprint] срез {live} почти равен файлу {file_bytes}", flush=True)
        return 1
    if cuda_peak and cuda_peak >= file_bytes:
        print(f"[footprint] пик CUDA {cuda_peak} не меньше файла {file_bytes}", flush=True)
        return 1
    if stats["rss_delta"] >= file_bytes:
        print(f"[footprint] прирост RSS {stats['rss_delta']} не меньше файла {file_bytes}", flush=True)
        return 1
    print(f"[footprint] срез меньше файла: {live/2**30:.2f} < {file_bytes/2**30:.2f} GiB", flush=True)
    return 0


def main(argv: Iterable[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Прототип пайплайна слоёв между двумя процессами")
    parser.add_argument("role", choices=["first", "second", "check", "footprint", "mxfp4-selftest"], help="кто есть кто")
    parser.add_argument("--model", default="Qwen/Qwen3-0.6B", help="модель на Hugging Face")
    parser.add_argument("--split", type=int, default=14, help="граница: первым [0:split)")
    parser.add_argument("--half", choices=["first", "second"], default="first", help="какой срез мерить")
    parser.add_argument("--device", default="auto", choices=["auto", "cuda", "cpu"], help="где держать свой срез")
    parser.add_argument("--port", type=int, default=31330, help="порт стадии")
    parser.add_argument("--peer", default="127.0.0.1:31330", help="адрес стадии")
    parser.add_argument("--max-new-tokens", type=int, default=24)
    parser.add_argument("--prompt", default="Столица Франции — это")
    args = parser.parse_args(list(argv) if argv is not None else None)

    if args.role == "check":
        return run_check(args)
    if args.role == "footprint":
        return run_footprint(args)
    if args.role == "mxfp4-selftest":
        return run_mxfp4_selftest(args)
    if args.role == "first":
        run_first(args)
    else:
        run_second(args)
    return 0


if __name__ == "__main__":
    sys.exit(main())
