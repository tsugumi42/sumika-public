//go:build e2e

package main

// 只给量性能的构建：带上 mygo 的自动化插件，设了 MYGO_AUTOMATION 才监听。发布的画廊不带它。
import _ "github.com/egoist/mygo/plugins/automation"
