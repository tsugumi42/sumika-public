package main

// 设置 → 定期任务 的新建、编辑表单（规格-定期任务页）：顶上“新建定期任务”和每行“编辑”共用一个框，
// 字段是任务说明、频率（让她自己定 / 固定间隔 / 每天 / 每周几）、聊天方式（同一个聊天 / 每次新聊天）、通知档；
// 写得不对的在框里写原因、不关框。动作走 api.LoopControl 的 AddLoop、EditLoop，和输入框 /loop 同一个调度器。
// 控件都取自 winshell.Kit，不另拼。

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/winshell"
)

// loopEdit 是新建、编辑共用的表单：id 为 0 是新建。频率、聊天方式、通知档都存成选项的下标。
type loopEdit struct {
	open    bool
	id      int
	key     api.ConversationKey // 改：任务所在的会话；新建：由 charSel 选
	charSel int
	text    string
	freq    int     // loopFreqKinds 的下标
	every   string  // 固定间隔的数字
	unit    int     // loopUnitNames 的下标
	at      string  // 每天、每周的时间，如 09:00
	days    [7]bool // 下标对应 loopDayNames（周一在前）
	newChat int     // 0 同一个聊天，1 每次新聊天
	notify  int     // loopNotifyKeys 的下标
	err     string
	busy    bool
}

var (
	loopFreqNames   = []string{"让她自己定", "固定间隔", "每天", "每周"}
	loopFreqKinds   = []string{"self", "interval", "daily", "weekly"}
	loopUnitNames   = []string{"分钟", "小时", "天"}
	loopUnitSeconds = []int64{60, 3600, 86400}
	loopDayNames    = []string{"一", "二", "三", "四", "五", "六", "日"}
	loopChatNames   = []string{"同一个聊天", "每次新聊天"}
	loopNotifyNames = []string{"有内容就提醒", "只提醒出错", "不提醒"}
	loopNotifyKeys  = []string{"content", "errors", "none"}
)

// loopDayAPI 把表单里的星期下标（周一是 0）换成接口的写法（周日是 0）。
func loopDayAPI(i int) int { return (i + 1) % 7 }

func loopBool(b bool) int {
	if b {
		return 1
	}
	return 0
}

func loopNotifyIndex(n string) int {
	if i := slices.Index(loopNotifyKeys, n); i >= 0 {
		return i
	}
	return 0
}

// loopNew 打开空白表单：默认让她自己定、同一个聊天、有内容就提醒，角色选第一个。
func (a *app) loopNew() {
	a.loopsUI.ed = loopEdit{open: true, every: "30", at: "09:00"}
}

// loopEditOpen 把这个任务的现值填进表单。
func (a *app) loopEditOpen(r loopRow) {
	t := r.info
	ed := loopEdit{open: true, id: t.ID, key: r.key, text: t.Prompt, every: "30", at: "09:00", newChat: loopBool(t.NewChat), notify: loopNotifyIndex(t.Notify)}
	ed.freq = max(slices.Index(loopFreqKinds, t.Kind), 0)
	if t.IntervalSec > 0 {
		ed.freq = 1
		for i := len(loopUnitSeconds) - 1; i > 0; i-- {
			if t.IntervalSec%loopUnitSeconds[i] == 0 {
				ed.unit = i
				break
			}
		}
		ed.every = fmt.Sprint(t.IntervalSec / loopUnitSeconds[ed.unit])
	}
	if t.At != "" {
		ed.at = t.At
	}
	for _, d := range t.Days {
		ed.days[(d+6)%7] = true
	}
	a.loopsUI.ed = ed
}

func (a *app) wbLoopEditor(c *ui.Context, kit *winshell.Kit) {
	ed := &a.loopsUI.ed
	title := "新建定期任务"
	if ed.id != 0 {
		title = "编辑定期任务"
	}
	kit.ActionDialog(c, "loop-edit", &ed.open, winshell.DialogSpec{
		Title: title,
		Actions: []winshell.DialogAction{
			{ID: "save", Label: "保存", Default: true, KeepOpen: true, Disabled: ed.busy},
			{ID: "cancel", Label: "取消"},
		},
		OnAction: func(id string) {
			if id == "save" {
				a.loopSave()
			}
		},
	}, func() {
		g := kit.G
		ui.Column(c.Key("loop-form")).FillWidth().Gap(g.SpaceSmall).Children(func() {
			if ed.id == 0 && len(a.chars) > 1 {
				names := make([]string, len(a.chars))
				for i, ch := range a.chars {
					names[i] = ch.Name
				}
				kit.Chips(c, "loop-char", names, &ed.charSel)
			} else if ed.id != 0 {
				kit.Note(c, "角色："+a.loopOwner(ed.key), false)
			}
			kit.Note(c, "她每次醒来要做什么", false)
			kit.TextArea(c, "loop-text", &ed.text, "任务说明：她每次醒来要做什么", 2, 5)
			kit.Chips(c, "loop-freq", loopFreqNames, &ed.freq)
			switch loopFreqKinds[ed.freq] {
			case "interval":
				ui.Row(c.Key("loop-every-row")).FillWidth().Gap(g.SpaceSmall).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "每").FontSize(g.LabelSize).FixedLineHeight(g.LabelLine)
					ui.Box(c.Key("loop-every-box")).Width(g.HitSmall * 3).Shrink(0).Children(func() {
						kit.NumberField(c, "loop-every", &ed.every, "多久一次", 9999, false, nil)
					})
					kit.Chips(c, "loop-unit", loopUnitNames, &ed.unit)
				})
			case "daily":
				kit.TextField(c, "loop-at", &ed.at, "几点（24 小时制，例如 09:00）")
				kit.Note(c, "第一次在下一个这个时间，不会马上跑。", false)
			case "weekly":
				ui.Row(c.Key("loop-days")).Wrap().Gap(g.SpaceSmall).Children(func() {
					for i, n := range loopDayNames {
						kit.Button(c, "loop-day-"+fmt.Sprint(i), "周"+n, ed.days[i], false, func() { ed.days[i] = !ed.days[i] }) // 选中的是主按钮样子
					}
				})
				kit.TextField(c, "loop-at", &ed.at, "几点（24 小时制，例如 09:00）")
				kit.Note(c, "第一次在下一个选中的日子和这个时间，不会马上跑。", false)
			default:
				kit.Note(c, "每次醒来后，她自己决定下次什么时候再醒。", false)
			}
			kit.Chips(c, "loop-chat", loopChatNames, &ed.newChat)
			kit.Chips(c, "loop-notify", loopNotifyNames, &ed.notify)
			if ed.err != "" {
				kit.Note(c, ed.err, true)
			}
		})
	})
}

// spec 把表单读成接口的写法；说明为空、间隔不是数字、每周没选天这几样表单自己拦，时间的写法交给后台校验。
func (ed *loopEdit) spec() (api.LoopSpec, error) {
	sp := api.LoopSpec{Prompt: strings.TrimSpace(ed.text), Kind: loopFreqKinds[ed.freq], NewChat: ed.newChat == 1, Notify: loopNotifyKeys[ed.notify]}
	if sp.Prompt == "" {
		return sp, errors.New("写一下她每次要做什么")
	}
	switch sp.Kind {
	case "interval":
		n, err := strconv.Atoi(strings.TrimSpace(ed.every))
		if err != nil || n <= 0 {
			return sp, errors.New("写一下多久一次，要是一个大于 0 的整数")
		}
		sp.IntervalSec = int64(n) * loopUnitSeconds[ed.unit]
	case "daily":
		sp.At = strings.TrimSpace(ed.at)
	case "weekly":
		sp.At = strings.TrimSpace(ed.at)
		for i, on := range ed.days {
			if on {
				sp.Days = append(sp.Days, loopDayAPI(i))
			}
		}
		if len(sp.Days) == 0 {
			return sp, errors.New("每周要至少选一天")
		}
	}
	return sp, nil
}

// loopSave 校验后在后台线程里调 LoopControl；成功才关框，出错写在框里。
func (a *app) loopSave() {
	ed := &a.loopsUI.ed
	ed.err = ""
	ed.open = true // 保存按钮不自动关：成功了才关
	sp, err := ed.spec()
	if err != nil {
		ed.err = err.Error()
		return
	}
	ctl, ok := a.reader.(api.LoopControl)
	if !ok {
		ed.err = "后台没有开着，暂时不能改定期任务"
		return
	}
	key, id := ed.key, ed.id
	if id == 0 {
		if len(a.chars) == 0 {
			ed.err = "没有角色，不能建定期任务"
			return
		}
		key = remindLocalKey(a.chars[min(ed.charSel, len(a.chars)-1)].ID)
	}
	ed.busy = true
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		var err error
		if id == 0 {
			_, err = ctl.AddLoop(ctx, key, sp)
		} else {
			err = ctl.EditLoop(ctx, key, id, sp)
		}
		a.post(func() {
			ed.busy = false
			if err != nil {
				ed.err = err.Error()
				return
			}
			ed.open = false
		})
	}()
}
