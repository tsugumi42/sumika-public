"""本机模型服务：一个进程装多个嵌入、重排模型，按需加载、空闲卸载。

接口照 OpenAI（`/v1/embeddings`）和 Cohere / Jina（`/v1/rerank`）的形状；池化、归一化、
前缀、长度全在 models.toml 里，代码只认配置。规格：docs/archive/规格/规格-本地模型服务.md。

跑：<按 requirements.txt 建的独立环境目录>\\Scripts\\python.exe server.py（环境怎么建见同目录的 requirements.txt）
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import contextlib
import gc
import hmac
import json
import logging
import os
import socket
import threading
import time
import tomllib
from collections import OrderedDict
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator, Literal

import numpy as np
import onnxruntime as ort
import uvicorn
from fastapi import FastAPI, HTTPException, Request
from pydantic import BaseModel, ConfigDict
from starlette.concurrency import run_in_threadpool
from tokenizers import Tokenizer

HERE = Path(__file__).resolve().parent
DEFAULT_CONFIG = HERE / "models.toml"

MAX_BATCH_SIZE = 64
MAX_BATCH_TOKENS = 32768
MAX_BODY_BYTES = 32 * 1024**2
CACHE_SIZE = 256
MIN_DIMENSIONS = 32
# 单条字符数上限 = max_tokens × 这个倍数，用在分词之前（4 字符/token 对中英混排都够宽）。
CHARS_PER_TOKEN = 4
# 重排一对里留给特殊 token 和文档的余量：查询最多占 max_tokens - 这个数。
RERANK_SPECIAL_RESERVE = 16
# 一次嵌入 / 重排最多算多少秒（`--max-request-seconds`）：到了就在下一句之前停、回 504。
# 10 分钟和 Go 那边 `-embed-timeout` 的默认值一样；60 条 8192 token 的极端批次也够。
DEFAULT_MAX_REQUEST_SECONDS = 600.0
# 盯"客户端断开 / 到截止时间"的间隔：50 毫秒一次，只是看一眼事件循环，不花钱。
STOP_POLL_SECONDS = 0.05
TOKEN_ENV = "SUMIKA_EMBED_TOKEN"
PORT_ENV = "SUMIKA_EMBED_PORT"
TOKENIZER_FILE = "tokenizer.json"
POOLINGS = {"last", "cls", "mean", "model", "logit"}
KINDS = {"embed", "rerank"}
TRUNCATIONS = {"longest_first", "only_second"}

LOGGER = logging.getLogger("local-models")


class ConfigError(Exception):
    """models.toml 读不进来、字段不对、文件不在。启动时直接退出，不带着半份配置跑。"""


class PoolError(Exception):
    """加载 / 容量 / 内存的问题；接口层翻成对应的 HTTP 码。"""

    def __init__(self, status_code: int, detail: str):
        super().__init__(detail)
        self.status_code = status_code
        self.detail = detail


class Cancelled(Exception):
    """客户端断开 / 到了服务端截止时间：在逐条前向之间抛出，把模型锁放掉，剩下的不再算。

    慢模型上一批 64 条要几分钟（pplx 实测 192 秒）；客户端早就走了还接着算，后面的请求全排在
    它后面等（规格-本地模型服务 第五节）。
    """

    def __init__(self, reason: str):
        super().__init__(reason)
        self.reason = reason  # "disconnect" / "timeout"


class StopSignal:
    """一次请求的停止信号：接口层在事件循环里置位，计算线程在逐条前向之间查。

    为什么不直接在路由里问 `request.is_disconnected()`：同步路由拿不到；而且正文中间件回放给
    FastAPI 的 `receive` 不是真的连接（第二次调用直接回 `http.disconnect`），照那个问会一直答"断了"。
    断开由 `BodyLimitMiddleware` 起一个任务盯着真的 `receive`，置 `threading.Event`（规格 第八节）。
    """

    def __init__(self) -> None:
        self._event = threading.Event()
        self._reason = ""

    def set(self, reason: str) -> None:
        if not self._event.is_set():
            self._reason = reason
        self._event.set()

    @property
    def reason(self) -> str:
        return self._reason

    def check(self) -> None:
        """在计算线程里调：置位了就抛 `Cancelled`，`with self.lock` 会跟着放掉锁。"""
        if self._event.is_set():
            raise Cancelled(self._reason or "disconnect")


@dataclass(frozen=True)
class ModelSpec:
    id: str
    kind: str
    onnx: Path
    tokenizer: Path
    dimension: int
    pooling: str
    normalize: bool
    max_tokens: int
    matryoshka: bool
    truncation: str
    min_dimensions: int = MIN_DIMENSIONS
    allowed_dimensions: tuple[int, ...] = ()
    query_prefix: str = ""
    document_prefix: str = ""
    query_task: str = ""
    estimated_bytes: int = 0

    def prefix(self, input_type: str, task: str | None) -> str:
        template = self.query_prefix if input_type == "query" else self.document_prefix
        if not template:
            return ""
        return template.replace("{task}", task or self.query_task)

    @property
    def char_limit(self) -> int:
        """单条字符串允许的字符数（分词之前拦，见规格 3.6）。"""
        return self.max_tokens * CHARS_PER_TOKEN

    def dimensions_error(self, dimensions: int) -> str | None:
        """`dimensions` 不合模型卡时返回一句原因，合法返回 None。"""
        if not self.matryoshka:
            return f"Model {self.id!r} does not support dimensions truncation"
        if self.allowed_dimensions:
            if dimensions not in self.allowed_dimensions:
                return f"dimensions must be one of {list(self.allowed_dimensions)} for {self.id!r}"
            return None
        if not self.min_dimensions <= dimensions <= self.dimension:
            return f"dimensions must be within [{self.min_dimensions}, {self.dimension}] for {self.id!r}"
        return None


def _estimate_bytes(onnx_path: Path) -> int:
    """内存估计：ONNX 本体 + 外部数据文件，再加 30% 的运行时开销。

    不是实测 RSS（ORT 的分配在卸载后也要过几秒才还回去），只用来挡"一次装八个"。
    """
    total = onnx_path.stat().st_size
    for sidecar in (onnx_path.with_suffix(onnx_path.suffix + ".data"), onnx_path.parent / (onnx_path.name + "_data")):
        if sidecar.is_file():
            total += sidecar.stat().st_size
    return int(total * 1.3)


def load_specs(config_path: Path, root_override: str | None = None) -> dict[str, ModelSpec]:
    try:
        raw = tomllib.loads(config_path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ConfigError(f"找不到配置 {config_path}") from exc
    except tomllib.TOMLDecodeError as exc:
        raise ConfigError(f"{config_path} 不是合法的 TOML：{exc}") from exc

    root = Path(root_override or raw.get("root") or "")
    if not root.is_dir():
        raise ConfigError(f"模型根目录不存在：{root}（models.toml 的 root 或环境变量 LOCAL_MODELS_ROOT）")

    entries = raw.get("models")
    if not isinstance(entries, dict) or not entries:
        raise ConfigError(f"{config_path} 里没有 [models.\"...\"]")

    specs: dict[str, ModelSpec] = {}
    problems: list[str] = []
    for model_id, body in entries.items():
        if not isinstance(body, dict):
            problems.append(f"{model_id}: 不是一个表")
            continue
        try:
            kind = str(body["kind"])
            pooling = str(body["pooling"])
            truncation = str(body.get("truncation", "longest_first"))
            if kind not in KINDS:
                problems.append(f"{model_id}: kind 只能是 {sorted(KINDS)}")
                continue
            if pooling not in POOLINGS:
                problems.append(f"{model_id}: pooling 只能是 {sorted(POOLINGS)}")
                continue
            if truncation not in TRUNCATIONS:
                problems.append(f"{model_id}: truncation 只能是 {sorted(TRUNCATIONS)}")
                continue
            if kind == "embed" and pooling == "logit":
                problems.append(f"{model_id}: 嵌入模型不能用 logit 池化")
                continue
            if kind == "rerank" and pooling != "logit":
                problems.append(f"{model_id}: 重排模型的 pooling 必须是 logit")
                continue
            model_dir = root / str(body["dir"])
            onnx = model_dir / str(body["onnx"])
            if not onnx.is_file():
                problems.append(f"{model_id}: 权重不在 {onnx}")
                continue
            tokenizer = model_dir / TOKENIZER_FILE
            if not tokenizer.is_file():
                problems.append(f"{model_id}: 分词器不在 {tokenizer}")
                continue
            dimension = int(body["dimension"])
            if kind == "embed" and dimension <= 0:
                problems.append(f"{model_id}: 嵌入维度必须大于 0")
                continue
            max_tokens = int(body["max_tokens"])
            if max_tokens < 8:
                problems.append(f"{model_id}: max_tokens 至少 8")
                continue
            matryoshka = bool(body.get("matryoshka", False))
            min_dimensions = int(body.get("min_dimensions", MIN_DIMENSIONS))
            allowed = tuple(int(value) for value in body.get("allowed_dimensions", ()))
            if matryoshka:
                if not 1 <= min_dimensions <= dimension:
                    problems.append(f"{model_id}: min_dimensions 要在 [1, {dimension}]")
                    continue
                if allowed and any(not 1 <= value <= dimension for value in allowed):
                    problems.append(f"{model_id}: allowed_dimensions 里只能有 [1, {dimension}] 的值")
                    continue
            elif allowed:
                problems.append(f"{model_id}: 写了 allowed_dimensions 就要写 matryoshka = true")
                continue
            specs[model_id] = ModelSpec(
                id=model_id,
                kind=kind,
                onnx=onnx,
                tokenizer=tokenizer,
                dimension=dimension,
                pooling=pooling,
                normalize=bool(body.get("normalize", True)),
                max_tokens=max_tokens,
                matryoshka=matryoshka,
                truncation=truncation,
                min_dimensions=min_dimensions,
                allowed_dimensions=allowed,
                query_prefix=str(body.get("query_prefix", "")),
                document_prefix=str(body.get("document_prefix", "")),
                query_task=str(body.get("query_task", "")),
                estimated_bytes=_estimate_bytes(onnx),
            )
        except (KeyError, TypeError, ValueError) as exc:
            problems.append(f"{model_id}: {type(exc).__name__}: {exc}")

    if problems:
        raise ConfigError("models.toml 有问题：\n  " + "\n  ".join(problems))
    return specs


def build_tokenizer(spec: ModelSpec) -> Tokenizer:
    tokenizer = Tokenizer.from_file(str(spec.tokenizer))
    tokenizer.enable_truncation(max_length=spec.max_tokens, strategy=spec.truncation)
    return tokenizer


def _new_session(spec: ModelSpec, threads: int) -> ort.InferenceSession:
    options = ort.SessionOptions()
    options.intra_op_num_threads = threads
    options.inter_op_num_threads = 1
    options.log_severity_level = 3
    options.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_BASIC
    return ort.InferenceSession(str(spec.onnx), sess_options=options, providers=["CPUExecutionProvider"])


class LoadedModel:
    """装好的一份模型：会话、分词器、缓存、引用计数。

    `lock` 从 `embed_texts` / `rerank` 进门到出门整段持有：ONNX 会话、LRU 缓存、KV 零数组
    都只在锁里碰。不同模型各一把，并行上限由池的 K 管。
    """

    def __init__(
        self,
        spec: ModelSpec,
        session: ort.InferenceSession,
        tokenizer: Tokenizer,
        load_ms: int,
        raw_tokenizer: Tokenizer | None = None,
    ):
        self.spec = spec
        self.session = session
        self.tokenizer = tokenizer
        # 不带截断的同一份分词器：只用来量重排查询本身的长度（`only_second` 不截第一段，
        # 量不出超长的查询）。两者不许共用一份再中途改截断。
        self.raw_tokenizer = raw_tokenizer
        self.inputs = session.get_inputs()
        self.output_names = {entry.name for entry in session.get_outputs()}
        self.load_ms = load_ms

        if spec.kind == "rerank":
            if "logits" not in self.output_names:
                raise RuntimeError(f"{spec.id}: 重排图没有 logits 输出")
            self.hidden_output = None
            self.pooled_output = None
            self.score_output = "logits"
        else:
            self.score_output = None
            self.pooled_output = None
            if spec.pooling == "model":
                for candidate in ("sentence_embedding", "pooler_output"):
                    if candidate in self.output_names:
                        self.pooled_output = candidate
                        break
                if self.pooled_output is None:
                    raise RuntimeError(f"{spec.id}: pooling=model 但图里没有 sentence_embedding / pooler_output")
                self.hidden_output = None
            else:
                self.hidden_output = next(
                    (name for name in ("last_hidden_state", "token_embeddings") if name in self.output_names), None
                )
                if self.hidden_output is None:
                    raise RuntimeError(f"{spec.id}: 图里没有 last_hidden_state / token_embeddings，不能自己池化")

        self.lock = threading.RLock()
        self.refs = 0
        self.last_used = time.monotonic()
        # 文本（已加前缀） → (归一化后的全维向量, token 数)
        self.cache: OrderedDict[str, tuple[np.ndarray, int]] = OrderedDict()
        self._kv: dict[int, dict[str, np.ndarray]] = {}

    def consume(self) -> None:
        """丢掉会话和缓存；引用计数已经归零才会走到这里。"""
        self.cache.clear()
        self._kv.clear()
        self.session = None  # type: ignore[assignment]
        gc.collect()

    # ---- 喂图 -------------------------------------------------------------

    def _forward(self, rows: list[list[int]]) -> np.ndarray:
        """跑一次图，返回引擎那一层的输出（池化结果 `(行, 维度)` 或 hidden `(行, 序列, 维度)`）。

        一次只喂一行：这些量化图对**批次大小**也敏感（2026-10-28 实测，三句同 token 长度的文本
        一次前向 vs 逐条，同一句的余弦：qwen 0.932、gte 0.987、granite 0.978；pplx、harrier、
        bekko 不受影响）。批次一变向量就变，同一个索引里同一段文本会有两种向量，所以宁可一条
        一条跑。也不补 pad（原因见下面 `_feeds` 和规格第八节）。
        """
        feeds, _lengths = self._feeds(rows)
        output = self.score_output or self.pooled_output or self.hidden_output
        return np.asarray(self.session.run([output], feeds)[0], dtype=np.float32)

    def _zero_cache(self, batch: int) -> dict[str, np.ndarray]:
        """Qwen 那份图要 56 个空的 past_key_values.*，形状里带 batch。按 batch 缓存一份。"""
        cached = self._kv.get(batch)
        if cached is not None:
            return cached
        feeds: dict[str, np.ndarray] = {}
        for entry in self.inputs:
            if not entry.name.startswith("past_key_values."):
                continue
            shape: list[int] = []
            for axis in entry.shape:
                if isinstance(axis, int):
                    shape.append(axis)
                elif axis == "batch_size":
                    shape.append(batch)
                elif axis == "past_sequence_length":
                    shape.append(0)
                else:
                    raise RuntimeError(f"{self.spec.id}: 不认识的 KV 形状 {entry.shape}")
            dtype = np.float16 if entry.type == "tensor(float16)" else np.float32
            feeds[entry.name] = np.zeros(shape, dtype=dtype)
        self._kv[batch] = feeds
        return feeds

    def _feeds(self, rows: list[list[int]]) -> tuple[dict[str, np.ndarray], np.ndarray]:
        """把 id 行摆成图要的张量。`_forward` 只传一行，所以这里的 width 永远等于行长，不会有 pad 列。"""
        lengths = np.asarray([len(row) for row in rows], dtype=np.int64)
        width = int(lengths.max())
        input_ids = np.zeros((len(rows), width), dtype=np.int64)
        mask = np.zeros((len(rows), width), dtype=np.int64)
        for index, row in enumerate(rows):
            input_ids[index, : len(row)] = row
            mask[index, : len(row)] = 1

        feeds: dict[str, np.ndarray] = {}
        for entry in self.inputs:
            name = entry.name
            if name == "input_ids":
                feeds[name] = input_ids
            elif name == "attention_mask":
                feeds[name] = mask
            elif name == "position_ids":
                feeds[name] = np.tile(np.arange(width, dtype=np.int64), (len(rows), 1))
            elif name.startswith("past_key_values."):
                feeds[name] = self._zero_cache(len(rows))[name]
            else:
                raise RuntimeError(f"{self.spec.id}: 不认识的模型输入 {name}")
        return feeds, lengths

    # ---- 嵌入 -------------------------------------------------------------

    def embed_texts(
        self, texts: list[str], dimensions: int | None, stop: StopSignal | None = None
    ) -> tuple[list[np.ndarray], int, int]:
        """返回（向量, 总 token 数, 缓存命中数）。`dimensions` 只对 matryoshka 模型生效。

        缓存、分词、前向全在 `self.lock` 里；截断在锁外做（只读本次请求自己的向量）。
        `stop` 在逐条前向之间查：客户端断开或到截止时间就抛 `Cancelled`，锁跟着放掉，剩下的不算。
        """
        with self.lock:
            if stop is not None:
                stop.check()
            results, token_counts, hits = self._embed_texts_locked(texts, stop)
        out: list[np.ndarray] = []
        for vector in results:
            if dimensions is not None and dimensions != self.spec.dimension:
                vector = vector[:dimensions]
                norm = float(np.linalg.norm(vector))
                if norm <= 0:
                    raise HTTPException(status_code=400, detail="The requested dimensions produce a zero vector")
                vector = vector / norm
            out.append(vector)
        return out, token_counts, hits

    def _embed_texts_locked(
        self, texts: list[str], stop: StopSignal | None = None
    ) -> tuple[list[np.ndarray], int, int]:
        results: list[np.ndarray | None] = [None] * len(texts)
        token_counts = [0] * len(texts)
        missing: list[int] = []
        for index, text in enumerate(texts):
            cached = self.cache.get(text)
            if cached is None:
                missing.append(index)
                continue
            self.cache.move_to_end(text)
            results[index], token_counts[index] = cached

        if missing:
            rows = [self.tokenizer.encode(texts[index]).ids for index in missing]
            for index, row in zip(missing, rows):
                if not row:
                    raise HTTPException(status_code=400, detail="Input must tokenize to at least one token")
                token_counts[index] = len(row)

        # 全部命中缓存也要查：规格 3.6 的上限是"一批最多多少 token"，没有缓存命中的豁免。
        tokens = int(sum(token_counts))
        if tokens > MAX_BATCH_TOKENS:
            raise HTTPException(status_code=400, detail=f"A batch is limited to {MAX_BATCH_TOKENS} tokens")

        if missing:
            vectors = self._forward_embeddings(rows, stop)
            for index, vector in zip(missing, vectors):
                results[index] = vector
                self.cache[texts[index]] = (vector, token_counts[index])
            while len(self.cache) > CACHE_SIZE:
                self.cache.popitem(last=False)

        out: list[np.ndarray] = []
        for vector in results:
            assert vector is not None
            out.append(vector)
        return out, tokens, len(texts) - len(missing)

    def _forward_embeddings(self, rows: list[list[int]], stop: StopSignal | None = None) -> list[np.ndarray]:
        """调用方必须持 `self.lock`。一次前向只有一行（见 `_forward` 的说明）。

        每条之间查一次 `stop`：断开或超时就抛 `Cancelled`，中断的这批不进缓存（下次重算）。
        """
        vectors = np.empty((len(rows), self.spec.dimension), dtype=np.float32)
        for position, row in enumerate(rows):
            if stop is not None:
                stop.check()
            part = self._forward([row])
            if self.pooled_output is not None:
                vectors[position] = part[0]
                continue
            hidden = part[0]
            if self.spec.pooling == "last":
                vectors[position] = hidden[-1]
            elif self.spec.pooling == "cls":
                vectors[position] = hidden[0]
            elif self.spec.pooling == "mean":
                vectors[position] = hidden.mean(axis=0)
            else:  # pragma: no cover - 配置校验已经挡掉
                raise RuntimeError(f"{self.spec.id}: 不认识的池化 {self.spec.pooling}")

        if vectors.shape != (len(rows), self.spec.dimension):
            raise RuntimeError(f"{self.spec.id}: 向量形状 {vectors.shape} 和登记的维度 {self.spec.dimension} 不一致")
        if not np.isfinite(vectors).all():
            raise RuntimeError(f"{self.spec.id}: 模型吐出了 NaN / Inf")
        norms = np.linalg.norm(vectors, axis=1)
        if (norms <= 0).any():
            raise RuntimeError(f"{self.spec.id}: 模型吐出了零向量")
        if self.spec.normalize:
            vectors = vectors / norms[:, None]
        return [vectors[index].copy() for index in range(len(rows))]

    # ---- 重排 -------------------------------------------------------------

    def rerank(self, query: str, documents: list[str], stop: StopSignal | None = None) -> tuple[np.ndarray, int]:
        """整段持 `self.lock`（分词器、KV 数组、会话都在里面）。

        每对之间查一次 `stop`（和嵌入一样：断开或超时就不再算剩下的）。
        """
        with self.lock:
            if stop is not None:
                stop.check()
            query_tokens = len(self.raw_tokenizer.encode(query).ids) if self.raw_tokenizer else 0
            budget = self.spec.max_tokens - RERANK_SPECIAL_RESERVE
            if query_tokens > budget:
                raise HTTPException(
                    status_code=400,
                    detail=(
                        f"Query is {query_tokens} tokens; {self.spec.id!r} allows at most {budget} "
                        f"(max_tokens {self.spec.max_tokens} minus {RERANK_SPECIAL_RESERVE} for the pair)"
                    ),
                )
            try:
                rows = [self.tokenizer.encode(query, document).ids for document in documents]
            except Exception as exc:  # 分词器在超长的一对上会抛，不能让它变成 500
                raise HTTPException(
                    status_code=400, detail=f"Failed to tokenize a (query, document) pair: {type(exc).__name__}"
                ) from exc
            if any(not row for row in rows):
                raise HTTPException(
                    status_code=400, detail="Every (query, document) pair must tokenize to at least one token"
                )
            if any(len(row) > self.spec.max_tokens for row in rows):
                longest = max(len(row) for row in rows)
                raise HTTPException(
                    status_code=400,
                    detail=f"A pair is {longest} tokens after truncation; the limit is {self.spec.max_tokens}",
                )
            tokens = sum(len(row) for row in rows)
            if tokens > MAX_BATCH_TOKENS:
                raise HTTPException(status_code=400, detail=f"A batch is limited to {MAX_BATCH_TOKENS} tokens")
            logits = np.empty(len(rows), dtype=np.float64)
            for position, row in enumerate(rows):
                if stop is not None:
                    stop.check()
                logits[position] = float(self._forward([row])[0][0])
        if logits.shape != (len(documents),) or not np.isfinite(logits).all():
            raise RuntimeError(f"{self.spec.id}: 重排输出形状 {logits.shape} 不对或含 NaN / Inf")
        # sigmoid：越大越相关。
        scores = 1.0 / (1.0 + np.exp(-logits))
        return scores, tokens


def _load(spec: ModelSpec, threads: int) -> LoadedModel:
    started = time.monotonic()
    session = _new_session(spec, threads)
    tokenizer = build_tokenizer(spec)
    raw_tokenizer = Tokenizer.from_file(str(spec.tokenizer)) if spec.kind == "rerank" else None
    load_ms = int((time.monotonic() - started) * 1000)
    return LoadedModel(spec, session, tokenizer, load_ms, raw_tokenizer)


class ModelPool:
    """model id → 装好的模型。第一次请求才加载，空闲卸载，最多装 K 个，内存有个软上限。"""

    def __init__(
        self,
        specs: dict[str, ModelSpec],
        *,
        max_loaded: int = 2,
        idle_seconds: float = 600.0,
        max_bytes: int = 4 * 1024**3,
        threads: int = 2,
        wait_seconds: float = 30.0,
        reap_interval: float = 15.0,
    ):
        self._specs = specs
        self.max_loaded = max(1, max_loaded)
        self._max_loaded = self.max_loaded
        self._idle = idle_seconds
        self._max_bytes = max_bytes
        self._threads = threads
        self._wait = wait_seconds
        self._reap_interval = reap_interval
        self._lock = threading.Lock()
        self._loaded: dict[str, LoadedModel] = {}
        self._pending: dict[str, int] = {}
        self._loading: dict[str, threading.Event] = {}
        self._stop = threading.Event()
        self._reaper = threading.Thread(target=self._reap, name="local-models-unload", daemon=True)
        self._reaper.start()

    def close(self) -> None:
        self._stop.set()
        with self._lock:
            for model in list(self._loaded.values()):
                model.consume()
            self._loaded.clear()

    def spec_ids(self) -> set[str]:
        return set(self._specs)

    def require(self, model_id: str, kind: str) -> ModelSpec:
        spec = self._specs.get(model_id)
        if spec is None:
            raise PoolError(404, f"Unknown model {model_id!r}; available: {', '.join(sorted(self._specs))}")
        if spec.kind != kind:
            raise PoolError(404, f"Model {model_id!r} is a {spec.kind} model, not {kind}")
        return spec

    @contextlib.contextmanager
    def lease(self, model_id: str) -> Iterator[LoadedModel]:
        model = self.acquire(model_id)
        try:
            yield model
        finally:
            self.release(model)

    def acquire(self, model_id: str) -> LoadedModel:
        spec = self._specs.get(model_id)
        if spec is None:
            raise PoolError(404, f"Unknown model {model_id!r}")
        deadline = time.monotonic() + self._wait
        room_reason = "No free slot"
        while True:
            with self._lock:
                model = self._loaded.get(model_id)
                if model is not None:
                    model.refs += 1
                    model.last_used = time.monotonic()
                    return model
                event = self._loading.get(model_id)
                if event is None:
                    reason, retryable = self._make_room(spec)
                    if reason is None:
                        self._pending[model_id] = spec.estimated_bytes
                        self._loading[model_id] = threading.Event()
                        break
                    if not retryable:
                        raise PoolError(503, reason)
                    room_reason = reason
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                if event is not None:
                    raise PoolError(503, f"Model {model_id!r} is still loading")
                raise PoolError(503, room_reason)
            if event is not None:
                event.wait(min(remaining, 0.2))
            else:
                # 槽位满：放开池锁等别的请求放租约 / 加载完，到 30 秒截止再 503。
                time.sleep(min(remaining, 0.1))

        try:
            model = _load(spec, self._threads)
        except BaseException as exc:
            with self._lock:
                self._pending.pop(model_id, None)
                self._loading.pop(model_id).set()
            if isinstance(exc, Exception):
                raise PoolError(500, f"Model {model_id!r} failed to load: {type(exc).__name__}: {exc}") from exc
            raise

        with self._lock:
            self._pending.pop(model_id, None)
            model.refs = 1
            self._loaded[model_id] = model
            self._loading.pop(model_id).set()
        LOGGER.info(
            "LOADED model=%s kind=%s dimension=%s load_ms=%s estimated_mb=%s",
            model_id,
            spec.kind,
            spec.dimension or "-",
            model.load_ms,
            round(spec.estimated_bytes / 1e6),
        )
        return model

    def release(self, model: LoadedModel) -> None:
        with self._lock:
            model.refs = max(0, model.refs - 1)
            model.last_used = time.monotonic()

    def unload(self, model_id: str) -> tuple[bool, str]:
        with self._lock:
            model = self._loaded.get(model_id)
            if model is None:
                return False, "not_loaded"
            if model.refs > 0:
                return False, "in_use"
            self._unload_locked(model)
            return True, "unloaded"

    def snapshot(self) -> list[dict[str, object]]:
        now = time.monotonic()
        with self._lock:
            rows = []
            for model_id in sorted(self._specs):
                model = self._loaded.get(model_id)
                rows.append(
                    {
                        "id": model_id,
                        "kind": self._specs[model_id].kind,
                        "dimension": self._specs[model_id].dimension,
                        "max_tokens": self._specs[model_id].max_tokens,
                        "matryoshka": self._specs[model_id].matryoshka,
                        "loaded": model is not None,
                        "load_ms": model.load_ms if model is not None else None,
                        "last_used_ms_ago": int((now - model.last_used) * 1000) if model is not None else None,
                    }
                )
            return rows

    def resident_bytes(self) -> int:
        with self._lock:
            return sum(model.spec.estimated_bytes for model in self._loaded.values())

    # ---- 内部 -------------------------------------------------------------

    def _make_room(self, spec: ModelSpec) -> tuple[str | None, bool]:
        """给即将加载的模型腾位置；必须持锁调用。

        返回（原因, 能不能等）。能等的由 `acquire` 放开锁重试，等不到 30 秒截止才 503；
        永远装不下（一个模型就超过内存上限）的立刻报。
        """
        while True:
            loaded_bytes = sum(model.spec.estimated_bytes for model in self._loaded.values())
            pending_bytes = sum(self._pending.values())
            over_count = len(self._loaded) + len(self._pending) + 1 > self._max_loaded
            over_bytes = loaded_bytes + pending_bytes + spec.estimated_bytes > self._max_bytes
            if not over_count and not over_bytes:
                return None, True
            idle = [model for model in self._loaded.values() if model.refs == 0]
            if not idle:
                if spec.estimated_bytes > self._max_bytes:
                    return (
                        f"Model {spec.id!r} alone needs about {spec.estimated_bytes // (1024**2)} MiB, "
                        f"over the {self._max_bytes // (1024**3)} GiB resident budget",
                        False,
                    )
                if over_count:
                    return "Every loaded model is in use; waiting for one to be released", True
                return (
                    f"Loading {spec.id!r} would exceed the {self._max_bytes // (1024**3)} GiB resident budget; "
                    "waiting for a model to be unloaded"
                ), True
            victim = min(idle, key=lambda model: model.last_used)
            self._unload_locked(victim)

    def _unload_locked(self, model: LoadedModel) -> None:
        self._loaded.pop(model.spec.id, None)
        model.consume()
        LOGGER.info("UNLOADED model=%s", model.spec.id)

    def _reap(self) -> None:
        while not self._stop.wait(self._reap_interval):
            if self._idle <= 0:
                continue
            now = time.monotonic()
            with self._lock:
                for model in list(self._loaded.values()):
                    if model.refs == 0 and now - model.last_used > self._idle:
                        self._unload_locked(model)


# ---- 接口 -----------------------------------------------------------------


class EmbeddingsRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    model: str
    input: str | list[str]
    encoding_format: Literal["float", "base64"] = "float"
    dimensions: int | None = None
    input_type: Literal["query", "document"] = "document"
    task: str | None = None
    user: str | None = None


class RerankRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    model: str
    query: str
    documents: list[str]
    top_n: int | None = None


def _encode_vector(vector: np.ndarray, encoding_format: str) -> list[float] | str:
    if encoding_format == "base64":
        return base64.b64encode(np.asarray(vector, dtype="<f4").tobytes()).decode("ascii")
    return [float(value) for value in vector]


async def _send_error(send, status: int, detail: str, extra: list[tuple[bytes, bytes]] | None = None) -> None:
    body = json.dumps({"detail": detail}, ensure_ascii=False).encode("utf-8")
    headers = [
        (b"content-type", b"application/json; charset=utf-8"),
        (b"content-length", str(len(body)).encode("ascii")),
    ] + list(extra or [])
    await send({"type": "http.response.start", "status": status, "headers": headers})
    await send({"type": "http.response.body", "body": body})


class TokenMiddleware:
    """每个请求（含 `/health`）都要 `Authorization: Bearer $SUMIKA_EMBED_TOKEN`，否则 401。

    令牌由起服务的一方生成（Go 那边照 `local.go` 的 `randomToken`），服务只校验、不回显。
    """

    def __init__(self, app, token: str):
        self.app = app
        self.expected = b"Bearer " + token.encode("utf-8")

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            await self.app(scope, receive, send)
            return
        header = None
        for name, value in scope["headers"]:
            if name == b"authorization":
                header = value
                break
        if header is None or not hmac.compare_digest(header, self.expected):
            await _send_error(
                send,
                401,
                "Missing or wrong Authorization header; send 'Bearer <SUMIKA_EMBED_TOKEN>'",
                [(b"www-authenticate", b"Bearer")],
            )
            return
        await self.app(scope, receive, send)


class BodyLimitMiddleware:
    """`Content-Length` 和实际收到的字节都卡在 `MAX_BODY_BYTES`，超了 413。

    正文先在这里收完（不收进内存就到不了 Pydantic），再回放给 FastAPI：这样超限时是我们自己
    回 413，而不是 FastAPI 把 receive 里的异常包成 400（"There was an error parsing the body"）。
    在进 Pydantic / 分词之前拦：一条几千万字的字符串到不了 `tokenizer.encode`。
    """

    def __init__(self, app, max_bytes: int):
        self.app = app
        self.max_bytes = max_bytes

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            await self.app(scope, receive, send)
            return
        for name, value in scope["headers"]:
            if name == b"content-length":
                try:
                    declared = int(value)
                except ValueError:
                    declared = 0
                if declared > self.max_bytes:
                    await _send_error(
                        send,
                        413,
                        f"Request body is larger than {self.max_bytes} bytes (Content-Length {declared})",
                    )
                    return

        body = bytearray()
        while True:
            message = await receive()
            if message["type"] == "http.disconnect":
                return
            body.extend(message.get("body") or b"")
            if len(body) > self.max_bytes:
                await _send_error(send, 413, f"Request body is larger than {self.max_bytes} bytes")
                return
            if not message.get("more_body", False):
                break

        replayed = False

        async def replay():
            nonlocal replayed
            if replayed:
                return {"type": "http.disconnect"}
            replayed = True
            return {"type": "http.request", "body": bytes(body), "more_body": False}

        # 正文收完以后，再等一条消息就是断开（uvicorn 在连接断掉或响应开始时回 http.disconnect）。
        # 这里是**真的** receive，回放给 FastAPI 的那条 `replay` 第二次调用就直接答"断了"，不能拿来问。
        # 灯放在 scope["state"] 里，路由从 `request.state.disconnect` 拿（见 `StopSignal`）。
        gone = threading.Event()
        scope.setdefault("state", {})["disconnect"] = gone
        watching = asyncio.ensure_future(self._watch_disconnect(receive, gone))
        try:
            await self.app(scope, replay, send)
        finally:
            watching.cancel()

    @staticmethod
    async def _watch_disconnect(receive, gone: threading.Event) -> None:
        try:
            while True:
                message = await receive()
                if message["type"] == "http.disconnect":
                    gone.set()
                    return
        except asyncio.CancelledError:
            raise  # 请求正常结束，这里跟着取消
        except BaseException:  # noqa: BLE001 - 连接出错就当断开，不能让请求挂住
            gone.set()


def create_app(pool: ModelPool, token: str, max_request_seconds: float = DEFAULT_MAX_REQUEST_SECONDS) -> FastAPI:
    app = FastAPI(title="Sumika local models", version="1")
    app.state.pool = pool
    app.add_middleware(BodyLimitMiddleware, max_bytes=MAX_BODY_BYTES)
    app.add_middleware(TokenMiddleware, token=token)

    @contextlib.contextmanager
    def lease(model_id: str) -> Iterator[LoadedModel]:
        try:
            with pool.lease(model_id) as model:
                yield model
        except PoolError as exc:
            raise HTTPException(status_code=exc.status_code, detail=exc.detail) from exc

    def require(model_id: str, kind: str) -> ModelSpec:
        try:
            return pool.require(model_id, kind)
        except PoolError as exc:
            raise HTTPException(status_code=exc.status_code, detail=exc.detail) from exc

    def run(model_id: str, work, stop: StopSignal):
        """在计算线程里拿租约、算、放租约。等槽位（最多 30 秒）也是阻塞的，不能放在事件循环里。"""
        with lease(model_id) as model:
            return work(model, stop)

    async def watch(request: Request, stop: StopSignal) -> None:
        """断开或到截止时间就置位：每 50 毫秒看一眼，置位以后计算线程在下一句之前停。"""
        gone = getattr(request.state, "disconnect", None)
        deadline = None if max_request_seconds <= 0 else time.monotonic() + max_request_seconds
        while True:
            if isinstance(gone, threading.Event) and gone.is_set():
                stop.set("disconnect")
                return
            if deadline is not None and time.monotonic() >= deadline:
                stop.set("timeout")
                return
            await asyncio.sleep(STOP_POLL_SECONDS)

    async def compute(request: Request, model_id: str, work):
        """算一次，同时盯着客户端断开和 `--max-request-seconds`；停了就翻成 499 / 504。"""
        started = time.monotonic()
        stop = StopSignal()
        watching = asyncio.ensure_future(watch(request, stop))
        try:
            return await run_in_threadpool(run, model_id, work, stop)
        except Cancelled as exc:
            LOGGER.info(
                "STOPPED model=%s reason=%s ms=%s",
                model_id,
                exc.reason,
                int((time.monotonic() - started) * 1000),
            )
            if exc.reason == "timeout":
                raise HTTPException(
                    status_code=504,
                    detail=(
                        f"Gave up after --max-request-seconds {max_request_seconds:g}: the batch did not finish. "
                        "Send fewer inputs or raise the limit"
                    ),
                ) from exc
            # 客户端已经走了：499 是 nginx 那个"客户端关了连接"的码，没人收得到这个响应
            raise HTTPException(status_code=499, detail="Client disconnected") from exc
        finally:
            watching.cancel()

    @app.get("/health")
    def health() -> dict[str, object]:
        models = pool.snapshot()
        return {
            "status": "ok",
            "models": models,
            "loaded": [row["id"] for row in models if row["loaded"]],
            "resident_bytes": pool.resident_bytes(),
            "max_loaded": pool.max_loaded,
        }

    @app.get("/v1/models")
    def list_models() -> dict[str, object]:
        return {
            "object": "list",
            "data": [
                {
                    "id": row["id"],
                    "object": "model",
                    "owned_by": "local",
                    "kind": row["kind"],
                    "dimension": row["dimension"],
                    "max_tokens": row["max_tokens"],
                    "loaded": row["loaded"],
                }
                for row in pool.snapshot()
            ],
        }

    @app.post("/v1/embeddings")
    async def embeddings(request: EmbeddingsRequest, raw: Request) -> dict[str, object]:
        spec = require(request.model, "embed")
        texts = [request.input] if isinstance(request.input, str) else request.input
        if not texts:
            raise HTTPException(status_code=400, detail="Provide at least 1 input string")
        if len(texts) > MAX_BATCH_SIZE:
            raise HTTPException(status_code=400, detail=f"Provide between 1 and {MAX_BATCH_SIZE} input strings")
        if any(not text.strip() for text in texts):
            raise HTTPException(status_code=400, detail="Empty input strings are not supported")
        if request.dimensions is not None:
            reason = spec.dimensions_error(request.dimensions)
            if reason is not None:
                raise HTTPException(status_code=400, detail=reason)

        prepared = [spec.prefix(request.input_type, request.task) + text for text in texts]
        for text in prepared:
            if len(text) > spec.char_limit:
                raise HTTPException(
                    status_code=400,
                    detail=(
                        f"An input is {len(text)} characters; {spec.id!r} allows at most {spec.char_limit} "
                        f"({spec.max_tokens} tokens × {CHARS_PER_TOKEN}), prefix included"
                    ),
                )
        started = time.monotonic()
        vectors, tokens, hits = await compute(
            raw, spec.id, lambda model, stop: model.embed_texts(prepared, request.dimensions, stop)
        )
        LOGGER.info(
            "EMBED model=%s inputs=%s tokens=%s cache_hits=%s dimensions=%s ms=%s",
            spec.id,
            len(texts),
            tokens,
            hits,
            len(vectors[0]),
            int((time.monotonic() - started) * 1000),
        )
        return {
            "object": "list",
            "data": [
                {"object": "embedding", "index": index, "embedding": _encode_vector(vector, request.encoding_format)}
                for index, vector in enumerate(vectors)
            ],
            "model": spec.id,
            "usage": {"prompt_tokens": tokens, "total_tokens": tokens},
        }

    @app.post("/v1/rerank")
    async def rerank(request: RerankRequest, raw: Request) -> dict[str, object]:
        spec = require(request.model, "rerank")
        if not request.query.strip():
            raise HTTPException(status_code=400, detail="Empty query strings are not supported")
        if not request.documents:
            raise HTTPException(status_code=400, detail="Provide at least 1 document")
        if len(request.documents) > MAX_BATCH_SIZE:
            raise HTTPException(status_code=400, detail=f"Provide between 1 and {MAX_BATCH_SIZE} documents")
        if any(not document.strip() for document in request.documents):
            raise HTTPException(status_code=400, detail="Empty document strings are not supported")
        for label, text in (("query", request.query), *[("document", doc) for doc in request.documents]):
            if len(text) > spec.char_limit:
                raise HTTPException(
                    status_code=400,
                    detail=(
                        f"The {label} is {len(text)} characters; {spec.id!r} allows at most {spec.char_limit} "
                        f"({spec.max_tokens} tokens × {CHARS_PER_TOKEN})"
                    ),
                )

        started = time.monotonic()
        scores, tokens = await compute(
            raw, spec.id, lambda model, stop: model.rerank(request.query, request.documents, stop)
        )
        order = sorted(range(len(scores)), key=lambda index: (-float(scores[index]), index))
        limit = len(order) if request.top_n is None else max(1, min(request.top_n, len(order)))
        LOGGER.info(
            "RERANK model=%s documents=%s tokens=%s top_n=%s ms=%s",
            spec.id,
            len(request.documents),
            tokens,
            limit,
            int((time.monotonic() - started) * 1000),
        )
        return {
            "model": spec.id,
            "results": [
                {"index": index, "relevance_score": float(scores[index])} for index in order[:limit]
            ],
            "usage": {"total_tokens": tokens},
        }

    @app.post("/v1/models/{model_id}/unload")
    def unload(model_id: str) -> dict[str, object]:
        if model_id not in pool.spec_ids():
            raise HTTPException(status_code=404, detail=f"Unknown model {model_id!r}")
        unloaded, reason = pool.unload(model_id)
        return {"model": model_id, "unloaded": unloaded, "reason": reason}

    return app


def _port_available(host: str, port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        try:
            probe.bind((host, port))
        except OSError:
            return False
    return True


def _env_port(default: int) -> int:
    raw = (os.environ.get(PORT_ENV) or "").strip()
    if not raw:
        return default
    try:
        return int(raw)
    except ValueError as exc:
        raise ConfigError(f"{PORT_ENV}={raw!r} 不是整数") from exc


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Sumika local model server (embeddings + rerank)")
    parser.add_argument("--config", default=str(DEFAULT_CONFIG))
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=None, help=f"默认读 {PORT_ENV}，没有再取 11440")
    parser.add_argument("--max-loaded", type=int, default=2)
    parser.add_argument("--idle-unload", type=float, default=600.0, help="空闲多少秒卸载；0 = 不自动卸")
    parser.add_argument("--threads", type=int, default=2, help="每个模型的 ONNX intra-op 线程数")
    parser.add_argument("--max-bytes", type=int, default=4 * 1024**3)
    parser.add_argument("--wait", type=float, default=30.0, help="等加载 / 腾位置的秒数")
    parser.add_argument(
        "--max-request-seconds",
        type=float,
        default=DEFAULT_MAX_REQUEST_SECONDS,
        help="一次嵌入 / 重排最多算多少秒，到了在下一句之前停下回 504；0 = 不设上限",
    )
    args = parser.parse_args(argv)

    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
        datefmt="%H:%M:%S",
    )

    if args.host not in ("127.0.0.1", "localhost", "::1"):
        LOGGER.error("只允许监听本机回环地址，收到 %s", args.host)
        return 2
    token = (os.environ.get(TOKEN_ENV) or "").strip()
    if not token:
        LOGGER.error(
            "环境变量 %s 没设：每个请求都要 Authorization: Bearer <令牌>，没有令牌就拒绝启动"
            "（不让 11440 裸奔；令牌由起服务的一方生成，Go 那边照 native/internal/embed/local.go 的 randomToken）",
            TOKEN_ENV,
        )
        return 2
    try:
        specs = load_specs(Path(args.config), os.environ.get("LOCAL_MODELS_ROOT"))
        port = args.port if args.port is not None else _env_port(11440)
    except ConfigError as exc:
        LOGGER.error("%s", exc)
        return 2
    if not _port_available(args.host, port):
        LOGGER.error("%s:%s 已被占用（或没有权限绑定）；用 --port 换一个", args.host, port)
        return 2

    LOGGER.info(
        "models=%s max_loaded=%s idle_unload=%ss auth=bearer",
        ", ".join(specs),
        args.max_loaded,
        args.idle_unload,
    )
    pool = ModelPool(
        specs,
        max_loaded=args.max_loaded,
        idle_seconds=args.idle_unload,
        max_bytes=args.max_bytes,
        threads=args.threads,
        wait_seconds=args.wait,
    )
    app = create_app(pool, token, args.max_request_seconds)
    try:
        uvicorn.run(app, host=args.host, port=port, access_log=False, log_config=None)
    finally:
        pool.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
