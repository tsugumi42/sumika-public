package main

import (
	"context"
	"strings"

	"github.com/ELY-Open-Source/CrychicUI/button"
	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/ELY-Open-Source/CrychicUI/input"
	"github.com/ELY-Open-Source/CrychicUI/overlay"
	"github.com/ELY-Open-Source/CrychicUI/typography"
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

type groupChoice struct {
	character character
	selected  bool
}
type groupCreate struct {
	open, busy bool
	name, err  string
	choices    []groupChoice
	cancel     context.CancelFunc
	generation uint64
}

func (a *app) canCreateGroup() bool {
	if _, ok := a.reader.(api.LocalGroupWriter); !ok {
		return false
	}
	for _, c := range a.caps {
		if c.Name == "local-group" {
			return c.State == api.Ready
		}
	}
	return false
}
func (a *app) beginGroup() {
	g := &a.gc
	g.generation++
	g.open, g.busy, g.name, g.err = true, false, "", ""
	g.choices = nil
	for _, c := range a.chars {
		if c.Err == "" {
			g.choices = append(g.choices, groupChoice{character: c})
		}
	}
}
func (a *app) createGroup() {
	g := &a.gc
	var members []string
	for _, c := range g.choices {
		if c.selected {
			members = append(members, c.character.ID)
		}
	}
	ctx, cancel := context.WithCancel(a.viewCtx)
	g.cancel, g.busy, g.err = cancel, true, ""
	generation, source := g.generation, a.srcGen
	writer := a.reader.(api.LocalGroupWriter)
	req := api.LocalGroupCreate{Name: g.name, Characters: members}
	go func() {
		value, err := writer.CreateLocalGroup(ctx, req)
		cancel()
		a.post(func() {
			if a.srcGen != source || a.gc.generation != generation {
				return
			}
			a.gc.busy, a.gc.cancel = false, nil
			if err != nil {
				a.gc.err = err.Error()
				return
			}
			a.gc.open = false
			a.keys[value.Key.Conversation] = value.Key
			exists := false
			for _, cv := range a.homes {
				exists = exists || cv.Key == value.Key
			}
			if !exists {
				a.homes = append(a.homes, convFromView(value))
				a.dialogRevision++
			}
			a.open(value.Key.Conversation)
		})
	}()
}
func (a *app) groupDialog(c *ui.Context) {
	g := &a.gc
	// 内容保留到退场结束；关闭只取消请求，不立即清空成员和名字。
	was := g.open
	overlay.Modal(c, "新建群聊", &g.open, overlay.ModalOptions{Close: "关闭建群"}, func() {
		input.TextField(c, "群聊名称", &g.name, input.TextFieldOptions{}).Label("群聊名称输入").Disabled(g.busy)
		typography.Caption(c, "选择 2 到 10 个角色，主人会自动加入。")
		ui.Scroll(c).MaxHeight(c.Theme().Space(60)).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Gap(c.Theme().Space(2)).Children(func() {
				for i := range g.choices {
					item := &g.choices[i]
					c.Key(item.character.ID)
					choice.Check(c, item.character.Name+" · "+item.character.ID, &item.selected).FillWidth().MinHeight(32).Disabled(g.busy)
				}
			})
		})
		if len(g.choices) < 2 {
			typography.Caption(c, "需要至少两个资料可读的角色才能建群。")
		}
		if g.err != "" {
			typography.Caption(c, g.err).TextColor(c.Theme().Danger).FillWidth()
		}
		count := 0
		for _, item := range g.choices {
			if item.selected {
				count++
			}
		}
		label := "创建群聊"
		if g.busy {
			label = "正在创建群聊…"
		}
		button.Solid(c, label).Disabled(g.busy || strings.TrimSpace(g.name) == "" || count < 2 || count > 10).OnClick(a.createGroup)
	})
	if was && !g.open {
		if g.cancel != nil {
			g.cancel()
			g.cancel = nil
		}
		g.generation++
		g.busy = false
	}
}
