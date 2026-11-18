package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// 规格-记忆页 3.4 那张"形状 × 入口"表，每一行一个测试。数据全是编的，预期都是手算的，不调被测的函数去算预期。

var bctx = context.Background()

// 状态组：每个（status, confirmed_by）组合，SQL 里算的（筛选、计数）和 Go 里算的（GroupOf）逐格一致，也和手写的预期一致
func TestBrowseGroupsAgreeEverywhere(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	statuses := []string{"active", "candidate", "revoked", "superseded", "weird"}
	bys := []any{nil, "", "owner", "ai", "owner_said", "zzz"}
	want := func(status string, by any) string {
		b, _ := by.(string)
		switch status {
		case "active":
			switch b {
			case "owner":
				return "owner"
			case "ai":
				return "ai"
			case "owner_said":
				return "said"
			case "":
				return "imported"
			}
			return "other"
		case "candidate", "revoked", "superseded":
			return status
		}
		return "other"
	}
	wantCount := map[string]int{}
	n := 0
	for _, s := range statuses {
		for _, by := range bys {
			id := fmt.Sprintf("g%02d", n)
			n++
			x.put(bf{id: id, status: s, by: by, hasBy: true})
			wantCount[want(s, by)]++
			if got := GroupOf(s, fmt.Sprint(orEmpty(by))); got != want(s, by) {
				t.Errorf("GroupOf(%q, %v) = %s，手算 %s", s, by, got, want(s, by))
			}
		}
	}
	b := x.browser(BrowserOptions{})
	// 计数
	c, err := b.Counts(bctx)
	if err != nil {
		t.Fatal(err)
	}
	for g, w := range wantCount {
		if got := c.Facts["fact"][g]; got != w {
			t.Errorf("计数 %s = %d，手算 %d", g, got, w)
		}
	}
	// 筛选：每个组各取一遍，取到的条数和计数一致，并且每一行自己的组就是筛的那个
	for g, w := range wantCount {
		page, err := b.List(bctx, BrowseQuery{Groups: []string{g}, Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Rows) != w {
			t.Errorf("筛 %s 得 %d 行，应 %d", g, len(page.Rows), w)
		}
		for _, r := range page.Rows {
			if r.Group != g {
				t.Errorf("筛 %s 却得到 %s 的行 %s", g, r.Group, r.Ref)
			}
		}
	}
	// 不认识的组名是参数错误，不是"没有"
	if _, err := b.List(bctx, BrowseQuery{Groups: []string{"nope"}}); !errors.Is(err, ErrBrowseParam) {
		t.Errorf("不认识的组名应是参数错误：%v", err)
	}
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// 筛选和分页：种类、范围类型、会话、关于谁各筛一遍；翻页不重不漏、顺序是新的在前、同一毫秒按 id
func TestBrowseFilterAndPaging(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	base := browseNow.Add(-10 * time.Hour).UnixMilli()
	for i := 0; i < 130; i++ {
		f := bf{id: fmt.Sprintf("f%03d", i), created: base + int64(i/3)*1000} // 三条一组同一毫秒
		switch i % 4 {
		case 0:
			f.kind = "promise"
		case 1:
			f.kind = "preference"
		}
		switch i % 5 {
		case 0:
			f.scope, f.scopes = "public", []string{"public"}
		case 1:
			f.scope, f.scopes = "conv:qq:group:g1", []string{"conv:qq:group:g1", "owner"}
		case 2:
			f.scope, f.scopes = "person:qq:u1", []string{"person:qq:u1"}
		}
		if i%7 == 0 {
			f.conv = "qq:group:g1"
		}
		if i%11 == 0 {
			f.subject = "owner"
		}
		x.put(f)
	}
	b := x.browser(BrowserOptions{})
	all := func(q BrowseQuery) []string {
		t.Helper()
		var out []string
		for page := 0; page < 20; page++ {
			p, err := b.List(bctx, q)
			if err != nil {
				t.Fatalf("%+v：%v", q, err)
			}
			if len(p.Rows) > BrowsePageSize {
				t.Fatalf("一页 %d 行，超过 %d", len(p.Rows), BrowsePageSize)
			}
			out = append(out, browseIDs(p.Rows)...)
			if p.Next == "" {
				return out
			}
			q.Cursor = p.Next
		}
		t.Fatal("翻不完")
		return nil
	}
	// 全部：130 条，新的在前，同一毫秒 id 大的在前
	got := all(BrowseQuery{})
	var want []string
	for i := 129; i >= 0; i-- {
		want = append(want, fmt.Sprintf("f%03d", i))
	}
	// 手算顺序：created 降序（i/3 大的在前），同组内 id 降序 —— 正好就是 i 从大到小
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("全部翻完的顺序不对：前 6 个 %v，共 %d 个", got[:6], len(got))
	}
	// 种类
	countKind := func(mod, eq int) (out []string) {
		for _, id := range want {
			var i int
			fmt.Sscanf(id, "f%d", &i)
			if i%mod == eq {
				out = append(out, id)
			}
		}
		return
	}
	if got := all(BrowseQuery{Kind: "promise"}); !reflect.DeepEqual(got, countKind(4, 0)) {
		t.Errorf("约定：%v", got)
	}
	if got := all(BrowseQuery{Kind: "preference"}); !reflect.DeepEqual(got, countKind(4, 1)) {
		t.Errorf("喜好：%v", got)
	}
	// 范围类型：i%5==0 public；==1 有 conv: 和 owner；==2 person:；3、4 只有 owner
	if got := all(BrowseQuery{ScopeKind: "public"}); !reflect.DeepEqual(got, countKind(5, 0)) {
		t.Errorf("所有场合：%v", got)
	}
	if got := all(BrowseQuery{ScopeKind: "conv"}); !reflect.DeepEqual(got, countKind(5, 1)) {
		t.Errorf("某个会话：%v", got)
	}
	if got := all(BrowseQuery{ScopeKind: "person"}); !reflect.DeepEqual(got, countKind(5, 2)) {
		t.Errorf("某人私聊：%v", got)
	}
	wantOwner := append(countKind(5, 1), append(countKind(5, 3), countKind(5, 4)...)...)
	slices.SortFunc(wantOwner, func(a, b string) int { return strings.Compare(b, a) })
	if got := all(BrowseQuery{ScopeKind: "owner"}); !reflect.DeepEqual(got, wantOwner) {
		t.Errorf("主人场合：%v", got)
	}
	// 会话、关于谁
	if got := all(BrowseQuery{Conversation: "qq:group:g1"}); !reflect.DeepEqual(got, countKind(7, 0)) {
		t.Errorf("会话：%v", got)
	}
	if got := all(BrowseQuery{Subject: "owner"}); !reflect.DeepEqual(got, countKind(11, 0)) {
		t.Errorf("关于谁：%v", got)
	}
	// 筛选是 AND
	got = all(BrowseQuery{Kind: "promise", ScopeKind: "public", Conversation: "qq:group:g1"})
	for _, id := range got {
		var i int
		fmt.Sscanf(id, "f%d", &i)
		if i%4 != 0 || i%5 != 0 || i%7 != 0 {
			t.Errorf("AND 筛选放进了 %s", id)
		}
	}
	// 一页大小：自己指定小一点
	p, err := b.List(bctx, BrowseQuery{Limit: 7})
	if err != nil || len(p.Rows) != 7 || p.Next == "" {
		t.Errorf("Limit 7：%d 行，next %q，%v", len(p.Rows), p.Next, err)
	}
	for _, bad := range []int{-1, BrowseSearchMax + 1} {
		if _, err := b.List(bctx, BrowseQuery{Limit: bad}); !errors.Is(err, ErrBrowseParam) {
			t.Errorf("Limit %d 应是参数错误：%v", bad, err)
		}
	}
	if _, err := b.List(bctx, BrowseQuery{Kind: "nope"}); !errors.Is(err, ErrBrowseParam) {
		t.Errorf("不认识的种类应是参数错误：%v", err)
	}
	if _, err := b.List(bctx, BrowseQuery{ScopeKind: "nope"}); !errors.Is(err, ErrBrowseParam) {
		t.Errorf("不认识的范围类型应是参数错误：%v", err)
	}
}

// 游标：别的筛选条件下发的、被篡改的、乱写的，都是参数错误（改坏检查：拆掉指纹比对，TestCursorBoundToFilter 就红）
func TestCursorBoundToFilter(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.bulk(func() {
		for i := 0; i < 60; i++ {
			x.put(bf{id: fmt.Sprintf("f%03d", i), created: browseNow.Add(-time.Duration(i) * time.Minute).UnixMilli(), kind: map[bool]string{true: "promise", false: "fact"}[i%2 == 0]})
		}
	})
	b := x.browser(BrowserOptions{})
	p, err := b.List(bctx, BrowseQuery{Kind: "fact", Limit: 10})
	if err != nil || p.Next == "" {
		t.Fatalf("取第一页：%v %q", err, p.Next)
	}
	if _, err := b.List(bctx, BrowseQuery{Kind: "fact", Limit: 10, Cursor: p.Next}); err != nil {
		t.Fatalf("同一筛选的游标应能用：%v", err)
	}
	bads := map[string]BrowseQuery{
		"别的种类":   {Kind: "promise", Limit: 10, Cursor: p.Next},
		"别的状态组":  {Kind: "fact", Limit: 10, Groups: []string{"owner"}, Cursor: p.Next},
		"别的搜索词":  {Kind: "fact", Limit: 10, Query: "x", Cursor: p.Next},
		"乱写":     {Kind: "fact", Limit: 10, Cursor: "garbage"},
		"只有一半":   {Kind: "fact", Limit: 10, Cursor: strings.Split(p.Next, ".")[0]},
		"篡改签名":   {Kind: "fact", Limit: 10, Cursor: strings.Split(p.Next, ".")[0] + ".AAAAAAAAAAAAAAAA"},
		"篡改正文":   {Kind: "fact", Limit: 10, Cursor: "eyJmIjoieCJ9." + strings.Split(p.Next, ".")[1]},
		"空签名":    {Kind: "fact", Limit: 10, Cursor: strings.Split(p.Next, ".")[0] + "."},
		"别的实例的":  {Kind: "fact", Limit: 10, Cursor: otherBrowserCursor(t)},
		"证据游标混用": {Kind: "fact", Limit: 10, Cursor: "x.y"},
	}
	for name, q := range bads {
		if _, err := b.List(bctx, q); !errors.Is(err, ErrBrowseParam) {
			t.Errorf("%s：应是参数错误，得到 %v", name, err)
		}
	}
}

func otherBrowserCursor(t *testing.T) string {
	x := newBrowseDB(t, true)
	x.bulk(func() {
		for i := 0; i < 60; i++ {
			x.put(bf{id: fmt.Sprintf("f%03d", i), created: browseNow.Add(-time.Duration(i) * time.Minute).UnixMilli()})
		}
	})
	p, err := x.browser(BrowserOptions{}).List(bctx, BrowseQuery{Kind: "fact", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return p.Next
}

// 搜索：命中、空结果、转义（% _ \ 按字面）、空词等于不搜、关于谁里也找、调用方给的人 id 也算
func TestSearchHitsEmptyAndEscapes(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a", text: "喜欢吃草莓蛋糕"})
	x.put(bf{id: "b", text: "百分之百的 100% 确定"})
	x.put(bf{id: "c", text: "下划线 a_b 不是 aXb"})
	x.put(bf{id: "d", text: `路径 C:\data\x 里`})
	x.put(bf{id: "e", text: "无关的话", subject: "qq:10001"})
	x.put(bf{id: "f", text: "别人说的", subject: "qq:10002"})
	x.put(bf{id: "g", text: "只有 aXb 这个"})
	b := x.browser(BrowserOptions{})
	search := func(q BrowseQuery) []string {
		t.Helper()
		p, err := b.List(bctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out := browseIDs(p.Rows)
		slices.Sort(out)
		return out
	}
	cases := []struct {
		name string
		q    BrowseQuery
		want []string
	}{
		{"命中", BrowseQuery{Query: "草莓"}, []string{"a"}},
		{"没有命中", BrowseQuery{Query: "不存在的词"}, []string{}},
		{"百分号按字面", BrowseQuery{Query: "100%"}, []string{"b"}},
		{"单个百分号只命中有百分号的", BrowseQuery{Query: "%"}, []string{"b"}},
		{"下划线按字面", BrowseQuery{Query: "a_b"}, []string{"c"}},
		{"下划线不当通配", BrowseQuery{Query: "aXb"}, []string{"c", "g"}},
		{"反斜杠按字面", BrowseQuery{Query: `\data`}, []string{"d"}},
		{"空词等于不搜", BrowseQuery{Query: ""}, []string{"a", "b", "c", "d", "e", "f", "g"}},
		{"全是空白等于不搜", BrowseQuery{Query: "   "}, []string{"a", "b", "c", "d", "e", "f", "g"}},
		{"关于谁里找", BrowseQuery{Query: "10001"}, []string{"e"}},
		{"调用方给的人 id", BrowseQuery{Query: "某个别名", SubjectIDs: []string{"qq:10002"}}, []string{"f"}},
		{"搜索加筛选是 AND", BrowseQuery{Query: "说", Subject: "qq:10002"}, []string{"f"}},
	}
	for _, c := range cases {
		if got := search(c.q); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s：得到 %v，应 %v", c.name, got, c.want)
		}
	}
	// 通配符真的没生效：搜 "_" 只命中含下划线的那一条
	if got := search(BrowseQuery{Query: "_"}); !reflect.DeepEqual(got, []string{"c"}) {
		t.Errorf("搜下划线：%v", got)
	}
}

// 搜索结果最多 200 条，多了标 Truncated、没有下一页
func TestSearchCapsAtTwoHundred(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.bulk(func() {
		for i := 0; i < 230; i++ {
			x.put(bf{id: fmt.Sprintf("f%03d", i), text: "同一个词", created: browseNow.Add(-time.Duration(i) * time.Second).UnixMilli()})
		}
	})
	b := x.browser(BrowserOptions{})
	p, err := b.List(bctx, BrowseQuery{Query: "同一个词"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != BrowseSearchMax || !p.Truncated || p.Next != "" {
		t.Errorf("搜索：%d 行，Truncated %v，Next %q", len(p.Rows), p.Truncated, p.Next)
	}
	// 刚好 200 条不算多
	x2 := newBrowseDB(t, true)
	x2.bulk(func() {
		for i := 0; i < BrowseSearchMax; i++ {
			x2.put(bf{id: fmt.Sprintf("f%03d", i), text: "同一个词"})
		}
	})
	p, err = x2.browser(BrowserOptions{}).List(bctx, BrowseQuery{Query: "同一个词"})
	if err != nil || len(p.Rows) != BrowseSearchMax || p.Truncated {
		t.Errorf("刚好 200 条：%d 行，Truncated %v，%v", len(p.Rows), p.Truncated, err)
	}
}

// 没迁移的 Bun 库：没有 confirmed_by 列、item_scopes、fact_evidence、before/after_json；片段没有 status。列表、计数、详情照样读
func TestBrowseUnmigrated(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, false)
	x.put(bf{id: "a", text: "旧库里的一条", scope: "public"})
	x.put(bf{id: "b", status: "candidate", scope: "conv:qq:group:g1"})
	x.put(bf{id: "c", status: "revoked"})
	x.episode("e1", "qq:group:g1", "一个片段", "owner", "active", 0, 3, 9)
	x.revision("a", "add", 1, "summary", "旧库的修订", "", "")
	b := x.browser(BrowserOptions{})
	sc, err := b.Schema(bctx)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Migrated() || sc.ConfirmedBy || sc.ItemScopes || sc.Evidence || sc.RevJSON {
		t.Errorf("没迁移的库被认成迁移过：%+v", sc)
	}
	if !sc.EpStatus || !sc.Revisions {
		t.Errorf("Bun 库本来就有的东西没认出来：%+v", sc)
	}
	c, err := b.Counts(bctx)
	if err != nil || c.Facts["fact"]["imported"] != 1 || c.Facts["fact"]["candidate"] != 1 || c.Facts["fact"]["revoked"] != 1 || c.Episodes["active"] != 1 || c.Total != 4 {
		t.Errorf("计数：%+v，%v", c, err)
	}
	p, err := b.List(bctx, BrowseQuery{})
	if err != nil || len(p.Rows) != 3 {
		t.Fatalf("列表：%d 行 %v", len(p.Rows), err)
	}
	for _, r := range p.Rows {
		if r.ConfirmedBy != "" || len(r.Scopes) != 1 {
			t.Errorf("%s：ConfirmedBy %q Scopes %v", r.Ref, r.ConfirmedBy, r.Scopes)
		}
		if r.Ref == "f:a" && (r.Group != GroupImported || r.Scopes[0] != "public") {
			t.Errorf("f:a：%+v", r)
		}
	}
	// 范围筛选退回 scope 列
	if p, err := b.List(bctx, BrowseQuery{ScopeKind: "conv"}); err != nil || !reflect.DeepEqual(browseIDs(p.Rows), []string{"b"}) {
		t.Errorf("没有范围组表时按 scope 列筛：%v %v", browseIDs(p.Rows), err)
	}
	d, err := b.Detail(bctx, "f:b")
	if err != nil || d.Occasions != nil || d.Recall.Reason != RecallOK {
		t.Errorf("详情：%+v %v", d, err)
	}
	ev, err := b.Evidence(bctx, "f:b", "")
	if err != nil || !ev.Missing || len(ev.Rows) != 0 {
		t.Errorf("旧库没有证据表应标 Missing：%+v %v", ev, err)
	}
	rv, err := b.Revisions(bctx, "f:a", "")
	if err != nil || len(rv.Rows) != 1 || rv.Rows[0].Reason != "旧库的修订" || rv.Rows[0].After.Status != "" {
		t.Errorf("没有 before/after_json 列的修订只写 op、谁、时间、原因：%+v %v", rv, err)
	}
	// 片段没有 status 列的库
	x2 := newBrowseDB(t, false)
	if _, err := x2.db.Exec(`DROP TABLE episodes; CREATE TABLE episodes (id TEXT PRIMARY KEY, conversation TEXT NOT NULL, text TEXT NOT NULL, visibility TEXT NOT NULL,
		from_seq INTEGER NOT NULL, to_seq INTEGER NOT NULL, created_at INTEGER NOT NULL, scope TEXT, origin TEXT, audience_at_origin TEXT, event_at INTEGER, learned_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := x2.db.Exec(`INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, scope) VALUES ('e9', 'c', 't', 'v', 1, 2, 3, 'owner')`); err != nil {
		t.Fatal(err)
	}
	b2 := x2.browser(BrowserOptions{})
	p, err = b2.List(bctx, BrowseQuery{Kind: "episode"})
	if err != nil || len(p.Rows) != 1 || p.Rows[0].Status != "active" || p.Rows[0].Group != GroupActive {
		t.Errorf("片段没有 status 列应全当有效：%+v %v", p.Rows, err)
	}
}

// 不认识的状态、种类：原样显示那个词，状态组归"其他"
func TestBrowseUnknownStatus(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a", status: "historical", kind: "mystery"})
	x.put(bf{id: "b", status: "active", by: "robot", hasBy: true})
	x.episode("e1", "c", "t", "owner", "disputed", 0, 1, 2)
	b := x.browser(BrowserOptions{})
	p, err := b.List(bctx, BrowseQuery{Groups: []string{GroupOther}})
	if err != nil || len(p.Rows) != 2 {
		t.Fatalf("其他：%v %v", p.Rows, err)
	}
	for _, r := range p.Rows {
		if r.Ref == "f:a" && (r.Status != "historical" || r.Kind != "mystery") {
			t.Errorf("原样显示那个词：%+v", r)
		}
		if r.Ref == "f:b" && r.ConfirmedBy != "robot" {
			t.Errorf("不认识的放行者原样带出来：%+v", r)
		}
	}
	p, err = b.List(bctx, BrowseQuery{Kind: "episode", Groups: []string{GroupOther}})
	if err != nil || len(p.Rows) != 1 || p.Rows[0].Status != "disputed" {
		t.Errorf("片段的其他：%+v %v", p.Rows, err)
	}
	d, _ := b.Detail(bctx, "f:a")
	if d.Recall.Can || d.Recall.Reason != RecallOther {
		t.Errorf("不认识的状态她想不起：%+v", d.Recall)
	}
}

// created_at 为 0 排在最后；learned_at 为 0 不影响
func TestBrowseZeroCreatedSortsLast(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a", created: 5000})
	x.put(bf{id: "b", created: 9000})
	if _, err := x.db.Exec(`INSERT INTO facts (id, kind, text, visibility, source_seqs, status, created_at) VALUES ('z', 'fact', 'zero', 'v', '[]', 'active', 0)`); err != nil {
		t.Fatal(err)
	}
	x.db.Exec(`INSERT INTO item_scopes (ref, scope) VALUES ('f:z', 'owner')`)
	p, err := x.browser(BrowserOptions{}).List(bctx, BrowseQuery{})
	if err != nil || !reflect.DeepEqual(browseIDs(p.Rows), []string{"b", "a", "z"}) {
		t.Errorf("顺序 %v %v", browseIDs(p.Rows), err)
	}
	if p.Rows[2].CreatedAt != 0 {
		t.Errorf("创建时间为 0 应原样给 0（界面写不详）：%d", p.Rows[2].CreatedAt)
	}
}

// 范围组：item_scopes 是真源，列表的 Scopes 和 scopesOf 一致；组里没有这一条退回 scope 列
func TestBrowseScopesFromGroupTable(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a", scope: "owner", scopes: []string{"conv:qq:group:g1", "owner"}})
	x.put(bf{id: "b", scope: "public"})
	if _, err := x.db.Exec(`DELETE FROM item_scopes WHERE ref = 'f:b'`); err != nil { // 迁移后 Bun 又写的：组里没有
		t.Fatal(err)
	}
	b := x.browser(BrowserOptions{})
	p, err := b.List(bctx, BrowseQuery{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, r := range p.Rows {
		got[r.Ref] = r.Scopes
	}
	if !reflect.DeepEqual(got["f:a"], []string{"owner", "conv:qq:group:g1"}) { // scopesOf：第一个值（scope 列）排最前，其余按字典序
		t.Errorf("f:a 的范围 %v", got["f:a"])
	}
	if !reflect.DeepEqual(got["f:b"], []string{"public"}) {
		t.Errorf("f:b 组里没有，应退回 scope 列：%v", got["f:b"])
	}
	// 筛选也一样：f:b 按 scope 列算 public
	p, _ = b.List(bctx, BrowseQuery{ScopeKind: "public"})
	if !reflect.DeepEqual(browseIDs(p.Rows), []string{"b"}) {
		t.Errorf("按 public 筛：%v", browseIDs(p.Rows))
	}
	// 范围为空串按最严的 owner
	x.put(bf{id: "c", scope: "-"})
	x.db.Exec(`UPDATE facts SET scope = '' WHERE id = 'c'`)
	x.db.Exec(`DELETE FROM item_scopes WHERE ref = 'f:c'`)
	p, _ = b.List(bctx, BrowseQuery{ScopeKind: "owner"})
	if !slices.Contains(browseIDs(p.Rows), "c") {
		t.Errorf("范围为空应按 owner：%v", browseIDs(p.Rows))
	}
}

// 片段：列表、详情、片段没有放行者
func TestBrowseEpisodes(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.episode("e1", "qq:group:g1", "群里聊了周末去哪玩", "conv:qq:group:g1", "active", browseNow.Add(-time.Hour).UnixMilli(), 10, 40)
	x.episode("e2", "qq:group:g1", "已撤销的一段", "owner", "revoked", browseNow.Add(-2*time.Hour).UnixMilli(), 41, 50)
	x.put(bf{id: "f1"})
	b := x.browser(BrowserOptions{})
	p, err := b.List(bctx, BrowseQuery{Kind: "episode"})
	if err != nil || !reflect.DeepEqual(browseIDs(p.Rows), []string{"e1", "e2"}) {
		t.Fatalf("片段列表 %v %v", browseIDs(p.Rows), err)
	}
	if p.Rows[0].Kind != "episode" || p.Rows[0].Group != GroupActive || p.Rows[0].FromSeq != 10 || p.Rows[0].ToSeq != 40 || p.Rows[1].Group != GroupRevoked {
		t.Errorf("片段字段 %+v", p.Rows)
	}
	if p, _ := b.List(bctx, BrowseQuery{Kind: "episode", Groups: []string{GroupRevoked}}); !reflect.DeepEqual(browseIDs(p.Rows), []string{"e2"}) {
		t.Errorf("已撤销的片段 %v", browseIDs(p.Rows))
	}
	if p, _ := b.List(bctx, BrowseQuery{Kind: "episode", ScopeKind: "conv"}); !reflect.DeepEqual(browseIDs(p.Rows), []string{"e1"}) {
		t.Errorf("片段按范围 %v", browseIDs(p.Rows))
	}
	if p, _ := b.List(bctx, BrowseQuery{Kind: "episode", Query: "周末"}); !reflect.DeepEqual(browseIDs(p.Rows), []string{"e1"}) {
		t.Errorf("片段搜索 %v", browseIDs(p.Rows))
	}
	// 事实列表里没有片段
	if p, _ := b.List(bctx, BrowseQuery{}); !reflect.DeepEqual(browseIDs(p.Rows), []string{"f1"}) {
		t.Errorf("事实列表混进了片段 %v", browseIDs(p.Rows))
	}
	c, _ := b.Counts(bctx)
	if c.Episodes["active"] != 1 || c.Episodes["revoked"] != 1 || c.Total != 3 {
		t.Errorf("片段计数 %+v", c)
	}
	d, err := b.Detail(bctx, "e:e1")
	if err != nil || d.Row.FromSeq != 10 || d.Row.ToSeq != 40 || d.Row.Conversation != "qq:group:g1" || !d.Recall.Can || d.Occasions != nil {
		t.Errorf("片段详情 %+v %v", d, err)
	}
	d, _ = b.Detail(bctx, "e:e2")
	if d.Recall.Can || d.Recall.Reason != RecallRevoked {
		t.Errorf("已撤销的片段她想不起 %+v", d.Recall)
	}
}

// ref 畸形：参数错误，不拼进 SQL
func TestRefValidation(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a"})
	b := x.browser(BrowserOptions{})
	for _, ref := range []string{"", "f:", "e:", "x:1", "f", "f:a b", "f:a'b", `f:a"b`, "f:a;DROP TABLE facts", "f:" + strings.Repeat("a", 81), "F:a", "f:a\n", "f: a", "f:a%", "f:\x00"} {
		if _, err := b.Detail(bctx, ref); !errors.Is(err, ErrBrowseParam) {
			t.Errorf("Detail(%q) 应是参数错误：%v", ref, err)
		}
		if _, err := b.Evidence(bctx, ref, ""); !errors.Is(err, ErrBrowseParam) {
			t.Errorf("Evidence(%q) 应是参数错误：%v", ref, err)
		}
		if _, err := b.Revisions(bctx, ref, ""); !errors.Is(err, ErrBrowseParam) {
			t.Errorf("Revisions(%q) 应是参数错误：%v", ref, err)
		}
	}
	// 片段没有证据、修订
	if _, err := b.Evidence(bctx, "e:a", ""); !errors.Is(err, ErrBrowseParam) {
		t.Errorf("片段的证据应是参数错误：%v", err)
	}
	if _, err := b.Detail(bctx, "f:nothere"); !errors.Is(err, ErrBrowseNotFound) {
		t.Errorf("不存在的应是没有这一条：%v", err)
	}
	if _, err := b.Detail(bctx, "f:a"); err != nil {
		t.Errorf("正常的取不到：%v", err)
	}
	// 库还在，没被注入的语句动过
	if c, err := b.Counts(bctx); err != nil || c.Total != 1 {
		t.Errorf("库被动过 %+v %v", c, err)
	}
}

// 来源序号：数组里非整数、负数、超大数、重复的要处理；不是数组当没有
func TestBrowseSourceSeqs(t *testing.T) {
	t.Parallel()
	long := "["
	for i := 0; i < 80; i++ {
		long += fmt.Sprintf("%d,", i+1)
	}
	long += "5]"
	cases := []struct {
		name  string
		raw   string
		want  []int64
		total int
	}{
		{"正常", `[3,4,5]`, []int64{3, 4, 5}, 3},
		{"空数组", `[]`, nil, 0},
		{"空串", ``, nil, 0},
		{"对象", `{"a":1}`, nil, 0},
		{"字符串", `"12"`, nil, 0},
		{"坏 JSON", `[1,2`, nil, 0},
		{"数字", `7`, nil, 0},
		{"null", `null`, nil, 0},
		{"混着非整数", `[1,"2",3.5,null,true,[4],{"a":5},6,1e3,7]`, []int64{1, 6, 7}, 3},
		{"负数", `[-1,0,2]`, []int64{0, 2}, 2},
		{"超大数", `[9007199254740993,99999999999999999999,5]`, []int64{5}, 1},
		{"重复", `[4,4,5,4,5]`, []int64{4, 5}, 2},
		{"超过 50 个", long, nil, 80},
	}
	for _, c := range cases {
		got, total := parseSourceSeqs(c.raw, "qq:group:g1")
		var seqs []int64
		for _, s := range got {
			seqs = append(seqs, s.Seq)
			if s.Conversation != "qq:group:g1" {
				t.Errorf("%s：会话 %q", c.name, s.Conversation)
			}
		}
		if c.name == "超过 50 个" {
			if len(seqs) != maxSourceSeqs || total != c.total {
				t.Errorf("%s：留 %d 个 总数 %d", c.name, len(seqs), total)
			}
			continue
		}
		if !reflect.DeepEqual(seqs, c.want) || total != c.total {
			t.Errorf("%s：得到 %v（总数 %d），应 %v（%d）", c.name, seqs, total, c.want, c.total)
		}
	}
}

// 变化轮询：别的连接提交以后 DataVersion 变；自己读不变
func TestDataVersionChangesOnForeignCommit(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a"})
	b := x.browser(BrowserOptions{})
	v0, err := b.DataVersion(bctx)
	if err != nil {
		t.Fatal(err)
	}
	b.List(bctx, BrowseQuery{})
	b.Counts(bctx)
	if v1, _ := b.DataVersion(bctx); v1 != v0 {
		t.Errorf("只读不该让版本变：%d → %d", v0, v1)
	}
	x.put(bf{id: "b"})
	if v2, _ := b.DataVersion(bctx); v2 == v0 {
		t.Errorf("别的连接提交以后版本该变：%d", v2)
	}
}

// 关了以后库文件能立刻删掉（Windows 上被占用是真问题）
func TestBrowserCloseReleasesFile(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a"})
	x.db.Close()
	b, err := OpenBrowser(x.file, BrowserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b.List(bctx, BrowseQuery{})
	b.DataVersion(bctx)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(x.file + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("关了以后删 %s：%v", suffix, err)
		}
	}
}
