# -*- coding: utf-8 -*-
"""Go 版本地模型（native/internal/localmodels）的对拍参考数据：用 Python 版服务实际走的那条路算出来。

对拍用，不是换电脑的路径：它要 requirements.txt 里的 Python 环境（fastapi、onnxruntime、tokenizers 等）。
规格：docs/规格-Go本地模型.md 2.4、第五节、GM1、GM2。

用法（在能 import server.py 的 Python 里跑：按 tools/local-models/requirements.txt 用 uv 建的独立环境，版本钉在那个文件里，
不借别的项目的环境；-I 让它不从当前目录加载别的模块）。子命令：

    python -I tools/local-models/parity_ref.py tokenizer --models-root <模型根> --testdata native/internal/localmodels/tokenizer/testdata
    python -I tools/local-models/parity_ref.py engine --models-root <模型根> --out native/internal/localmodels/ort/testdata
    python -I tools/local-models/parity_ref.py rules --models-root <模型根> --out native/internal/localmodels/engine/testdata
    python -I tools/local-models/parity_ref.py http --models-root <模型根> --out native/internal/localmodels/server/testdata

tokenizer（GM1）：对 cases.json 和 probes.json 里每条文本，gte 和 bge 各用 `server.build_tokenizer`（含登记表里的截断设置，
和服务里 `embed_texts` 的 `tokenizer.encode(text).ids` 同一条路）编码，存成 ref-ids-gte.json、ref-ids-bge.json；
bge 另外对 pairs.json 里每一对用 `tokenizer.encode(query, document).ids`（服务里重排的路）编码，存成 ref-pairs-bge.json。
cases.json、probes.json、pairs.json 和 categories.json 是 Go 和 Python 共用的同一份输入（全是编的），本命令只读不写。
categories.json 定义"已知差异"的类别（规格 2.4 的"已解释"判法）：样本里含某个类别的字符时，另外导出"把这些字符去掉以后"的 id
（cases_explained / probes_explained / pairs 的 explained），Go 的对拍测试用它证明一个不一致确实只由那些字符造成。

engine（GM2）：参考值走 server.py 实际用的那条路（load_specs、build_tokenizer、LoadedModel 的 _forward / _forward_embeddings），
不是裸 onnxruntime：池化、截断、归一化都是服务的口径。读 --out 目录里的 cases.json（42 句编的样本）和 pairs.json
（6 对编的查询、文档），写出：
  ref-ids-gte.json     每句 gte 的 token id（含首尾特殊符号，截到 8192）
  ref-raw0-gte.f32     每句池化前的第 0 行（token_embeddings[0, 0, :]，未归一化），小端 float32，42 × 768
  ref-vecs-gte.f32     每句最终的向量（第 0 行 L2 归一化），小端 float32，42 × 768
  ref-logits-bge.json  每对 bge 重排的 token id 和 logit（float32 的值，用 float64 存）
  ref-meta.json        生成时的 CPU、各包版本、模型文件哈希。读参照的测试遇到 CPU 型号不同就 t.Skip，不放宽门槛

样本都是编的，不含真实聊天数据。再生成这些文件时先看 git diff：CPU、包版本变了，数据可能跟着变。

rules（GM3）：引擎要和 Python 版保持一致的两样规则，不跑模型，只读 models.toml 和 Python 自己的 str.isspace：
  ref-registry.json    gte、bge 两条登记（server.load_specs 解出来的值，缺省已补上）和 models.toml 的 SHA-256（换行统一成 LF 再算）。
                       Go 侧测试拿它逐字段比，并核对 tools/local-models/models.toml 现在的哈希，对不上就是 Python 的表改过，要重导
  ref-whitespace.json  str.isspace() 为真的全部码点（server.py 用 `not text.strip()` 判空，strip 和 isspace 是同一个集合）

http（GM5）：HTTP 层的状态码参照，不跑模型。拿 server.py 真的 `create_app`（令牌、正文上限、Pydantic 校验、路由里的检查全是真的），
配一个假的池（`require` 照抄 ModelPool.require，`lease` 给一个只会回固定向量的假模型），用 starlette 的 TestClient 逐条发请求，记状态码：
  python-http.json     zoo：每个码点（空白、控制字符、常见 Cf 等）单独当 input、夹在字母之间、两侧带空格，三种排法各一个状态码；
                       shapes：请求体的各种形状（非 JSON、字段类型不对、未知字段、null、空数组……）的状态码。
                       Go 版的 TestWhitespaceZoo 逐个比 zoo，TestStrictBody 逐个比 shapes（Go 版更严的地方在测试里逐条登记）。
输入全是编的。

real（GM6）：真实样本的 id 参照。读 `memory-eval -dump-embed-inputs` 写的那份文件（native/internal/embedinputs 的格式），
对条目、考题查询、群消息、守卫句子用 gte 的 `tokenizer.encode(text).ids`，对重排的（查询，候选）用 bge 的 `tokenizer.encode(query, doc).ids`
（和 tokenizer 子命令同一条路），含已登记类别字符的另带"去掉以后"的 id，写成 --out 目录里的 ref-real.json。
**输入是真实记忆和聊天的原文，输出里是 id（能还原成文字）**：--out 在 git 工作树里、又没被 .gitignore 忽略就拒绝，
不然只许放临时目录；命令只打条数，不打任何文字；用完自己删（models-parity 默认替你删）。
    python -I tools/local-models/parity_ref.py real --models-root <模型根> --input <dump.json> --out <临时目录>

sweep（复核补的）：全部码点的 NFD、NFKD 形式（跟自己不同的，约 3.2 万条）各用 gte、bge 的 `tokenizer.encode(text).ids` 编码，
含已登记类别字符的另带"去掉以后"的 id，写成 --out 目录里的 ref-sweep-gte.json、ref-sweep-bge.json。文字和 id 都在文件里（Go 不用自己算
NFD / NFKD：Python 的 Unicode 表是 15.0，Go 的更新，同一个码点的分解可能不同）。体积几 MB，**不进 git**，--out 放临时目录；
Go 的 TestUnicodeSweep（设 SUMIKA_TEST_SWEEP_DIR 指到这个目录）逐条比，要求每个不一致都归得到类别、去掉类别字符以后一致。
类别表漏了一类字符（原来的 T5 漏了 Mc 里的 Other_Grapheme_Extend）时，这一批会量出来，不靠手工拼范围。
    python -I tools/local-models/parity_ref.py sweep --models-root <模型根> --testdata native/internal/localmodels/tokenizer/testdata --out <临时目录>

threads（GM6）：线程数 1 / 2 / 4 的输出差（规格第五节"跨线程差另记一行，不进门槛"）和同参数三次重新加载的差，
用 cases.json 的编的句子，在进程里直接算（不走 HTTP）。stdout 打一行 JSON。
    python -I tools/local-models/parity_ref.py threads --models-root <模型根> --cases native/internal/localmodels/ort/testdata/cases.json
"""
from __future__ import annotations

import argparse
import hashlib
import json
import platform
import sys
import unicodedata
from datetime import date
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import numpy as np  # noqa: E402
import server  # noqa: E402  (tools/local-models/server.py)
from tokenizers import Tokenizer  # noqa: E402
import tokenizers  # noqa: E402

GTE = "gte-multilingual-base"
BGE = "bge-reranker-v2-m3"
MODELS = {"gte": GTE, "bge": BGE}
CRLF, LF = bytes([13, 10]), bytes([10])  # 算 models.toml 的哈希前统一换行（工作区可能是 CRLF，仓库里是 LF）


def expand(entry: dict) -> str:
    """cases.json 里的一条：text 直接给，或 unit 重复 count 次（Go 那边 sample.expand 同样的规则）"""
    if entry.get("unit"):
        return entry["unit"] * entry["count"]
    return entry.get("text", "")


def load_texts(testdata: Path) -> tuple[list[str], list[str]]:
    cases = [expand(e) for e in json.loads((testdata / "cases.json").read_text(encoding="utf-8"))]
    probes = [e["text"] for e in json.loads((testdata / "probes.json").read_text(encoding="utf-8"))]
    return cases, probes


def load_categories(testdata: Path) -> dict:
    return json.loads((testdata / "categories.json").read_text(encoding="utf-8"))


def remove_category(text: str, cat: dict) -> str:
    """把类别的字符从文本里去掉：区间里的码点，和列出的字符串（去到不再出现为止：去掉 <s> 可能让两边拼出新的 <s>）。
    nfkc 为真时字符串在 NFKC 之后的文本里找（normalizer 会把全角 ＜s＞ 折成 <s>）：找到了才算命中，去掉以后返回 NFKC 之后的文本；
    没找到原样返回。Go 那边是 tokenizer/categories.go 的 Strip，规则要一样"""
    spans = cat["remove"].get("ranges", [])
    if spans:
        text = "".join(ch for ch in text if not any(lo <= ord(ch) <= hi for lo, hi in spans))
    strings = cat["remove"].get("strings", [])
    if not strings:
        return text
    t = unicodedata.normalize("NFKC", text) if cat["remove"].get("nfkc") else text
    if not any(sub in t for sub in strings):
        return text
    while True:
        before = t
        for sub in strings:
            t = t.replace(sub, "")
        if t == before:
            return t


def removed_text(cats: dict, key: str, text: str):
    """文本里含有的类别（按名字排序，Go 那边同样排序）和全部去掉以后的文本；没有类别字符返回 None"""
    names = [n for n in sorted(cats) if key in cats[n]["models"] and remove_category(text, cats[n]) != text]
    if not names:
        return None
    for n in names:
        text = remove_category(text, cats[n])
    return names, text


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for block in iter(lambda: fh.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def write_json(path: Path, obj) -> None:
    """每个 id 序列占一行，改动时 diff 看得清"""
    head = {k: v for k, v in obj.items() if not isinstance(v, list)}
    lists = {k: v for k, v in obj.items() if isinstance(v, list)}
    parts = ['"%s": %s' % (k, json.dumps(v, ensure_ascii=True)) for k, v in head.items()]
    for k, rows in lists.items():
        parts.append('"%s": [\n%s\n]' % (k, ",\n".join(json.dumps(r, ensure_ascii=True, separators=(",", ":")) for r in rows)))
    path.write_text("{\n" + ",\n".join(parts) + "\n}\n", encoding="utf-8", newline="\n")


def write_text_lf(path: Path, text: str) -> None:
    """以 LF 换行、UTF-8 写文件，末尾补一个换行（仓库里统一 LF，不随 Windows 的默认换行变）"""
    path.write_text(text + chr(10), encoding="utf-8", newline=chr(10))


def encode_ids(tokenizer: Tokenizer, text: str):
    """服务里嵌入走的 `tokenizer.encode(text).ids`。抛错的记 None：bge 的截断策略是 only_second，
    单条超过 max_tokens 时它会抛 "Second sequence not provided"（服务里 bge 只走重排的一对，不走单条）。"""
    try:
        return tokenizer.encode(text).ids
    except Exception:
        return None


def explain(tokenizer: Tokenizer, cats: dict, key: str, text: str):
    """含类别字符的样本：{"cats": [类别], "ids": 全部去掉以后的 id}；不含返回 None"""
    r = removed_text(cats, key, text)
    if r is None:
        return None
    names, rest = r
    return {"cats": names, "ids": encode_ids(tokenizer, rest)}


def tokenizer_meta(spec: "server.ModelSpec") -> dict:
    return {
        "model": spec.id,
        "tokenizer_sha256": sha256_file(spec.tokenizer),
        "tokenizers": tokenizers.__version__,
        "python": platform.python_version(),
        "max_tokens": spec.max_tokens,
        "truncation": spec.truncation,
    }


def run_tokenizer(args: argparse.Namespace) -> None:
    testdata = Path(args.testdata)
    specs = server.load_specs(HERE / "models.toml", args.models_root)
    cases, probes = load_texts(testdata)
    cats = load_categories(testdata)

    for key, model_id in MODELS.items():
        spec = specs[model_id]
        tokenizer = server.build_tokenizer(spec)
        out = tokenizer_meta(spec)
        out["cases"] = [encode_ids(tokenizer, t) for t in cases]
        out["probes"] = [encode_ids(tokenizer, t) for t in probes]
        out["cases_explained"] = [explain(tokenizer, cats, key, t) for t in cases]
        out["probes_explained"] = [explain(tokenizer, cats, key, t) for t in probes]
        if key == "gte":
            # S2：生产传 --max-line-tokens 2048（规格 2.5），Go 在 2048 处截断，Python 的服务没有这个开关。
            # 这里给"Python 的分词器同样截到 2048"的 id（只对全长超过 2048 的样本），Go 的 TestTruncation2048MatchesPython 逐个比：
            # 截断的语义（内容留到 2046 个，再补首尾）两边必须逐位相同；Python 全长 vs Go 截断的向量余弦在 cmd/models-parity 之外量过
            tok2048 = server.build_tokenizer(spec)
            tok2048.enable_truncation(max_length=2048, strategy=spec.truncation)
            out["cases_trunc2048"] = [encode_ids(tok2048, t) if ids is not None and len(ids) > 2048 else None for t, ids in zip(cases, out["cases"])]
        write_json(testdata / f"ref-ids-{key}.json", out)
        print(f"ref-ids-{key}.json: {len(cases)} 条样本 + {len(probes)} 条探针")

    # 重排的一对：输入在 pairs.json（{"pairs": [{"name", "q", "d"}]}，q 和 d 同样可以是 unit / count 食谱），
    # 输出 ref-pairs-bge.json（同样的顺序，每对多 query_tokens 和 ids，抛错的记 error）
    pairs_in = testdata / "pairs.json"
    if pairs_in.is_file():
        spec = specs[MODELS["bge"]]
        tokenizer = server.build_tokenizer(spec)
        raw = Tokenizer.from_file(str(spec.tokenizer))  # 不截断：算查询的 token 数（server.py 的 raw_tokenizer）
        rows = []
        for p in json.loads(pairs_in.read_text(encoding="utf-8"))["pairs"]:
            q, d = expand(p["q"]), expand(p["d"])
            row = {"name": p["name"], "query_tokens": len(raw.encode(q).ids)}
            try:
                row["ids"] = tokenizer.encode(q, d).ids
            except Exception as exc:  # 超长的一对分词器会抛，server.py 把它变成 400
                row["error"] = type(exc).__name__
            rq, rd = removed_text(cats, "bge", q), removed_text(cats, "bge", d)
            if rq or rd:
                names = sorted(set((rq[0] if rq else []) + (rd[0] if rd else [])))
                q2, d2 = q, d
                for n in names:
                    q2, d2 = remove_category(q2, cats[n]), remove_category(d2, cats[n])
                try:
                    row["explained"] = {"cats": names, "ids": tokenizer.encode(q2, d2).ids}
                except Exception as exc:
                    row["explained"] = {"cats": names, "error": type(exc).__name__}
            rows.append(row)
        out = tokenizer_meta(spec)
        out["pairs"] = rows
        pairs_out = testdata / "ref-pairs-bge.json"
        head = {k: v for k, v in out.items() if k != "pairs"}
        lines = ",\n".join(json.dumps(r, ensure_ascii=True, separators=(",", ":")) for r in rows)
        top = ",\n".join('"%s": %s' % (k, json.dumps(v)) for k, v in head.items())
        pairs_out.write_text("{\n" + top + ',\n"pairs": [\n' + lines + "\n]\n}\n", encoding="utf-8", newline="\n")
        print(f"ref-pairs-bge.json: {len(rows)} 对")


def run_sweep(args: argparse.Namespace) -> None:
    """全部码点的 NFD、NFKD 形式 → 两个模型的分词 id（和 run_tokenizer 同一条路），写进 --out（不进 git）"""
    testdata = Path(args.testdata)
    out_dir = Path(args.out)
    refuse_tracked_path(out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    specs = server.load_specs(HERE / "models.toml", args.models_root)
    cats = load_categories(testdata)
    texts, seen = [], set()
    for cp in range(0x110000):
        if 0xD800 <= cp <= 0xDFFF:
            continue
        ch = chr(cp)
        for form in ("NFD", "NFKD"):
            t = unicodedata.normalize(form, ch)
            if t != ch and t not in seen:
                seen.add(t)
                texts.append(t)
    for key, model_id in MODELS.items():
        spec = specs[model_id]
        tokenizer = server.build_tokenizer(spec)
        out = tokenizer_meta(spec)
        out["unicode"] = unicodedata.unidata_version
        out["texts"] = texts
        out["ids"] = [encode_ids(tokenizer, t) for t in texts]
        out["explained"] = [explain(tokenizer, cats, key, t) for t in texts]
        write_json(out_dir / f"ref-sweep-{key}.json", out)
        print(f"ref-sweep-{key}.json: {len(texts)} 条（NFD、NFKD 形式，Unicode {unicodedata.unidata_version}）")


def refuse_tracked_path(path: Path) -> None:
    """写真实样本的输出前的检查（和 Go 的 embedinputs.CheckPrivatePath 同一个规矩）：路径在 git 工作树里、又没被 .gitignore 忽略就退出。
    不在任何仓库里（临时目录）或被忽略都放行；git 不在、判断不了也放行不了的话按拒绝处理。"""
    import shutil
    import subprocess

    target = path.resolve()
    probe = target if target.is_dir() else target.parent
    while not probe.exists() and probe != probe.parent:
        probe = probe.parent
    git = shutil.which("git")
    if git is None:
        return  # 没有 git，也就没有"不小心提交"
    inside = subprocess.run([git, "-C", str(probe), "rev-parse", "--is-inside-work-tree"], capture_output=True, text=True)
    if inside.returncode != 0:
        return  # 不在任何仓库里
    if inside.stdout.strip() != "true":
        raise SystemExit(f"拒绝：{target} 在 git 目录里，不能放真实样本")
    ignored = subprocess.run([git, "-C", str(probe), "check-ignore", "-q", "--", str(target)], capture_output=True)
    if ignored.returncode == 0:
        return
    if ignored.returncode == 1:
        raise SystemExit(f"拒绝：{target} 在 git 工作树里，而且没被 .gitignore 忽略：真实样本只许放临时目录或被忽略的路径")
    raise SystemExit(f"拒绝：问 git {target} 有没有被忽略失败（退出码 {ignored.returncode}）")


def run_real(args: argparse.Namespace) -> None:
    out = Path(args.out)
    refuse_tracked_path(out / "ref-real.json")
    out.mkdir(parents=True, exist_ok=True)
    data = json.loads(Path(args.input).read_text(encoding="utf-8"))
    if data.get("version") != 1:
        raise SystemExit(f"输入文件的格式版本是 {data.get('version')}，这个脚本认 1")
    cats_path = Path(args.categories) if args.categories else HERE.parent.parent / "native" / "internal" / "localmodels" / "tokenizer" / "testdata" / "categories.json"
    cats = json.loads(cats_path.read_text(encoding="utf-8"))
    specs = server.load_specs(HERE / "models.toml", args.models_root)

    gte_spec, bge_spec = specs[GTE], specs[BGE]
    gte, bge = server.build_tokenizer(gte_spec), server.build_tokenizer(bge_spec)
    raw_bge = Tokenizer.from_file(str(bge_spec.tokenizer))  # 不截断：算查询的 token 数
    result = {
        "python": platform.python_version(),
        "tokenizers": tokenizers.__version__,
        "gte_tokenizer_sha256": sha256_file(gte_spec.tokenizer),
        "bge_tokenizer_sha256": sha256_file(bge_spec.tokenizer),
        "lists": {},
    }
    counts = {}
    for name in ("items", "queries", "group", "sentences"):
        texts = data.get(name) or []
        result["lists"][name] = {
            "ids": [encode_ids(gte, s) for s in texts],
            "explained": [explain(gte, cats, "gte", s) for s in texts],
        }
        counts[name] = len(texts)
    pairs = []
    for item in data.get("rerank") or []:
        q = item["query"]
        row = {"query_tokens": len(raw_bge.encode(q).ids), "docs": []}
        for d in item["docs"]:
            cell = {}
            try:
                cell["ids"] = bge.encode(q, d).ids
            except Exception as exc:  # 超长的一对分词器会抛，server.py 把它变成 400
                cell["error"] = type(exc).__name__
            rq, rd = removed_text(cats, "bge", q), removed_text(cats, "bge", d)
            if rq or rd:
                names = sorted(set((rq[0] if rq else []) + (rd[0] if rd else [])))
                q2, d2 = q, d
                for n in names:
                    q2, d2 = remove_category(q2, cats[n]), remove_category(d2, cats[n])
                try:
                    cell["explained"] = {"cats": names, "ids": bge.encode(q2, d2).ids}
                except Exception as exc:
                    cell["explained"] = {"cats": names, "error": type(exc).__name__}
            row["docs"].append(cell)
        pairs.append(row)
    result["pairs"] = pairs
    (out / "ref-real.json").write_text(json.dumps(result, ensure_ascii=True, separators=(",", ":")), encoding="utf-8", newline=chr(10))
    print(f"ref-real.json: " + "、".join(f"{k} {v} 条" for k, v in counts.items()) + f"、重排 {len(pairs)} 次（{sum(len(p['docs']) for p in pairs)} 对）")


def run_threads(args: argparse.Namespace) -> None:
    import gc

    cases = [expand(e) for e in json.loads(Path(args.cases).read_text(encoding="utf-8"))]
    specs = server.load_specs(HERE / "models.toml", args.models_root)

    def vectors(threads: int) -> list[np.ndarray]:
        model = server._load(specs[GTE], threads)
        try:
            return [model._forward_embeddings([model.tokenizer.encode(t).ids])[0].copy() for t in cases if t.strip()]
        finally:
            del model
            gc.collect()

    def max_diff(a: list[np.ndarray], b: list[np.ndarray]) -> float:
        return float(max(np.max(np.abs(x.astype(np.float64) - y.astype(np.float64))) for x, y in zip(a, b)))

    base = vectors(2)
    report = {
        "samples": len(base),
        "repeat_threads2_max_abs_diff": [max_diff(base, vectors(2)) for _ in range(2)],  # 同参数重新加载两次，对第一次
        "threads_max_abs_diff_vs_2": {str(n): max_diff(base, vectors(n)) for n in (1, 4)},
    }
    print(json.dumps(report))


def cpu_name() -> str:
    """Windows 上从注册表读处理器名；读不到就退回 platform.processor()。"""
    try:
        import winreg

        with winreg.OpenKey(winreg.HKEY_LOCAL_MACHINE, r"HARDWARE\DESCRIPTION\System\CentralProcessor\0") as key:
            return str(winreg.QueryValueEx(key, "ProcessorNameString")[0]).strip()
    except Exception:  # noqa: BLE001  不是 Windows 或读不到：退回
        return platform.processor() or "unknown"


def write_f32(path: Path, rows: list[np.ndarray]) -> None:
    np.asarray(rows, dtype="<f4").tofile(path)


def run_engine(args: argparse.Namespace) -> None:
    import onnxruntime as ort
    import tokenizers

    out = Path(args.out)
    cases = json.loads((out / "cases.json").read_text(encoding="utf-8"))
    pairs = json.loads((out / "pairs.json").read_text(encoding="utf-8"))

    specs = server.load_specs(server.DEFAULT_CONFIG, args.models_root)
    gte = server._load(specs[GTE], args.threads)
    bge = server._load(specs[BGE], args.threads)

    # ---- gte：42 句 ----
    ids_rows, raw0_rows, vec_rows = [], [], []
    all_outputs_same = True
    for case in cases:
        ids = gte.tokenizer.encode(case["text"]).ids
        assert ids, f"{case['cat']}: 分词结果为空"
        hidden = gte._forward([ids])[0]  # (序列, 维度)，服务只请求一个输出（token_embeddings）
        raw0 = hidden[0]
        vec = gte._forward_embeddings([ids])[0]
        # 服务请求单个输出，Go 的绑定一律请求全部输出：确认"请求哪些输出"不改变第 0 行
        feeds, _ = gte._feeds([ids])
        every = gte.session.run(None, feeds)
        names = [entry.name for entry in gte.session.get_outputs()]
        token_embeddings = every[names.index("token_embeddings")][0][0]
        all_outputs_same = all_outputs_same and np.array_equal(token_embeddings, raw0)
        ids_rows.append({"cat": case["cat"], "ids": ids})
        raw0_rows.append(raw0)
        vec_rows.append(vec)

    # ---- bge：6 对 ----
    pair_rows = []
    for pair in pairs:
        ids = bge.tokenizer.encode(pair["q"], pair["d"]).ids
        logit = float(bge._forward([ids])[0][0])
        pair_rows.append({"q": pair["q"], "d": pair["d"], "ids": ids, "logit": logit})

    (out / "ref-ids-gte.json").write_text(json.dumps(ids_rows, ensure_ascii=False) + "\n", encoding="utf-8")
    write_f32(out / "ref-raw0-gte.f32", raw0_rows)
    write_f32(out / "ref-vecs-gte.f32", vec_rows)
    (out / "ref-logits-bge.json").write_text(json.dumps(pair_rows, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")

    root = Path(args.models_root)
    meta = {
        "generated": date.today().isoformat(),
        "cpu": cpu_name(),
        "python": platform.python_version(),
        "platform": platform.platform(),
        "packages": {
            "onnxruntime": ort.__version__,
            "tokenizers": tokenizers.__version__,
            "numpy": np.__version__,
        },
        "session": {"threads": args.threads, "inter_op": 1, "graph_optimization": "BASIC", "cpu_mem_arena": "onnxruntime 默认（开）"},
        "files": {
            "gte_onnx": sha256_file(specs[GTE].onnx),
            "gte_tokenizer": sha256_file(specs[GTE].tokenizer),
            "bge_onnx": sha256_file(specs[BGE].onnx),
            "bge_tokenizer": sha256_file(specs[BGE].tokenizer),
            "onnxruntime_dll": sha256_file(Path(ort.__file__).parent / "capi" / "onnxruntime.dll"),
        },
        "models_root_name": root.name,
        "counts": {"gte": len(ids_rows), "bge_pairs": len(pair_rows)},
        "single_output_equals_all_outputs": bool(all_outputs_same),
    }
    (out / "ref-meta.json").write_text(json.dumps(meta, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    print(f"gte {len(ids_rows)} 句，bge {len(pair_rows)} 对；只请求一个输出 == 请求全部输出：{all_outputs_same}")


def run_rules(args: argparse.Namespace) -> None:
    import tomllib
    import unicodedata

    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    toml_path = HERE / "models.toml"
    raw_bytes = toml_path.read_bytes()
    raw = tomllib.loads(raw_bytes.decode("utf-8"))
    specs = server.load_specs(toml_path, args.models_root)
    models = {}
    for model_id in (GTE, BGE):
        spec, body = specs[model_id], raw["models"][model_id]
        models[model_id] = {
            "kind": spec.kind,
            "dir": body["dir"],
            "onnx": body["onnx"],
            "dimension": spec.dimension,
            "pooling": spec.pooling,
            "normalize": spec.normalize,
            "max_tokens": spec.max_tokens,
            "matryoshka": spec.matryoshka,
            "min_dimensions": spec.min_dimensions,
            "allowed_dimensions": list(spec.allowed_dimensions),
            "truncation": spec.truncation,
            "query_prefix": spec.query_prefix,
            "document_prefix": spec.document_prefix,
            "query_task": spec.query_task,
        }
    registry = {
        "models_toml_sha256": hashlib.sha256(raw_bytes.replace(CRLF, LF)).hexdigest(),
        "python": platform.python_version(),
        "models": models,
    }
    write_text_lf(out / "ref-registry.json", json.dumps(registry, ensure_ascii=False, indent=1))

    spaces = [cp for cp in range(0x110000) if chr(cp).isspace()]
    # server.py 判空用 text.strip()：strip 去掉的就是 isspace 的字符，确认两者一致
    assert all(chr(cp).strip() == "" for cp in spaces)
    whitespace = {"python": platform.python_version(), "unicode": unicodedata.unidata_version, "isspace": spaces}
    write_text_lf(out / "ref-whitespace.json", json.dumps(whitespace, indent=1))
    print(f"ref-registry.json: {len(models)} 条登记；ref-whitespace.json: {len(spaces)} 个空白码点")


def run_http(args: argparse.Namespace) -> None:
    import base64
    import contextlib
    import unicodedata

    from starlette.testclient import TestClient

    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    specs = server.load_specs(HERE / "models.toml", args.models_root)
    token = "parity-token"

    class FakeModel:
        """只会回固定向量和分数：这里只比状态码，不比数。"""

        def embed_texts(self, prepared, dimensions, stop=None):
            size = dimensions or specs[GTE].dimension
            return [np.full(size, 1.0 / size**0.5, dtype=np.float32) for _ in prepared], len(prepared) * 3, 0

        def rerank(self, query, documents, stop=None):
            return np.linspace(0.9, 0.1, len(documents)).astype(np.float32), len(documents) * 5

    class FakePool:
        max_loaded = 2

        def require(self, model_id, kind):  # 照抄 ModelPool.require
            spec = specs.get(model_id)
            if spec is None:
                raise server.PoolError(404, f"Unknown model {model_id!r}; available: {', '.join(sorted(specs))}")
            if spec.kind != kind:
                raise server.PoolError(404, f"Model {model_id!r} is a {spec.kind} model, not {kind}")
            return spec

        @contextlib.contextmanager
        def lease(self, model_id):
            yield FakeModel()

        def snapshot(self):
            return []

        def resident_bytes(self):
            return 0

        def spec_ids(self):
            return set(specs)

    client = TestClient(server.create_app(FakePool(), token), raise_server_exceptions=False)
    headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}

    def post(path: str, body: bytes) -> int:
        return client.post(path, content=body, headers=headers).status_code

    def emb(**kw) -> bytes:
        return json.dumps({"model": GTE, **kw}).encode("utf-8")

    # zoo：C0、DEL、C1、全部 Unicode 空白、Zs / Zl / Zp、常见的 Cf，外加几个普通字符当对照
    # （代理区不能放进 UTF-8 的请求体，另在 shapes 里用转义写）
    cps = set(range(0x20)) | {0x7F} | set(range(0x80, 0xA0))
    cps |= {cp for cp in range(0x110000) if chr(cp).isspace()}
    cps |= {cp for cp in range(0x110000) if unicodedata.category(chr(cp)) in ("Zs", "Zl", "Zp")}
    cps |= {0xAD, 0x61C, 0x180E, 0x200B, 0x200C, 0x200D, 0x200E, 0x200F, 0x202A, 0x202E, 0x2060, 0x2061, 0x2064, 0x206F, 0xFEFF, 0xFFF9, 0xFFFB}
    cps |= {ord("a"), ord("1"), 0x4E2D, 0x1F600, 0x2581}
    layouts = {
        "alone": lambda c: c,
        "letters": lambda c: "a" + c + "b",
        "spaced": lambda c: "a " + c + " b",
    }
    zoo = []
    for cp in sorted(cps):
        for name, make in layouts.items():
            zoo.append({"cp": cp, "layout": name, "python_status": post("/v1/embeddings", emb(input=make(chr(cp))))})

    # shapes：请求体的形状。模板里的 $EMBED / $RERANK 由两边各自换成自己的模型 id
    E, R = "$EMBED", "$RERANK"
    shapes = []

    def shape(name: str, path: str, body, note: str = "") -> None:
        raw = body if isinstance(body, bytes) else body.encode("utf-8")
        real = raw.replace(b"$EMBED", GTE.encode()).replace(b"$RERANK", BGE.encode())
        shapes.append({"name": name, "path": path, "body_b64": base64.b64encode(raw).decode("ascii"), "note": note,
                       "python_status": post(path, real)})

    e = "/v1/embeddings"
    shape("嵌入 一句", e, f'{{"model":"{E}","input":"你好"}}')
    shape("嵌入 一组", e, f'{{"model":"{E}","input":["你好","世界"]}}')
    shape("嵌入 base64", e, f'{{"model":"{E}","input":"你好","encoding_format":"base64"}}')
    shape("嵌入 dimensions 合法", e, f'{{"model":"{E}","input":"你好","dimensions":256}}')
    shape("嵌入 dimensions 太小", e, f'{{"model":"{E}","input":"你好","dimensions":0}}')
    shape("嵌入 dimensions null", e, f'{{"model":"{E}","input":"你好","dimensions":null}}')
    shape("嵌入 dimensions 字符串", e, f'{{"model":"{E}","input":"你好","dimensions":"256"}}', 'Pydantic 宽松模式把 "256" 当 256')
    shape("嵌入 dimensions 带小数点的整数", e, f'{{"model":"{E}","input":"你好","dimensions":256.0}}', "Pydantic 宽松模式收 256.0")
    shape("嵌入 dimensions 有小数部分", e, f'{{"model":"{E}","input":"你好","dimensions":256.5}}')
    shape("嵌入 dimensions 布尔", e, f'{{"model":"{E}","input":"你好","dimensions":true}}')
    shape("嵌入 input_type query", e, f'{{"model":"{E}","input":"你好","input_type":"query"}}')
    shape("嵌入 input_type 不认识", e, f'{{"model":"{E}","input":"你好","input_type":"passage"}}')
    shape("嵌入 input_type null", e, f'{{"model":"{E}","input":"你好","input_type":null}}')
    shape("嵌入 encoding_format 不认识", e, f'{{"model":"{E}","input":"你好","encoding_format":"hex"}}')
    shape("嵌入 encoding_format null", e, f'{{"model":"{E}","input":"你好","encoding_format":null}}')
    shape("嵌入 task 是数字", e, f'{{"model":"{E}","input":"你好","task":5}}')
    shape("嵌入 task null", e, f'{{"model":"{E}","input":"你好","task":null}}')
    shape("嵌入 user 字符串", e, f'{{"model":"{E}","input":"你好","user":"u1"}}')
    shape("嵌入 user 是数字", e, f'{{"model":"{E}","input":"你好","user":7}}')
    shape("嵌入 未知字段", e, f'{{"model":"{E}","input":"你好","bogus":1}}')
    shape("嵌入 缺 model", e, '{"input":"你好"}')
    shape("嵌入 缺 input", e, f'{{"model":"{E}"}}')
    shape("嵌入 model 是空串", e, '{"model":"","input":"你好"}')
    shape("嵌入 model 是数字", e, '{"model":5,"input":"你好"}')
    shape("嵌入 model null", e, '{"model":null,"input":"你好"}')
    shape("嵌入 input 是数字", e, f'{{"model":"{E}","input":5}}')
    shape("嵌入 input null", e, f'{{"model":"{E}","input":null}}')
    shape("嵌入 input 数组里有数字", e, f'{{"model":"{E}","input":["你好",1]}}')
    shape("嵌入 input 数组里有 null", e, f'{{"model":"{E}","input":["你好",null]}}')
    shape("嵌入 input 空数组", e, f'{{"model":"{E}","input":[]}}')
    shape("嵌入 input 空串", e, f'{{"model":"{E}","input":""}}')
    shape("嵌入 input 是对象", e, f'{{"model":"{E}","input":{{"a":1}}}}')
    shape("嵌入 input 65 条", e, f'{{"model":"{E}","input":' + json.dumps(["x"] * 65) + "}")
    shape("嵌入 不是 JSON", e, "这不是 JSON")
    shape("嵌入 空正文", e, "")
    shape("嵌入 数组", e, "[1]")
    shape("嵌入 null", e, "null")
    shape("嵌入 数字", e, "5")
    shape("嵌入 尾部有多余内容", e, f'{{"model":"{E}","input":"你好"}} xx')
    shape("嵌入 同一个键写两次", e, f'{{"model":"{E}","input":"甲","input":"乙"}}')
    shape("嵌入 不是合法 UTF-8", e, f'{{"model":"{E}","input":"'.encode() + bytes([0xFF]) + b'"}')
    shape("嵌入 孤立代理对", e, f'{{"model":"{E}","input":"' + chr(92) + 'ud800"}', "JSON 转义写的孤立代理对。假模型下 Python 回 200（字符串里带着代理码点）；真分词器编码时抛 TypeError，路由没接，实际是 500")
    shape("嵌入 不认识的模型", e, '{"model":"nope","input":"你好"}')
    shape("嵌入 拿重排模型", e, f'{{"model":"{R}","input":"你好"}}')
    r = "/v1/rerank"
    shape("重排 一对", r, f'{{"model":"{R}","query":"天气","documents":["晴","雨"]}}')
    shape("重排 top_n 合法", r, f'{{"model":"{R}","query":"天气","documents":["晴","雨"],"top_n":1}}')
    shape("重排 top_n 零", r, f'{{"model":"{R}","query":"天气","documents":["晴","雨"],"top_n":0}}', "夹成 1")
    shape("重排 top_n 负数", r, f'{{"model":"{R}","query":"天气","documents":["晴","雨"],"top_n":-3}}')
    shape("重排 top_n null", r, f'{{"model":"{R}","query":"天气","documents":["晴","雨"],"top_n":null}}')
    shape("重排 top_n 字符串", r, f'{{"model":"{R}","query":"天气","documents":["晴","雨"],"top_n":"1"}}', 'Pydantic 宽松模式收 "1"')
    shape("重排 documents 不是数组", r, f'{{"model":"{R}","query":"天气","documents":"晴"}}')
    shape("重排 documents 空数组", r, f'{{"model":"{R}","query":"天气","documents":[]}}')
    shape("重排 documents 里有 null", r, f'{{"model":"{R}","query":"天气","documents":["晴",null]}}')
    shape("重排 query null", r, f'{{"model":"{R}","query":null,"documents":["晴"]}}')
    shape("重排 query 空白", r, f'{{"model":"{R}","query":"  ","documents":["晴"]}}')
    shape("重排 未知字段", r, f'{{"model":"{R}","query":"天气","documents":["晴"],"return_documents":true}}')
    shape("重排 缺 documents", r, f'{{"model":"{R}","query":"天气"}}')
    shape("重排 拿嵌入模型", r, f'{{"model":"{E}","query":"天气","documents":["晴"]}}')

    data = {
        "python": platform.python_version(),
        "fastapi": __import__("fastapi").__version__,
        "pydantic": __import__("pydantic").VERSION,
        "starlette": __import__("starlette").__version__,
        "zoo": zoo,
        "shapes": shapes,
    }
    write_text_lf(out / "python-http.json", json.dumps(data, ensure_ascii=False, indent=1))
    from collections import Counter

    print(f"python-http.json: zoo {len(zoo)} 条（状态码 {dict(Counter(z['python_status'] for z in zoo))}），"
          f"shapes {len(shapes)} 条（状态码 {dict(Counter(x['python_status'] for x in shapes))}）")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    tok = sub.add_parser("tokenizer", help="导出 GM1 用的分词 id 参考数据（含类别去字符后的 id、重排的一对）")
    tok.add_argument("--models-root", required=True, help="模型根目录（登记表里 root 的位置，如 <模型根>）")
    tok.add_argument("--testdata", required=True, help="native/internal/localmodels/tokenizer/testdata")
    tok.set_defaults(run=run_tokenizer)
    engine = sub.add_parser("engine", help="导出 GM2 用的 token id、向量、logit 参考数据")
    engine.add_argument("--models-root", required=True, help="模型根目录，例如 <模型根>")
    engine.add_argument("--out", required=True, help="testdata 目录，里面要有 cases.json、pairs.json")
    engine.add_argument("--threads", type=int, default=2, help="算子内线程数，和服务的 --threads 一致（缺省 2）")
    engine.set_defaults(run=run_engine)
    rules = sub.add_parser("rules", help="导出 GM3 用的登记表和空白集合参照")
    rules.add_argument("--models-root", required=True, help="模型根目录，例如 <模型根>（load_specs 要核对文件在）")
    rules.add_argument("--out", required=True, help="native/internal/localmodels/engine/testdata")
    rules.set_defaults(run=run_rules)
    http = sub.add_parser("http", help="导出 GM5 用的 HTTP 状态码参照（真的 create_app 加假池）")
    http.add_argument("--models-root", required=True, help="模型根目录，例如 <模型根>（load_specs 要核对文件在）")
    http.add_argument("--out", required=True, help="native/internal/localmodels/server/testdata")
    http.set_defaults(run=run_http)
    real = sub.add_parser("real", help="导出 GM6 用的真实样本 id 参照（输入是 memory-eval -dump-embed-inputs 的文件）")
    real.add_argument("--models-root", required=True, help="模型根目录，例如 <模型根>")
    real.add_argument("--input", required=True, help="embedinputs 格式的文件")
    real.add_argument("--out", required=True, help="输出目录，只许临时目录或被 git 忽略的路径")
    real.add_argument("--categories", default="", help="categories.json 的路径（缺省取仓库里 tokenizer/testdata 那份）")
    real.set_defaults(run=run_real)
    sweep = sub.add_parser("sweep", help="导出全部码点的 NFD、NFKD 形式的分词 id（几 MB，不进 git）")
    sweep.add_argument("--models-root", required=True, help="模型根目录，例如 <模型根>")
    sweep.add_argument("--testdata", required=True, help="native/internal/localmodels/tokenizer/testdata（读 categories.json）")
    sweep.add_argument("--out", required=True, help="输出目录，只许临时目录或被 git 忽略的路径")
    sweep.set_defaults(run=run_sweep)
    threads = sub.add_parser("threads", help="导出线程数 1 / 2 / 4 的输出差和同参数重新加载的差（编的句子）")
    threads.add_argument("--models-root", required=True, help="模型根目录，例如 <模型根>")
    threads.add_argument("--cases", required=True, help="cases.json（编的句子）")
    threads.set_defaults(run=run_threads)
    args = parser.parse_args()
    args.run(args)


if __name__ == "__main__":
    main()
