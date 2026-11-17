package store

import (
	"context"
	"testing"

	"sumika-native/internal/audience"
)

// 自检话题：建一次、重复建不动、别的角色占着 id 不抢、不是自检话题的 id 一律不建；最新的按建的先后选
func TestEnsureSelftestTopic(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	local, _ := set.Platform("local")
	ctx := context.Background()
	fixed := audience.SelftestTopicID("a", "")
	created, err := local.EnsureSelftestTopic(ctx, "a", fixed, "", 10)
	if err != nil || !created {
		t.Fatalf("第一次该建出来：%v %v", created, err)
	}
	if created, err = local.EnsureSelftestTopic(ctx, "a", fixed, "", 20); err != nil || created {
		t.Fatalf("重复建不该再建：%v %v", created, err)
	}
	var title, by string
	var at int64
	if err := local.Events.QueryRow(`SELECT title,title_by,created_at FROM local_topics WHERE id=?`, fixed).Scan(&title, &by, &at); err != nil || title != SelftestTopicTitle || at != 10 {
		t.Fatalf("话题行：%q %q %d %v", title, by, at, err)
	}
	if _, err := local.EnsureSelftestTopic(ctx, "b", fixed, "", 30); err == nil {
		t.Fatal("别的角色占着的话题 id 不该被抢")
	}
	for _, bad := range []string{"t1", "selftestx", "console", ""} {
		if _, err := local.EnsureSelftestTopic(ctx, "a", bad, "", 40); err == nil {
			t.Fatalf("%q 不是自检话题 id，不该建", bad)
		}
	}
	for _, bad := range []string{"", "a:b", "a b", "a\n"} {
		if _, err := local.EnsureSelftestTopic(ctx, bad, audience.SelftestTopicID("a", ""), "", 50); err == nil {
			t.Fatalf("角色 %q 不合格，不该建", bad)
		}
	}
	fresh := audience.SelftestTopicID("a", "x1")
	if _, err := local.EnsureSelftestTopic(ctx, "a", fresh, "自检 新的", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Events.Exec(`INSERT INTO local_topics(id,character_id,title,created_at) VALUES ('普通话题','a','普通',200)`); err != nil {
		t.Fatal(err)
	}
	got, err := local.LatestSelftestTopic(ctx, "a")
	if err != nil || got != fresh {
		t.Fatalf("最新的自检话题该是 %s（普通话题不算）：%q %v", fresh, got, err)
	}
	if got, err = local.LatestSelftestTopic(ctx, "b"); err != nil || got != "" {
		t.Fatalf("没有自检话题的角色该返回空：%q %v", got, err)
	}
}
