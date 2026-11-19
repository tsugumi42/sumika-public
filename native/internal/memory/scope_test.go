package memory

import (
	"os"
	"reflect"
	"testing"
	"time"
)

// 表达集合仍需保护群友套话；召回集合由 TestADR242RecallEveryPlace 独立验证。
func TestDisclosureScopes(t *testing.T) {
	t.Parallel()
	const GA, GB, PEER = "qq:group:30001", "qq:group:30002", "qq:60001"
	const PEER_DM, OWNER_DM = "qq:private:20001:60001", "qq:private:20001:10001"
	at := func(conv, ch, scene string, people []string, privateOK bool) Audience {
		return Audience{Channel: ch, Conversation: conv, Scene: scene, PersonIDs: people, PrivateOK: privateOK}
	}
	cases := []struct {
		name string
		a    Audience
		want []string
	}{
		{"本地私聊的主人", at("console:a", "console", "private", []string{"owner"}, true), nil},
		{"主人的 QQ 私聊", at(OWNER_DM, "qq", "private", []string{"owner"}, true), []string{"public", "conv:" + OWNER_DM, "owner", "person:owner"}},
		{"某人的 QQ 私聊", at(PEER_DM, "qq", "private", []string{PEER}, false), []string{"public", "conv:" + PEER_DM, "person:" + PEER}},
		{"QQ 群 A", at(GA, "qq", "group", []string{PEER}, false), []string{"public", "conv:" + GA}},
		{"QQ 群 B", at(GB, "qq", "group", []string{PEER}, false), []string{"public", "conv:" + GB}},
		{"本地群聊（主人在场）", at("console:group:1", "console", "group", []string{"owner"}, true), []string{"public", "conv:console:group:1", "owner"}},
	}
	for _, c := range cases {
		if got := DisclosureScopes(c.a); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s：得到 %v，要 %v", c.name, got, c.want)
		}
	}
	// 白名单只管私聊：同一个人在群里照样按群算
	g := at(GA, "qq", "group", []string{PEER}, false)
	if got := DisclosureScopes(g); !reflect.DeepEqual(got, []string{"public", "conv:" + GA}) {
		t.Errorf("群里白名单不该生效：%v", got)
	}
}

// 同角色记忆的范围标签不再限制跨场合召回（ADR-242）。
func TestCanSeeScopeSet(t *testing.T) {
	t.Parallel()
	const GA = "qq:group:30001"
	item := []string{"owner", "conv:" + GA}
	cases := []struct {
		name string
		a    Audience
		want bool
	}{
		{"本地私聊", Audience{Channel: "console", Conversation: "console:a", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}, true},
		{"本地群聊", Audience{Channel: "console", Conversation: "console:group:1", Scene: "group", PersonIDs: []string{"owner"}, PrivateOK: true}, true},
		{"主人的 QQ 私聊", Audience{Channel: "qq", Conversation: "qq:private:20001:10001", Scene: "private", PersonIDs: []string{"owner"}, PrivateOK: true}, true},
		{"QQ 群 A", Audience{Channel: "qq", Conversation: GA, Scene: "group"}, true},
		{"别的 QQ 群", Audience{Channel: "qq", Conversation: "qq:group:30002", Scene: "group"}, true},
	}
	for _, c := range cases {
		if got := CanSee(item, AllowedScopes(c.a)); got != c.want {
			t.Errorf("%s：得到 %v，要 %v", c.name, got, c.want)
		}
	}
}

func TestPlaceOf(t *testing.T) {
	t.Parallel()
	cases := map[[3]string]Place{
		{"console", "console:a", ""}:         LocalPrivate,
		{"console", "console:group:1", ""}:   LocalGroup,
		{"qq", "qq:private:1:2", ""}:         PlatformPrivate,
		{"qq", "qq:group:3", ""}:             PlatformGroup,
		{"qq", "qq:group:3", "private"}:      PlatformPrivate, // scene 优先
		{"telegram", "telegram:group:9", ""}: PlatformGroup,   // 别的平台一样分
	}
	for in, want := range cases {
		if got := PlaceOf(in[0], in[1], in[2]); got != want {
			t.Errorf("%v：得到 %s，要 %s", in, got, want)
		}
	}
}

func TestExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	day := int64(86_400_000)
	past, future := now.UnixMilli()-day, now.UnixMilli()+day
	cases := []struct {
		name string
		it   Item
		want bool
	}{
		{"写了 valid_to、已经过了", Item{ValidTo: &past}, true},
		{"写了 valid_to、还没到", Item{ValidTo: &future, Temporal: "temporary", LearnedAt: now.UnixMilli() - 30*day}, false},
		{"暂时的事、8 天前记的", Item{Temporal: "temporary", LearnedAt: now.UnixMilli() - 8*day}, true},
		{"暂时的事、6 天前记的", Item{Temporal: "temporary", LearnedAt: now.UnixMilli() - 6*day}, false},
		{"暂时的事、没有 learned_at 按 created_at", Item{Temporal: "temporary", CreatedAt: now.UnixMilli() - 8*day}, true},
		{"长期的事、很久以前", Item{CreatedAt: now.UnixMilli() - 400*day}, false},
	}
	for _, c := range cases {
		if got := c.it.Expired(now); got != c.want {
			t.Errorf("%s：得到 %v", c.name, got)
		}
	}
}

// 读 Bun 版的库：SUMIKA_MEMORY_DB 指向一份副本时跑。有效、待确认的事实和有效的片段都读进来，撤销的不读
func TestLoadItemsFromBunDb(t *testing.T) {
	t.Parallel()
	file := os.Getenv("SUMIKA_MEMORY_DB")
	if file == "" {
		t.Skip("没设 SUMIKA_MEMORY_DB（记忆库副本的路径）")
	}
	db, err := openReadOnly(file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	items, err := loadItems(db)
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, it := range items {
		n[it.Type+"/"+it.Status]++
		if len(it.Scopes) == 0 || it.Text == "" {
			t.Fatalf("%s 没有范围或正文", it.Ref)
		}
	}
	var active, cand, eps int
	db.QueryRow(`SELECT count(*) FROM facts WHERE status = 'active'`).Scan(&active)
	db.QueryRow(`SELECT count(*) FROM facts WHERE status = 'candidate'`).Scan(&cand)
	db.QueryRow(`SELECT count(*) FROM episodes WHERE status = 'active'`).Scan(&eps)
	if n["fact/active"] != active || n["fact/candidate"] != cand || n["episode/active"] != eps {
		t.Fatalf("读进来的条数对不上：%v，库里 有效 %d 待确认 %d 片段 %d", n, active, cand, eps)
	}
	t.Logf("读进来 %v", n)
}
