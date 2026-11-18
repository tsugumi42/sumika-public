package remind

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

// 工具名（规格第四节）：一件事一个名字。声明在这里，谁能看见、谁能调由宿主定（app/remindwire.go）
const (
	ToolCreate = "reminder_create"
	ToolList   = "reminder_list"
	ToolUpdate = "reminder_update"
	ToolFinish = "reminder_finish"
	ToolDelete = "reminder_delete"
)

// ToolNames 是这几个工具的名字，policy.Specs 登记和测试都用它
var ToolNames = []string{ToolCreate, ToolList, ToolUpdate, ToolFinish, ToolDelete}

const repeatSchema = `"repeat":{"type":"object","description":"周期，不写就是一次性。unit 是 minute、hour、day、week、month，every 是每几个单位（默认 1），weekdays 只给 week（1 周一到 7 周日）。工作日写 {\"unit\":\"week\",\"weekdays\":[1,2,3,4,5]}，不看调休。","properties":{"unit":{"type":"string"},"every":{"type":"integer"},"weekdays":{"type":"array","items":{"type":"integer"}}},"additionalProperties":false}`

const whenSchema = `"at":{"type":"string","description":"什么时候响，本机时区，写成 2026-11-10 09:00。算日子可以先用 calendar"},"in_minutes":{"type":"integer","description":"多少分钟以后响；“半小时后”这种用它，不用自己算。和 at 二选一"}`

const targetsSchema = `"targets":{"type":"array","items":{"type":"string"},"description":"发到哪：local 本地窗口、qq 所有者 QQ 私聊。不写用默认（一般是本地窗口）"}`

// Decls 是各工具给模型看的声明
var Decls = map[string]tools.Declaration{
	ToolCreate: {
		Name:        ToolCreate,
		Description: "建提醒。只用于所有者要你提醒的事，或者你和他约好的事；到点你会用自己的话跟他说一声。想主动找他聊天不要用这个。程序没开着时错过的，下次启动会补上。结果里有实际下次时间，跟他说的时候用它。",
		Parameters:  `{"type":"object","required":["text"],"properties":{"text":{"type":"string","description":"要提醒什么，最多 500 字"},` + whenSchema + `,` + repeatSchema + `,` + targetsSchema + `},"additionalProperties":false}`,
	},
	ToolList: {
		Name:        ToolList,
		Description: "看现有的提醒：编号、内容、下次时间、周期、谁建的、发到哪。",
		Parameters:  `{"type":"object","properties":{"include_ended":{"type":"boolean","description":"也列已经结束的，默认不列"}},"additionalProperties":false}`,
	},
	ToolUpdate: {
		Name:        ToolUpdate,
		Description: "改提醒的内容、时间、周期、发到哪，或者暂停、恢复。只写要改的。repeat 写 {\"unit\":\"none\"} 变回一次性。已经结束的不能改，要的话新建。",
		Parameters:  `{"type":"object","required":["id"],"properties":{"id":{"type":"integer","description":"提醒编号"},"text":{"type":"string"},` + whenSchema + `,` + repeatSchema + `,` + targetsSchema + `,"paused":{"type":"boolean","description":"true 暂停，false 恢复"}},"additionalProperties":false}`,
	},
	ToolFinish: {
		Name:        ToolFinish,
		Description: "让提醒不再响，记录留着。事情办了用 done，不需要了用 cancelled。",
		Parameters:  `{"type":"object","required":["id","outcome"],"properties":{"id":{"type":"integer","description":"提醒编号"},"outcome":{"type":"string","enum":["done","cancelled"]}},"additionalProperties":false}`,
	},
	ToolDelete: {
		Name:        ToolDelete,
		Description: "把提醒从表里删掉，记录也没了。只是不想让它响用 reminder_finish。",
		Parameters:  `{"type":"object","required":["id"],"properties":{"id":{"type":"integer","description":"提醒编号"}},"additionalProperties":false}`,
	},
}

func argObject(args jsjson.Value) (map[string]jsjson.Value, error) {
	switch args.Kind {
	case jsjson.Object:
		return args.Obj, nil
	case jsjson.Null:
		return nil, nil
	}
	return nil, errors.New("参数要写成对象。")
}

func optString(o map[string]jsjson.Value, key string) (string, bool, error) {
	v, ok := o[key]
	if !ok || v.Kind == jsjson.Null {
		return "", false, nil
	}
	if v.Kind != jsjson.String {
		return "", false, fmt.Errorf("%s 要写成文字。", key)
	}
	return v.Str, true, nil
}

func parseWhen(o map[string]jsjson.Value, now time.Time) (*time.Time, error) {
	at, hasAt, err := optString(o, "at")
	if err != nil {
		return nil, err
	}
	v, hasIn := o["in_minutes"]
	hasIn = hasIn && v.Kind != jsjson.Null
	if hasAt && strings.TrimSpace(at) == "" {
		hasAt = false
	}
	switch {
	case hasAt && hasIn:
		return nil, errors.New("at 和 in_minutes 只写一个。")
	case hasIn:
		if v.Kind != jsjson.Number {
			return nil, errors.New("in_minutes 要写成整数（多少分钟以后）。")
		}
		t, err := minutesToAt(v.Num, now)
		if err != nil {
			return nil, err
		}
		return &t, nil
	case hasAt:
		t, err := ParseTime(at, now)
		if err != nil {
			return nil, err
		}
		return &t, nil
	}
	return nil, nil
}

func parseRepeat(o map[string]jsjson.Value) (*Repeat, error) {
	v, ok := o["repeat"]
	if !ok || v.Kind == jsjson.Null {
		return nil, nil
	}
	if v.Kind != jsjson.Object {
		return nil, errors.New("repeat 要写成对象，比如 {\"unit\":\"day\"}。")
	}
	var r Repeat
	unit, _, err := optString(v.Obj, "unit")
	if err != nil {
		return nil, errors.New("repeat.unit 要写成文字。")
	}
	if unit == "" {
		return nil, errors.New("repeat 要写 unit：minute、hour、day、week、month。")
	}
	if unit != UnitNone {
		r.Unit = unit
	}
	if e, ok := v.Obj["every"]; ok && e.Kind != jsjson.Null {
		if e.Kind != jsjson.Number || e.Num != float64(int64(e.Num)) || e.Num < 1 {
			return nil, errors.New("repeat.every 要写成不小于 1 的整数。")
		}
		r.Every = int(e.Num)
	}
	if w, ok := v.Obj["weekdays"]; ok && w.Kind != jsjson.Null {
		if w.Kind != jsjson.Array {
			return nil, errors.New("repeat.weekdays 要写成数组，比如 [1,3,5]。")
		}
		for _, d := range w.Arr {
			if d.Kind != jsjson.Number || d.Num != float64(int64(d.Num)) {
				return nil, errors.New("repeat.weekdays 里只能写 1 到 7 的整数。")
			}
			r.Weekdays = append(r.Weekdays, int(d.Num))
		}
	}
	return &r, nil
}

func parseTargets(o map[string]jsjson.Value) (*[]string, error) {
	v, ok := o["targets"]
	if !ok || v.Kind == jsjson.Null {
		return nil, nil
	}
	if v.Kind != jsjson.Array {
		return nil, errors.New("targets 要写成数组，比如 [\"local\"]。")
	}
	out := []string{}
	for _, t := range v.Arr {
		if t.Kind != jsjson.String {
			return nil, errors.New("targets 里只能写 local、qq。")
		}
		out = append(out, t.Str)
	}
	return &out, nil
}

// ParseCreate 整理 reminder_create 的参数
func ParseCreate(args jsjson.Value, now time.Time) (Draft, error) {
	o, err := argObject(args)
	if err != nil {
		return Draft{}, err
	}
	text, _, err := optString(o, "text")
	if err != nil {
		return Draft{}, err
	}
	when, err := parseWhen(o, now)
	if err != nil {
		return Draft{}, err
	}
	if when == nil {
		return Draft{}, fmt.Errorf("要写什么时候响：at（比如 2026-11-10 09:00）或 in_minutes。现在是 %s。", TimeText(now, now))
	}
	d := Draft{Text: text, At: *when}
	rep, err := parseRepeat(o)
	if err != nil {
		return Draft{}, err
	}
	if rep != nil {
		d.Repeat = *rep
	}
	ts, err := parseTargets(o)
	if err != nil {
		return Draft{}, err
	}
	if ts != nil {
		d.Targets = *ts
	}
	return d, nil
}

// ParseID 取参数里的提醒编号
func ParseID(args jsjson.Value) (int, error) {
	o, err := argObject(args)
	if err != nil {
		return 0, err
	}
	v, ok := o["id"]
	if !ok || v.Kind != jsjson.Number || v.Num != float64(int64(v.Num)) || v.Num < 1 {
		return 0, errors.New("id 要写成提醒的编号（正整数）。不知道编号先用 reminder_list。")
	}
	return int(v.Num), nil
}

// ParseUpdate 整理 reminder_update 的参数
func ParseUpdate(args jsjson.Value, now time.Time) (int, Patch, error) {
	id, err := ParseID(args)
	if err != nil {
		return 0, Patch{}, err
	}
	o := args.Obj
	var p Patch
	if s, ok, err := optString(o, "text"); err != nil {
		return 0, Patch{}, err
	} else if ok {
		p.Text = &s
	}
	if p.At, err = parseWhen(o, now); err != nil {
		return 0, Patch{}, err
	}
	if p.Repeat, err = parseRepeat(o); err != nil {
		return 0, Patch{}, err
	}
	if p.Targets, err = parseTargets(o); err != nil {
		return 0, Patch{}, err
	}
	if v, ok := o["paused"]; ok && v.Kind != jsjson.Null {
		if v.Kind != jsjson.Bool {
			return 0, Patch{}, errors.New("paused 要写成 true 或 false。")
		}
		b := v.Bool
		p.Paused = &b
	}
	return id, p, nil
}

// ParseFinish 整理 reminder_finish 的参数：done 为真是完成，否则是取消
func ParseFinish(args jsjson.Value) (int, bool, error) {
	id, err := ParseID(args)
	if err != nil {
		return 0, false, err
	}
	s, _, err := optString(args.Obj, "outcome")
	if err != nil {
		return 0, false, err
	}
	switch s {
	case "done":
		return id, true, nil
	case "cancelled":
		return id, false, nil
	}
	return 0, false, errors.New("outcome 只能是 done（办完了）或 cancelled（不需要了）。")
}

// ParseIncludeEnded 取 reminder_list 的参数
func ParseIncludeEnded(args jsjson.Value) (bool, error) {
	o, err := argObject(args)
	if err != nil {
		return false, err
	}
	v, ok := o["include_ended"]
	if !ok || v.Kind == jsjson.Null {
		return false, nil
	}
	if v.Kind != jsjson.Bool {
		return false, errors.New("include_ended 要写成 true 或 false。")
	}
	return v.Bool, nil
}

// Described 是建好或改好以后给模型看的一句：实际下次时间、还有多久、发到哪
func Described(r Reminder, now time.Time, defaults []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "提醒 #%d：%s。", r.ID, clip(r.Text, 60))
	switch r.Status {
	case StatusActive:
		fmt.Fprintf(&b, "下次 %s（本机时区，%s后）", TimeText(r.NextAt, now), DurationText(r.NextAt.Sub(now)))
	case StatusPaused:
		b.WriteString("已暂停")
	default:
		b.WriteString(r.Status.Text())
	}
	if !r.Repeat.None() {
		b.WriteString("，" + r.Repeat.Text())
	}
	fmt.Fprintf(&b, "，发到%s。", TargetsText(r.TargetsOr(defaults)))
	return b.String()
}
