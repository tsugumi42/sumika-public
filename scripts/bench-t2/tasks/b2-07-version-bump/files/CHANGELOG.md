# 更新记录

本文件格式参考 Keep a Changelog，版本号遵循语义化版本。

## [未发布]

## [1.4.2] - 2026-09-30

### 修复
- worker：系统时间回拨时，重试间隔会算成负数
- api：空标题的任务也能被创建出来

### 变更
- 任务 ID 改成字符串（不兼容变更，最低兼容版本提到 1.4.2）

## [1.4.1] - 2026-09-12

### 修复
- web：任务列表翻到第二页后筛选条件丢失

## [1.4.0] - 2026-08-28

### 新增
- api：支持批量创建任务
- web：任务页增加“重试”按钮

[未发布]: https://example.com/harbor/compare/v1.4.2...HEAD
[1.4.2]: https://example.com/harbor/compare/v1.4.1...v1.4.2
[1.4.1]: https://example.com/harbor/compare/v1.4.0...v1.4.1
[1.4.0]: https://example.com/harbor/releases/tag/v1.4.0
