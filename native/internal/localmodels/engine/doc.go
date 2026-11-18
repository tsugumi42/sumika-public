// Package engine 是本机嵌入和重排的计算本身（规格-Go本地模型 GM3）：登记表、池化、归一化、matryoshka 截断、前缀、
// 重排的 sigmoid、缓存、一次一行、每行一把锁。口径照 tools/local-models/server.py 逐项抄过来，差异写在规格里。
//
// 它只依赖两个接口：Tokenizer（分词器，*tokenizer.Tokenizer 满足）和 ort.Session（ONNX 会话），测试用假的。
// 加载文件、核哈希、建会话是 GM4（模型池）的事；这里的 Embedder / Reranker 拿到的是已经建好的会话，关闭也归创建者。
// 这个包和它的依赖里不许有 net、net/http、os/exec（只有 server 包可以 net/http，规格 4.2）。
package engine
