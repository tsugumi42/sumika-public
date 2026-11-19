package turn

import (
	"fmt"
	"strings"

	"sumika-native/internal/store"
)

// slim 是 agent.ts transformContext 的软门槛：实例里的消息估下来超了软门槛，就把本轮之前（最后一条主人的话之前，插话不算）
// 的长工具结果换成短占位（util/context.ts slimOldToolResults）：调用和结果仍然成对，只改这一次请求，实例里的不动。
// 短的（不到 200 个码元）和宿主标记的受控资料不换；换完调用和结果对不上就这次不精简。返回发出去的消息和换掉了几个
func (b *Built) slim(msgs []Msg) ([]Msg, int) {
	if float64(b.Fixed+conversationTokens(msgs)) <= b.Soft {
		return msgs, 0
	}
	lastUser := -1
	for i, m := range msgs {
		if m.Role == "user" && !strings.HasPrefix(m.Text, steerPrefix) {
			lastUser = i
		}
	}
	out := append([]Msg(nil), msgs...)
	n := 0
	for i := range out {
		if i < lastUser && out[i].Role == "user" && len(out[i].Images) > 0 {
			// 较早的主人发的图不再随每次请求重发；文字占位还在，重建后会按事件取回
			out[i].Images = nil
			n++
			continue
		}
		if i >= lastUser || out[i].Role != "toolResult" || out[i].Controlled {
			continue
		}
		if len(out[i].Images) > 0 {
			// 较早读过的图片不再随每次请求重发；文字留着，要看再读一次
			out[i].Images = nil
			out[i].Text += "（较早读过的图片已省略，需要的话重新读一次）"
			n++
			continue
		}
		chars := store.JSLen(strings.TrimSuffix(out[i].Text, out[i].ControlledText))
		if chars < 200 {
			continue
		}
		n++
		out[i].Text = fmt.Sprintf("（较早的工具结果，约 %d 字，上下文快满了先省略；需要的话重新查一次）", chars)
		if out[i].Ref != nil {
			out[i].Text = fmt.Sprintf("（较早的工具结果已省略；用 result_read 读取 id=%s，完整性=%t）", out[i].Ref.ID, out[i].Ref.Complete)
		}
		out[i].Text += out[i].ControlledText
	}
	if toolPairProblems(out) {
		return msgs, 0
	}
	return out, n
}

// toolPairProblems 是 util/context.ts toolPairProblems：有没有前面没有调用的结果、后面没有结果的调用
func toolPairProblems(msgs []Msg) bool {
	open := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, c := range m.Calls {
				if c.ID != "" {
					open[c.ID] = true
				}
			}
		}
		if m.Role == "toolResult" {
			if m.CallID == "" || !open[m.CallID] {
				return true
			}
			delete(open, m.CallID)
		}
	}
	return len(open) > 0
}
