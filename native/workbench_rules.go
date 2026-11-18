package main

// “工具与审批”设置页的放行规则（规格-审批分级第七节）：所有“记住”和“这个对话”列在一起，能看是什么、什么时候记的、
// 最近用过没有，能撤销。撤销不二次确认：撤错了最多再问一次。

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/winshell"
)

// rulesRefresh 是页面开着时多久重读一次：规则是聊天里随时记下、随时用到的。
const rulesRefresh = 3 * time.Second

type approvalRulesUI struct {
	rows      []api.ApprovalRule
	err       string // 读列表失败；重读成功就清
	revokeErr string // 撤销没写进文件；重读不清，主人下一次撤销时才清（后台内存里已撤，重读看不出文件里还在）
	loading   bool
	loaded    time.Time
	revoking  map[string]bool
	draft     string // 添加规则的输入
	project   string // 添加规则的作用范围：项目目录（空是不限）
	thisConv  bool   // 添加规则的作用范围：只对当前打开的对话
	addErr    string // 添加失败的原因（写法不对之类）
	adding    bool
}

func ruleID(r api.ApprovalRule) string { return r.Scope + "|" + r.Conversation + "|" + r.Key }

// 返回是否画了这一段；画了，调用方在后面接“审批设置”的小标题。
func (a *app) wbApprovalRules(c *ui.Context, kit *winshell.Kit) bool {
	st := &a.rulesUI
	reader, ok := a.reader.(api.ApprovalRules)
	if !ok {
		return false // 后台没开着：这一段不画，下面的设置项照旧
	}
	if !st.loading && (st.loaded.IsZero() || c.Now().Sub(st.loaded) >= rulesRefresh) {
		a.loadApprovalRules(reader, c.Now())
	}
	c.After(rulesRefresh)
	kit.SectionHeading(c, "放行规则")
	if st.err != "" {
		kit.Note(c, st.err, true)
	}
	if st.revokeErr != "" {
		kit.Note(c, st.revokeErr, true)
	}
	if len(st.rows) == 0 && st.err == "" && st.revokeErr == "" {
		kit.Note(c, "还没有。审批卡上选“这个对话”或“记住”以后会列在这里。", false)
	}
	now := time.Now()
	for _, r := range st.rows {
		a.wbApprovalRuleRow(c, kit, reader, r, now)
	}
	if editor, ok := a.reader.(api.ApprovalRuleEditor); ok {
		a.wbAddApprovalRule(c, kit, editor, reader)
	}
	return true
}

func (a *app) wbApprovalRuleRow(c *ui.Context, kit *winshell.Kit, reader api.ApprovalRules, r api.ApprovalRule, now time.Time) {
	st := &a.rulesUI
	id := ruleID(r)
	key := "rule-" + id
	label := r.Label
	if label == "" {
		label = r.Key // 手写进文件的条目可能没写名字
	}
	kit.Row(c, key, label, a.ruleSummary(r, now), func() {
		kit.Button(c, key+"-revoke", "撤销", false, st.revoking[id], func() { a.revokeApprovalRule(reader, r) })
	})
}

// wbAddApprovalRule 是列表下面的"写一条规则"：一个输入框加"允许""拒绝"两个按钮。写法对齐 Claude Code，
// 例如 Bash(git status:*)、Write(src/**)、WebFetch(domain:example.com)；拒绝先于一切放行。
func (a *app) wbAddApprovalRule(c *ui.Context, kit *winshell.Kit, editor api.ApprovalRuleEditor, reader api.ApprovalRules) {
	st := &a.rulesUI
	empty := st.adding || strings.TrimSpace(st.draft) == ""
	kit.Row(c, "rule-add", "写一条规则", "例如 Bash(git status:*) 或 Write(src/**)；拒绝先于“每次问”，“每次问”先于允许", func() {
		kit.TextField(c, "rule-add-text", &st.draft, "规则").Placeholder("Bash(git status:*)").Disabled(st.adding).Width(260)
		kit.FieldButton(c, "rule-add-allow", "允许", false, empty, func() { a.addApprovalRule(editor, reader, "allow") })
		kit.FieldButton(c, "rule-add-ask", "每次问", false, empty, func() { a.addApprovalRule(editor, reader, "ask") })
		kit.FieldButton(c, "rule-add-deny", "拒绝", false, empty, func() { a.addApprovalRule(editor, reader, "deny") })
	})
	kit.Row(c, "rule-add-scope", "作用范围", "允许、每次问可以只对一个项目目录、只对当前对话；都不填是全局。拒绝一律全局", func() {
		kit.TextField(c, "rule-add-project", &st.project, "项目目录").Placeholder(`D:\Projects\demo`).Disabled(st.adding).Width(220)
		kit.Check(c, "rule-add-conv", "只对当前对话", &st.thisConv, st.adding)
	})
	if st.addErr != "" {
		kit.Note(c, st.addErr, true)
	}
}

func (a *app) addApprovalRule(editor api.ApprovalRuleEditor, reader api.ApprovalRules, effect string) {
	st := &a.rulesUI
	raw := strings.TrimSpace(st.draft)
	project, session := strings.TrimSpace(st.project), ""
	if st.thisConv {
		k := a.keyOf(a.sel)
		if k.Platform != "local" || k.Scene != "private" || k.Conversation == "" {
			st.addErr = "没加上：只有打开着本地私聊的时候才能限定到当前对话"
			return
		}
		session = k.Conversation
	}
	st.adding, st.addErr = true, ""
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		err := editor.AddApprovalRule(ctx, raw, effect, project, session)
		a.post(func() {
			st.adding = false
			if err != nil {
				st.addErr = "没加上：" + err.Error()
				return
			}
			st.draft, st.project, st.thisConv = "", "", false
			st.loaded = time.Time{} // 马上重读，新规则出现在列表里
		})
	}()
}

// ruleSummary 是行下面的一句：哪一档 · 什么时候记的 · 最近用过没有。
func (a *app) ruleSummary(r api.ApprovalRule, now time.Time) string {
	scope := "记住"
	switch {
	case r.Scope == string(api.ApproveConversation):
		scope = "这个对话 · " + a.ruleConversation(r.Conversation)
	case r.Effect == "deny":
		scope = "你写的规则 · 拒绝"
	case r.Effect == "ask":
		scope = "你写的规则 · 每次都问"
	case r.Effect != "":
		scope = "你写的规则 · 允许"
	}
	parts := []string{scope}
	if r.Project != "" {
		parts = append(parts, "只在项目 "+r.Project)
	}
	if r.Session != "" {
		parts = append(parts, "只在对话 "+a.ruleSession(r.Session))
	}
	if len(r.Dirs) > 0 {
		parts = append(parts, "碰得到 "+strings.Join(r.Dirs, "、"))
	}
	if r.At > 0 {
		parts = append(parts, ruleWhen(r.At, now)+"记下")
	}
	if r.LastUsed > 0 {
		parts = append(parts, ruleWhen(r.LastUsed, now)+"用过")
	} else {
		parts = append(parts, "还没用过")
	}
	return strings.Join(parts, " · ")
}

// ruleConversation 是规则属于哪个会话：角色名，话题加一句；子任务自己的内部会话写子任务。
func (a *app) ruleConversation(conv string) string {
	if strings.HasPrefix(conv, "internal:") {
		return "子任务"
	}
	name := a.loopOwner(api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conv})
	if strings.Contains(conv, ":topic:") {
		return name + "的话题"
	}
	return name
}

// ruleSession 是规则限定的会话：认得出的写角色名，认不出（会话已经删了）的写"已不存在"，规则还在、能撤销。
func (a *app) ruleSession(conv string) string {
	for _, k := range a.keys {
		if k.Conversation == conv {
			return a.ruleConversation(conv)
		}
	}
	return conv + "（这个对话已不在，规则不会再生效，可以撤销）"
}

// ruleWhen 是一个过去的时刻：刚才、几分钟前、几小时前、几天前，再早写日期。
func ruleWhen(ms int64, now time.Time) string {
	at := time.UnixMilli(ms)
	switch d := now.Sub(at); {
	case d < time.Minute:
		return "刚才"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " 分钟前"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " 小时前"
	case d < 30*24*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + " 天前"
	}
	return at.Format("2006-01-02") + " "
}

func (a *app) loadApprovalRules(reader api.ApprovalRules, now time.Time) {
	st := &a.rulesUI
	st.loading, st.loaded = true, now
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func() {
		rows, err := reader.ListApprovalRules(ctx)
		a.post(func() {
			st.loading = false
			if err != nil {
				st.err = "读不到放行规则：" + err.Error()
				return
			}
			st.err = ""
			st.rows = rows
		})
	}()
}

// revokeApprovalRule 撤销一条。后台确认后马上从列表拿掉，不等下一次重读。
func (a *app) revokeApprovalRule(reader api.ApprovalRules, r api.ApprovalRule) {
	st := &a.rulesUI
	id := ruleID(r)
	if st.revoking == nil {
		st.revoking = map[string]bool{}
	}
	st.revoking[id] = true
	st.revokeErr = ""
	ctx := a.viewCtx
	if ctx == nil {
		ctx = a.base()
	}
	go func(ctx context.Context) {
		err := reader.RevokeApprovalRule(ctx, r)
		a.post(func() {
			delete(st.revoking, id)
			if err != nil {
				st.revokeErr = "撤销出了问题：" + err.Error()
				st.loaded = time.Time{} // 失败了就重读一遍，列表以后台为准
				return
			}
			kept := st.rows[:0:0]
			for _, x := range st.rows {
				if ruleID(x) != id {
					kept = append(kept, x)
				}
			}
			sort.SliceStable(kept, func(i, j int) bool {
				return kept[i].Scope == string(api.ApproveAlways) && kept[j].Scope != string(api.ApproveAlways)
			})
			st.rows = kept
		})
	}(ctx)
}
