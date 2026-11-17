package loop

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

// CmdKind 是 /loop 命令的种类
type CmdKind int

const (
	CmdAdd  CmdKind = iota // /loop [间隔] <任务>
	CmdList                // /loop
	CmdStop
	CmdPause
	CmdResume
	CmdEdit // /loop edit <编号> [间隔] [新任务文字]
)

// Command 是解析出来的命令
type Command struct {
	Kind     CmdKind
	Interval time.Duration // CmdAdd：0 表示由她自己定节奏；CmdEdit：0 表示不改间隔
	Prompt   string        // CmdAdd；CmdEdit：空表示不改任务文字
	ID       int           // CmdStop / CmdPause / CmdResume：0 表示这个会话里所有的
}

// ParseCommand 是"文本进、命令出"：text 是主人在输入框里发的整条消息。
//   - 不是 /loop 开头（含 /looper 这类）：ok 为 false，宿主照原文处理；
//   - 是 /loop 但写法不对：ok 为 true、err 是能直接显示给主人看的说明。
//
// 写法：/loop 30m <任务>（单位 m、h、d，最短 1 分钟）、/loop <任务>、/loop、
// /loop stop|pause|resume [编号]。"stop 后面只有编号或没有东西"才算子命令，
// 否则整句当任务（所以 /loop stop the server 是一个任务）。
func ParseCommand(text string) (cmd Command, ok bool, err error) {
	t := strings.TrimSpace(text)
	if len(t) < 5 || !strings.EqualFold(t[:5], "/loop") {
		return Command{}, false, nil
	}
	rest := t[5:]
	if rest != "" && !unicode.IsSpace([]rune(rest)[0]) {
		return Command{}, false, nil
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return Command{Kind: CmdList}, true, nil
	}
	first, after := splitFirst(rest)

	if strings.EqualFold(first, "edit") {
		cmd, err := parseEdit(after)
		return cmd, true, err
	}
	if kind, isSub := map[string]CmdKind{"stop": CmdStop, "pause": CmdPause, "resume": CmdResume}[strings.ToLower(first)]; isSub {
		if ids, allIDs := parseIDs(after); allIDs {
			switch len(ids) {
			case 0:
				return Command{Kind: kind}, true, nil
			case 1:
				return Command{Kind: kind, ID: ids[0]}, true, nil
			}
			return Command{}, true, errors.New("一次只能指定一个编号，例如 /loop " + strings.ToLower(first) + " 3")
		}
	}

	if looksLikeInterval(first) {
		d, perr := parseInterval(first)
		if perr != nil {
			return Command{}, true, perr
		}
		if after == "" {
			return Command{}, true, errors.New("缺少任务内容，例如 /loop " + first + " 报一下现在几点")
		}
		return Command{Kind: CmdAdd, Interval: d, Prompt: after}, true, nil
	}
	return Command{Kind: CmdAdd, Prompt: rest}, true, nil
}

// parseEdit 解析 edit 后面的 "<编号> [间隔] [新任务文字]"：间隔和文字至少改一样
func parseEdit(after string) (Command, error) {
	usage := errors.New("用法：/loop edit <编号> [间隔] [新任务文字]，间隔和任务文字至少改一样，例如 /loop edit 3 2h 或 /loop edit 3 改成新任务")
	idWord, rest := splitFirst(after)
	ids, ok := parseIDs(idWord)
	if idWord == "" || !ok || len(ids) != 1 {
		return Command{}, usage
	}
	cmd := Command{Kind: CmdEdit, ID: ids[0]}
	if first, tail := splitFirst(rest); looksLikeInterval(first) {
		d, err := parseInterval(first)
		if err != nil {
			return Command{}, err
		}
		cmd.Interval, rest = d, tail
	}
	cmd.Prompt = strings.TrimSpace(rest)
	if cmd.Interval == 0 && cmd.Prompt == "" {
		return Command{}, usage
	}
	return cmd, nil
}

func splitFirst(s string) (first, after string) {
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// parseIDs 把 "3" 或 "#3" 这样的词列成编号；任何一个词不是编号就 allIDs=false
func parseIDs(s string) (ids []int, allIDs bool) {
	for _, w := range strings.Fields(s) {
		n, err := strconv.Atoi(strings.TrimPrefix(w, "#"))
		if err != nil || n < 1 {
			return nil, false
		}
		ids = append(ids, n)
	}
	return ids, true
}

// looksLikeInterval：数字（可带小数点）加字母，像是想写间隔。写错了要报错，不能悄悄当成任务正文
func looksLikeInterval(w string) bool {
	i := 0
	for i < len(w) && (w[i] >= '0' && w[i] <= '9' || w[i] == '.') {
		i++
	}
	if i == 0 || i == len(w) {
		return false
	}
	for _, r := range w[i:] {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func parseInterval(w string) (time.Duration, error) {
	bad := fmt.Errorf("间隔「%s」写得不对：用整数加单位 m、h、d，例如 30m、2h、1d", w)
	var base time.Duration
	switch w[len(w)-1] {
	case 'm', 'M':
		base = time.Minute
	case 'h', 'H':
		base = time.Hour
	case 'd', 'D':
		base = 24 * time.Hour
	default:
		return 0, bad
	}
	n, err := strconv.Atoi(w[:len(w)-1])
	if err != nil || n < 0 {
		return 0, bad
	}
	if n == 0 || time.Duration(n) > MaxInterval/base {
		return 0, fmt.Errorf("间隔要在 %s 到 %s 之间", DurationText(MinInterval), DurationText(MaxInterval))
	}
	return time.Duration(n) * base, nil
}

// Usage 是 /loop 的用法，列表和空列表的回复末尾都带上
const Usage = "用法：/loop 30m <任务> 新建；/loop <任务> 让她自己定节奏；/loop edit <编号> [间隔] [新任务文字] 修改；/loop pause|resume|stop [编号]；/loop 看列表"

// Apply 执行命令并返回一段给主人看的回复文字。session 是发命令的会话；
// 返回的 error 文字也可以直接显示
func (s *Scheduler) Apply(session string, c Command) (string, error) {
	now := s.opt.Now()
	switch c.Kind {
	case CmdAdd:
		t, err := s.Add(session, c.Prompt, AddOptions{Interval: c.Interval})
		if err != nil {
			return "", err
		}
		msg := "已建立 " + t.Line(now)
		if t.SelfPaced() {
			msg += "。每轮结束时由她自己决定下次什么时候醒，不决定就 1 小时后再醒"
		}
		return msg + "。第一轮马上开始", nil
	case CmdList:
		ts := s.List(session)
		if len(ts) == 0 {
			return "这个会话还没有定期任务。" + Usage, nil
		}
		lines := make([]string, len(ts))
		for i, t := range ts {
			lines[i] = t.Line(now)
		}
		return strings.Join(lines, "\n") + "\n" + Usage, nil
	}
	if c.Kind == CmdEdit {
		t, err := s.Edit(session, c.ID, EditOptions{Interval: c.Interval, Prompt: c.Prompt})
		if err != nil {
			return "", err
		}
		return "已修改 " + t.Line(now), nil
	}
	var (
		n    int
		err  error
		verb string
	)
	switch c.Kind {
	case CmdStop:
		n, err = s.Stop(session, c.ID)
		verb = "停止"
	case CmdPause:
		n, err = s.Pause(session, c.ID)
		verb = "暂停"
	case CmdResume:
		n, err = s.Resume(session, c.ID)
		verb = "恢复"
	default:
		return "", errors.New("不认识的命令")
	}
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", fmt.Errorf("没有可以%s的定期任务", verb)
	}
	return fmt.Sprintf("已%s %d 个定期任务", verb, n), nil
}

// ScheduleNextDecl 是 schedule_next 工具给模型看的声明
var ScheduleNextDecl = tools.Declaration{
	Name:        "schedule_next",
	Description: "你正在一个没有固定间隔的定期任务里。这一轮做完后，用它决定下次过多久叫醒你：事情还在变化、需要盯得紧就设短一点，暂时没动静就设长一点。seconds 是秒数（60 到 86400），reason 用一句话说明为什么这么定。不调用就按 1 小时后再醒。只在定期任务的这一轮里有效，别处调用会报错。",
	Parameters:  `{"type":"object","required":["seconds","reason"],"properties":{"seconds":{"type":"integer","minimum":60,"maximum":86400,"description":"过多少秒再叫醒你，60 到 86400"},"reason":{"type":"string","description":"一句话：为什么选这个时间"}},"additionalProperties":false}`,
}

// ExecScheduleNext 执行 schedule_next：session 是调用工具的会话。返回给模型的文字或出错说明
func (s *Scheduler) ExecScheduleNext(session string, args jsjson.Value) (string, error) {
	if args.Kind != jsjson.Object {
		return "", errors.New("参数要是一个对象")
	}
	v := args.Obj["seconds"]
	if v.Kind != jsjson.Number || v.Num != float64(int64(v.Num)) {
		return "", fmt.Errorf("seconds 要是 %d 到 %d 之间的整数", MinNextSeconds, MaxNextSeconds)
	}
	if v.Num < MinNextSeconds || v.Num > MaxNextSeconds {
		return "", fmt.Errorf("seconds 要在 %d 到 %d 之间，收到 %v", MinNextSeconds, MaxNextSeconds, v.Num)
	}
	reason := ""
	if r := args.Obj["reason"]; r.Kind == jsjson.String {
		reason = r.Str
	}
	d := time.Duration(int64(v.Num)) * time.Second
	if err := s.ScheduleNext(session, int(v.Num), reason); err != nil {
		return "", err
	}
	return fmt.Sprintf("好，这一轮结束后过 %s 再叫醒你（约 %s）。", DurationText(d), s.opt.Now().Add(d).Format("15:04")), nil
}

// ParseInterval 把 30m、2h、1d 这样的写法读成间隔（sumika-ctl 的 --every 用），写错返回能直接显示的说明
func ParseInterval(w string) (time.Duration, error) {
	if !looksLikeInterval(w) {
		return 0, fmt.Errorf("间隔「%s」写得不对：用整数加单位 m、h、d，例如 30m、2h、1d", w)
	}
	return parseInterval(w)
}
