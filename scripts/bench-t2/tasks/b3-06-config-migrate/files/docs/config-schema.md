# config.json 的结构和迁移规则

三个环境的 ini 合并成根目录的一个 `config.json`（UTF-8，缩进随意）。

## 结构

顶层只有三个键：

- `version`：数字 1。
- `defaults`：以 dev 环境为基准，dev 的全部配置都在这里。
- `environments`：有 `dev`、`test`、`prod` 三项，每一项只有一个键 `overrides`。`overrides` 里只写和 `defaults` 不同的键，层级和 `defaults` 一样；某一节没有任何不同就整节不写；dev 的 `overrides` 是空对象 `{}`。

某个环境的实际配置 = `defaults` 逐层合并上这个环境的 `overrides`（同名的键以 `overrides` 为准）。

## 从 ini 迁移的规则

1. 节名：`server` → `server`，`db` → `database`，`log` → `logging`，`feature` → `features`。
2. 有三个键改了名：`db` 节的 `pool_size` → `database.pool`；`log` 节的 `file` → `logging.path`；`feature` 节的 `beta_ui` → `features.betaUi`。其余键名不变。
3. 废弃键不迁移：`server.legacy_mode`、`db.legacy_charset`。
4. 取值的类型：
   - 带双引号的值一律是字符串，去掉引号，内容原样保留（`"0042"` 是字符串 `0042`）。
   - 不带引号的整数（只有数字，可带负号）是 JSON 数字。
   - `true` / `false` 是 JSON 布尔值。
   - `server.allowed_hosts` 和 `feature.enabled`（迁移后是 `features.enabled`）永远是字符串数组：按逗号拆开、去掉两边空格，即使只有一项也是数组。
   - 其他不带引号的值是字符串（`127.0.0.1` 这类也是字符串）。
5. ini 里以 `;` 或 `#` 开头的行是注释，不迁移。
