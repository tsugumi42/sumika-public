// sumika-models 是本机嵌入和重排服务（规格-Go本地模型）：纯 Go 的分词器加 purego 加载的 onnxruntime.dll，
// HTTP 形状和 tools/local-models/server.py 一样（差异见规格第三节）。由 Go 后台（embed.Local）起，只听 127.0.0.1，
// 令牌从环境变量 SUMIKA_EMBED_TOKEN 读。这里只有入口，一切都在 internal/localmodels/server。
//
// 退出码：0 正常退出；1 --verify 发现文件对不上、或运行中没法恢复的错；2 配置、文件、dll 的确定性错误
// （Local 见到停止重试）；3 端口被占（Local 换口重试）；4 看门狗：一次前向卡死（Local 重启）。
package main

import (
	"os"

	"sumika-native/internal/localmodels/server"
)

func main() {
	os.Exit(server.Run(os.Args[1:], server.Env{}))
}
