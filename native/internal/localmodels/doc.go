// Package localmodels 是本机嵌入和重排模型的纯 Go 实现（规格-Go本地模型）的父目录：
// 分词器、ONNX Runtime 薄封装、引擎、模型池、HTTP 服务各占一个子包，最后由 cmd/sumika-models 装起来。
//
// 这一棵树只给 sumika-models 和对拍命令用：桌面程序和 sumika-go 不许链接它（ORT 是原生代码，
// 崩了只该带走那个小进程，见 internal/archcheck 的 toolOnly）。
package localmodels
