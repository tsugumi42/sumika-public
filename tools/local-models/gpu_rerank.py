"""GPU 重排小服务：`/v1/rerank` 和 `server.py` 一字不差（同样的请求、返回、错误码、令牌、`/health`），
第一次请求才把模型搬上 GPU，空闲卸载，同一时间只放一个模型。

只做重排，不做嵌入：两个 4B 的 Qwen3 系重排模型（MemReranker-4B、Qwen3-Reranker-4B）都用
sentence-transformers 的 `CrossEncoder` 装（`modules.json` 里的 `1_LogitScore` 吐 log-odds：
`logit(yes) - logit(no)`），这里再 sigmoid 成 0～1，和 `server.py` 的数字口径一致。

显存不够（加载时 OOM）回 503，不退回 CPU：这次是拿它量数的，退回会把数字弄混。

用法（`SUMIKA_EMBED_TOKEN` 要和调用方一致）：

    set SUMIKA_EMBED_TOKEN = 32 位随机串
    python tools/local-models/gpu_rerank.py --port 11480 --idle-unload 120 --batch-size 8

`memory-eval -rerank-url http://127.0.0.1:11480/v1 -rerank-model memreranker-4b` 就能指过来。
"""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import gc
import hmac
import json
import logging
import os
import socket
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Iterator

import numpy as np
import torch
import uvicorn
from fastapi import FastAPI, HTTPException, Request
from fastapi.concurrency import run_in_threadpool
from pydantic import BaseModel, ConfigDict
from sentence_transformers import CrossEncoder
from sentence_transformers.util import batch_to_device

# 和 server.py 同一套上限：调用方不用为换服务改任何东西。
MAX_BATCH_SIZE = 64
MAX_BATCH_TOKENS = 32768
MAX_BODY_BYTES = 32 * 1024**2
# 单条字符数上限 = max_tokens × 这个倍数，用在分词之前（4 字符/token 对中英混排都够宽）。
CHARS_PER_TOKEN = 4
# 查询最多占 max_tokens - 这个数（留给 4 个特殊 token 和至少一段短文档），和 server.py 一样。
RERANK_SPECIAL_RESERVE = 16
DEFAULT_MAX_REQUEST_SECONDS = 600.0
STOP_POLL_SECONDS = 0.05
DEFAULT_MAX_TOKENS = 8192
TOKEN_ENV = "SUMIKA_EMBED_TOKEN"
MODELS_ROOT = Path(os.environ.get("LOCAL_MODELS_ROOT", "models"))
DEFAULT_MODELS = (
    ("memreranker-4b", str(MODELS_ROOT / "Reranker" / "memreranker-4b")),
    ("qwen3-reranker-4b", str(MODELS_ROOT / "Reranker" / "qwen3-reranker-4b")),
)

LOGGER = logging.getLogger("gpu-rerank")


class ConfigError(Exception):
    """启动参数 / 模型目录不对：直接退出，不带着半份配置跑。"""


class PoolError(Exception):
    """加载 / 容量的问题；接口层翻成对应的 HTTP 码。"""

    def __init__(self, status_code: int, detail: str):
        super().__init__(detail)
        self.status_code = status_code
        self.detail = detail


class Cancelled(Exception):
    """客户端断开 / 到了服务端截止时间：在批与批之间抛出，把模型锁放掉，剩下的不再算。"""

    def __init__(self, reason: str):
        super().__init__(reason)
        self.reason = reason  # "disconnect" / "timeout"


class StopSignal:
    """一次请求的停止信号：接口层在事件循环里置位，计算线程在批与批之间查。"""

    def __init__(self) -> None:
        self._event = threading.Event()
        self._reason: str | None = None

    def set(self, reason: str) -> None:
        if not self._event.is_set():
            self._reason = reason
            self._event.set()

    def check(self) -> None:
        if self._event.is_set():
            raise Cancelled(self._reason or "disconnect")


@dataclass(frozen=True)
class ModelSpec:
    id: str
    path: Path
    max_tokens: int = DEFAULT_MAX_TOKENS

    @property
    def char_limit(self) -> int:
        return self.max_tokens * CHARS_PER_TOKEN


@dataclass
class LoadedModel:
    spec: ModelSpec
    encoder: CrossEncoder
    load_ms: int
    # 只在 pool 锁里读写；卸载接口靠它判断"正在用"。
    refs: int = 0
    last_used: float = field(default_factory=time.monotonic)

    def pair_tokens(self, query: str, documents: list[str]) -> int:
        """和真正进模型的一模一样的 token 数（含模板和特殊 token，截断之后）。"""
        features = self.encoder.preprocess([(query, document) for document in documents])
        return int(features["attention_mask"].sum())

    def score(
        self,
        query: str,
        documents: list[str],
        batch_size: int,
        stop: StopSignal | None,
    ) -> tuple[np.ndarray, int]:
        """分批送进 GPU，返回（0～1 的分数, token 数）。

        显式分批而不是信 `predict` 的内部排序：批大小是参数，量数时要能说清一批到底几条。
        """
        scores = np.empty(len(documents), dtype=np.float64)
        tokens = 0
        self.encoder.eval()
        for start in range(0, len(documents), batch_size):
            if stop is not None:
                stop.check()
            chunk = [(query, document) for document in documents[start : start + batch_size]]
            features = self.encoder.preprocess(chunk)
            tokens += int(features["attention_mask"].sum())
            features = batch_to_device(features, self.encoder.device)
            with torch.inference_mode():
                logits = self.encoder(features)["scores"]
            logits = logits.float().reshape(-1).cpu().numpy()
            # 和 server.py 一样：sigmoid 之后越大越相关（LogitScore 吐的是 logit(yes) - logit(no)）。
            scores[start : start + len(chunk)] = 1.0 / (1.0 + np.exp(-logits))
        if scores.shape != (len(documents),) or not np.isfinite(scores).all():
            raise RuntimeError(f"{self.spec.id}: 重排输出形状 {scores.shape} 不对或含 NaN / Inf")
        return scores, tokens


def _is_oom(exc: BaseException) -> bool:
    if isinstance(exc, torch.cuda.OutOfMemoryError):
        return True
    return "out of memory" in str(exc).lower()


def _gpu_mb() -> float:
    return torch.cuda.memory_allocated() / 1024**2


def _peak_mb() -> float:
    return torch.cuda.max_memory_allocated() / 1024**2


class ModelPool:
    """一次只装一个模型：load / 卸载 / 推理都在同一把锁里，进来的第二路排队。"""

    def __init__(
        self,
        specs: dict[str, ModelSpec],
        *,
        idle_seconds: float = 120.0,
        batch_size: int = 8,
        wait_seconds: float = 30.0,
    ):
        self._specs = specs
        self._lock = threading.Lock()
        self._current: LoadedModel | None = None
        self._idle_seconds = idle_seconds
        self._batch_size = batch_size
        self._wait = wait_seconds
        self._stop = threading.Event()
        self._reaper = threading.Thread(target=self._reap, name="idle-unload", daemon=True)
        self._reaper.start()

    def spec_ids(self) -> set[str]:
        return set(self._specs)

    def spec(self, model_id: str) -> ModelSpec:
        spec = self._specs.get(model_id)
        if spec is None:
            raise PoolError(404, f"Unknown model {model_id!r}; available: {', '.join(sorted(self._specs))}")
        return spec

    @property
    def batch_size(self) -> int:
        return self._batch_size

    @property
    def max_loaded(self) -> int:
        return 1

    @contextlib.contextmanager
    def lease(self, model_id: str) -> Iterator[LoadedModel]:
        if model_id not in self._specs:
            raise PoolError(404, f"Unknown model {model_id!r}; available: {', '.join(sorted(self._specs))}")
        if not self._lock.acquire(timeout=self._wait):
            raise PoolError(503, f"Another request is still using the GPU after {self._wait:g}s; retry later")
        try:
            model = self._current
            if model is not None and model.spec.id != model_id:
                # 先把自己的局部引用丢掉再空缓存：否则老模型还活着，新模型装不下。
                self._current = None
                del model
                model = None
                self._free_cuda("switch")
            if model is None:
                model = self._load_locked(self._specs[model_id])
                self._current = model
            model.refs += 1
            model.last_used = time.monotonic()
            try:
                yield model
            finally:
                model.refs -= 1
                model.last_used = time.monotonic()
        finally:
            self._lock.release()

    def unload(self, model_id: str) -> tuple[bool, str]:
        if model_id not in self._specs:
            raise PoolError(404, f"Unknown model {model_id!r}")
        # 正在推理时等它算完再卸：接口语义和 server.py 一致（"用着的不卸"），这里不并发抢锁。
        with self._lock:
            model = self._current
            if model is None or model.spec.id != model_id:
                return False, "not_loaded"
            if model.refs > 0:
                return False, "in_use"
            self._current = None
            del model
            self._free_cuda("requested")
            return True, "unloaded"

    def snapshot(self) -> list[dict[str, object]]:
        now = time.monotonic()
        with self._lock:
            rows = []
            for model_id in sorted(self._specs):
                model = self._current if self._current is not None and self._current.spec.id == model_id else None
                rows.append(
                    {
                        "id": model_id,
                        "kind": "rerank",
                        "dimension": 0,
                        "max_tokens": self._specs[model_id].max_tokens,
                        "matryoshka": False,
                        "loaded": model is not None,
                        "load_ms": model.load_ms if model is not None else None,
                        "last_used_ms_ago": int((now - model.last_used) * 1000) if model is not None else None,
                    }
                )
            return rows

    def resident_bytes(self) -> int:
        with self._lock:
            return int(torch.cuda.memory_allocated()) if self._current is not None else 0

    def close(self) -> None:
        self._stop.set()
        self._reaper.join(timeout=5)
        with self._lock:
            model = self._current
            self._current = None
            del model
            self._free_cuda("shutdown")

    # ---- 内部 -------------------------------------------------------------

    def _load_locked(self, spec: ModelSpec) -> LoadedModel:
        if not spec.path.is_dir():
            raise PoolError(500, f"Model directory {str(spec.path)!r} is missing")
        started = time.monotonic()
        torch.cuda.reset_peak_memory_stats()
        LOGGER.info("LOADING model=%s gpu_mb=%.1f", spec.id, _gpu_mb())
        try:
            encoder = CrossEncoder(
                str(spec.path),
                device="cuda",
                model_kwargs={"torch_dtype": torch.bfloat16},
                max_length=spec.max_tokens,
                local_files_only=True,
            )
            # 预热一次：把 CUDA 上下文、kernel 和显存池先摊开，冷启动耗时才好量。
            encoder.predict([("预热", "预热")], batch_size=1, show_progress_bar=False)
        except BaseException as exc:  # noqa: BLE001 - 加载失败要翻成 HTTP 码，不能崩
            torch.cuda.empty_cache()
            gc.collect()
            if _is_oom(exc):
                LOGGER.error("LOAD-OOM model=%s gpu_mb=%.1f peak_mb=%.1f", spec.id, _gpu_mb(), _peak_mb())
                raise PoolError(
                    503,
                    f"Not enough GPU memory to load {spec.id!r}: CUDA out of memory. "
                    "Not falling back to CPU",
                ) from exc
            if isinstance(exc, PoolError):
                raise
            LOGGER.exception("LOAD-FAILED model=%s", spec.id)
            raise PoolError(500, f"Failed to load {spec.id!r}: {type(exc).__name__}: {exc}") from exc
        load_ms = int((time.monotonic() - started) * 1000)
        LOGGER.info(
            "LOAD model=%s ms=%s gpu_mb=%.1f peak_mb=%.1f",
            spec.id,
            load_ms,
            _gpu_mb(),
            _peak_mb(),
        )
        return LoadedModel(spec=spec, encoder=encoder, load_ms=load_ms)

    def _free_cuda(self, reason: str) -> None:
        """调用方必须先把模型引用丢干净（`self._current` 和局部变量）再进这里。"""
        gc.collect()
        torch.cuda.empty_cache()
        torch.cuda.reset_peak_memory_stats()
        LOGGER.info("UNLOAD reason=%s gpu_mb=%.1f", reason, _gpu_mb())

    def _reap(self) -> None:
        while not self._stop.wait(1.0):
            if self._idle_seconds <= 0:
                continue
            if not self._lock.acquire(timeout=1.0):
                continue
            try:
                if self._current is None or self._current.refs > 0:
                    continue
                idle = time.monotonic() - self._current.last_used
                if idle < self._idle_seconds:
                    continue
                # 不把模型绑到局部变量：绑了的话 `_free_cuda` 里它还活着，显存放不掉。
                LOGGER.info("IDLE model=%s idle_s=%.1f", self._current.spec.id, idle)
                self._current = None
                self._free_cuda("idle")
            finally:
                self._lock.release()


# ---- 接口 -----------------------------------------------------------------


class RerankRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    model: str
    query: str
    documents: list[str]
    top_n: int | None = None


async def _send_error(send, status: int, detail: str, extra: list[tuple[bytes, bytes]] | None = None) -> None:
    body = json.dumps({"detail": detail}, ensure_ascii=False).encode("utf-8")
    headers = [(b"content-type", b"application/json; charset=utf-8"), (b"content-length", str(len(body)).encode())]
    headers.extend(extra or [])
    await send({"type": "http.response.start", "status": status, "headers": headers})
    await send({"type": "http.response.body", "body": body})


class TokenMiddleware:
    """每个请求（含 `/health`）都要 `Authorization: Bearer $SUMIKA_EMBED_TOKEN`，否则 401。

    令牌由起服务的一方生成（和 server.py 一样），服务只校验、不回显。
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
    """`Content-Length` 和实际收到的字节都卡在 `MAX_BODY_BYTES`，超了 413（和 server.py 同一套）。"""

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
    app = FastAPI(title="Sumika GPU rerank", version="1")
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

    def run(model_id: str, work, stop: StopSignal):
        with lease(model_id) as model:
            return work(model, stop)

    async def watch(request: Request, stop: StopSignal) -> None:
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
        started = time.monotonic()
        stop = StopSignal()
        watching = asyncio.ensure_future(watch(request, stop))
        try:
            return await run_in_threadpool(run, model_id, work, stop)
        except Cancelled as exc:
            LOGGER.info(
                "STOPPED model=%s reason=%s ms=%s", model_id, exc.reason, int((time.monotonic() - started) * 1000)
            )
            if exc.reason == "timeout":
                raise HTTPException(
                    status_code=504,
                    detail=(
                        f"Gave up after --max-request-seconds {max_request_seconds:g}: the batch did not finish. "
                        "Send fewer documents or raise the limit"
                    ),
                ) from exc
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

    @app.post("/v1/rerank")
    async def rerank(request: RerankRequest, raw: Request) -> dict[str, object]:
        if request.model not in pool.spec_ids():
            raise HTTPException(
                status_code=404,
                detail=f"Unknown model {request.model!r}; available: {', '.join(sorted(pool.spec_ids()))}",
            )
        if not request.query.strip():
            raise HTTPException(status_code=400, detail="Empty query strings are not supported")
        if not request.documents:
            raise HTTPException(status_code=400, detail="Provide at least 1 document")
        if len(request.documents) > MAX_BATCH_SIZE:
            raise HTTPException(status_code=400, detail=f"Provide between 1 and {MAX_BATCH_SIZE} documents")
        if any(not document.strip() for document in request.documents):
            raise HTTPException(status_code=400, detail="Empty document strings are not supported")
        spec = pool.spec(request.model)
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

        def work(model: LoadedModel, stop: StopSignal):
            if stop is not None:
                stop.check()
            query_tokens = len(model.encoder.tokenizer.encode(request.query, add_special_tokens=False))
            budget = model.spec.max_tokens - RERANK_SPECIAL_RESERVE
            if query_tokens > budget:
                raise HTTPException(
                    status_code=400,
                    detail=(
                        f"Query is {query_tokens} tokens; {model.spec.id!r} allows at most {budget} "
                        f"(max_tokens {model.spec.max_tokens} minus {RERANK_SPECIAL_RESERVE} for the pair)"
                    ),
                )
            tokens = model.pair_tokens(request.query, request.documents)
            if tokens > MAX_BATCH_TOKENS:
                raise HTTPException(status_code=400, detail=f"A batch is limited to {MAX_BATCH_TOKENS} tokens")
            return model.score(request.query, request.documents, pool.batch_size, stop)

        scores, tokens = await compute(raw, request.model, work)
        order = sorted(range(len(scores)), key=lambda index: (-float(scores[index]), index))
        limit = len(order) if request.top_n is None else max(1, min(request.top_n, len(order)))
        LOGGER.info(
            "RERANK model=%s documents=%s tokens=%s top_n=%s ms=%s gpu_mb=%.1f peak_mb=%.1f",
            request.model,
            len(request.documents),
            tokens,
            limit,
            int((time.monotonic() - started) * 1000),
            _gpu_mb(),
            _peak_mb(),
        )
        return {
            "model": request.model,
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


def load_specs(entries: list[str]) -> dict[str, ModelSpec]:
    specs: dict[str, ModelSpec] = {}
    for entry in entries:
        model_id, sep, raw_path = entry.partition("=")
        if not sep or not model_id or not raw_path:
            raise ConfigError(f"--model 要写成 id=目录，收到 {entry!r}")
        path = Path(raw_path)
        if not path.is_dir():
            raise ConfigError(f"模型 {model_id!r} 的目录不在：{path}")
        if model_id in specs:
            raise ConfigError(f"--model 里 {model_id!r} 写了两次")
        specs[model_id] = ModelSpec(id=model_id, path=path)
    if not specs:
        raise ConfigError("至少登记一个模型")
    return specs


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Sumika GPU rerank server (Qwen3-Reranker family)")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=11480)
    parser.add_argument(
        "--model",
        action="append",
        default=None,
        help="id=目录，可写多次；默认 " + ", ".join(f"{model_id}={path}" for model_id, path in DEFAULT_MODELS),
    )
    parser.add_argument("--idle-unload", type=float, default=120.0, help="空闲多少秒卸载；0 = 不自动卸")
    parser.add_argument("--batch-size", type=int, default=8, help="一次请求的文档分几批送进 GPU")
    parser.add_argument("--wait", type=float, default=30.0, help="等 GPU 锁的秒数，等不到 503")
    parser.add_argument(
        "--max-request-seconds",
        type=float,
        default=DEFAULT_MAX_REQUEST_SECONDS,
        help="一次重排最多算多少秒，到了在下一批之前停下回 504；0 = 不设上限",
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
            "环境变量 %s 没设：每个请求都要 Authorization: Bearer <令牌>，没有令牌就拒绝启动",
            TOKEN_ENV,
        )
        return 2
    if args.batch_size <= 0:
        LOGGER.error("--batch-size 要大于 0，收到 %s", args.batch_size)
        return 2
    if not torch.cuda.is_available():
        LOGGER.error("torch 看不到 CUDA（torch %s）；这个服务不退回 CPU", torch.__version__)
        return 2
    try:
        specs = load_specs(args.model or [f"{model_id}={path}" for model_id, path in DEFAULT_MODELS])
    except ConfigError as exc:
        LOGGER.error("%s", exc)
        return 2
    if not _port_available(args.host, args.port):
        LOGGER.error("%s:%s 已被占用（或没有权限绑定）；用 --port 换一个", args.host, args.port)
        return 2

    LOGGER.info(
        "models=%s device=%s idle_unload=%ss batch_size=%s auth=bearer",
        ", ".join(specs),
        torch.cuda.get_device_name(0),
        args.idle_unload,
        args.batch_size,
    )
    pool = ModelPool(
        specs,
        idle_seconds=args.idle_unload,
        batch_size=args.batch_size,
        wait_seconds=args.wait,
    )
    app = create_app(pool, token, args.max_request_seconds)
    try:
        uvicorn.run(app, host=args.host, port=args.port, access_log=False, log_config=None)
    finally:
        pool.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
