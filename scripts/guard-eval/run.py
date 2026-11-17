"""前台量测入口：保存检查原始输出，或按断点跑少量样本；没有后台任务。"""
import argparse
import json
import subprocess
from pathlib import Path


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--data", type=Path, required=True)
    p.add_argument("--action", choices=["verify", "eval", "report"], required=True)
    p.add_argument("--round", type=int, default=1)
    p.add_argument("--first-round", type=int, default=1)
    p.add_argument("--method", choices=["A", "B", "C"], default="A")
    p.add_argument("--count", type=int, default=6)
    args = p.parse_args()
    root = Path(__file__).resolve().parents[2]
    native = root / "native"
    args.data.mkdir(parents=True, exist_ok=True)
    if args.action == "verify":
        commands = [
            ["gofmt", "-w", "cmd/guard-eval/main.go", "cmd/guard-eval/eval.go", "cmd/guard-eval/eval_test.go"],
            ["go", "vet", "./cmd/guard-eval/"],
            ["go", "test", "-count=1", "-v", "./cmd/guard-eval/"],
        ]
    else:
        command = ["go", "run", "./cmd/guard-eval/", "-data", str(args.data)]
        if args.action == "report":
            command += ["-report", "-first-round", str(args.first_round)]
        else:
            path = args.data / f"round{args.round}-{args.method}.jsonl"
            start = len(path.read_text(encoding="utf-8").splitlines()) if path.exists() else 0
            ds = json.loads((args.data / "dataset.json").read_text(encoding="utf-8"))
            if start == len(ds["samples"]):
                print(f"round={args.round} method={args.method} complete={start}")
                return
            command += ["-round", str(args.round), "-method", args.method, "-start", str(start), "-limit", str(args.count)]
        commands = [command]
    for command in commands:
        result = subprocess.run(command, cwd=native, capture_output=True, text=True, encoding="utf-8")
        # 原始输出在指定 scratchpad 留证，不依赖终端的折叠或摘要。
        with (args.data / "commands.log").open("a", encoding="utf-8") as f:
            f.write("$ " + " ".join(command) + "\n" + result.stdout + result.stderr + f"exit={result.returncode}\n")
        print(result.stdout + result.stderr, end="", flush=True)
        print(f"exit={result.returncode}", flush=True)
        if result.returncode:
            raise SystemExit(result.returncode)


if __name__ == "__main__":
    main()
