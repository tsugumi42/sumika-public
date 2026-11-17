package ctlserver

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"sumika-native/internal/ctlproto"
)

// SlashRequest 是交给后端的一次 /goal 或 /loop 命令；到后端手里字段都是本层校验过的。
// Text 是拼好的斜杠命令全文（和主窗口输入框里发的一样），后端用同一套解析和处理函数执行；
// Sub 是命令行里的子命令名，后端用它核对"拼出来的全文被解析成了同一件事"（比如目标文字恰好是 pause 时不能变成暂停）。
// 没有会话 id：会话由后端按角色拼，只会是本地私聊的自检话题。
type SlashRequest struct {
	Command   string // goal / loop
	Sub       string
	Character string
	Text      string
	ID        int // loop edit|pause|resume|remove 的编号
	// Loop 非空是 loop add|edit 带了日程、聊天方式或通知档的选项：此时 Text 只是任务文字（add 必有，edit 可空），
	// 不再拼成斜杠命令，后端走和定期任务页同一个入口
	Loop *ctlproto.LoopPatch
}

// SlashBackend 是可选扩展，沿用 ChatBackend 的做法：没接可写桌面的宿主不实现它，回 readonly
type SlashBackend interface {
	Slash(ctx context.Context, req SlashRequest) (ctlproto.SlashResult, error)
}

var (
	loopID      = regexp.MustCompile(`^#?([0-9]{1,9})$`)
	loopEvery   = regexp.MustCompile(`^([0-9]{1,6})([mhdMHD])$`)
	loopClock   = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	loopWeekly  = regexp.MustCompile(`^[1-7](,[1-7]){0,6}$`)
	loopNotifys = []string{"content", "errors", "none"}
)

// loopPatch 校验 loop add|edit 的选项并换成后端用的形状。只查写法；间隔的上下限、时间合不合理由定期任务本身再查一遍
func loopPatch(a *ctlproto.LoopArgs) (*ctlproto.LoopPatch, string) {
	p := &ctlproto.LoopPatch{NewChat: a.NewChat, Notify: a.Notify}
	if a.Notify != "" && !slices.Contains(loopNotifys, a.Notify) {
		return nil, "--notify 只能是 " + strings.Join(loopNotifys, "、")
	}
	n := 0
	for _, on := range []bool{a.Every != "", a.Daily != "", a.Weekly != "", a.Self} {
		if on {
			n++
		}
	}
	if n > 1 {
		return nil, "--every、--daily、--weekly、--self 只能选一个"
	}
	if a.At != "" && a.Weekly == "" {
		return nil, "--at 只和 --weekly 一起用（每天的写 --daily 09:00）"
	}
	switch {
	case a.Every != "":
		m := loopEvery.FindStringSubmatch(a.Every)
		if m == nil {
			return nil, "--every 要写成整数加单位 m、h、d，例如 30m、2h、1d"
		}
		v, _ := strconv.ParseInt(m[1], 10, 64)
		p.Kind, p.IntervalSec = "interval", v*map[string]int64{"m": 60, "h": 3600, "d": 86400}[strings.ToLower(m[2])]
	case a.Daily != "":
		if !loopClock.MatchString(a.Daily) {
			return nil, "--daily 要写成 24 小时制的 时:分，例如 09:30"
		}
		p.Kind, p.At = "daily", a.Daily
	case a.Weekly != "":
		if !loopWeekly.MatchString(a.Weekly) || !loopClock.MatchString(a.At) {
			return nil, "--weekly 要写星期几（周一是 1、周日是 7，例如 1,3,5），并用 --at 09:30 写几点"
		}
		for _, d := range strings.Split(a.Weekly, ",") {
			v, _ := strconv.Atoi(d)
			p.Days = append(p.Days, v%7)
		}
		p.Kind, p.At = "weekly", a.At
	case a.Self:
		p.Kind = "self"
	}
	return p, ""
}

// slash 校验 goal、loop 命令。和 chat 一样，这里是"谁能以主人身份发命令"的关口：
// 子命令必须在表里，文字合格，目标文字不能以 / 开头（不让命令行借目标文字再发别的斜杠命令）。
func (s *Server) slash(req ctlproto.Request) (ctlproto.Reply, func()) {
	if req.Loop != nil && (req.Cmd != ctlproto.CmdLoop || len(req.Args) == 0 || (req.Args[0] != "add" && req.Args[0] != "edit")) {
		return invalid("--every、--daily、--weekly、--at、--self、--new-chat、--same-chat、--notify 只用于 loop add 和 loop edit")
	}
	if req.Wait != "" || req.Text != "" || req.New || req.N != 0 || req.Cursor != nil {
		return invalid("%s 只接子命令和文字（可加 --character），不接别的选项", req.Cmd)
	}
	if req.Character != "" && !ctlproto.ValidCharacter(req.Character) {
		return invalid("角色 id 不能带空白、冒号、斜杠")
	}
	if req.Cmd == ctlproto.CmdTrust { // 不带子命令也不带文字：针对该角色自检话题现在的工作目录
		if len(req.Args) != 0 {
			return invalid("trust 不带参数（可加 --character）")
		}
		if r, ok := s.demo(req.Cmd); ok {
			return r, nil
		}
		b, ok := s.b.(SlashBackend)
		if !ok {
			return fail(ctlproto.CodeReadOnly, "项目说明信任的接口不可用"), nil
		}
		d, err := b.Slash(s.ctx, SlashRequest{Command: req.Cmd, Character: req.Character, Text: "/trust"})
		return s.result(cleanSlash(d), err), nil
	}
	subs := ctlproto.GoalSubs
	if req.Cmd == ctlproto.CmdLoop {
		subs = ctlproto.LoopSubs
	}
	args := req.Args
	if len(args) == 0 { // goal 不带子命令 = show，loop 不带 = list
		args = []string{subs[0]}
	}
	sub, rest := args[0], args[1:]
	if !slices.Contains(subs, sub) {
		return invalid("%s 的子命令只能是 %s", req.Cmd, strings.Join(subs, "、"))
	}
	out := SlashRequest{Command: req.Cmd, Sub: sub, Character: req.Character}
	text := strings.TrimSpace(strings.Join(rest, " "))
	if text != "" && !ctlproto.ValidChatText(text) {
		return invalid("文字最多 %d 字，也不能带控制字符", ctlproto.MaxChatText)
	}
	need := func(what string) bool { return text != "" || (what == "") }
	switch req.Cmd + " " + sub {
	case "goal show", "goal pause", "goal resume", "goal clear", "loop list":
		if len(rest) != 0 {
			return invalid("%s %s 不带参数", req.Cmd, sub)
		}
		out.Text = "/" + req.Cmd
		if sub != "show" && sub != "list" {
			out.Text += " " + sub
		}
	case "goal set", "goal edit":
		if !need("目标") {
			return invalid("%s %s 后面要写目标", req.Cmd, sub)
		}
		if strings.HasPrefix(text, "/") {
			return invalid("目标不能以 / 开头")
		}
		out.Text = "/goal " + text
		if sub == "edit" {
			out.Text = "/goal edit " + text
		}
	case "loop add":
		if !need("任务") {
			return invalid("loop add [间隔] <任务>，例如 loop add 30m 报一下现在几点")
		}
		out.Text = "/loop " + text
		if req.Loop != nil {
			p, why := loopPatch(req.Loop)
			if why != "" {
				return invalid("%s", why)
			}
			out.Loop, out.Text = p, text
		}
	case "loop edit", "loop pause", "loop resume", "loop remove":
		if len(rest) == 0 {
			return invalid("loop %s 要先写任务编号（loop list 能看到）", sub)
		}
		m := loopID.FindStringSubmatch(rest[0])
		if m == nil {
			return invalid("任务编号要写成 3 或 #3：%q", truncate(rest[0], 20))
		}
		out.ID, _ = strconv.Atoi(m[1])
		if out.ID < 1 {
			return invalid("任务编号从 1 开始")
		}
		tail := strings.TrimSpace(strings.Join(rest[1:], " "))
		verb := sub
		if sub == "remove" {
			verb = "stop"
		}
		if sub == "edit" && req.Loop != nil {
			p, why := loopPatch(req.Loop)
			if why != "" {
				return invalid("%s", why)
			}
			if tail != "" && !ctlproto.ValidChatText(tail) {
				return invalid("文字最多 %d 字，也不能带控制字符", ctlproto.MaxChatText)
			}
			if tail == "" && p.Kind == "" && p.NewChat == nil && p.Notify == "" {
				return invalid("loop edit 没有要改的：写新任务文字，或 --every、--daily、--weekly、--self、--new-chat、--same-chat、--notify")
			}
			out.Loop, out.Text = p, tail
		} else if sub == "edit" {
			if tail == "" {
				return invalid("loop edit <编号> [间隔] [新任务文字]：间隔和任务文字至少改一样")
			}
			out.Text = "/loop edit " + strconv.Itoa(out.ID) + " " + tail
		} else {
			if tail != "" {
				return invalid("loop %s 只要编号", sub)
			}
			out.Text = "/loop " + verb + " " + strconv.Itoa(out.ID)
		}
	}
	if r, ok := s.demo(req.Cmd); ok {
		return r, nil
	}
	b, ok := s.b.(SlashBackend)
	if !ok {
		return fail(ctlproto.CodeReadOnly, "目标和定期任务的接口不可用"), nil
	}
	d, err := b.Slash(s.ctx, out)
	return s.result(cleanSlash(d), err), nil
}

// cleanSlash 外面来的文字（目标、任务、回复里的原文）出去前过一遍
func cleanSlash(r ctlproto.SlashResult) ctlproto.SlashResult {
	r.Character, r.Topic, r.Conversation = ctlproto.Clean(r.Character), ctlproto.Clean(r.Topic), ctlproto.Clean(r.Conversation)
	r.Reply = ctlproto.CleanText(r.Reply)
	if g := r.Goal; g != nil {
		v := *g
		v.Text, v.PauseReason, v.LastJudge, v.Line = ctlproto.CleanText(v.Text), ctlproto.Clean(v.PauseReason), ctlproto.Clean(v.LastJudge), ctlproto.Clean(v.Line)
		r.Goal = &v
	}
	loops := make([]ctlproto.LoopTask, len(r.Loops)) // 空也给 [] 不给 null
	for i, t := range r.Loops {
		t.Prompt, t.Line = ctlproto.CleanText(t.Prompt), ctlproto.Clean(t.Line)
		t.Kind, t.At, t.Notify = ctlproto.Clean(t.Kind), ctlproto.Clean(t.At), ctlproto.Clean(t.Notify)
		loops[i] = t
	}
	r.Loops = loops
	return r
}
