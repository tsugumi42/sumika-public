#!/usr/bin/env bash
# 备份数据库（示例脚本）
# TODO p1: 备份前先检查磁盘空间
KEEP=3   # TODO(P2) [easy]: 保留份数从 3 改成 7
mkdir -p backups
cp shelf.db "backups/shelf-$(date +%Y%m%d).db"
ls -1t backups | tail -n +$((KEEP + 1)) | while read -r f; do rm -f "backups/$f"; done
