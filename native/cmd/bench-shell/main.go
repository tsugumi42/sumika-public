// bench-shell：表二（规格-Go后台 8.2，真实模型）的 Go 壳。读一份题目（internal/bench 的 Task），在题目给的临时数据目录、
// 工作目录里建一个 Hub，接执行器给的网关（记账代理）真跑，写一份结果（Result）。
// 审批用 yolo：工作目录里的读写、看得清的命令自动放行，要问的走一遍审批、下一拍取消（不执行），和 Bun 壳一样；
// 题目给了 approveInWorkdir 的，只在工作目录里的那些替主人批准一次（bench.ApproveInWorkdir），其余说明规则拒绝并让模型换法继续。
//
//	go run ./cmd/bench-shell -task task.json -out result.json [-root 项目根]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/bench"
	"sumika-native/internal/config"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm"
	"sumika-native/internal/memory"
	"sumika-native/internal/memplugin"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/store/memsource"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// 编的角色卡，三个壳一样（Bun 壳写同一份）
const (
	cardYML = "name: 铃\n"
	cardMD  = "## 身份\n铃，住在主人电脑里的女仆，帮主人在电脑上做事。\n\n## 行为\n干活利落，话说到点上；要动文件、跑命令时用工具去做，做完说清楚做了什么。\n\n## 表达\n短句，像发消息。\n\n## 私下\n喜欢被夸。\n"
)

func main() {
	taskFile := flag.String("task", "", "题目（JSON）")
	outFile := flag.String("out", "", "结果写到哪（JSON）")
	root := flag.String("root", "", "项目根（出厂提示词在 prompts/）；不给就从当前目录往上找")
	flag.Parse()
	res := bench.Result{Shell: "go"}
	if err := run(*taskFile, *root, &res); err != nil {
		res.Error = err.Error()
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(*outFile, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写结果出错："+err.Error())
		os.Exit(1)
	}
}

func run(taskFile, root string, res *bench.Result) error {
	b, err := os.ReadFile(taskFile)
	if err != nil {
		return err
	}
	var t bench.Task
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	if root == "" {
		if root, err = config.FindRoot("."); err != nil {
			return err
		}
	}
	// grep、find 用的 rg、fd：照 daemon/main.ts（Bun 壳同样）把仓库的 vendor/bin 放在 PATH 最前面；
	// 原来漏了，2026-10-27 B1 本地题里 Go 的 grep、find 报"rg / fd 没有"
	os.Setenv("PATH", filepath.Join(root, "vendor", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	p, err := paths.Init(t.DataDir)
	if err != nil {
		return err
	}
	card := p.CardDir(t.Character)
	if err := os.MkdirAll(card, 0o755); err != nil {
		return err
	}
	os.WriteFile(filepath.Join(card, "character.yml"), []byte(cardYML), 0o644)
	os.WriteFile(filepath.Join(card, "character.md"), []byte(cardMD), 0o644)
	// 只开本地库（主窗口只用它）和共用的 trace（规格-聊天记录按平台分库 第五节第 5 步）：st 是本地那份，查步骤、模型调用走它带的共用 trace
	set, err := store.OpenGo(p, "local")
	if err != nil {
		return err
	}
	defer set.Close()
	conv := "console:" + t.Character
	group := false
	if t.Conversation != "" {
		conv = t.Conversation
		group = strings.Contains(conv, ":group:")
	}
	st, err := set.ForConversation(conv)
	if err != nil {
		return err
	}
	base := time.Now().Add(-time.Duration(len(t.Seed)) * time.Minute).UnixMilli()
	for i, l := range t.Seed {
		at := l.At
		if at == 0 {
			at = base + int64(i)*60_000
		}
		who := "owner"
		if l.Who == "character" {
			who = t.Character
		}
		if _, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: l.Who, ActorID: who, Origin: l.Who, Kind: "message", Text: l.Text, CreatedAt: at}, []string{t.Character}); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := llm.New(llm.Magpie(t.Gateway))
	models, err := client.Models(ctx)
	if err != nil {
		return err
	}
	gate := approval.NewGate(p.Root)
	gate.Mode = "yolo"
	// 评测量的是模型和工具，机器忙时拆命令慢不能算成"看不清"、把命令取消掉（2026-11-09 合并队列 TestShellStopAfterTool 这样红过）。
	gate.AnalyzeBudget = 2 * time.Minute
	defer gate.Analyzer.Close()
	// 先把拆命令的 pwsh 起好，第一条命令不用再等冷启动（照 Bun 壳 analyze('Get-Date', 20_000)）
	gate.AnalyzeShell(ctx, "Get-Date")
	// 要问的照真的走一遍审批（步骤记成 waiting → cancelled，和 Bun 壳一样），卡片一发出就下一拍取消这一张；
	// 原来不接审批、Ask 为空直接当取消，步骤只落成 error，B5 会把它算成工具出错（2026-10-27 B1 本地题 b1-l03 看出来的）
	approvals := approval.NewApprovals(gate)
	turn.WireApprovalSteps(approvals, set.TraceStore())
	// 题目要"工作目录里的一律批准"时（B2、B3 量长任务本身）：照 bench.ApproveInWorkdir 判，批的那张替主人点"允许一次"，
	// 别的给出规则拒绝说明。卡片里没有碰到的外部目录，在 Ask 那一层记下来（发卡片之前就有）
	var dirsOf sync.Map
	ask := gate.Ask
	gate.Ask = func(ctx context.Context, req approval.Request, kind *approval.Pending) approval.Decision {
		if kind != nil {
			dirsOf.Store(req.ID, kind.Dirs)
		}
		defer dirsOf.Delete(req.ID)
		d := ask(ctx, req, kind)
		if t.ApproveInWorkdir && d.Kind == "denied" {
			d.Feedback = bench.WorkdirDenied
		}
		return d
	}
	approvals.OnRequest = append(approvals.OnRequest, func(req approval.Request) {
		var dirs []string
		if v, ok := dirsOf.Load(req.ID); ok {
			dirs = v.([]string)
		}
		if t.ApproveInWorkdir {
			choice, reason := "deny", bench.WorkdirDenied
			if bench.ApproveInWorkdir(req.Tool, req.Subject, t.Workdir, dirs) {
				choice, reason = "once", ""
			}
			go approvals.Decide(req.ID, choice, reason)
			return
		}
		go approvals.Cancel(req.ID)
	})
	defaults := config.Defaults().Agent
	tc := turn.Config{Root: root, Data: p, Thinking: t.Thinking, RecentMessages: defaults.RecentMessages, Workdir: t.Workdir,
		Models: map[string]string{t.Character: t.Model}, Shell: turn.DetectShell(ctx), Now: time.Now, Gate: gate,
		MaxLive: defaults.MaxLive, IdleAfter: time.Duration(defaults.IdleMinutes * float64(time.Minute)),
		Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }}
	ac := defaults.Context
	tc.Context = turn.ContextLimits{Window: ac.Window, Reserve: ac.Reserve, SoftRatio: ac.SoftRatio, HardRatio: ac.HardRatio, KeepTokens: ac.KeepTokens, NoAutoCompact: !ac.AutoCompactChat}
	if t.KeepTokens > 0 {
		tc.Context.KeepTokens = t.KeepTokens
	}
	if t.RecentMessages > 0 {
		tc.RecentMessages = t.RecentMessages
	}
	// 在固定的工具调用边界停（B3）：第 n 个工具执行完以后按停止，等这一轮停下再交回结果（下一次请求不会发出去）；
	// 或者第 n 个工具开始执行以后过一会儿按停止（工具正在跑时停）。只数真执行了的内置工具（ExecTool 只经过这些），
	// 给了 StopMatch 的只数对得上的；Bun 壳、omp 驱动照同样的数法（protocol.go Step）
	var mu sync.Mutex
	var hub *turn.Hub
	count, cur := 0, bench.Step{}
	var stopTriggered atomic.Bool
	tc.ExecTool = func(ctx context.Context, workdir, name string, args jsjson.Value) (tools.Result, error) {
		mu.Lock()
		counts := cur.Counts(name, jsjson.Stringify(args))
		if counts {
			count++
		}
		n, s := count, cur
		mu.Unlock()
		if counts && s.StopDuringTool > 0 && n == s.StopDuringTool {
			timer := time.AfterFunc(time.Duration(s.StopDelay())*time.Millisecond, func() {
				if ctx.Err() == nil {
					stopTriggered.Store(true)
					hub.StopIn(t.Character, conv)
				}
			})
			defer timer.Stop() // 目标工具已结束就撤销，不能迟到停在下一次调用上。
		}
		r, err := tools.Execute(ctx, workdir, name, args)
		if counts && s.StopAfterTool > 0 && n == s.StopAfterTool {
			stopTriggered.Store(true)
			go hub.StopIn(t.Character, conv)
			<-ctx.Done()
		}
		return r, err
	}
	hub = turn.NewHub(ctx, tc, set, client, models)
	defer hub.Idle()
	defer cancel()
	// 记忆插件：Bun 壳装了 Memory（空库、不开嵌入），请求里有记忆的三个工具；Go 这边照样装真的记忆插件、给一个空库，
	// 两边发给模型的工具清单才一样。recall_raw 查原话要读本地库的事件：原来没接，Go 的 recall_raw 一律报 no_events
	// （2026-10-27 B4 第一轮 20 次里 12 次调到它全失败，Bun 的照常查）
	src, err := memsource.New(st, t.Character)
	if err != nil {
		return err
	}
	mem, err := emptyMemory(ctx, p.Root, p.MemoryDB(t.Character), t.Character, src)
	if err != nil {
		return err
	}
	defer mem.Close()
	mp := memplugin.New(memplugin.Options{Memories: func(id string) *memory.Memory {
		if id == t.Character {
			return mem
		}
		return nil
	}, Logf: tc.Logf})
	if err := hub.Use(mp); err != nil {
		return err
	}
	defer hub.StopPlugin(memplugin.ID) // 先停插件（等在途的召回、整理退出）再关库

	limit := time.Duration(t.TurnSeconds) * time.Second
	if limit <= 0 {
		limit = 600 * time.Second
	}
	for _, s := range t.Steps {
		var sr bench.StepResult
		start := time.Now()
		if s.Compact {
			c, err := hub.Compact(t.Character, conv, "manual", 0)
			sr.Compact = &bench.CompactState{OK: c.OK, Error: c.Err}
			if err != nil && sr.Compact.Error == "" {
				sr.Compact.Error = err.Error()
			}
			// 压出来的摘要原文（B4 直接看要紧的事实还在不在摘要里），和 Bun 壳一样取这个会话最新一份成功的
			st.Events.QueryRow(`SELECT summary FROM context_epochs WHERE conversation = ? AND status = 'ok' ORDER BY created_at DESC, id DESC LIMIT 1`, conv).Scan(&sr.Compact.Summary)
		}
		if s.Say != "" {
			mu.Lock()
			count, cur = 0, s
			mu.Unlock()
			stopTriggered.Store(false)
			var timedOut atomic.Bool
			timer := time.AfterFunc(limit, func() {
				timedOut.Store(true)
				hub.StopIn(t.Character, conv)
			})
			var out turn.Outcome
			if group {
				// 群聊对照：主人这句先入库，再按群听众请她回（和 app 的本地群聊一样，不走 Send 的私聊入口）
				seq, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "owner",
					Origin: "owner", Kind: "message", Text: s.Say, CreatedAt: time.Now().UnixMilli()}, []string{t.Character})
				if err != nil {
					return err
				}
				aud := audience.Audience{Channel: "console", Conversation: conv, Scene: "group", PrivateOK: true, OwnerAsked: true}
				out = <-hub.Respond(turn.Input{CharacterID: t.Character, Conversation: conv, Seqs: []int64{seq}, Text: s.Say,
					Audience: aud, Addressed: true, Scope: memory.ScopeKey(aud)})
			} else {
				out = <-hub.Send(t.Character, s.Say)
			}
			timer.Stop()
			sr.TimedOut = timedOut.Load()
			if s.StopAfterTool > 0 || s.StopDuringTool > 0 {
				triggered := stopTriggered.Load()
				sr.StopTriggered = &triggered
			}
			if out.Err != nil {
				sr.Error = out.Err.Error()
			}
			if r := out.Result; r != nil {
				var said []string
				for _, x := range r.Said {
					said = append(said, x.Text)
				}
				sr.Reply = strings.Join(said, "\n")
				if sr.Reply == "" {
					sr.Reply = r.Reply
				}
				sr.Stopped = r.Stopped
			}
			if out.TurnID != "" {
				st.Trace.QueryRow(`SELECT COALESCE(outcome, '') FROM turns WHERE id = ?`, out.TurnID).Scan(&sr.Outcome)
				if sr.Tools, err = toolsOf(st, out.TurnID); err != nil {
					return err
				}
				if sr.Calls, err = callsOf(st, out.TurnID); err != nil {
					return err
				}
			}
		}
		sr.Ms = time.Since(start).Milliseconds()
		res.Steps = append(res.Steps, sr)
	}
	return nil
}

// emptyMemory 在这一次的临时数据目录里建一个空的记忆库（和程序启动时一样走 memory.CreateIfEmpty：基础表 + 写入侧迁移），
// 可写打开、不开嵌入（Bun 壳也关着嵌入）；ev 是查原话（recall_raw）读的事件
func emptyMemory(ctx context.Context, data, file, character string, ev memory.EventSource) (*memory.Memory, error) {
	rel, err := filepath.Rel(data, file)
	if err != nil {
		return nil, err
	}
	if _, _, err := memory.CreateIfEmpty(ctx, data, file, filepath.Join(data, "go", "upgrade-snapshots"), filepath.ToSlash(rel)); err != nil {
		return nil, err
	}
	return memory.Open(ctx, file, nil, nil, memory.Options{Writable: true, Character: character, Events: ev})
}

func toolsOf(st *store.Store, turnID string) ([]bench.ToolUse, error) {
	rs, err := st.Trace.Query(`SELECT COALESCE(name, ''), COALESCE(args_json, ''), status, is_error, COALESCE(preview, '') FROM steps
		WHERE turn_id = ? AND kind = 'tool' ORDER BY started_at, rowid`, turnID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []bench.ToolUse{}
	for rs.Next() {
		var u bench.ToolUse
		if err := rs.Scan(&u.Name, &u.Args, &u.Status, &u.IsError, &u.Preview); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rs.Err()
}

func callsOf(st *store.Store, turnID string) ([]bench.Call, error) {
	rs, err := st.Trace.Query(`SELECT COALESCE(ttft_ms, 0), COALESCE(total_ms, 0), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0),
		COALESCE(cache_read_tokens, 0), COALESCE(cache_write_tokens, 0), COALESCE(reasoning_tokens, 0), COALESCE(error, '')
		FROM model_runs WHERE turn_id = ? ORDER BY started_at, rowid`, turnID)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []bench.Call{}
	for rs.Next() {
		var c bench.Call
		if err := rs.Scan(&c.TTFTMs, &c.Ms, &c.Input, &c.Output, &c.CacheRead, &c.CacheWrite, &c.Reasoning, &c.Error); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rs.Err()
}
