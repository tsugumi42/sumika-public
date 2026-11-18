package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 所有场合都能想起；QQ 群主人问到的 owner 私事另带披露提醒。

func renderFor(t *testing.T, m *Memory, a Audience) []string {
	t.Helper()
	b := Render(m.Recall(context.Background(), a, Query{Text: "考试 吉他 搬家 猫", Mode: ModeExplicit}), time.Now())
	return b.Lines(true)
}

func lineWith(lines []string, sub string) string {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestOwnerMark四种场合(t *testing.T) {
	file := bunDb(t, [][]string{
		{"1", "fact", "active", "主人下周要考试，考完才有空", "owner", "owner"},
		{"2", "promise", "active", "考试前提醒他早睡", "owner", "owner"},
		{"3", "fact", "active", "小A约好下周搬家，群里帮忙", "qq:60001", "conv:" + GA},
		{"4", "fact", "candidate", "主人好像说过在学吉他", "owner", "owner"},
	}, nil)
	m, err := Open(context.Background(), file, textEmbed{}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	const mark = "所有者私下说的，他问才答："

	cases := []struct {
		name string
		a    Audience
		// 要标的 / 不该标的行里的字；看不见的行不检查
		marked, plain []string
		invisible     []string
	}{
		{"本地私聊", Audience{Channel: "console", Conversation: "console:a", Scene: "private", PrivateOK: true, OwnerAsked: true},
			nil, []string{"主人下周要考试", "提醒他早睡", "在学吉他"}, nil},
		{"本地群聊（主人在场）", Audience{Channel: "console", Conversation: "console:group:1", Scene: "group", PrivateOK: true, PersonIDs: []string{"owner"}, OwnerAsked: true},
			nil, []string{"主人下周要考试", "提醒他早睡", "在学吉他", "群里帮忙"}, nil},
		{"主人的 QQ 私聊", Audience{Channel: "qq", Conversation: ownerDM, Scene: "private", PrivateOK: true, PersonIDs: []string{"owner"}, OwnerAsked: true},
			nil, []string{"主人下周要考试", "提醒他早睡", "在学吉他", "群里帮忙"}, nil},
		{"某人的 QQ 私聊", Audience{Channel: "qq", Conversation: peerDM, Scene: "private", PersonIDs: []string{peerA}, OwnerAsked: false},
			nil, []string{"主人下周要考试", "提醒他早睡", "在学吉他", "群里帮忙"}, nil},
		{"QQ 群：群友问", qqGroup(GA, false, peerA),
			nil, []string{"主人下周要考试", "提醒他早睡", "在学吉他", "群里帮忙"}, nil},
		{"QQ 群：主人问", qqGroup(GA, true, "owner"),
			[]string{"主人下周要考试", "提醒他早睡", "在学吉他"}, []string{"群里帮忙"}, nil},
	}
	for _, c := range cases {
		lines := renderFor(t, m, c.a)
		for _, s := range c.marked {
			l := lineWith(lines, s)
			if l == "" || !strings.Contains(l, mark) {
				t.Errorf("%s：「%s」应该带标记，得到 %q", c.name, s, l)
			}
		}
		for _, s := range c.plain {
			l := lineWith(lines, s)
			if l == "" {
				t.Errorf("%s：「%s」应该给出来，没找到（%q）", c.name, s, lines)
			} else if strings.Contains(l, mark) {
				t.Errorf("%s：「%s」不该带标记：%q", c.name, s, l)
			}
		}
		for _, s := range c.invisible {
			if l := lineWith(lines, s); l != "" {
				t.Errorf("%s：「%s」不该给出来：%q", c.name, s, l)
			}
		}
	}
	// 待确认的写法：标记在最前，「听说：」在后
	l := lineWith(renderFor(t, m, qqGroup(GA, true, "owner")), "在学吉他")
	if !strings.Contains(l, mark+"听说：主人好像") {
		t.Errorf("待确认的标记顺序不对：%q", l)
	}
}

// 主人问过后群友再问仍能想起；不把上一轮的披露提醒当成当前授权。
func TestOwnerMarkThenPeerAsksAgain(t *testing.T) {
	t.Parallel()
	file := bunDb(t, [][]string{{"1", "fact", "active", "主人下周要考试，考完才有空", "owner", "owner"}}, nil)
	m, err := Open(context.Background(), file, textEmbed{}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if l := lineWith(renderFor(t, m, qqGroup(GA, true, "owner")), "主人下周要考试"); !strings.Contains(l, "他问才答") {
		t.Fatalf("主人问时应该带标记：%q", l)
	}
	if l := lineWith(renderFor(t, m, qqGroup(GA, false, peerA)), "主人下周要考试"); l == "" || strings.Contains(l, "他问才答") {
		t.Fatalf("群友再问的召回或披露提醒错误：%q", l)
	}
}

// 范围是一组：同时在这个群里看得见的（conv:<群>）不算「只因主人问才看见」，群友也看得到，不标
func TestOwnerOnlyFnScopeSets(t *testing.T) {
	t.Parallel()
	a := qqGroup(GA, true, "owner")
	f := ownerOnlyFn(a, DisclosureScopes(a))
	for _, c := range []struct {
		scopes []string
		want   bool
	}{
		{[]string{"owner"}, true},
		{[]string{"owner", "conv:" + GA}, false},
		{[]string{"owner", "public"}, false},
		{[]string{"conv:" + GA}, false},
		{[]string{"owner", "conv:" + GB}, true},
	} {
		if got := f(c.scopes); got != c.want {
			t.Errorf("%v：得到 %v，要 %v", c.scopes, got, c.want)
		}
	}
	// 群友问、能看全部：永远为假
	b := qqGroup(GA, false, peerA)
	if ownerOnlyFn(b, DisclosureScopes(b))([]string{"owner"}) {
		t.Error("群友问不该有 owner 标记")
	}
	if ownerOnlyFn(a, nil)([]string{"owner"}) {
		t.Error("能看全部时不该有 owner 标记")
	}
}
