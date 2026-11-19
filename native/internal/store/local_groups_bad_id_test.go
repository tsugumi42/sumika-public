package store

import (
	"context"
	"testing"
)

// 库里有一个 ID 不合法的本地群（脏数据）：列表跳过它，其余的群照常出来，不让整张列表失败。
func TestReadLocalGroupsSkipsBadID(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	local, _ := set.Platform("local")
	ctx := context.Background()
	good := LocalGroup{ID: "g1", Name: "编的好群", CreatedAt: 1, Members: []LocalGroupMember{{CharacterID: "a"}}}
	bad := LocalGroup{ID: "坏:冒号", Name: "编的坏群", CreatedAt: 2, Members: []LocalGroupMember{{CharacterID: "a"}}}
	for _, g := range []LocalGroup{good, bad} {
		if err := local.CreateLocalGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	got, err := local.ReadLocalGroups(ctx, []string{"a"}, "")
	if err != nil {
		t.Fatalf("一个坏群不该让整张列表失败：%v", err)
	}
	if len(got) != 1 || got[0].ID != "g1" {
		t.Fatalf("应只剩好群：%+v", got)
	}
}
