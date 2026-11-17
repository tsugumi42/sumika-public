// Package server 是 sumika-models 的 HTTP 层和进程外壳（规格-Go本地模型 GM5）：路由、令牌、请求体上限、错误码、日志行、
// 启动检查和退出码、看门狗、stdin 关闭就退出。接口形状、限额和错误码照 tools/local-models/server.py 逐项搬过来，
// 差异只在规格第三节列的那几处。
//
// 这个包是 localmodels 下唯一可以 import net/http 的，也不许 import os/exec（archcheck 的 TestLocalModelsImportWhitelist 钉住）：
// 这个进程能看到所有送去嵌入的文本，它不该有往外连或起别的程序的能力。
//
// 三条贯穿全包的规矩：
//   - 日志和响应里不写输入文字、令牌、绝对路径。422 和 500 的正文只写字段名、数字、模型 id 和相对路径；
//     panic 只记类型和函数名，不记值（值可能就是输入文字）；
//   - 先验再取租约：条数、空白、维度、字符上限这些不碰模型的检查（engine.CheckEmbedRequest 等）在 pool.Acquire 之前做，
//     坏请求不为了回 400 去加载模型，池满时也回 400；
//   - 客户端走了或到 --max-request-seconds，只停在行边界（ORT 的 Run 中途打断不了），回 499 / 504。
package server
