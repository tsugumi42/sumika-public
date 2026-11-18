// Package archcheck 只放架构测试：整棵依赖树上的约束（谁能接适配器、插件只依赖 types、发出去的程序不链接一次性的包）。
// 单独成包是为了挑包时便宜地带上：合并前只测改了的包（ADR-225），只要挑上了 Go 包就连它一起跑，它只调 go list，几秒跑完。
package archcheck
