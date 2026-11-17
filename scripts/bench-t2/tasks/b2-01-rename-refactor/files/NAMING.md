# 命名约定

本项目按下面的约定命名导出的标识符。旧名一律不再使用，注释和文档里提到也要写新名。

1. 包里最主要的类型直接叫包名（首字母大写），不加缩写：`config.Config`，不叫 `config.Cfg`。
2. 读取并返回主要对象的函数叫 `Load`；不重复包名，不缩写。
3. 取值函数不加 `Get` 前缀，直接写取的是什么：`Quantity`。
4. 往 `io.Writer` 里输出的函数用 `Write` 开头，不用 `Print`（`Print` 留给直接写标准输出的函数）。

## 这次要改的

| 旧名 | 新名 |
|---|---|
| `config.Cfg`（类型） | `config.Config` |
| `config.LoadCfg` | `config.Load` |
| `store.Store.GetQty` | `store.Store.Quantity` |
| `report.PrintLowStock` | `report.WriteLowStock` |

只改上面四个。`report.PrintSummary` 是直接写标准输出的，按第 4 条不用改；别的名字也不用动。
