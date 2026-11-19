#!/bin/sh
# 第零步 c 的冷遍对照：P1（纯文字基线）、E2（行内框现排）、E3（行内框预排）交替跑 N 轮，每次新起进程。
# 用法（在 native/ 里）：sh _spike/run.sh [轮数=3] [输出目录=_spike/out]
# 校验缓存可用条件（不计时）：SPIKE_CHECK=1 SPIKE_VARIANT=E3 go test ./_spike -run TestC -v，输出一行 CHECK。
# 注意：out/ 的 r1 到 r6 里的 cpuPerFrame 是旧写法算的，作废；out/review-c3/ 里的"CPU 每帧"出自不在仓库里的另一版，算式查不到，一并作废；修正后重跑的在 out/cpu-fix/。
# 数据全是合成的，不连 7720、不读 data/。跑的时候机器上别的东西越少越好（单轮离散很大）。
set -e
N=${1:-3}
OUT=${2:-_spike/out}
mkdir -p "$OUT"
go test -c -o "$OUT/spike.test.exe" ./_spike
cd _spike
for r in $(seq 1 "$N"); do
  for v in P1 E2 E3; do
    SPIKE_VARIANT=$v "../$OUT/spike.test.exe" -test.run TestC -test.v > "../$OUT/r${r}_$v.txt" 2>&1
  done
done
rm -f "../$OUT/spike.test.exe"
