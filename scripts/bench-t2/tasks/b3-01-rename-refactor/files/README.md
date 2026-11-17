# shelf

一个很小的库存查看工具：读入货架上的物品，打印汇总和缺货清单，也能导出 CSV。

## 设置

设置由 `config.LoadCfg` 读取。先取 `config/defaults.json` 里的默认值，再用你给的设置文件里写了的项覆盖。
设置的类型是 `config.Cfg`，字段如下：

| 字段 | 含义 | 默认值 |
|---|---|---|
| `low_stock_threshold` | 数量小于它就算缺货 | 5 |
| `page_size` | 汇总里每页最多列几件 | 20 |
| `currency` | 金额的货币符号 | CNY |

设置文件的写法见 `shelf.example.json`。

## 查数量

用 `store.Store.GetQty(sku)` 取一个物品的数量，没有这个物品返回 0。

## 输出

- `report.PrintSummary` 打印汇总到标准输出。
- `report.PrintLowStock` 把缺货清单写进给定的 `io.Writer`。
- `export.WriteCSV` 把全部物品写成 CSV。

运行：`go run ./cmd/shelf`。
