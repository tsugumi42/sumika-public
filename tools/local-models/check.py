"""检查 tools/local-models 的服务：兜底启动、令牌、请求体上限、每个嵌入模型的三句相似度、批量不变性、
和旧服务对拍、维度截断、重排、并发、槽位满、边界、一批 64 条的耗时、客户端断开和服务端截止时间。

跑（低优先级，一次只装一个模型）：

    powershell -NoProfile -ExecutionPolicy Bypass -File scripts/run-low.ps1 \\
        <独立环境>\\Scripts\\python.exe tools/local-models/check.py

只用标准库发请求；数值用 numpy（venv 里有）。模型目录只读。和旧服务对拍要另设 `OLD_SERVER`
指向旧服务的 server.py（不设就跳过这一项），也不改旧项目的任何文件。
令牌由 check.py 自己生成（`secrets.token_hex(16)`），和服务子进程的环境变量对上；另起一个没令牌的
子进程验"拒绝启动 + 退出码 2"。
"""

from __future__ import annotations

import argparse
import base64
import gc
import importlib.util
import json
import os
import secrets
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

import numpy as np

HERE = Path(__file__).resolve().parent
OLD_SERVER = Path(os.environ["OLD_SERVER"]) if os.environ.get("OLD_SERVER") else None

# 三句编的中文：前两句意思接近，第三句无关。
TRIPLE = (
    "明天下午三点在会议室开项目评审会，记得带上笔记本电脑和充电器。",
    "评审会定在下午三点，在会议室，电脑和充电器别忘了。",
    "冰箱里的牛奶已经过期两天了，得赶紧扔掉。",
)

RERANK_QUERY = "主人喜欢喝什么口味的咖啡？"
RERANK_DOCUMENTS = (
    "主人每天早上都要喝一杯冰美式，不加糖也不加奶。",
    "冰箱里的牛奶过期了，得扔掉。",
    "周末打算去公园散步，天气预报说会下雨。",
)
# 编的中文"问题 - 相关 / 不相关"对：每个重排模型都要验相关的分数更高。
RERANK_PAIRS = (
    ("明天开会要带什么？", "下午三点在会议室开评审会，记得带上笔记本电脑和充电器。", "周末打算去公园散步，天气预报说会下雨。"),
    ("怎么给 Python 列表去重？", "用 dict.fromkeys(列表) 或者 set(列表) 就能去重，set 不保序。", "今天晚饭吃的是番茄炒蛋和一碗米饭。"),
    ("冰箱里的牛奶还能喝吗？", "牛奶已经过期两天了，得赶紧扔掉。", "主人每天早上都要喝一杯冰美式，不加糖也不加奶。"),
)

EMBED_MODELS = (
    "qwen3-embedding-0.6b",
    "gte-multilingual-base",
    "pplx-embed-v1-0.6b",
    "harrier-270m",
    "granite-97m-r2",
    "bekko-a25m",
    "horizon-embedding-small",
)
RERANK_MODELS = (
    "bge-reranker-v2-m3",
    "horizon-reranker-small",
    "sno-lab-reranker-small-int8",
)
# 边界用例只打一个重排模型就够了：三个走的是同一条路由和同一套错误码。
RERANK_MODEL = RERANK_MODELS[0]

# 每个支持截断的模型的维度用例（按模型卡）：能用的、不能用的。没列出来的模型不该支持 dimensions。
DIMENSION_CASES = {
    "qwen3-embedding-0.6b": ([64, 1024], [16, 2048]),
    "gte-multilingual-base": ([128, 768], [64, 127]),
    "pplx-embed-v1-0.6b": ([256], [16, 2048]),
    "bekko-a25m": ([64, 128, 256], [32, 100, 512]),
}

# Client 里"用构造时带的令牌"的标记；传 None = 故意不带 Authorization，传字符串 = 换一个错的。
_USE_CLIENT = object()


def cosine(a: np.ndarray, b: np.ndarray) -> float:
    return float(np.dot(a, b) / (np.linalg.norm(a) * np.linalg.norm(b)))


class Report:
    def __init__(self) -> None:
        self.failures: list[str] = []
        self.lines: list[str] = []

    def say(self, text: str) -> None:
        print(text, flush=True)
        self.lines.append(text)

    def check(self, condition: bool, label: str) -> bool:
        if condition:
            return True
        self.failures.append(label)
        self.say(f"   FAIL {label}")
        return False


class Client:
    def __init__(self, base: str, token: str | None):
        self.base = base
        self.token = token

    def get(self, path: str, token: object = _USE_CLIENT) -> tuple[int, object]:
        return self._send("GET", path, None, token)

    def post(self, path: str, payload: dict, token: object = _USE_CLIENT) -> tuple[int, object]:
        return self._send("POST", path, payload, token)

    def _send(self, method: str, path: str, payload: dict | None, token: object) -> tuple[int, object]:
        data = None if payload is None else json.dumps(payload, ensure_ascii=False).encode("utf-8")
        headers = {"Content-Type": "application/json"}
        bearer = self.token if token is _USE_CLIENT else token
        if bearer is not None:
            headers["Authorization"] = f"Bearer {bearer}"
        request = urllib.request.Request(
            self.base + path,
            data=data,
            method=method,
            headers=headers,
        )
        try:
            with urllib.request.urlopen(request, timeout=1800) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8", "replace")
            try:
                return exc.code, json.loads(body)
            except json.JSONDecodeError:
                return exc.code, {"raw": body}


def _status_of(response: str) -> int:
    try:
        return int(response.split("\r\n", 1)[0].split(" ")[1])
    except (IndexError, ValueError):
        return 0


def raw_request(base: str, token: str | None, path: str, headers: list[tuple[str, str]]) -> tuple[int, str]:
    """手写一个请求头（用来发一个不能靠 urllib 发的 Content-Length），只读回状态码和正文。"""
    host, port = base.removeprefix("http://").split(":")
    lines = [f"POST {path} HTTP/1.1", f"Host: {host}:{port}", "Content-Type: application/json"]
    if token is not None:
        lines.append(f"Authorization: Bearer {token}")
    lines.extend(f"{name}: {value}" for name, value in headers)
    request = ("\r\n".join(lines) + "\r\n\r\n").encode("ascii")
    with socket.create_connection((host, int(port)), timeout=60) as sock:
        sock.sendall(request)
        chunks: list[bytes] = []
        while True:
            try:
                part = sock.recv(65536)
            except ConnectionResetError:
                break
            if not part:
                break
            chunks.append(part)
    response = b"".join(chunks).decode("utf-8", "replace")
    return _status_of(response), response


def chunked_over_limit(base: str, token: str) -> tuple[int, int]:
    """分块传输（没有 Content-Length）：真发超过上限的字节，看"实际收到的字节"那条路径。"""
    host, port = base.removeprefix("http://").split(":")
    with socket.create_connection((host, int(port)), timeout=60) as sock:
        sock.settimeout(10.0)
        head = (
            f"POST /v1/embeddings HTTP/1.1\r\nHost: {host}:{port}\r\nAuthorization: Bearer {token}\r\n"
            "Content-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n"
        )
        sock.sendall(head.encode())
        chunk = b"a" * (4 * 1024 * 1024)
        sent = 0
        try:
            for _ in range(10):
                sock.sendall(hex(len(chunk))[2:].encode() + b"\r\n" + chunk + b"\r\n")
                sent += len(chunk)
                if sent > 32 * 1024**2:
                    break
        except OSError:
            pass  # 服务已经回了 413 并停止读，客户端这边写不进去是正常的
        data = b""
        try:
            while b"\r\n\r\n" not in data:
                part = sock.recv(65536)
                if not part:
                    break
                data += part
        except OSError:
            pass
    return _status_of(data.decode("utf-8", "replace")), sent


def free_port(start: int) -> int:
    for port in range(start, start + 40):
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
            try:
                probe.bind(("127.0.0.1", port))
            except OSError:
                continue
            return port
    raise RuntimeError(f"从 {start} 起 40 个端口都不空闲")


class Server:
    """把 server.py 起成一个子进程，日志收进缓冲，出问题时打出来。

    `token = None` 表示不设 `SUMIKA_EMBED_TOKEN`（服务应该拒绝启动，用来验退出码 2）。
    """

    def __init__(self, port: int, extra: list[str] | None = None, token: str | None = _USE_CLIENT):
        self.port = port
        self.base = f"http://127.0.0.1:{port}"
        if token is _USE_CLIENT:
            token = secrets.token_hex(16)
        self.token = token
        self.log: list[str] = []
        command = [
            sys.executable,
            str(HERE / "server.py"),
            "--port",
            str(port),
            "--max-loaded",
            "1",
            "--idle-unload",
            "0",
            "--threads",
            "2",
        ] + list(extra or [])
        env = {key: value for key, value in os.environ.items() if key != "SUMIKA_EMBED_TOKEN"}
        if token is not None:
            env["SUMIKA_EMBED_TOKEN"] = token
        self.proc = subprocess.Popen(
            command,
            cwd=str(HERE),
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
        self._reader = threading.Thread(target=self._drain, daemon=True)
        self._reader.start()

    def _drain(self) -> None:
        assert self.proc.stdout is not None
        for line in self.proc.stdout:
            self.log.append(line.rstrip())

    def wait_ready(self, timeout: float = 120.0) -> None:
        deadline = time.time() + timeout
        client = Client(self.base, self.token)
        while time.time() < deadline:
            if self.proc.poll() is not None:
                raise RuntimeError("服务进程提前退出：\n" + "\n".join(self.log[-40:]))
            try:
                status, _ = client.get("/health")
                if status == 200:
                    return
            except (urllib.error.URLError, ConnectionError, OSError):
                pass
            time.sleep(0.3)
        raise RuntimeError("等 /health 超时：\n" + "\n".join(self.log[-40:]))

    def stop(self) -> None:
        self.proc.terminate()
        try:
            self.proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait(timeout=15)


def health_row(client: Client, model_id: str) -> dict:
    _status, body = client.get("/health")
    assert isinstance(body, dict)
    for row in body["models"]:
        if row["id"] == model_id:
            return row
    raise AssertionError(f"/health 没有 {model_id}")


_MODULE = None


def server_module():
    """导入 server.py（同一份代码、同一份配置）。只导一次。"""
    global _MODULE
    if _MODULE is None:
        spec = importlib.util.spec_from_file_location("local_models_server", HERE / "server.py")
        assert spec is not None and spec.loader is not None
        module = importlib.util.module_from_spec(spec)
        sys.modules["local_models_server"] = module
        spec.loader.exec_module(module)
        _MODULE = module
    return _MODULE


def check_batch_invariance(module, spec, report: Report) -> None:
    """同一个会话，一批同 token 长度的文本 vs 一条一条：逐维一致。

    2026-10-28 复核时发现量化图对**批次大小**敏感（同一句一次算 vs 和别人一批算，余弦
    qwen 0.932 / gte 0.987 / granite 0.978），所以服务改成一个前向只放一行；这条拦住
    "又为吞吐把几行合成一次前向"。
    """
    loaded = module._load(spec, threads=2)
    try:
        tokenizer = loaded.tokenizer
        base = "评审会定在下午三点，在会议室，电脑和充电器别忘了。"
        candidates = [base + suffix for suffix in ("", "呢", "。", "啊", "吗", "呀", "哦", "吧", "。呢")]
        by_length: dict[int, list[str]] = {}
        for text in candidates:
            by_length.setdefault(len(tokenizer.encode(text).ids), []).append(text)
        texts = max(by_length.values(), key=len)[:3]
        tokens = len(tokenizer.encode(texts[0]).ids)
        if not report.check(len(texts) >= 2, f"{spec.id} 找不到两条同 token 长度的文本，批量不变性没验到"):
            return
        loaded.cache.clear()
        batched = loaded.embed_texts(texts, None)[0]
        singles = []
        for text in texts:
            loaded.cache.clear()
            singles.append(loaded.embed_texts([text], None)[0][0])
        diff = max(float(np.max(np.abs(batched[index] - singles[index]))) for index in range(len(texts)))
        report.say(
            f"   批量不变性：{len(texts)} 条 × {tokens} token 一次批量 vs 逐条 max|Δ|={diff:.3g}"
        )
        report.check(diff <= 1e-6, f"{spec.id} 批量结果和逐条不一致 (max|Δ|={diff:.3g})")
    finally:
        loaded.consume()
        del loaded
        gc.collect()


def old_qwen_vector(text: str, model_dir: Path) -> np.ndarray:
    """只读地 import OLD_SERVER 指向的旧 server.py 里的编码类，算同一句话的向量。

    `model_dir` 从 models.toml 的规格拿（`spec.onnx.parent`）：模型根目录和 `_待分类/` 前缀只在
    配置里写一次，这里不再写一份。
    """
    spec = importlib.util.spec_from_file_location("maibot_embeddings_server", OLD_SERVER)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules["maibot_embeddings_server"] = module
    spec.loader.exec_module(module)
    encoder = module.QwenEncoder(model_dir)
    try:
        vectors, _tokens = encoder.encode([text], None)
        return np.asarray(vectors[0], dtype=np.float64)
    finally:
        del encoder
        gc.collect()


def check_dimensions(
    client: Client, report: Report, spec, model_id: str, full: list[np.ndarray]
) -> None:
    """`dimensions` 按模型卡：能用的截断以后重新归一化、三句顺序不变；不能用的 400。

    `full` 是同一批三句的全维向量（缓存已经热了，这些请求不再前向）。
    """
    ok_dims, bad_dims = DIMENSION_CASES.get(model_id, ([], []))
    if not ok_dims and not bad_dims:
        report.check(not spec.matryoshka, f"{model_id} 登记成 matryoshka 但 check.py 没列维度用例")
        return
    for dimensions in bad_dims:
        status, body = client.post(
            "/v1/embeddings", {"model": model_id, "input": TRIPLE[0], "dimensions": dimensions}
        )
        report.check(status == 400, f"{model_id} dimensions={dimensions}：期望 400，得到 {status} {body}")
    for dimensions in ok_dims:
        status, body = client.post("/v1/embeddings", {"model": model_id, "input": list(TRIPLE), "dimensions": dimensions})
        if not report.check(status == 200, f"{model_id} dimensions={dimensions}：期望 200，得到 {status} {body}"):
            continue
        rows = sorted(body["data"], key=lambda row: row["index"])
        vectors = [np.asarray(row["embedding"], dtype=np.float64) for row in rows]
        if not report.check(
            all(len(vector) == dimensions for vector in vectors),
            f"{model_id} dimensions={dimensions} 返回的维度不是 {dimensions}",
        ):
            continue
        if dimensions == spec.dimension:
            report.say(f"   dimensions={dimensions}（全维）：长度对")
            continue
        norm = float(np.linalg.norm(vectors[0]))
        report.check(abs(norm - 1.0) <= 1e-3, f"{model_id} 截到 {dimensions} 维以后没归一化 (norm={norm:.4f})")
        close = cosine(vectors[0], vectors[1])
        far = cosine(vectors[0], vectors[2])
        full_close = cosine(full[0], full[1])
        report.say(
            f"   dimensions={dimensions}：cos(近1,近2)={close:.4f}（全维 {full_close:.4f}）"
            f"  cos(近1,远)={far:.4f}"
        )
        report.check(close > far, f"{model_id} 截到 {dimensions} 维以后三句顺序坏了")
        report.check(
            close >= 0.85 * full_close,
            f"{model_id} 截到 {dimensions} 维以后和全维差太多（{close:.4f} vs {full_close:.4f}）",
        )


def check_embed(client: Client, report: Report, model_id: str, spec) -> None:
    started = time.time()
    status, body = client.post("/v1/embeddings", {"model": model_id, "input": list(TRIPLE)})
    first_wall_ms = int((time.time() - started) * 1000)
    report.check(status == 200, f"{model_id} 嵌入请求 HTTP {status}: {body}")
    if status != 200:
        return
    rows = sorted(body["data"], key=lambda row: row["index"])
    vectors = [np.asarray(row["embedding"], dtype=np.float64) for row in rows]
    dimension = len(vectors[0])
    health = health_row(client, model_id)
    report.say(
        f"   维度={dimension}  加载={health['load_ms']}ms  首次请求（含加载）={first_wall_ms}ms  "
        f"tokens={body['usage']['prompt_tokens']}"
    )
    report.check(all(len(vector) == dimension for vector in vectors), f"{model_id} 同一批向量维度不一致")
    report.check(health["loaded"] is True, f"{model_id} 请求以后 /health 里没标 loaded")
    norm = float(np.linalg.norm(vectors[0]))
    report.check(abs(norm - 1.0) <= 1e-3, f"{model_id} 向量没有归一化 (norm={norm:.4f})")

    close = cosine(vectors[0], vectors[1])
    far_a = cosine(vectors[0], vectors[2])
    far_b = cosine(vectors[1], vectors[2])
    report.say(f"   cos(近1,近2)={close:.4f}  cos(近1,远)={far_a:.4f}  cos(近2,远)={far_b:.4f}")
    report.check(close > far_a and close > far_b, f"{model_id} 接近的两句没有高于无关那句")

    # base64 和 float 是同一个向量
    status_b64, body_b64 = client.post(
        "/v1/embeddings", {"model": model_id, "input": TRIPLE[0], "encoding_format": "base64"}
    )
    if report.check(status_b64 == 200, f"{model_id} base64 请求 HTTP {status_b64}"):
        decoded = np.frombuffer(base64.b64decode(body_b64["data"][0]["embedding"]), dtype="<f4").astype(np.float64)
        report.check(
            decoded.shape == vectors[0].shape and float(np.max(np.abs(decoded - vectors[0]))) <= 1e-6,
            f"{model_id} base64 和 float 不是同一个向量",
        )

    check_dimensions(client, report, spec, model_id, vectors)

    unloaded = client.post(f"/v1/models/{model_id}/unload", {})
    report.check(
        unloaded[0] == 200 and bool(unloaded[1]["unloaded"]),
        f"{model_id} 卸载失败: {unloaded}",
    )

    if model_id == "qwen3-embedding-0.6b":
        if OLD_SERVER is None:
            report.say("   没设 OLD_SERVER，跳过和旧服务的对拍")
        else:
            old = old_qwen_vector(TRIPLE[0], spec.onnx.parent)
            cos = cosine(vectors[0], old)
            diff = float(np.max(np.abs(vectors[0] - old)))
            report.say(f"   和旧服务（{OLD_SERVER}）比：cos={cos:.6f}  max|Δ|={diff:.2e}")
            report.check(cos >= 0.999, f"{model_id} 和旧服务的 Qwen 向量余弦 {cos:.6f} < 0.999")


def check_rerank(client: Client, report: Report, model_id: str) -> None:
    started = time.time()
    status, body = client.post(
        "/v1/rerank",
        {"model": model_id, "query": RERANK_QUERY, "documents": list(RERANK_DOCUMENTS)},
    )
    wall_ms = int((time.time() - started) * 1000)
    report.check(status == 200, f"{model_id} 重排请求 HTTP {status}: {body}")
    if status != 200:
        return
    health = health_row(client, model_id)
    scores = {row["index"]: row["relevance_score"] for row in body["results"]}
    order = [row["index"] for row in body["results"]]
    report.say(
        f"   加载={health['load_ms']}ms  请求={wall_ms}ms  排名={order}  "
        + " ".join(f"doc{index}={scores[index]:.4f}" for index in sorted(scores))
    )
    report.check(order[0] == 0, f"{model_id} 重排第一名不是相关那段 (order={order})")
    report.check(all(0.0 <= score <= 1.0 for score in scores.values()), f"{model_id} 重排分数不在 0～1")
    report.check(scores[0] > scores[1], f"{model_id} 相关那段的分数没有高于无关那段")
    report.check(
        [row["index"] for row in body["results"]] == sorted(order, key=lambda index: -scores[index]),
        f"{model_id} 重排结果没有按分数从大到小",
    )

    # 编的中文问题：相关的那段分数要高过不相关的那段（池化 / 权重错时这里先红）。
    for query, relevant, irrelevant in RERANK_PAIRS:
        status_pair, body_pair = client.post(
            "/v1/rerank", {"model": model_id, "query": query, "documents": [irrelevant, relevant]}
        )
        if not report.check(status_pair == 200, f"{model_id} 问题对请求 HTTP {status_pair}: {body_pair}"):
            continue
        pair_scores = {row["index"]: row["relevance_score"] for row in body_pair["results"]}
        report.say(
            f"   问题对 {query!r}：相关={pair_scores[1]:.4f} 不相关={pair_scores[0]:.4f}"
            f"（文档顺序故意反过来）"
        )
        report.check(
            pair_scores[1] > pair_scores[0],
            f"{model_id} 问题 {query!r} 的相关文档分数没有更高",
        )

    status_top, body_top = client.post(
        "/v1/rerank",
        {"model": model_id, "query": RERANK_QUERY, "documents": list(RERANK_DOCUMENTS), "top_n": 2},
    )
    report.check(status_top == 200 and len(body_top["results"]) == 2, f"{model_id} top_n=2 没生效: {body_top}")

    # 查询本身超过 max_tokens - RERANK_SPECIAL_RESERVE：量 token 以后 400，不是 500、也不是静默截
    long_query = "咖啡" * 2000
    status_long, body_long = client.post(
        "/v1/rerank",
        {"model": model_id, "query": long_query, "documents": list(RERANK_DOCUMENTS)},
    )
    report.check(
        status_long == 400,
        f"{model_id} 超长查询（{len(long_query)} 字）：期望 400，得到 {status_long} {body_long}",
    )
    if status_long == 400:
        report.say(f"   超长查询（{len(long_query)} 字）：400，{body_long.get('detail', '')[:80]}")


    unloaded = client.post(f"/v1/models/{model_id}/unload", {})
    report.check(unloaded[0] == 200 and bool(unloaded[1]["unloaded"]), f"{model_id} 重排卸载失败: {unloaded}")


def check_boundaries(client: Client, report: Report, specs) -> None:
    report.say("\n== 边界")
    long_text = "这是一段用来检查按 token 截断的很长的中文文本。" * 800
    for model_id, spec in specs.items():
        if spec.kind != "embed":
            continue
        encoded = server_module().build_tokenizer(spec).encode(long_text).ids
        tokenizer = server_module().build_tokenizer(spec)
        report.check(
            len(encoded) <= spec.max_tokens,
            f"{model_id} 截断以后还有 {len(encoded)} token（上限 {spec.max_tokens}）",
        )
        report.check(
            len(tokenizer.encode(long_text[:200]).ids) < spec.max_tokens,
            f"{model_id} 短文本被截断了",
        )
    report.say(f"   截断：{len(long_text)} 字的长文本，各模型都截到自己的 max_tokens 以内")

    cases = [
        ("空 input 列表", "/v1/embeddings", {"model": "granite-97m-r2", "input": []}, 400),
        ("空字符串", "/v1/embeddings", {"model": "granite-97m-r2", "input": [""]}, 400),
        ("只有空白", "/v1/embeddings", {"model": "granite-97m-r2", "input": ["   "]}, 400),
        ("65 条", "/v1/embeddings", {"model": "granite-97m-r2", "input": ["x"] * 65}, 400),
        ("不认识的模型", "/v1/embeddings", {"model": "nope", "input": "x"}, 404),
        ("重排模型当嵌入用", "/v1/embeddings", {"model": RERANK_MODEL, "input": "x"}, 404),
        ("granite 不支持 dimensions", "/v1/embeddings", {"model": "granite-97m-r2", "input": "x", "dimensions": 64}, 400),
        ("空 documents", "/v1/rerank", {"model": RERANK_MODEL, "query": "q", "documents": []}, 400),
        ("空 query", "/v1/rerank", {"model": RERANK_MODEL, "query": " ", "documents": ["d"]}, 400),
        ("空文档", "/v1/rerank", {"model": RERANK_MODEL, "query": "q", "documents": [""]}, 400),
        ("未知字段", "/v1/embeddings", {"model": "granite-97m-r2", "input": "x", "typo": 1}, 422),
    ]
    for label, path, payload, expected in cases:
        status, body = client.post(path, payload)
        report.check(status == expected, f"{label}：期望 {expected}，得到 {status} {body}")

    started = time.time()
    status, body = client.post("/v1/embeddings", {"model": "granite-97m-r2", "input": long_text})
    report.check(status == 200, f"超长文本（截断后）请求失败: {status} {body}")
    if status == 200:
        report.say(
            f"   超长文本（{len(long_text)} 字）截断后仍出向量：维度={len(body['data'][0]['embedding'])}  "
            f"墙钟={int((time.time() - started) * 1000)}ms  tokens={body['usage']['prompt_tokens']}"
        )

    status, body = client.post(
        "/v1/embeddings",
        {"model": "qwen3-embedding-0.6b", "input": TRIPLE[0], "input_type": "query"},
    )
    report.check(status == 200, f"input_type=query 请求失败: {status} {body}")
    if status == 200:
        plain = client.post("/v1/embeddings", {"model": "qwen3-embedding-0.6b", "input": TRIPLE[0]})[1]
        query_vec = np.asarray(body["data"][0]["embedding"], dtype=np.float64)
        plain_vec = np.asarray(plain["data"][0]["embedding"], dtype=np.float64)
        report.say(f"   input_type=query vs document：cos={cosine(query_vec, plain_vec):.4f}（应该明显小于 1）")
        report.check(cosine(query_vec, plain_vec) < 0.99, "query 前缀没有生效（和原文向量一样）")

    # 卸载以后 /health 里是 false，下一次请求重新加载（load_ms 重新计时）
    client.post("/v1/models/granite-97m-r2/unload", {})
    row = health_row(client, "granite-97m-r2")
    report.check(row["loaded"] is False, "卸载以后 /health 里仍然 loaded")
    status, body = client.post("/v1/embeddings", {"model": "granite-97m-r2", "input": TRIPLE[0]})
    report.check(status == 200, f"卸载后重新请求失败: {status} {body}")
    row = health_row(client, "granite-97m-r2")
    report.check(row["loaded"] is True and row["load_ms"] is not None, "重新加载以后 /health 没标 loaded / load_ms")
    report.say(f"   卸载后重新加载：load_ms={row['load_ms']}")

    client.post("/v1/models/qwen3-embedding-0.6b/unload", {})
    client.post("/v1/models/granite-97m-r2/unload", {})
    client.post(f"/v1/models/{RERANK_MODEL}/unload", {})


def check_auth(client: Client, report: Report) -> None:
    report.say("\n== 令牌")
    status, body = client.get("/health", token=None)
    report.check(status == 401, f"不带 Authorization 的 /health：期望 401，得到 {status}")
    status, body = client.post("/v1/embeddings", {"model": "granite-97m-r2", "input": "x"}, token="wrong")
    report.check(status == 401, f"令牌不对的 /v1/embeddings：期望 401，得到 {status} {body}")
    status, body = client.get("/v1/models", token=None)
    report.check(status == 401, f"不带 Authorization 的 /v1/models：期望 401，得到 {status}")
    report.say("   不带 / 带错令牌的 /health、/v1/models、/v1/embeddings 都是 401")


def check_startup_without_token(report: Report) -> None:
    """没设 SUMIKA_EMBED_TOKEN 就拒绝启动（退出码 2），不留一个 11440 裸奔。"""
    report.say("\n== 没设令牌")
    server = Server(free_port(11500), token=None)
    try:
        code = server.proc.wait(timeout=120)
        server._reader.join(timeout=5)
    finally:
        if server.proc.poll() is None:
            server.stop()
    output = "\n".join(server.log)
    report.check(code == 2, f"没设 SUMIKA_EMBED_TOKEN 时退出码 {code}（期望 2）：\n{output}")
    report.check("SUMIKA_EMBED_TOKEN" in output, f"拒绝启动的错误里没提变量名：\n{output}")
    report.say(f"   退出码 {code}：" + next((line for line in server.log if "SUMIKA_EMBED_TOKEN" in line), "")[:120])


def check_body_limits(client: Client, report: Report) -> None:
    report.say("\n== 请求体上限")
    # 声明的 Content-Length 超上限：服务在读正文之前就 413（所以只发头、不发正文）
    status, _text = raw_request(
        client.base, client.token, "/v1/embeddings", [("Content-Length", str(64 * 1024**2))]
    )
    report.check(status == 413, f"Content-Length=64MiB：期望 413，得到 {status}")

    # 分块传输（没有 Content-Length）：真发 36 MiB，看"实际收到的字节"那条路径也是 413
    started = time.time()
    status, sent = chunked_over_limit(client.base, client.token)
    report.check(status == 413, f"分块发 {sent} 字节（32 MiB 上限）：期望 413，得到 {status}")
    report.say(f"   分块 {sent // (1024**2)} MiB → {status}（{int((time.time() - started) * 1000)}ms）")

    # 单条字符数超上限：分词之前 400（400 万字 ≈ 12 MiB，低于 32 MiB 的正文上限）
    huge = "咖啡" * 2_000_000
    started = time.time()
    status, body = client.post("/v1/embeddings", {"model": "granite-97m-r2", "input": huge})
    elapsed_ms = int((time.time() - started) * 1000)
    report.check(status == 400, f"单条 {len(huge)} 字：期望 400，得到 {status} {body}")
    report.say(f"   Content-Length 64 MiB → 413；单条 {len(huge)} 字 → {status}（{elapsed_ms}ms，没进分词）")


def check_concurrency(client: Client, report: Report) -> None:
    """同一个模型三路并发：都 200，向量和一条一条算的逐维一致（缓存不会串）。"""
    report.say("\n== 并发（同一个模型三路同时打）")
    model_id = "granite-97m-r2"
    texts = [f"并发检查第{index}句：主人喜欢喝冰美式。" for index in range(6)]
    pairs = [texts[0:2], texts[2:4], texts[4:6]]
    client.post(f"/v1/models/{model_id}/unload", {})

    concurrent: dict[int, tuple[int, object]] = {}
    barrier = threading.Barrier(len(pairs))

    def worker(index: int) -> None:
        barrier.wait()
        concurrent[index] = client.post("/v1/embeddings", {"model": model_id, "input": pairs[index]})

    threads = [threading.Thread(target=worker, args=(index,)) for index in range(len(pairs))]
    started = time.time()
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join(timeout=600)
    wall_ms = int((time.time() - started) * 1000)
    report.check(
        all(status == 200 for status, _body in concurrent.values()),
        f"并发请求里有失败的：{[status for status, _body in concurrent.values()]}",
    )
    if len(concurrent) != len(pairs) or any(status != 200 for status, _body in concurrent.values()):
        return
    report.say(f"   3 路并发（含首次加载）墙钟={wall_ms}ms")
    report.check(
        health_row(client, model_id)["loaded"] is True and health_row(client, model_id)["load_ms"] is not None,
        "并发以后 /health 没标 loaded",
    )

    got: dict[str, np.ndarray] = {}
    for index, (_status, body) in concurrent.items():
        for row in body["data"]:
            got[pairs[index][row["index"]]] = np.asarray(row["embedding"], dtype=np.float64)

    client.post(f"/v1/models/{model_id}/unload", {})
    reference = {
        text: np.asarray(
            client.post("/v1/embeddings", {"model": model_id, "input": text})[1]["data"][0]["embedding"],
            dtype=np.float64,
        )
        for text in texts
    }
    diff = max(float(np.max(np.abs(got[text] - reference[text]))) for text in texts)
    report.check(diff <= 1e-6, f"并发算出来的向量和一条一条算的不一致 (max|Δ|={diff:.3g})")
    hits = client.post("/v1/embeddings", {"model": model_id, "input": list(texts)})[1]
    batch = {
        texts[row["index"]]: np.asarray(row["embedding"], dtype=np.float64) for row in hits["data"]
    }
    batch_diff = max(float(np.max(np.abs(batch[text] - reference[text]))) for text in texts)
    report.check(batch_diff <= 1e-6, f"并发写过的缓存拿回来不一致 (max|Δ|={batch_diff:.3g})")
    report.say(f"   并发 vs 单条 max|Δ|={diff:.3g}；全部命中缓存再取 max|Δ|={batch_diff:.3g}")
    client.post(f"/v1/models/{model_id}/unload", {})


def batch_cost_texts() -> list[str]:
    """64 条长短不一的编的句子（二十来字到一百多字）：量"一批 64 条"要多久，挑模型看这个数。"""
    seeds = (
        "冰箱里的牛奶过期了。",
        "主人说下午三点在会议室开评审会，记得带上笔记本电脑和充电器。",
        "周末去公园散步，天气预报说会下雨，带把伞。",
        "那本讲分布式系统的书读完了，下一本想看讲编译原理的。",
        "今天的晚饭是番茄炒蛋和一碗米饭。",
    )
    texts = []
    for index in range(64):  # 一批最多 64 条（规格 3.3）
        seed = seeds[index % len(seeds)]
        texts.append(f"第{index + 1}条。" + seed * (1 + index % 4))
    return texts


def check_batch_cost(client: Client, report: Report, model_id: str) -> None:
    """一批 64 条长短不一的句子要多久：墙钟、token、均摊到每条（挑模型用的数字）。"""
    client.post(f"/v1/models/{model_id}/unload", {})
    texts = batch_cost_texts()
    started = time.time()
    status, body = client.post("/v1/embeddings", {"model": model_id, "input": texts})
    wall_ms = int((time.time() - started) * 1000)
    if not report.check(status == 200, f"{model_id} 一批 64 条：HTTP {status} {body}"):
        return
    row = health_row(client, model_id)
    load_ms = int(row["load_ms"] or 0)
    tokens = body["usage"]["prompt_tokens"]
    report.say(
        f"   64 条一批：墙钟={wall_ms}ms（含加载 {load_ms}ms，均摊 {(wall_ms - load_ms) / len(texts):.0f}ms/条）"
        f"  tokens={tokens}"
    )
    client.post(f"/v1/models/{model_id}/unload", {})


def send_and_abandon(base: str, token: str, path: str, payload: dict, after: float) -> None:
    """发一个请求，`after` 秒后直接关掉连接（照 Go 客户端超时走人的样子），不等响应。"""
    host, port = base.removeprefix("http://").split(":")
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    head = (
        f"POST {path} HTTP/1.1\r\nHost: {host}:{port}\r\nAuthorization: Bearer {token}\r\n"
        f"Content-Type: application/json\r\nContent-Length: {len(body)}\r\n\r\n"
    ).encode("ascii")
    sock = socket.create_connection((host, int(port)), timeout=30)
    try:
        sock.sendall(head + body)
        time.sleep(after)
    finally:
        sock.close()


def check_disconnect(client: Client, report: Report) -> None:
    """客户端断开以后服务要停：紧接着的下一个请求不用等那批算完（规格-本地模型服务 第五节）。

    pplx 一条前向约 3 秒，8 条一批要二十几秒；不断的话下一路请求会一路排在它后面。
    """
    report.say("\n== 客户端断开（慢请求 1 秒后走人）")
    model_id = "pplx-embed-v1-0.6b"
    client.post(f"/v1/models/{model_id}/unload", {})
    warm = client.post("/v1/embeddings", {"model": model_id, "input": TRIPLE[0]})
    if not report.check(warm[0] == 200, f"预热 {model_id} 失败：{warm}"):
        return
    slow = [f"第{index}条：主人说明天要早点起，因为上午有课。" for index in range(8)]
    send_and_abandon(client.base, client.token, "/v1/embeddings", {"model": model_id, "input": slow}, 1.0)
    time.sleep(0.5)  # 让服务收下断开
    started = time.time()
    status, body = client.post("/v1/embeddings", {"model": model_id, "input": f"断开检查：现在是{time.time():.0f}"})
    elapsed = time.time() - started
    report.check(status == 200, f"断开以后下一个请求 HTTP {status}：{body}")
    report.check(
        elapsed < 12.0,
        f"断开以后下一个请求等了 {elapsed:.1f}s：慢请求还在接着算（8 条 × 约 3s）",
    )
    report.say(f"   断开后下一个请求 {int(elapsed * 1000)}ms 返回；慢的那批 8 条 × 约 3s 没有接着算完")
    client.post(f"/v1/models/{model_id}/unload", {})


def check_request_deadline(report: Report) -> None:
    """`--max-request-seconds` 到点在下一句之前停下回 504，模型锁放掉（下一路请求照常）。"""
    report.say("\n== 服务端截止时间（--max-request-seconds 3）")
    port = free_port(11560)
    server = Server(port, extra=["--max-request-seconds", "3"])
    try:
        server.wait_ready()
        client = Client(server.base, server.token)
        model_id = "pplx-embed-v1-0.6b"
        warm = client.post("/v1/embeddings", {"model": model_id, "input": TRIPLE[0]})
        if not report.check(warm[0] == 200, f"预热 {model_id} 失败：{warm}"):
            return
        slow = [f"第{index}条：周末想去看那场说好要下映的电影。" for index in range(8)]
        started = time.time()
        status, body = client.post("/v1/embeddings", {"model": model_id, "input": slow})
        elapsed = time.time() - started
        report.check(status == 504, f"8 条 × 约 3s、截止 3 秒：期望 504，得到 {status} {body}")
        report.check(elapsed < 12.0, f"到截止以后还要 {elapsed:.1f}s 才回：每句之间没查")
        report.say(f"   {status}，墙钟={int(elapsed * 1000)}ms：{body.get('detail', '')[:70]}")
        status, _ = client.post("/v1/embeddings", {"model": model_id, "input": TRIPLE[0]})
        report.check(status == 200, f"504 以后模型锁没放掉：HTTP {status}")
    finally:
        server.stop()


def check_cache_token_limit(module, spec, report: Report) -> None:
    """一批全部命中缓存时也要查 32768 的上限（不用真跑前向：直接往缓存里放 token 数）。"""
    report.say("\n== 全部命中缓存时的 token 上限")
    loaded = module._load(spec, threads=1)
    try:
        vector = np.zeros(spec.dimension, dtype=np.float32)
        vector[0] = 1.0
        texts = [f"缓存命中检查 {index}" for index in range(5)]
        for text in texts:
            loaded.cache[text] = (vector.copy(), 8000)
        try:
            loaded.embed_texts(texts, None)
        except module.HTTPException as exc:
            report.check(exc.status_code == 400, f"全部命中缓存超上限时状态码 {exc.status_code}（期望 400）")
        else:
            report.check(False, "5 × 8000 token 全部命中缓存时没按 32768 上限报错")
        _vectors, tokens, hits = loaded.embed_texts(texts[:4], None)
        report.check(tokens == 32000 and hits == 4, f"4 × 8000 应该放行且全命中：tokens={tokens} hits={hits}")
        report.say(f"   5 × 8000 → 400；4 × 8000 → 200（tokens={tokens} hits={hits}）")
    finally:
        loaded.consume()
        del loaded
        gc.collect()


def check_pool_wait(module, specs, report: Report) -> None:
    """K=2 两个都在用时，第三个模型要等到截止才 503，不是当场失败；有人放开就装上。"""
    report.say("\n== 槽位满（K=2，两个都在用）")
    pool = module.ModelPool(specs, max_loaded=2, idle_seconds=0, threads=1, wait_seconds=4.0, reap_interval=600)
    try:
        held = [pool.acquire("granite-97m-r2"), pool.acquire("bekko-a25m")]
        outcome: dict[str, object] = {}
        started = time.monotonic()

        def grab() -> None:
            try:
                outcome["model"] = pool.acquire("gte-multilingual-base")
            except BaseException as exc:  # noqa: BLE001 - 原样带回去报
                outcome["error"] = exc

        thread = threading.Thread(target=grab, daemon=True)
        thread.start()
        time.sleep(1.0)
        report.check(thread.is_alive(), "槽位满时第三个模型没有等，当场返回了")
        pool.release(held[0])
        thread.join(timeout=60)
        waited = time.monotonic() - started
        third = outcome.get("model")
        report.check(third is not None, f"放开一个租约以后第三个模型没装上：{outcome.get('error')}")
        if third is not None:
            report.say(f"   放开一个租约 → 等 {waited:.1f}s 装上 {third.spec.id}（下一段故意不放这个租约）")

        started = time.monotonic()
        try:
            extra = pool.acquire("harrier-270m")
        except module.PoolError as exc:
            elapsed = time.monotonic() - started
            report.check(exc.status_code == 503, f"槽位满到截止时状态码 {exc.status_code}（期望 503）")
            report.check(elapsed >= 3.5, f"槽位满时 {elapsed:.1f}s 就报错，没等到 {pool._wait:.0f}s 截止")
            report.say(f"   谁也不放 → 等 {elapsed:.1f}s 后 503：{exc.detail}")
        else:
            pool.release(extra)
            report.check(False, "槽位满、没人放租约时还是把第四个模型装上了")
    finally:
        pool.close()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="检查本地模型服务")
    parser.add_argument("--port", type=int, default=0, help="默认自动找一个空闲端口")
    parser.add_argument("--keep-server-log", action="store_true")
    args = parser.parse_args(argv)

    report = Report()
    port = args.port or free_port(11441)
    report.say(f"启动服务：127.0.0.1:{port}（max_loaded=1，每个模型检查完卸载，令牌随机）")
    check_startup_without_token(report)
    module = server_module()
    specs = module.load_specs(HERE / "models.toml")
    server = Server(port)
    try:
        server.wait_ready()
        client = Client(server.base, server.token)

        check_auth(client, report)
        check_body_limits(client, report)

        status, models = client.get("/v1/models")
        ids = [row["id"] for row in models["data"]] if status == 200 else []
        report.say(f"/v1/models 登记了 {len(ids)} 个：{', '.join(ids)}")
        for model_id in (*EMBED_MODELS, *RERANK_MODELS):
            report.check(model_id in ids, f"/v1/models 里没有 {model_id}")

        for model_id in EMBED_MODELS:
            report.say(f"\n== {model_id}")
            check_embed(client, report, model_id, specs[model_id])

        report.say("\n== 批量不变性（同一个会话，同 token 长度的几条一起 vs 一条一条）")
        for model_id in EMBED_MODELS:
            report.say(f"   {model_id}")
            check_batch_invariance(module, specs[model_id], report)
            gc.collect()

        report.say("\n== 一批 64 条要多久（挑模型用）")
        for model_id in EMBED_MODELS:
            report.say(f"   {model_id}")
            check_batch_cost(client, report, model_id)

        for model_id in RERANK_MODELS:
            report.say(f"\n== {model_id}")
            check_rerank(client, report, model_id)

        check_boundaries(client, report, specs)
        check_concurrency(client, report)
        check_disconnect(client, report)
        check_request_deadline(report)
        check_cache_token_limit(module, specs["granite-97m-r2"], report)
        check_pool_wait(module, specs, report)
    finally:
        server.stop()
        if args.keep_server_log:
            print("\n".join(server.log))

    print()
    if report.failures:
        print(f"失败 {len(report.failures)} 项：")
        for failure in report.failures:
            print(f"  - {failure}")
        return 1
    print("全部通过")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
