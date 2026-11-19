package turn

import (
	"encoding/json"
	"fmt"
	"time"

	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

// ProcessStep 是这一轮里正在发生的一步（思考、一次工具调用），只给本地主窗口看（规格-本地回合显示）。
// 存库的那份在 trace 的 steps 表，这里只管还没结束时的进度。
type ProcessStep struct {
	ID      string
	Step    int    // 第几步模型调用（步号从 0 起）
	Kind    string // think | tool
	Tool    string // 工具名
	Target  string // 路径、关键词、命令第一行
	Thought string
	Output  string
	State   string // running | done | failed | stopped
	Started int64  // Unix 毫秒
	Seconds int    // 用时，结束后才有
	Rule    string // 这一步是哪条规则放行的（给主人看的一句）；没问、没放行的是空
	// Recall 是 Kind 为 recall 的那一步：这一轮自动召回、真正注入给模型的记忆（不是工具调用）
	Recall []RecallItem
}

// RecallItem 是自动召回里实际注入给模型的一条，只给本地主窗口看相关程度，不进模型上下文以外的任何外发路径。
// 分数只填真有的：没重排 Rerank 是 0，向量没中 Vector、Sim 是 0；界面按这个决定写不写，不编。
type RecallItem struct {
	Text      string
	Kind      string // 记忆自己的类型：fact | promise | preference | boundary | episode
	Pinned    bool   // 固定要点（"要一直遵守的约定和边界"），不是按这句话召回的
	Source    string // 来源（群友说的、模型提炼……），没核实写原样
	CreatedAt int64  // 记下的时间，Unix 毫秒
	Hearsay   bool   // 待确认（"听说"）
	Score     float64
	Rerank    float64
	Sim       float64
	Lexical   int           // 词法名次，0 没中
	Vector    int           // 向量名次，0 没中
	Previous  []RecallPrior // 被这条取代的旧版本（新的在前）：注入的那一行后面拼着它们，模型读到的旧值要能在界面核对
}

// RecallPrior 是命中带着的一条被取代的旧记录，正文原样、时间是记下的 Unix 毫秒。
type RecallPrior struct {
	Text      string
	CreatedAt int64
}

// ProcessLog 是一轮的进度。整个换新的，不原地改：快照里拿到的指针就是不会变的一份。
type ProcessLog struct{ Steps []ProcessStep }

const processOutputMax = 20000

// merge 按 ID 把这一步的新状态并进去，返回新的一份；ev 的空字段不覆盖旧值。
func (p *ProcessLog) merge(ev ProcessStep) *ProcessLog {
	var old []ProcessStep
	if p != nil {
		old = p.Steps
	}
	steps := make([]ProcessStep, len(old), len(old)+1)
	copy(steps, old)
	for i := range steps {
		if steps[i].ID != ev.ID {
			continue
		}
		cur := steps[i]
		cur.State = ev.State
		if ev.Thought != "" {
			cur.Thought = ev.Thought
		}
		if ev.Output != "" {
			cur.Output = ev.Output
		}
		if ev.Target != "" {
			cur.Target = ev.Target
		}
		if ev.Seconds != 0 {
			cur.Seconds = ev.Seconds
		}
		if ev.Rule != "" {
			cur.Rule = ev.Rule
		}
		if len(ev.Recall) > 0 {
			cur.Recall = ev.Recall
		}
		steps[i] = cur
		return &ProcessLog{Steps: steps}
	}
	return &ProcessLog{Steps: append(steps, ev)}
}

func toolProcessDone(id, name string, started time.Time, now time.Time, r tools.Result) ProcessStep {
	state := "done"
	if r.IsError {
		state = "failed"
	}
	out := r.Text
	if n := len(out); n > processOutputMax {
		out = out[n-processOutputMax:]
	}
	secs := 0
	if !started.IsZero() {
		secs = max(int(now.Sub(started).Seconds()), 0)
	}
	return ProcessStep{ID: id, Kind: "tool", Tool: name, State: state, Output: out, Seconds: secs}
}

// recallSeq 给这一轮自动召回记下的步骤编号，step 是它排在第几步模型调用之前。
type recallSeq struct{ step, n int }

// RecallStepKind 是自动召回在 trace 的 steps 表里的种类：不是工具，所以不叫 tool，不进工具统计。
const RecallStepKind = "recall"

// noteRecall 把这一次真正注入的记忆记成一步过程：本地回合存进 trace（刷新、重启后还在）并交给界面。
// 没有召回（或只有固定要点以外什么都没给）就什么都不记；存不进去只写一句不带内容的日志，回合照常。
// 渠道回合（QQ）没有 onProcess、也不是 local，所以这里什么都不做：记忆内容不离开用户本机窗口。
func (h *Hub) noteRecall(st *store.Store, in Input, turnID string, at time.Time, seq *recallSeq, items []RecallItem) {
	if len(items) == 0 || (!in.local() && in.onProcess == nil) {
		return
	}
	seq.n++
	id := fmt.Sprintf("recall:%s:%d", turnID, seq.n)
	if in.local() && st != nil {
		raw, err := json.Marshal(items)
		if err == nil {
			args := string(raw)
			err = st.StepStart(store.Step{ID: id, CharacterID: in.CharacterID, Conversation: in.Conversation, TurnID: turnID, Kind: RecallStepKind, Name: "memory",
				Args: &args, Preview: fmt.Sprintf("%d 条", len(items)), Status: "done", StartedAt: at.UnixMilli()})
		}
		if err != nil {
			h.plugins.logf("记自动召回的过程步骤出错：%v", err)
		}
	}
	if in.onProcess != nil {
		in.onProcess(ProcessStep{ID: id, Step: seq.step, Kind: RecallStepKind, State: "done", Started: at.UnixMilli(), Recall: items})
	}
}
