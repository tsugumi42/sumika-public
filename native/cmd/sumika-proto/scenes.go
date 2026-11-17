package main

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"strconv"
	"time"
)

type scenarioPanel struct {
	Target, Actor, Delay, Count, Interval, Seed string
	Source, Error                               string
	ShowScript                                  bool
	Trace                                       ui.ListState
}

var venueNames = map[string]string{"localPrivate": "本地私聊", "localGroup": "本地群聊", "qqPrivate": "QQ 私聊", "qqGroup": "QQ 群聊"}

func (a *app) scenarioRun(interleave, tools bool) {
	p := &a.Scenario
	cv := a.E.conv(p.Target)
	delay, err := strconv.Atoi(p.Delay)
	if err != nil || delay < 0 {
		p.Error = "延迟必须是非负整数（毫秒）。"
		return
	}
	count, err := strconv.Atoi(p.Count)
	if err != nil || count < 0 || count > 10000 {
		p.Error = "次数必须是 0 到 10000 的整数。"
		return
	}
	interval, err := strconv.Atoi(p.Interval)
	if err != nil || interval < 0 {
		p.Error = "间隔必须是非负整数（毫秒）。"
		return
	}
	seed, err := strconv.ParseInt(p.Seed, 10, 64)
	if err != nil {
		p.Error = "seed 必须是整数。"
		return
	}
	if tools && cv.Account != "local" {
		p.Error = "第一步的读搜只在本地场合演练。"
		return
	}
	s := replyScript(cv, "run.start", interleave, tools)
	s.Seed = seed
	if !interleave {
		s.Tracks[0].Target.Actor = p.Actor
	}
	source := scriptJSON(s)
	checked, err := parseScript(source, a.E)
	if err != nil {
		p.Error = err.Error()
		return
	}
	p.Source = source
	p.Error = ""
	for i := range count {
		a.E.schedule(time.Duration(delay+i*interval)*time.Millisecond, "", func() { a.E.start(checked, "run.start", "") })
	}
}
func (a *app) sceneView(c *ui.Context, height float32) {
	w, _ := c.Size()
	left := spacePanel
	if w >= breakpointNarrow {
		left += navWidth
	}
	panel := ui.Column(c.Key("scene-panel")).Absolute().Left(left).Right(spacePanel).Top(headerHeight + spacePanel).Height(height).Padding(spacePanel).Gap(spacePanel).Radius(radiusPanel).Background(a.P.Pane).BorderWidth(splitSize).BorderColor(a.P.Line).Modal()
	if panel.OverlayShortcut(0, ui.KeyEscape) {
		a.Scenes = false
	}
	a.surface(c, panel, "scene-panel", "pane", radiusPanel, spacePanel).Children(func() {
		ui.Row(c.Key("scene-header")).MinHeight(hitSmall).Gap(spaceSmall).Wrap().Children(func() {
			ui.Text(c, "场景").FontSize(labelSize).FixedLineHeight(metaLine).FontWeight(strongWeight).Grow(1)
			text := "暂停"
			if a.E.Paused {
				text = "继续"
			}
			a.button(c, "pause", text, false, false, func() { a.E.Paused = !a.E.Paused })
			a.button(c, "next", "下一事件", false, !a.E.Paused || len(a.E.Events) == 0, func() { a.E.next() })
			a.button(c, "reset", "重置", false, false, func() {
				wall := a.E.Wall
				a.E = newEngine(a.E.Run + 1)
				a.MD = nil // 重置以后旧消息的 markdown 状态不再用
				a.E.Wall = wall
				a.Reads = make(map[string]*reading)
			})
			a.button(c, "close-scenes", "收起", false, false, func() { a.Scenes = false })
		})
		ui.Scroll(c.Key("scene-scroll")).Grow(1).MinHeight(0).Gap(spacePanel).Children(func() {
			ui.Row(c).Gap(spaceMedium).Wrap().Children(func() {
				a.choice(c, "target", "场景目标", &a.Scenario.Target, []string{"A", "B", "C", "D"}, func(id string) string { cv := a.E.conv(id); return cv.ID + " · " + cv.Name }).OnChange(func() { a.Scenario.Actor = a.E.conv(a.Scenario.Target).Actors[0] })
				cv := a.E.conv(a.Scenario.Target)
				a.choice(c, "actor", "场景角色", &a.Scenario.Actor, cv.Actors, func(id string) string { return actorNames[id] })
				ui.Text(c, venueNames[cv.Venue]).FontSize(metaSize).TextColor(a.P.Muted)
			})
			ui.Row(c).Gap(spaceMedium).Wrap().Children(func() {
				for _, f := range []struct {
					Key, Label string
					Value      *string
				}{{"delay", "延迟 ms", &a.Scenario.Delay}, {"count", "次数", &a.Scenario.Count}, {"interval", "间隔 ms", &a.Scenario.Interval}, {"seed", "seed", &a.Scenario.Seed}} {
					ui.Column(c.Key(f.Key)).Gap(spaceTight).Children(func() {
						ui.Text(c, f.Label).FontSize(metaSize)
						a.textField(c, "field-"+f.Key, f.Value, f.Label).Width(headerHeight + spaceLarge)
					})
				}
			})
			ui.Row(c).Gap(spaceMedium).Wrap().Children(func() {
				a.button(c, "reply-one", "让她回一句", false, false, func() { a.scenarioRun(false, false) })
				a.button(c, "reply-two", "让两个角色交错说", false, false, func() { a.scenarioRun(true, false) })
				a.button(c, "fail-next", "发送失败", a.E.conv(a.Scenario.Target).FailNext, a.E.conv(a.Scenario.Target).Account != "local", func() {
					a.E.conv(a.Scenario.Target).FailNext = true
				})
				a.button(c, "read-search", "让她调工具", false, a.E.conv(a.Scenario.Target).Account != "local", func() { a.scenarioRun(false, true) })
				a.button(c, "script-toggle", "脚本", false, false, func() {
					a.Scenario.ShowScript = !a.Scenario.ShowScript
					if a.Scenario.Source == "" {
						a.Scenario.Source = scriptJSON(a.E.Scripts[a.Scenario.Target])
					}
				})
				a.button(c, "load-history", "一万条历史", false, a.E.active(a.Scenario.Target) || a.E.conv(a.Scenario.Target).PendingDraft != "", func() { cv := a.E.conv(a.Scenario.Target); a.E.history(cv, 10000); a.read(cv.ID).List.ScrollToEnd() })
			})
			ui.Row(c.Key("process-scenes")).Gap(spaceMedium).Wrap().Children(func() {
				for _, ps := range processScenes {
					a.button(c, "proc-"+ps.Key, ps.Label, false, a.E.conv(a.Scenario.Target).Account != "local", func() { a.processRun(ps.Key) })
				}
			})
			if a.Scenario.Error != "" {
				ui.Text(c, a.Scenario.Error).TextColor(a.P.Danger)
			}
			if a.E.Error != "" {
				ui.Text(c, a.E.Error).TextColor(a.P.Danger)
			}
			if a.Scenario.ShowScript {
				a.surface(c, ui.TextAreaBase(c.Key("script-source"), &a.Scenario.Source).Label("JSON 场景脚本").Lines(4, 8).FillWidth().Padding(spaceMedium).Radius(radiusControl).Background(a.P.Bubble).FontSize(metaSize).FixedLineHeight(metaLine), "script-source", "multiline", radiusControl, spaceMedium)
				a.button(c, "apply-script", "应用脚本", true, false, func() {
					s, err := parseScript(a.Scenario.Source, a.E)
					if err != nil {
						a.Scenario.Error = err.Error()
						return
					}
					for _, tr := range s.Tracks {
						if tr.On == "message.accepted" {
							a.E.Scripts[tr.Target.Conversation] = s
						}
					}
					a.E.start(s, "run.start", "")
					a.Scenario.Error = ""
				})
			}
			ui.List(c.Key("event-trace"), &a.Scenario.Trace, len(a.E.Trace), func(i int) {
				tr := a.E.Trace[i]
				ui.Text(c, fmt.Sprintf("%d ms · %s · %s → %s", tr.At.Milliseconds(), tr.Target, tr.From, tr.To)).FontSize(metaSize).FixedLineHeight(metaLine)
			}).Height(headerHeight * 2)
		})
	})
}

// processScenes 是场景面板里演练过程组件的几段脚本（规格-过程组件 六-5）：每段后面接一句回复，看 await 的分支接没接上。
var processScenes = []struct{ Key, Label string }{
	{"think", "想一会儿再回"}, {"cmd-fail", "命令失败"}, {"slow-cancel", "很慢的命令被取消"},
	{"approve-cmd", "要批准的命令"}, {"approve-edit", "要批准的改文件"}, {"expire", "审批过期"},
}

func processScene(kind string, cv *conversation, actor string) script {
	end := map[string][]step{"succeeded": {}, "failed": {}, "canceled": {}, "denied": {}, "expired": {}}
	var steps []step
	switch kind {
	case "think":
		steps = []step{{Op: "trigger", Trigger: "A01", ID: "think", Args: args{DurationMS: 3200, Text: "她刚才提到纸灯，先想想昨天说到哪了。\n上次停在窗边那段。"}}, {Op: "await", Ref: "think", Then: map[string][]step{"complete": {}}}}
	case "cmd-fail":
		steps = []step{
			{Op: "trigger", Trigger: "A02", ID: "read", Args: args{Tool: "fake-read", DurationMS: 400, Target: "notes/纸灯.md"}}, {Op: "await", Ref: "read", Then: map[string][]step{"succeeded": {}}},
			{Op: "trigger", Trigger: "A03", ID: "test", Args: args{Command: "go test ./lamp/...", Output: "=== RUN   TestLamp\n--- FAIL: TestLamp (0.01s)\n    lamp_test.go:12: 亮度 0.4，期望 0.6\nFAIL", Exit: 1, DurationMS: 2400}}, {Op: "await", Ref: "test", Then: end}}
	case "slow-cancel":
		steps = []step{
			{Op: "trigger", Trigger: "A04", ID: "build", Args: args{Command: "make all", Output: "编译 lamp\n编译 window\n编译 garden\n打包\n上传", DurationMS: 15000}},
			{Op: "wait", MS: 4000}, {Op: "trigger", Trigger: "A05", ID: "cancel", Ref: "build", Args: args{DurationMS: 400}}, {Op: "await", Ref: "build", Then: end}}
	case "approve-cmd", "expire":
		trigger, ttl := "P01", 270000
		if kind == "expire" {
			trigger, ttl = "P04", 8000
		}
		steps = []step{{Op: "trigger", Trigger: trigger, ID: "rm", Args: args{Tool: "fake-command", Command: "rm -rf build", Text: "清掉旧的构建，下一步重新编译。", TTLMS: ttl, DurationMS: 900, Output: "removed build/"}}, {Op: "await", Ref: "rm", Then: end}}
	case "approve-edit":
		steps = []step{{Op: "trigger", Trigger: "P01", ID: "edit", Args: args{Tool: "fake-edit", Path: "notes/纸灯.md", Diff: "@@ -3,2 +3,3 @@\n 窗边的纸灯亮起来了。\n-昨天说到一半。\n+昨天说到窗边那段。\n+今天接着说。\n", Text: "把昨天的笔记补完整。", TTLMS: 270000, DurationMS: 600}}, {Op: "await", Ref: "edit", Then: end}}
	}
	steps = append(steps, step{Op: "trigger", Trigger: "M06", ID: "reply", Args: args{Text: "好了，我们接着聊。", Chunk: "grapheme", IntervalMS: 50}},
		step{Op: "await", Ref: "reply", Then: map[string][]step{"complete": {}, "interrupted": {{Op: "endTrack"}}, "error": {{Op: "endTrack"}}}})
	return script{Version: 1, ID: "process-" + kind, Seed: 42, Policy: "current", Initial: "four-venues",
		Tracks: []track{{Name: "process-" + actor, Target: target{cv.Venue, cv.Account, cv.ID, actor, "$input.turnId"}, Steps: steps}}}
}

func (a *app) processRun(kind string) {
	p := &a.Scenario
	cv := a.E.conv(p.Target)
	if cv.Account != "local" {
		p.Error = "命令和审批只在本地场合演练。"
		return
	}
	s, err := parseScript(scriptJSON(processScene(kind, cv, p.Actor)), a.E)
	if err != nil {
		p.Error = err.Error()
		return
	}
	p.Source, p.Error = scriptJSON(s), ""
	a.E.start(s, "run.start", "")
}
