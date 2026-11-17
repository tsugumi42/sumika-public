package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"sumika-native/internal/paths"
)

// 平台登记做成值（规格-多平台装配第 2 步）：测试自己造一份带虚构平台的登记，不碰默认登记。

func fakeRegistry(t *testing.T) Registry {
	t.Helper()
	reg, err := NewRegistry(append(DefaultRegistry.Platforms(), Platform{Channel: "fakeplat", Name: "fakeplat"})...)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestNewRegistryRejects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ps   []Platform
	}{
		{"渠道名空", []Platform{{Channel: "", Name: "x"}}},
		{"平台名大写", []Platform{{Channel: "x", Name: "Bad"}}},
		{"平台名是保留的", []Platform{{Channel: "x", Name: "trace"}}},
		{"渠道重复", []Platform{{Channel: "x", Name: "a"}, {Channel: "x", Name: "b"}}},
		{"平台重复", []Platform{{Channel: "x", Name: "a"}, {Channel: "y", Name: "a"}}},
	} {
		if _, err := NewRegistry(tc.ps...); err == nil {
			t.Errorf("%s：应该拒", tc.name)
		}
	}
	// 零值什么都没登记：查渠道报错，不落到哪个库
	if got, err := (Registry{}).PlatformOf("qq"); err == nil {
		t.Errorf("零值登记给出了平台 %q", got)
	}
}

func TestOpenGoWithFakePlatform(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenGoWith(p, fakeRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(p.ChatDB("fakeplat")); err != nil {
		t.Fatalf("虚构平台的库没建出来：%v", err)
	}
	st, err := s.For("fakeplat")
	if err != nil {
		t.Fatalf("按这份登记取不到虚构平台：%v", err)
	}
	if got, err := s.ForConversation("fakeplat:group:1"); err != nil || got != st {
		t.Fatalf("按会话取虚构平台：%v %v", got, err)
	}
	if _, err := s.For("telegram"); err == nil {
		t.Fatal("没登记的渠道也取到了库")
	}
	// 默认登记不受影响：包级的 PlatformOf 照旧不认虚构平台
	if _, err := PlatformOf("fakeplat"); err == nil {
		t.Fatal("默认登记里冒出了虚构平台")
	}
	// 事件写得进去；就算它带着角色的观察记录，桌面也一条不列、按会话取历史被拒（新平台没写显示规矩前看不到）
	if _, err := st.AppendEvent(NewEvent{
		Channel: "fakeplat", Conversation: "fakeplat:group:1", ActorKind: "human", ActorID: "fakeplat:7",
		Origin: "peer", Kind: "message", Text: "编的话", CreatedAt: 1000,
	}, []string{"编的角色"}); err != nil {
		t.Fatalf("虚构平台写不进事件：%v", err)
	}
	ctx := context.Background()
	list, err := s.ReadDisplayConversations(ctx, []string{"编的角色"}, "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list.Items {
		if strings.HasPrefix(c.Conversation, "fakeplat:") {
			t.Fatalf("没写显示规矩的平台出现在会话列表里：%s", c.Conversation)
		}
	}
	if _, err := st.ReadDisplay(ctx, DisplayScope{Platform: "fakeplat", Conversation: "fakeplat:group:1", Characters: []string{"编的角色"}}, "", 10); !errors.Is(err, ErrDisplayScope) {
		t.Fatalf("没写显示规矩的平台按会话取历史应该被拒：%v", err)
	}
}

func TestOpenGoReadOnlyWithOptional(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := fakeRegistry(t)
	s, err := OpenGoWith(p, reg)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	ctx := context.Background()
	// 库在：照样打开
	ro, err := OpenGoReadOnlyWith(ctx, p, reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ro.Platform("fakeplat"); !ok {
		t.Fatal("虚构平台的库在，只读却没打开它")
	}
	ro.Close()
	// 不是必须有的库不在：跳过，照样能看历史
	for _, f := range []string{p.ChatDB("fakeplat"), p.ChatDB("fakeplat") + "-wal", p.ChatDB("fakeplat") + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	ro, err = OpenGoReadOnlyWith(ctx, p, reg)
	if err != nil {
		t.Fatalf("没有新平台的库就打不开历史：%v", err)
	}
	if _, ok := ro.Platform("fakeplat"); ok {
		t.Fatal("库不在却给出了虚构平台")
	}
	ro.Close()
	// 必须有的库不在：照旧报错
	for _, f := range []string{p.ChatDB("qq"), p.ChatDB("qq") + "-wal", p.ChatDB("qq") + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if ro, err := OpenGoReadOnlyWith(ctx, p, reg); err == nil {
		ro.Close()
		t.Fatal("缺了 QQ 的库也打开了")
	}
}
