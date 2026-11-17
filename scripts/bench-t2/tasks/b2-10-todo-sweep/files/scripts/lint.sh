#!/usr/bin/env bash
# 简单检查：python 文件能不能编译
# TODO [P3] - 也检查 .js 文件
for f in src/*.py scripts/*.py; do
  python -m py_compile "$f" || exit 1
done
echo "检查通过"
