package main

import (
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/winshell"
)

// wbToolsState 是输入框底行药丸的状态。审批方式按会话切（api.ConversationApproval），当场生效、不动全局配置；
// 换了会话就重读一次。
type wbToolsState struct {
	modelOpen, approvalOpen bool
	gen                     uint64 // 读的是哪一代数据源；后台换了要重读，不然读到一半被丢掉就再也没有了
	modelsAsked             bool
	approvalConv            string // 药丸现在显示的是哪个会话的
	approvalBusy            bool
	approval                string // 这个会话现在生效的档
	approvalDefault         string // 全局默认档
	approvalOverride        bool   // 这个会话自己切过
	approvalEditable        bool   // 只有本地私聊能切
	note                    string // 写入失败的原因，放在药丸的悬停提示里
	loadErr                 string // 读审批方式失败的原因
}

const approvalKey = "tools.approvalMode"

// approvalModes 和设置页的三个档位同名。
var approvalModes = []struct{ ID, Label string }{
	{"manual", "手动批准"}, {"auto", "自动放行"}, {"yolo", "免审批"},
}

// wbComposerTools 画本地输入框底行的药丸：私聊有模型和审批方式，本地群聊只有审批方式（几个角色各用各的模型）。
// 后台不提供对应接口就不画那一颗，不摆假按钮。
func (a *app) wbComposerTools(c *ui.Context, k api.ConversationKey) func() {
	kit := a.workbench.shell.kit
	if kit == nil {
		return nil
	}
	if st := &a.workbench.shell.tools; st.gen != a.srcGen {
		*st = wbToolsState{gen: a.srcGen}
	}
	char := ""
	if k.Scene == "private" {
		char = charOfConv(k)
	}
	if char != "" {
		a.loadModelsOnce()
	}
	a.loadApproval(k)
	return func() {
		if char != "" {
			a.wbModelPill(c, kit, char)
			a.wbCwdPill(c, kit, k)
		}
		a.wbApprovalPill(c, kit, k)
	}
}

// loadModelsOnce 给药丸读一次模型设置。不能每帧调 enterModels：不在设置页时每帧都有 leave 把“进过”清掉，
// 那样每帧都发一次读取，还一直占着 busy，选了模型也写不进去。
func (a *app) loadModelsOnce() {
	st := &a.workbench.shell.tools
	if st.modelsAsked || a.models.loaded {
		return
	}
	st.modelsAsked = true
	a.enterModels()
}

// wbDemoTools 是演示模式的药丸：角色取会话的观察者，模型列表来自演示的模型设置。
func (a *app) wbDemoTools(c *ui.Context, char string) func() {
	kit := a.workbench.shell.kit
	if kit == nil {
		return nil
	}
	a.loadModelsOnce()
	return func() { a.wbModelPill(c, kit, char) }
}

// wbModelPill 显示这个角色配的说话模型，点开按供应商分组换。写入和设置页是同一个 modelJob，失败时值不变。
func (a *app) wbModelPill(c *ui.Context, kit *winshell.Kit, char string) {
	s, st := &a.models, &a.workbench.shell.tools
	current := ""
	for _, ch := range s.view.Characters {
		if ch.ID == char {
			current = ch.ConfiguredModel
		}
	}
	if current == "" {
		if ch, ok := a.charOf(char); ok {
			current = ch.Model
		}
	}
	if current == "" {
		return
	}
	target := modelTarget{field: config.CharacterModel, character: char}
	hint := "这个角色用的模型"
	if note := s.notes[target]; note != "" {
		hint = note
	}
	var items []winshell.MenuItem
	if s.loaded && s.view.ModelsEditable {
		items = s.modelMenuItems(current, func(id string) func() {
			return func() {
				if id != current {
					a.modelJob(target, &api.ModelSettingsChange{Field: target.field, Character: char, String: &id}, false)
				}
			}
		})
	}
	kit.PillMenu(c, "composer-model", s.name(current), hint, winshell.IconSpark, &st.modelOpen, items)
}

// wbApprovalPill 显示这个会话现在的审批方式，点开换：只改这个会话，当场生效；换过的多一项"跟随默认"。
// 本地群聊没有自己的档，只显示默认。
func (a *app) wbApprovalPill(c *ui.Context, kit *winshell.Kit, k api.ConversationKey) {
	st := &a.workbench.shell.tools
	reg, ok := a.reader.(api.ConversationApproval)
	if !ok {
		return
	}
	if st.loadErr != "" { // 读失败不能悄悄消失：显示出来，悬停看原因
		kit.PillMenu(c, "composer-approval", "审批方式读不到", st.loadErr, winshell.IconEye, &st.approvalOpen, nil)
		return
	}
	if st.approval == "" || st.approvalConv != k.Conversation {
		return
	}
	label := approvalLabel(st.approval)
	hint := "这个对话的审批方式"
	if !st.approvalEditable {
		hint = "审批方式（本地群聊不自动放行）"
	} else if !st.approvalOverride {
		hint = "这个对话的审批方式（跟随默认）"
	}
	if st.note != "" {
		hint = st.note
	}
	var items []winshell.MenuItem
	if st.approvalEditable {
		for _, m := range approvalModes {
			id := m.ID
			items = append(items, winshell.MenuItem{Label: m.Label, Checked: id == st.approval, Action: func() {
				if id != st.approval && !st.approvalBusy {
					a.setApproval(reg, k, id)
				}
			}})
		}
		if st.approvalOverride {
			items = append(items, winshell.MenuItem{Label: "跟随默认：" + approvalLabel(st.approvalDefault), Action: func() {
				if !st.approvalBusy {
					a.setApproval(reg, k, "")
				}
			}})
		}
	}
	kit.PillMenu(c, "composer-approval", label, hint, winshell.IconEye, &st.approvalOpen, items)
}

func approvalLabel(id string) string {
	for _, m := range approvalModes {
		if m.ID == id {
			return m.Label
		}
	}
	return id
}

func (a *app) loadApproval(k api.ConversationKey) {
	st := &a.workbench.shell.tools
	reg, ok := a.reader.(api.ConversationApproval)
	if !ok || st.approvalBusy || (st.approvalConv == k.Conversation && st.loadErr == "") {
		return
	}
	st.approvalBusy = true
	st.approvalConv = k.Conversation
	ctx, generation := a.base(), a.srcGen
	go func() {
		v, err := reg.ConversationApprovalMode(ctx, k)
		a.post(func() {
			if a.srcGen != generation {
				return
			}
			st := &a.workbench.shell.tools
			st.approvalBusy = false
			if err != nil {
				st.loadErr = err.Error()
				return
			}
			st.loadErr = ""
			st.applyMode(v, k)
		})
	}()
}

func (a *app) setApproval(reg api.ConversationApproval, k api.ConversationKey, mode string) {
	st := &a.workbench.shell.tools
	st.approvalBusy, st.note = true, ""
	ctx, generation := a.base(), a.srcGen
	go func() {
		err := reg.SetConversationApprovalMode(ctx, k, mode)
		var v api.ConversationApprovalMode
		if err == nil {
			v, err = reg.ConversationApprovalMode(ctx, k)
		}
		a.post(func() {
			if a.srcGen != generation {
				return
			}
			st := &a.workbench.shell.tools
			st.approvalBusy = false
			if err != nil {
				st.note = "没有改成：" + err.Error() // 值不变，不回成功的话
				st.approvalConv = ""            // 下一帧重读
				return
			}
			st.applyMode(v, k)
		})
	}()
}

func (st *wbToolsState) applyMode(v api.ConversationApprovalMode, k api.ConversationKey) {
	st.approval, st.approvalDefault, st.approvalOverride = v.Mode, v.Default, v.Override
	st.approvalEditable = k.Scene == "private"
	st.approvalConv = k.Conversation
}
