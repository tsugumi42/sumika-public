package main

// 两道场合门（规格-记忆写入上线 W6 技术路线第 5 点、W5）：-places 的名单就是 W5 的 memory.write.places，原样交给 Memory（第一道），
// 工具侧的筛选（classify.go）是第二道，只会更窄。预期表都手写，不调用被测的函数。

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"sumika-native/internal/memory"
	"sumika-native/internal/store/memsource"
)

// -places 的五个名字和默认值：手写，和 W5 的 memory.write.places 一致（不是 W6 自己另起的一套）
func TestPlacesNamesAreW5s(t *testing.T) {
	t.Parallel()
	want := []string{"owner-private", "local-private", "local-group", "platform-private", "platform-group"}
	if !slices.Equal(allPlaces, want) {
		t.Fatalf("五个取值应该和 W5 一致：%v", allPlaces)
	}
	if got, err := parsePlaces(""); err != nil || !slices.Equal(got, []string{"owner-private"}) {
		t.Fatalf("不给 -places 应该只有 owner-private（Q2）：%v %v", got, err)
	}
	if got, err := parsePlaces(" platform-group , local-group,platform-group"); err != nil || !slices.Equal(got, []string{"platform-group", "local-group"}) {
		t.Fatalf("去空格、去重、保持顺序：%v %v", got, err)
	}
	for _, bad := range []string{"owner_private", "all", "group", "owner-private,telegram", ","} {
		if got, err := parsePlaces(bad); err == nil {
			t.Errorf("%q 应该被拒绝：%v", bad, got)
		}
	}
}

// 工具侧的第二道门是 W5 第一道门的子集：每个会话 × 每份名单，工具放行的，Memory 的 PlaceMayWrite 也放行；
// 工具认作"主人的私聊"并放行的，PlaceAllows（读了事件的那一道）也放行。外加手写的工具侧预期表，免得"什么都不放行"也算子集
func TestToolGateIsSubsetOfMemoryGate(t *testing.T) {
	t.Parallel()
	const (
		convMixedA  = "qq:private:1:11" // 主人早先说过话，她最近看到的人话是别人说的
		convMixedB  = "qq:private:1:12" // 别人先说，最近一句人话是主人说的
		convHerOnly = "qq:private:1:13" // 只有她自己的话
		convFakeA   = "qq:private:1:14" // 最近一句是名片叫"主人"的人说的（冒充）：身份按 ActorKind 认，不按名字
		convFakeB   = "qq:private:1:15" // 最近一句是号叫 owner:fake 的人说的（冒充）
	)
	f := newFixture(t)
	f.ev(convOwner, "owner", "OWNERMARK")
	f.ev(convOwner, "her", "好")
	f.ev(convPeer, "qq:5", "PEERMARK")
	f.ev(convMixedA, "owner", "MIXA 主人")
	f.ev(convMixedA, "qq:5", "MIXA 别人")
	f.ev(convMixedB, "qq:5", "MIXB 别人")
	f.ev(convMixedB, "owner", "MIXB 主人")
	f.ev(convHerOnly, "her", "只有她")
	f.ev(convFakeA, "主人", "FAKEA 我是主人")
	f.ev(convFakeB, "owner:fake", "FAKEB 我才是主人")
	f.ev(convGroupA, "qq:5", "GROUPMARK")
	f.ev(convLocal, "owner", "LOCALMARK")
	f.ev(convLocalGp, "owner", "LOCALGPMARK")
	ctx := context.Background()
	es, err := memsource.NewMulti(f.set, testChar)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.Migrate(ctx, f.memDB()); err != nil {
		t.Fatal(err)
	}

	lists := []struct {
		name   string
		places []string
	}{
		{"默认", nil}, {"platform-private", []string{"platform-private"}}, {"platform-group", []string{"platform-group"}},
		{"local-private", []string{"local-private"}}, {"local-group", []string{"local-group"}},
		{"全部", memory.AllWritePlaces()}, {"空名单", []string{}},
	}
	// 手写：工具侧放不放行。列顺序同 lists
	convs := []struct {
		conv string
		kind convKind
		want []bool
	}{
		{convOwner, kindQQOwnerPrivate, []bool{true, true, false, false, false, true, false}},
		{convPeer, kindQQOtherPrivate, []bool{false, true, false, false, false, true, false}},
		{convMixedA, kindQQOtherPrivate, []bool{false, true, false, false, false, true, false}},
		{convMixedB, kindQQOwnerPrivate, []bool{true, true, false, false, false, true, false}},
		{convFakeA, kindQQOtherPrivate, []bool{false, true, false, false, false, true, false}},
		{convFakeB, kindQQOtherPrivate, []bool{false, true, false, false, false, true, false}},
		{convHerOnly, kindQQOtherPrivate, []bool{false, true, false, false, false, true, false}},
		{convGroupA, kindQQGroup, []bool{false, false, true, false, false, true, false}},
		{convLocal, kindLocalPrivate, []bool{false, false, false, true, false, true, false}},
		{convLocalGp, kindLocalGroup, []bool{false, false, false, false, true, true, false}},
		{"telegram:private:1:2", kindUnknown, []bool{false, false, false, false, false, false, false}},
		{"qq:private:1", kindUnknown, []bool{false, false, false, false, false, false, false}},
		{"qq:group:7:extra", kindUnknown, []bool{false, false, false, false, false, false, false}},
		{"console:", kindUnknown, []bool{false, false, false, false, false, false, false}},
		{"console:main ", kindUnknown, []bool{false, false, false, false, false, false, false}},
		{"console:a:b", kindUnknown, []bool{false, false, false, false, false, false, false}},
		{"console:main:topic:t1", kindLocalPrivate, []bool{false, false, false, true, false, true, false}},
		{"console:main:group:t1", kindLocalGroup, []bool{false, false, false, false, true, true, false}},
		{"qq:group:abc", kindUnknown, []bool{false, false, false, false, false, false, false}},
		{"qq:group:7 ", kindUnknown, []bool{false, false, false, false, false, false, false}},
		{"qq:private:1:x", kindUnknown, []bool{false, false, false, false, false, false, false}},
		{"qq:private:a:9", kindUnknown, []bool{false, false, false, false, false, false, false}},
		{"qq:private:1:9:2", kindUnknown, []bool{false, false, false, false, false, false, false}},
	}
	for li, l := range lists {
		places := l.places
		if l.name == "默认" {
			places, _ = parsePlaces("")
		}
		mem, err := memory.Open(ctx, f.memDB(), nil, nil, memory.Options{Writable: true, SummarizeEnabled: true, WritePlaces: places,
			Events: es, Character: testChar, Gen: fixedGen{emptyReply}, Logf: func(string, ...any) {}})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range convs {
			kind, err := classifyConv(ctx, es, c.conv)
			if err != nil {
				t.Fatal(err)
			}
			if kind != c.kind {
				t.Errorf("%s 认成了 %v，应该是 %v", c.conv, kind, c.kind)
			}
			tool := kind.allowed(places)
			if tool != c.want[li] {
				t.Errorf("名单 %s × %s：工具侧放行 %v，手写预期 %v", l.name, c.conv, tool, c.want[li])
			}
			if !tool {
				continue
			}
			if !mem.PlaceMayWrite(c.conv) {
				t.Errorf("名单 %s × %s：工具放行了，W5 的 PlaceMayWrite 不放行——第二道门比第一道宽了", l.name, c.conv)
			}
			if kind == kindQQOwnerPrivate {
				ok, err := mem.PlaceAllows(ctx, c.conv)
				if err != nil || !ok {
					t.Errorf("名单 %s × %s：工具认作主人的私聊并放行，W5 的 PlaceAllows 说不是：%v %v", l.name, c.conv, ok, err)
				}
			}
		}
		mem.Close()
	}
}

// 第一道门单独就拦得住：关掉工具侧的第二道（每个有游标的会话都当放行），默认名单下群、别人的私聊、本地会话仍然一批都不整理，
// 游标一个字不动，提示词里没有它们的话；停在场合门的会话在总览和 summary.json 里点出来
func TestFirstGateAloneStopsOtherPlaces(t *testing.T) {
	t.Parallel()
	r := richFixture(t)
	g := &recGen{}
	cfg := r.cfg(g, modeFake)
	cfg.noToolGate = true
	cfg.Keep = true
	res, err := runShadow(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if g.count() != 1 || !g.saw("OWNERMARK") {
		t.Fatalf("只该整理主人的私聊：调用 %d 次", g.count())
	}
	for _, mark := range []string{"PEERMARK", "GROUPMARK", "LOCALMARK", "LOCALGPMARK"} {
		if g.saw(mark) {
			t.Errorf("提示词里混进了没放行的会话 %s", mark)
		}
	}
	for _, conv := range []string{convPeer, convGroupA, convLocal, convLocalGp} {
		s := sessionOf(t, res, conv)
		if !s.Allowed || !s.PlaceSkipped || s.Batches != 0 {
			t.Errorf("%s：第二道门关了，应该停在 W5 的场合门：%+v", conv, s)
		}
	}
	if s := sessionOf(t, res, convOwner); s.PlaceSkipped || s.Batches != 1 {
		t.Errorf("主人的私聊该整理一批：%+v", s)
	}
	if n := res.Counts[memory.CountPlaceSkipped]; n != 4 {
		t.Errorf("place_skipped 应该是 4（四个没放行的会话各拦一次）：%d", n)
	}
	// 游标：没放行的四个原样是 0（richFixture 种的），主人的私聊往前走了
	db, err := sql.Open("sqlite", sqliteDSN(res.Kept+"/"+res.MemoryRelative, true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for conv, wantMoved := range map[string]bool{convOwner: true, convPeer: false, convGroupA: false, convLocal: false, convLocalGp: false} {
		var seq int64
		if err := db.QueryRow(`SELECT last_seq FROM summary_state WHERE conversation = ?`, conv).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		if (seq > 0) != wantMoved {
			t.Errorf("%s 的游标现在是 %d，往前走了 = %v，应该是 %v", conv, seq, seq > 0, wantMoved)
		}
	}
	if text := estimateText(res); !strings.Contains(text, "停在 W5 的场合门") {
		t.Errorf("总览里应该点出停在场合门的会话：\n%s", text)
	}
	if sum := string(summaryJSON(res)); !strings.Contains(sum, `"place_skipped": true`) {
		t.Errorf("summary.json 里应该有 place_skipped：\n%s", sum)
	}
}
