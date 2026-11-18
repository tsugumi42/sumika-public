package app

// 设置工具的装配（规格-客户端自知与设置工具 第十一节）：谁能用、读写设置、发审批卡、取来源原话。
// 流程在 internal/settingtool；这里只给它宿主的东西，写入走设置页同一套服务（writeConfigSetting、setLinkSettings），不碰文件。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/kernel"
	"sumika-native/internal/settingsreg"
	"sumika-native/internal/settingtool"
	"sumika-native/internal/store"
	"sumika-native/internal/store/memsource"
	"sumika-native/internal/turn"
)

func (a *App) openSettingTool() error {
	return a.hub.Use(&settingtool.Plugin{Host: settingHost{a}, Log: settingtool.NewLog(a.paths.SettingChangesFile())})
}

type settingHost struct{ a *App }

var _ settingtool.Host = settingHost{}

func (h settingHost) Visible(aud audience.Audience) bool { return clientInfoVisible(aud) }

// Authorize：回合是宿主正在跑的那一次，身份是主人（本地私聊的主人认证，或 QQ 私聊里现在配置的主人 × 当前接收账号）。
// 群里即使是主人也不行（clientInfoVisible 只认私聊）；QQ 的主人以配置文件现在写的为准
func (h settingHost) Authorize(ctx context.Context, in kernel.Invocation, aud audience.Audience) (settingtool.Who, error) {
	a := h.a
	if a.hub == nil || a.shutDowned.Load() || a.hub.CheckInvocation(ctx, in) != nil {
		return settingtool.Who{}, errors.New("身份无效")
	}
	if !a.clientInfoOwner(in, aud) {
		return settingtool.Who{}, errors.New("不是所有者的私聊")
	}
	place := "本地私聊"
	if in.Scope.Channel == "qq" {
		if !a.lifeQQBindingCurrent(in) {
			return settingtool.Who{}, errors.New("所有者身份或接收账号已变更")
		}
		place = "QQ 私聊"
	}
	return settingtool.Who{Character: in.Scope.CharacterID, Conversation: in.Scope.Conversation, Place: place}, nil
}

func (h settingHost) Spec(key string) (settingtool.Spec, bool) { return settingToolSpec(key) }
func (h settingHost) Keys() []settingtool.Spec                 { return settingToolSpecs }
func (h settingHost) Now() time.Time                           { return h.a.now() }
func (h settingHost) NewID() string                            { return store.NewID(h.a.now()) }
func (h settingHost) IsConflict(err error) bool                { return errors.Is(err, config.ErrConfigVersion) }

// settingSnapshot 取一项设置在这份配置里的值（可以直接再写回去的 JSON）和给人看的样子
func settingSnapshot(c config.Config, key string) (settingtool.Snapshot, error) {
	if key == "location" {
		raw, err := json.Marshal(c.Location)
		if err != nil {
			return settingtool.Snapshot{}, err
		}
		display := "没有设置"
		if c.Location.Set() {
			display = c.Location.Name
		}
		return settingtool.Snapshot{Value: raw, Display: display, Version: c.Version}, nil
	}
	items, err := settingsreg.Read(c, nil, "")
	if err != nil {
		return settingtool.Snapshot{}, err
	}
	for _, it := range items {
		if it.Key != key {
			continue
		}
		raw, err := json.Marshal(it.Value)
		if err != nil {
			return settingtool.Snapshot{}, err
		}
		return settingtool.Snapshot{Value: raw, Display: settingDisplay(it.Value), Version: c.Version}, nil
	}
	return settingtool.Snapshot{}, errors.New("设置不存在")
}

func settingDisplay(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "开"
		}
		return "关"
	case string:
		if x == "" {
			return "（空）"
		}
		return "「" + x + "」"
	}
	return fmt.Sprint(v)
}

func (h settingHost) Current(ctx context.Context, key string) (settingtool.Snapshot, error) {
	if _, ok := settingToolSpec(key); !ok {
		return settingtool.Snapshot{}, errors.New("设置不在可改范围")
	}
	c, err := config.LoadIn(h.a.root, h.a.opt.DataDir)
	if err != nil {
		return settingtool.Snapshot{}, err
	}
	return settingSnapshot(c, key)
}

// Draft 按配置校验正本空跑一遍：补丁和 Parse 都走一次，但不写盘
func (h settingHost) Draft(ctx context.Context, key string, value json.RawMessage) (settingtool.Snapshot, error) {
	if _, ok := settingToolSpec(key); !ok {
		return settingtool.Snapshot{}, errors.New("设置不在可改范围")
	}
	a := h.a
	c, err := config.LoadIn(a.root, a.opt.DataDir)
	if err != nil {
		return settingtool.Snapshot{}, err
	}
	raw, err := os.ReadFile(c.Source)
	if err != nil {
		return settingtool.Snapshot{}, errors.New("读不到配置文件")
	}
	var next config.Config
	if key == "location" {
		var v any
		dec := json.NewDecoder(bytes.NewReader(value))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return settingtool.Snapshot{}, errors.New("值的格式不对")
		}
		_, next, err = config.PatchSetting(raw, c.Source, config.SettingChange{Key: key, Value: v})
	} else {
		var ch config.LinkChange
		if ch, err = a.registryLinkChange(api.SettingChange{Key: key, Value: value}); err == nil {
			_, next, err = config.PatchLink(raw, c.Source, ch)
		}
	}
	if err != nil {
		return settingtool.Snapshot{}, err
	}
	next.Version = c.Version
	return settingSnapshot(next, key)
}

func (h settingHost) Places(ctx context.Context, query string) ([]settingtool.Candidate, error) {
	if h.a.life == nil {
		return nil, errors.New("日历和天气没装配")
	}
	places, err := h.a.life.SearchPlaces(ctx, query, 5)
	if err != nil {
		return nil, err
	}
	out := make([]settingtool.Candidate, 0, len(places))
	for _, p := range places {
		raw, err := json.Marshal(p.Location)
		if err != nil {
			return nil, err
		}
		out = append(out, settingtool.Candidate{ID: p.ID, Label: p.Label, Value: raw})
	}
	return out, nil
}

// Write 走设置页同一套服务：普通配置键是 writeConfigSetting，链接键是 setLinkSettings
func (h settingHost) Write(ctx context.Context, key string, value json.RawMessage, base string) (settingtool.Written, error) {
	a := h.a
	if _, ok := settingToolSpec(key); !ok {
		return settingtool.Written{}, errors.New("设置不在可改范围")
	}
	ch := api.SettingChange{Key: key, Value: value}
	var err error
	if config.WritableSetting(key) {
		err = a.writeConfigSetting(ctx, ch, base)
	} else {
		var lc config.LinkChange
		if lc, err = a.registryLinkChange(ch); err == nil {
			_, err = a.setLinkSettings(ctx, lc, base)
		}
	}
	if err != nil {
		return settingtool.Written{}, err
	}
	c, err := config.LoadIn(a.root, a.opt.DataDir)
	if err != nil {
		return settingtool.Written{}, nil // 写成了，回读由调用方再做并如实报
	}
	return settingtool.Written{Version: c.Version}, nil
}

// Ask 发审批卡并等回答。审批卡只投影本地私聊：QQ 私聊发起的，卡片也放到主人和这个角色的本地私聊里，卡片上写明来自 QQ 私聊。
// 这里不发任何 QQ 消息；QQ 私聊里的回复同意是 feat/qq-approval 那条线的活
func (h settingHost) Ask(ctx context.Context, in kernel.Invocation, who settingtool.Who, c settingtool.Card) (settingtool.Decision, error) {
	a := h.a
	if a.approvals == nil {
		return settingtool.Decision{}, errors.New("没有本机审批能力")
	}
	conv := who.Conversation
	if in.Scope.Channel == "qq" {
		conv = "console:" + who.Character
		c.Notes = append(c.Notes, "来自 QQ 私聊的请求，卡片放在这里确认")
	}
	now := a.now()
	req := approval.Request{
		Identity: in, ID: c.ID, CharacterID: who.Character, Conversation: conv, ToolCallID: c.ID, Tool: settingtool.ToolApply,
		Title: c.Title, Subject: c.Subject, Preview: &approval.Preview{Kind: "setting", Text: c.Text},
		Why: "只改这一项，写入前会再核一次版本", Notes: c.Notes, Options: []string{"once", "deny"},
		CreatedAt: now.UnixMilli(), ExpiresAt: c.ExpiresAt.UnixMilli(),
	}
	d := a.approvals.Ask(ctx, req, nil)
	return settingtool.Decision{Approved: d.Kind == "approved", Kind: d.Kind, Reason: d.Reason}, nil
}

// Source 取这一轮绑定的主人原话：宿主绑的输入 seq，从库里读，只认主人本人的消息
func (h settingHost) Source(ctx context.Context, c turn.ToolCall) string {
	if len(c.InputSeqs) == 0 {
		return ""
	}
	src, err := memsource.NewMulti(h.a.set, c.CharacterID)
	if err != nil {
		return ""
	}
	var texts []string
	for _, seq := range c.InputSeqs {
		if seq <= 0 {
			continue
		}
		rows, err := src.Events(ctx, c.Conversation, seq-1, 1)
		if err != nil || len(rows) != 1 {
			continue
		}
		ev := rows[0]
		if ev.Seq == seq && ev.Conversation == c.Conversation && ev.ActorKind == "owner" && ev.Origin == "owner" && ev.Kind == "message" && !ev.Revoked && !ev.Unsent {
			texts = append(texts, strings.TrimSpace(ev.Text))
		}
	}
	return strings.Join(texts, " / ")
}
