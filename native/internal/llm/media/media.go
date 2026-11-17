// Package media 是模型请求里的媒体载荷：工具、传输和编解码器都要用，但不依赖它们中的任何一个。
// 放在叶子包里，免得 tools 为了 llm.Image 反向依赖模型客户端（llm 要 import chat，chat 又要 import tools）。
package media

// Image 是一张随消息带去的图，DataURL 是 data:...;base64,... 形式。
type Image struct {
	DataURL string
}
