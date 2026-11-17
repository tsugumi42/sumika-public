// Package ort 是 ONNX Runtime 的薄封装（规格-Go本地模型 GM2）。
//
// 对外只有自己的小接口（Runtime、Session）和几个值类型，第三方绑定 github.com/younland/onnxruntime
// 只在 backend.go 里出现，包外不许 import 它（internal/archcheck 钉住）；以后换绑定（比如 cgo 版）只改这一个包。
//
// 加载动态库的几道关（Open，顺序固定，前面的不过后面的不做）：
//  1. 路径必须是绝对路径，不搜索、不回退到系统目录（Windows 自带一份 1.17 的 onnxruntime.dll，裸名会撞上它）；
//  2. 先算文件的 SHA-256，必须在已知好的清单里（known.go）。这一道在 LoadLibrary 之前：dll 一加载 DllMain 就跑了，
//     "加载以后再拒绝"等于没拦。没有"放行未知 dll"的开关；
//  3. 加载以后运行时报告的版本必须正好等于钉的版本（只有这一道在加载之后拒绝）；
//  4. 可用的执行后端里必须有 CPU，会话只注册 CPU。
//
// 会话默认值照 tools/local-models/server.py 的 _new_session：算子内 2 线程、算子间 1、图优化 BASIC、日志级别 error；
// 另外把 CPU 内存池（arena）关掉（Options.CpuMemArena 缺省 false，理由和实测见规格 2.5 与第十节）。
package ort
