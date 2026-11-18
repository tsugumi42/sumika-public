package store

import (
	"context"
	"testing"
)

func boundOf(conv, cwd string, at int64) LocalWorkspace {
	return LocalWorkspace{Conversation: conv, CWD: cwd, Root: cwd, RealPath: cwd, UpdatedAt: at}
}

// 绑定的完整周期：建、看、改、删。
func TestLocalWorkspaceBindingLifecycle(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	local, _ := set.Platform("local")
	ctx := context.Background()
	conv := "console:a"
	if _, ok, err := local.LocalWorkspaceOf(ctx, conv); err != nil || ok {
		t.Fatalf("还没绑定不该有：%v %v", ok, err)
	}
	if err := local.SetLocalWorkspace(ctx, boundOf(conv, `D:\编的\甲`, 1)); err != nil {
		t.Fatal(err)
	}
	if err := local.SetLocalWorkspace(ctx, boundOf(conv, `D:\编的\乙`, 2)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := local.LocalWorkspaceOf(ctx, conv)
	if err != nil || !ok || got.CWD != `D:\编的\乙` || got.UpdatedAt != 2 {
		t.Fatalf("改目录没生效：%+v %v %v", got, ok, err)
	}
	removed, err := local.ClearLocalWorkspace(ctx, conv)
	if err != nil || !removed {
		t.Fatalf("删绑定：%v %v", removed, err)
	}
	removed, err = local.ClearLocalWorkspace(ctx, conv)
	if err != nil || removed {
		t.Fatalf("再删一次应该什么都没动：%v %v", removed, err)
	}
	if err := local.SetLocalWorkspace(ctx, LocalWorkspace{Conversation: conv, CWD: `D:\编的`}); err == nil {
		t.Fatal("缺真实路径不该写进去")
	}
}

// 最近目录：新的在前，同一个目录只算一次，大小写不同也算同一个；有上限。
func TestRecentLocalWorkspaces(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	local, _ := set.Platform("local")
	ctx := context.Background()
	for i, w := range []LocalWorkspace{boundOf("console:a", `D:\编的\甲`, 1), boundOf("console:b", `D:\编的\乙`, 2), boundOf("console:c", `d:\编的\乙`, 3), boundOf("console:d", `D:\编的\丙`, 4)} {
		if err := local.SetLocalWorkspace(ctx, w); err != nil {
			t.Fatal(i, err)
		}
	}
	got, err := local.RecentLocalWorkspaces(ctx, 2)
	if err != nil || len(got) != 2 || got[0] != `D:\编的\丙` || got[1] != `d:\编的\乙` {
		t.Fatalf("最近目录不对：%v %v", got, err)
	}
}

// 迁移重跑不报错；只读归档打开没有这两张表的旧库，读取当没绑定。
func TestLocalWorkspacesMigrationAndArchive(t *testing.T) {
	t.Parallel()
	set, p := goSet(t)
	local, _ := set.Platform("local")
	ctx := context.Background()
	if err := local.SetLocalWorkspace(ctx, boundOf("console:a", `D:\编的`, 1)); err != nil {
		t.Fatal(err)
	}
	set.Close()
	again, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatalf("重开重跑迁移：%v", err)
	}
	local, _ = again.Platform("local")
	if got, ok, err := local.LocalWorkspaceOf(ctx, "console:a"); err != nil || !ok || got.CWD != `D:\编的` {
		t.Fatalf("重开丢了绑定：%+v %v %v", got, ok, err)
	}
	// 绕开 goose 再跑一遍迁移语句：手工补过的库也不能报错（IF NOT EXISTS）
	sqlText, err := migrations.ReadFile("migrations/local/0003_local_workspaces.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Events.Exec(string(sqlText)); err != nil {
		t.Fatalf("迁移语句不能重跑：%v", err)
	}
	// 旧库：把表去掉，模拟只读归档打开迁移之前的库
	if _, err := local.Events.Exec(`DROP TABLE local_workspaces`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := local.LocalWorkspaceOf(ctx, "console:a"); err != nil || ok {
		t.Fatalf("旧库读绑定：%v %v", ok, err)
	}
	if m, err := local.ReadLocalWorkspaces(ctx); err != nil || len(m) != 0 {
		t.Fatalf("旧库读全部绑定：%v %v", m, err)
	}
	if got, err := local.RecentLocalWorkspaces(ctx, 8); err != nil || len(got) != 0 {
		t.Fatalf("旧库读最近目录：%v %v", got, err)
	}
	if _, err := again.ReadDisplayConversations(ctx, []string{"a"}, "", "", 10); err != nil {
		t.Fatalf("旧库读列表：%v", err)
	}
	again.Close()
}

// 列表只给本地私聊带目录；本地群、QQ 就算库里被塞了同名行也不带路径。
func TestDisplayConversationsWorkspaceOnlyLocalPrivate(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	local, _ := set.Platform("local")
	qq, _ := set.Platform("qq")
	ctx := context.Background()
	if err := local.CreateLocalGroup(ctx, LocalGroup{ID: "g1", Name: "编的群", CreatedAt: 1, Members: []LocalGroupMember{{CharacterID: "a"}, {CharacterID: "b", Position: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: "qq:group:90004", ActorKind: "human", ActorID: "90003", Kind: "message", Text: "编的", CreatedAt: 1}, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	for _, conv := range []string{"console:a", "console:group:g1", "qq:group:90004"} {
		if err := local.SetLocalWorkspace(ctx, boundOf(conv, `D:\编的\`+conv[:7], 1)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := set.ReadDisplayConversations(ctx, []string{"a", "b"}, "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, v := range got.Items {
		seen[v.Conversation] = v.WorkspaceCWD
	}
	if seen["console:a"] == "" || seen["console:b"] != "" {
		t.Fatalf("本地私聊的目录不对：%v", seen)
	}
	if v, ok := seen["console:group:g1"]; !ok || v != "" {
		t.Fatalf("本地群不该带路径：%v", seen)
	}
	if v, ok := seen["qq:group:90004"]; !ok || v != "" {
		t.Fatalf("QQ 不该带路径：%v", seen)
	}
}
