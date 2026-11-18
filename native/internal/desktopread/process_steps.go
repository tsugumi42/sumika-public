package desktopread

import (
	"encoding/json"

	"sumika-native/internal/api"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// processOf 读这条回复之前的思考和工具（规格-本地回合显示）。只有本地会话有；读不到就当没有，消息照常显示。
func (s *Service) processOf(k api.ConversationKey, r store.DisplayRow) []api.LocalProcessStep {
	// 回复挂着它之前的思考和工具；中断的回合没有回复，它的步骤挂在“程序退出时这一轮被中断”的系统行上
	if k.Platform != "local" || (r.Origin != "character" && r.Origin != "system:"+store.SurfaceSource) || r.Seq == 0 {
		return nil
	}
	st, ok := s.stores.Platform("local")
	if !ok {
		return nil
	}
	rows, err := st.ReadSteps(k.Conversation, r.Seq)
	if err != nil {
		return nil
	}
	var out []api.LocalProcessStep
	for _, d := range rows {
		step := api.LocalProcessStep{ID: d.ID, Tool: d.Name, Started: d.StartedAt, State: stepState(d)}
		exit := exitInterrupted(d)
		if d.EndedAt > d.StartedAt {
			step.Seconds = int((d.EndedAt - d.StartedAt) / 1000)
		}
		if d.Kind == "recall" {
			// 自动召回：明细是回合当时存下的 JSON；坏了就当没有这一行，消息照常显示
			var items []api.RecallItem
			if json.Unmarshal([]byte(d.Args), &items) != nil || len(items) == 0 {
				continue
			}
			step.Kind, step.Tool, step.Recall = "recall", "", items
		} else if d.Kind == "thinking" {
			step.Kind, step.Thought = "think", d.Preview
		} else {
			step.Kind, step.Output, step.Rule = "tool", d.Preview, turn.StepRule(d.Approval)
			if exit { // 状态写清是程序退出时被切断的：不是转圈，也不是成功；已有的输出（旧版本留下的结果文字）跟在后面
				step.Output = ExitInterruptedNote
				if d.Preview != "" && d.Preview != store.InterruptedPreview() {
					step.Output += "\n" + d.Preview
				}
			}
			if args, err := jsjson.Parse(d.Args); err == nil {
				step.Target = tools.CallTarget(d.Name, args)
			}
		}
		out = append(out, step)
	}
	return out
}

// ExitInterruptedNote 是程序退出时被切断的工具在卡片展开处写的第一句。
const ExitInterruptedNote = "程序退出时中断：这一步没有跑完，结果不知道，可能已经执行了一部分。"

// exitInterrupted 这一步是程序退出时被切断的：新写的带 details 标记；旧版本收拾的行没有标记，认它写下的预览。
func exitInterrupted(d store.DisplayStep) bool {
	if d.Kind == "thinking" {
		return false
	}
	return d.Interrupted == store.InterruptedByExit || (d.Status == "error" && d.Preview == store.InterruptedPreview())
}

func stepState(d store.DisplayStep) string {
	switch {
	case exitInterrupted(d), d.Interrupted == store.InterruptedByStop:
		return "stopped"
	case d.Status == "denied":
		return "denied"
	case d.Status == "cancelled", d.Status == "expired":
		return "stopped"
	case d.IsError, d.Status == "error":
		return "failed"
	}
	return "done"
}
