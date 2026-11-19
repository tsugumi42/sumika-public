package store

import (
	"context"
	"testing"
)

// 新建话题：建一次、重复建不动、别的角色占着不抢、不合格的角色和 id 和空标题不建。
func TestCreateLocalTopic(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	local, _ := set.Platform("local")
	ctx := context.Background()
	created, err := local.CreateLocalTopic(ctx, "a", "loop-1-x", "定期任务 #1", "loop", 10)
	if err != nil || !created {
		t.Fatalf("第一次该建出来：%v %v", created, err)
	}
	if created, err = local.CreateLocalTopic(ctx, "a", "loop-1-x", "别的标题", "loop", 20); err != nil || created {
		t.Fatalf("重复建不该再建：%v %v", created, err)
	}
	var title, by string
	var at int64
	if err := local.Events.QueryRow(`SELECT title,title_by,created_at FROM local_topics WHERE id=?`, "loop-1-x").Scan(&title, &by, &at); err != nil || title != "定期任务 #1" || by != "loop" || at != 10 {
		t.Fatalf("话题行：%q %q %d %v", title, by, at, err)
	}
	if _, err := local.CreateLocalTopic(ctx, "b", "loop-1-x", "抢", "loop", 30); err == nil {
		t.Fatal("别的角色占着的话题 id 不该被抢")
	}
	cases := [][3]string{{"", "t", "标题"}, {"a:b", "t", "标题"}, {"a", "", "标题"}, {"a", "t:1", "标题"}, {"a", "t2", "  "}}
	for _, c := range cases {
		if _, err := local.CreateLocalTopic(ctx, c[0], c[1], c[2], "loop", 40); err == nil {
			t.Fatalf("%q 不合格，不该建", c)
		}
	}
}
