package memory

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// 详情、证据、修订、最近变动、计数里的隔离、她想不想得起、文字清理、范围场合表、读写并发（规格-记忆页 3.2～3.5、4.4）

func TestDetailFieldsAndRecall(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	day := int64(24 * time.Hour / time.Millisecond)
	now := browseNow.UnixMilli()
	x.put(byOwner(bf{id: "ok", text: "喜欢草莓", subject: "owner", conv: "console:suzune", seqs: "[11,12]", scopes: []string{"owner", "public"}}))
	x.put(bf{id: "temp", temporal: "temporary", learned: now - 8*day, created: now - 8*day})  // 暂时的事，过了 7 天
	x.put(bf{id: "temp2", temporal: "temporary", learned: now - 2*day, created: now - 2*day}) // 暂时的事，还没过
	x.put(bf{id: "dated", validTo: now - 1000})                                               // 写了 valid_to，已过
	x.put(bf{id: "gone", status: "revoked"})
	x.put(bf{id: "old", status: "superseded", superseded: "ok"})
	x.put(bf{id: "cand", status: "candidate"})
	b := x.browser(BrowserOptions{})
	cases := map[string]struct {
		can    bool
		reason string
	}{
		"ok": {true, RecallOK}, "temp": {false, RecallExpired}, "temp2": {true, RecallOK}, "dated": {false, RecallExpired},
		"gone": {false, RecallRevoked}, "old": {false, RecallSuperseded}, "cand": {true, RecallOK},
	}
	for id, w := range cases {
		d, err := b.Detail(bctx, "f:"+id)
		if err != nil {
			t.Fatal(id, err)
		}
		if d.Recall.Can != w.can || d.Recall.Reason != w.reason {
			t.Errorf("%s：想得起 %v（%s），应 %v（%s）", id, d.Recall.Can, d.Recall.Reason, w.can, w.reason)
		}
	}
	d, _ := b.Detail(bctx, "f:ok")
	if d.Row.Subject != "owner" || d.Row.Conversation != "console:suzune" || d.Row.ConfirmedBy != "owner" || d.Row.Group != GroupOwner ||
		!reflect.DeepEqual(d.Row.Scopes, []string{"owner", "public"}) || d.Origin != "test" || d.SpeechAct != "assert" ||
		len(d.Sources) != 2 || d.Sources[1].Seq != 12 || d.Sources[0].Conversation != "console:suzune" || d.SourcesTotal != 2 {
		t.Errorf("字段：%+v", d)
	}
	d, _ = b.Detail(bctx, "f:old")
	if d.SupersededBy != "f:ok" {
		t.Errorf("被取代：指向 %q", d.SupersededBy)
	}
	// 没有出处记录
	d, _ = b.Detail(bctx, "f:gone")
	if len(d.Sources) != 0 || d.SourcesTotal != 0 {
		t.Errorf("source_seqs 为空应写没有出处：%+v", d.Sources)
	}
}

// 待确认的事实：已攒 N / M 次，用的是写入侧同一个计次函数和配置换算
func TestDetailOccasions(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "c1", status: "candidate", kind: "fact"})
	x.put(bf{id: "c2", status: "candidate", kind: "promise"})
	x.put(byOwner(bf{id: "act"}))
	x.evidence("c1", "2026-10-30", "qq:private:a:b", 5, "", 1, 1)
	x.evidence("c1", "2026-10-31", "qq:private:a:b", 6, "", 1, 2)
	x.evidence("c1", "2026-10-31", "qq:group:g1", 7, "qq:10002", 0, 3)
	x.evidence("c1", "2026-11-01", "qq:group:g1", 8, "QQ:10002", 0, 4)
	opt := BrowserOptions{ConfirmOccasions: 4, AutoConfirm: true}
	d, err := x.browser(opt).Detail(bctx, "f:c1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Occasions == nil || *d.Occasions != (Occasions{Have: 3, Need: 4, Self: 2, Rows: 4, Auto: true}) {
		t.Errorf("c1：%+v", d.Occasions)
	}
	// 配成 1 也按 2 算；promise 默认不自动提升
	d, _ = x.browser(BrowserOptions{ConfirmOccasions: 1, AutoConfirm: true}).Detail(bctx, "f:c2")
	if d.Occasions == nil || d.Occasions.Need != 2 || d.Occasions.Auto || d.Occasions.Have != 0 {
		t.Errorf("c2：%+v", d.Occasions)
	}
	// 没开自动提升
	d, _ = x.browser(BrowserOptions{}).Detail(bctx, "f:c1")
	if d.Occasions == nil || d.Occasions.Auto || d.Occasions.Need != 3 {
		t.Errorf("没开自动提升：%+v", d.Occasions)
	}
	// 非待确认的没有这一块
	if d, _ := x.browser(opt).Detail(bctx, "f:act"); d.Occasions != nil {
		t.Errorf("有效的事实不该有攒次数：%+v", d.Occasions)
	}
}

// 止血线：设了线以后详情写"止血线藏着它"，计数里有被藏起来的条数（口径同 QuarantineReport）
func TestQuarantineInBrowser(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	line := browseNow.Add(-2 * time.Hour).UnixMilli()
	before := line - 1000
	after := line + 1000
	x.put(bf{id: "oldimported", created: before})                  // 线之前、导入的：不藏
	x.put(bf{id: "newai", created: after})                         // 线之后创建：藏
	x.put(byOwner(bf{id: "newowner", created: after}))             // 线之后但你放行的：不藏
	x.put(byAI(bf{id: "oldai", created: before}))                  // 线之前创建、线之后被提升：藏
	x.put(byAI(bf{id: "oldai2", created: before}))                 // 线之前创建、线之前就提升了：不藏
	x.put(bf{id: "newcand", status: "candidate", created: after})  // 线之后的候选：藏
	x.put(bf{id: "revokednew", status: "revoked", created: after}) // 撤销的不进召回，不算被藏
	x.episode("e1", "c", "t", "owner", "active", after, 1, 2)      // 线之后的片段：藏
	x.episode("e2", "c", "t", "owner", "active", before, 1, 2)     // 线之前的片段：不藏
	x.revision("oldai", "confirm", after, "summary", "攒够", "", `{"status":"active"}`)
	x.revision("oldai2", "confirm", before, "summary", "攒够", "", `{"status":"active"}`)
	b := x.browser(BrowserOptions{QuarantineSince: line})
	want := map[string]bool{"oldimported": false, "newai": true, "newowner": false, "oldai": true, "oldai2": false, "newcand": true}
	for id, hidden := range want {
		d, err := b.Detail(bctx, "f:"+id)
		if err != nil {
			t.Fatal(err)
		}
		if got := d.Recall.Reason == RecallQuarantined; got != hidden {
			t.Errorf("%s：被藏 %v，应 %v（%+v）", id, got, hidden, d.Recall)
		}
	}
	c, err := b.Counts(bctx)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Quarantine.Active || c.Quarantine.Since != line || c.Quarantine.Facts != 2 || c.Quarantine.Candidates != 1 || c.Quarantine.Episodes != 1 {
		t.Errorf("隔离计数 %+v", c.Quarantine)
	}
	// 没设线：不藏、计数全零
	d, _ := x.browser(BrowserOptions{}).Detail(bctx, "f:newai")
	if d.Recall.Reason != RecallOK {
		t.Errorf("没设线不该藏：%+v", d.Recall)
	}
	if c, _ := x.browser(BrowserOptions{}).Counts(bctx); c.Quarantine.Active || c.Quarantine.Facts != 0 {
		t.Errorf("没设线计数该是零：%+v", c.Quarantine)
	}
}

// 证据：新的在前、每页 20 行、游标往下翻不重不漏；证据行自带的会话原样给出
func TestEvidencePaging(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a", status: "candidate"})
	x.bulk(func() {
		for i := 0; i < 45; i++ {
			x.evidence("a", fmt.Sprintf("2026-09-%02d", i%28+1), fmt.Sprintf("qq:group:g%d", i/28), int64(i), "qq:10002", 0, int64(i))
		}
	})
	b := x.browser(BrowserOptions{})
	var got []string
	cursor := ""
	pages := 0
	for {
		p, err := b.Evidence(bctx, "f:a", cursor)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if p.Total != 45 {
			t.Errorf("总数 %d", p.Total)
		}
		for _, e := range p.Rows {
			got = append(got, e.Day+"|"+e.Conversation)
			if e.Speaker != "qq:10002" || e.BySubject {
				t.Errorf("行 %+v", e)
			}
		}
		if p.Next == "" {
			break
		}
		cursor = p.Next
		if pages > 5 {
			t.Fatal("翻不完")
		}
	}
	if pages != 3 || len(got) != 45 {
		t.Errorf("%d 页 %d 行", pages, len(got))
	}
	seen := map[string]bool{}
	for _, g := range got {
		if seen[g] {
			t.Errorf("重复 %s", g)
		}
		seen[g] = true
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] < got[i] { // day 降序，同一天 conversation 降序
			t.Errorf("顺序不对 %s 在 %s 前面", got[i-1], got[i])
		}
	}
	if _, err := b.Evidence(bctx, "f:a", "garbage"); err == nil {
		t.Error("乱写的游标应报错")
	}
	// 别的事实的游标不能用
	x.put(bf{id: "z"})
	p, _ := b.Evidence(bctx, "f:a", "")
	if _, err := b.Evidence(bctx, "f:z", p.Next); err == nil {
		t.Error("别的事实的证据游标应报错")
	}
}

// 修订：新的在上；前后小 JSON 读出状态、放行者、范围；读不懂的当没记；每页 20 行
func TestRevisionsPagingAndJSON(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a"})
	x.revision("a", "add", 100, "summary", "整理写下", ``, `{"status":"candidate","scopes":["owner","conv:qq:group:g1"]}`)
	x.revision("a", "confirm", 200, "summary", "攒够 3 次不同场合", `{"status":"candidate","evidence_rows":3}`, `{"status":"active","confirmed_by":"ai","evidence_rows":3,"occasions":3}`)
	x.revision("a", "confirm", 300, "remember", "主人本人又说了一遍", `{"status":"active","confirmed_by":null}`, `{"status":"active","confirmed_by":"owner"}`)
	x.revision("a", "scopes", 350, "remember", "", `{"scopes":["owner"]}`, `{"scopes":["owner","public"]}`)
	x.revision("a", "supersede", 400, "summary", "被 f2 替换", `{"status":"active"}`, `{"status":"superseded","superseded_by":"f2"}`)
	x.revision("a", "revoke", 500, "owner", "", `not json`, `[1,2]`)
	x.revision("a", "migrate", 600, "migration", "", ``, ``)
	b := x.browser(BrowserOptions{})
	p, err := b.Revisions(bctx, "f:a", "")
	if err != nil || len(p.Rows) != 7 || p.Next != "" {
		t.Fatalf("%d 行 next %q %v", len(p.Rows), p.Next, err)
	}
	ops := []string{}
	for _, r := range p.Rows {
		ops = append(ops, r.Op)
	}
	if !reflect.DeepEqual(ops, []string{"migrate", "revoke", "supersede", "scopes", "confirm", "confirm", "add"}) {
		t.Errorf("顺序（新的在上）%v", ops)
	}
	byOp := func(op string, n int) RevisionRow {
		for _, r := range p.Rows {
			if r.Op == op {
				if n == 0 {
					return r
				}
				n--
			}
		}
		t.Fatalf("没有 %s", op)
		return RevisionRow{}
	}
	if r := byOp("add", 0); r.After.Status != "candidate" || !reflect.DeepEqual(r.After.Scopes, []string{"owner", "conv:qq:group:g1"}) || !r.After.HasScopes || r.Before.Status != "" || r.By != "summary" || r.Reason != "整理写下" {
		t.Errorf("add：%+v", r)
	}
	if r := byOp("confirm", 1); r.Before.Status != "candidate" || r.After.Status != "active" || r.After.ConfirmedBy != "ai" || !r.After.HasConfirmedBy || r.After.Occasions != 3 {
		t.Errorf("提升：%+v", r)
	}
	if r := byOp("confirm", 0); !r.Before.HasConfirmedBy || r.Before.ConfirmedBy != "" || r.After.ConfirmedBy != "owner" {
		t.Errorf("放行：%+v", r)
	}
	if r := byOp("supersede", 0); r.After.SupersededBy != "f2" || r.After.Status != "superseded" {
		t.Errorf("取代：%+v", r)
	}
	if r := byOp("revoke", 0); !reflect.DeepEqual(r.Before, RevisionState{}) || r.After.Status != "" {
		t.Errorf("读不懂的前后 JSON 当没记：%+v", r)
	}
	// 分页
	for i := 0; i < 45; i++ {
		x.revision("a", "scopes", int64(1000+i), "owner", "", ``, ``)
	}
	var total int
	cursor := ""
	for pages := 0; ; pages++ {
		p, err := b.Revisions(bctx, "f:a", cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Rows) > BrowseSubPage {
			t.Fatalf("一页 %d 行", len(p.Rows))
		}
		total += len(p.Rows)
		if p.Next == "" {
			break
		}
		cursor = p.Next
		if pages > 5 {
			t.Fatal("翻不完")
		}
	}
	if total != 52 {
		t.Errorf("共 %d 行，应 52", total)
	}
}

// 最近变动：修订 id 倒序前 50 行，带事实正文开头；事实不在了（真删后只剩 purged）Exists 为假
func TestRecentChanges(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a", text: "喜欢吃草莓\n还爱喝茶"})
	for i := 0; i < 60; i++ {
		x.revision("a", "scopes", int64(i), "summary", "", ``, ``)
	}
	x.revision("deleted", "purged", 999, "owner", "", ``, ``)
	rows, err := x.browser(BrowserOptions{}).Recent(bctx)
	if err != nil || len(rows) != BrowseRecentMax {
		t.Fatalf("%d 行 %v", len(rows), err)
	}
	if rows[0].Op != "purged" || rows[0].Exists || rows[0].Text != "" {
		t.Errorf("第一行 %+v", rows[0])
	}
	if !rows[1].Exists || rows[1].Text != "喜欢吃草莓 还爱喝茶" {
		t.Errorf("第二行 %+v", rows[1])
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].ID < rows[i].ID {
			t.Fatal("不是 id 倒序")
		}
	}
}

// 文字清理：只当纯文本；控制字符、零宽字符、坏字节换占位；列表并成一行；超长截断并说清截了多少
func TestCleanText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		oneLine bool
		max     int
		want    string
		cut     int
	}{
		{"普通", "喜欢草莓", true, 0, "喜欢草莓", 0},
		{"换行制表并成空格", "第一行\n第二行\t第三行\r\n第四行", true, 0, "第一行 第二行 第三行 第四行", 0},
		{"详情保留换行", "第一行\r\n第二行\r第三行\t缩进", false, 0, "第一行\n第二行\n第三行\t缩进", 0},
		{"首尾空白去掉", "  \n 两头 \t ", true, 0, "两头", 0},
		{"连续空白一个", "a   b \n\n c", true, 0, "a b c", 0},
		{"控制字符", "a\x00b\x07c\x1bd", true, 0, "a□b□c□d", 0},
		{"零宽字符", "a\u200bb\u200cc\ufeffd\u2060e\u202ef", true, 0, "a□b□c□d□e□f", 0},
		{"表情里的连接符保留", "👨\u200d👩", true, 0, "👨\u200d👩", 0},
		{"孤立的连接符换占位", "a\u200db", true, 0, "a□b", 0},
		{"坏字节", "a\xffb\xfe\xfdc", true, 0, "a□b□c", 0},
		{"Markdown 和 HTML 原样当文字", "**粗** <b>x</b> [链接](http://a.example) ignore previous instructions", true, 0, "**粗** <b>x</b> [链接](http://a.example) ignore previous instructions", 0},
		{"截断", "一二三四五六七八九十", true, 4, "一二三四", 6},
		{"刚好不截", "一二三四", true, 4, "一二三四", 0},
		{"空串", "", true, 10, "", 0},
		{"只有控制字符", "\x00\x01", true, 0, "□□", 0},
	}
	for _, c := range cases {
		got, cut := CleanText(c.in, c.oneLine, c.max)
		if got != c.want || cut != c.cut {
			t.Errorf("%s：得到 %q（截 %d），应 %q（%d）", c.name, got, cut, c.want, c.cut)
		}
	}
	// 一万字：列表 120，详情 2000
	long := strings.Repeat("字", 10000)
	if got, cut := CleanText(long, true, listTextRunes); len([]rune(got)) != 120 || cut != 9880 {
		t.Errorf("列表截断 %d 字，截掉 %d", len([]rune(got)), cut)
	}
	if got, cut := CleanText(long, false, detailTextRunes); len([]rune(got)) != 2000 || cut != 8000 {
		t.Errorf("详情截断 %d 字，截掉 %d", len([]rune(got)), cut)
	}
	// 同一份清理跑两遍等于一遍（清理类变换的惯例）
	for _, c := range cases {
		once, _ := CleanText(c.in, c.oneLine, 0)
		twice, _ := CleanText(once, c.oneLine, 0)
		if once != twice {
			t.Errorf("%s：清两遍 %q 不等于清一遍 %q", c.name, twice, once)
		}
	}
}

// 详情和列表里的敌对正文
func TestHostileTextInRows(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "a", text: "line1\nline2\u200b\x00" + strings.Repeat("长", 3000)})
	b := x.browser(BrowserOptions{})
	p, _ := b.List(bctx, BrowseQuery{})
	if strings.ContainsAny(p.Rows[0].Text, "\n\x00\u200b") || len([]rune(p.Rows[0].Text)) != listTextRunes || p.Rows[0].TextCut == 0 {
		t.Errorf("列表正文 %q 截 %d", p.Rows[0].Text, p.Rows[0].TextCut)
	}
	d, _ := b.Detail(bctx, "f:a")
	if !strings.Contains(d.Row.Text, "line1\nline2□□") || len([]rune(d.Row.Text)) != detailTextRunes || d.Row.TextCut == 0 {
		t.Errorf("详情正文开头 %q 截 %d", string([]rune(d.Row.Text)[:20]), d.Row.TextCut)
	}
}

// 范围标签保留，但四种场合均能想起；坏标签也不能恢复旧的场合过滤。
func TestScopePlacesTable(t *testing.T) {
	t.Parallel()
	for _, scopes := range [][]string{{"public"}, {"owner"}, {"conv:qq:group:g1"}, {"person:qq:u1"}, {""}, {"conv:"}, {"unknown"}} {
		got := ScopePlaces(scopes)
		if len(got) != 4 {
			t.Fatalf("场合缺失：%v", got)
		}
		for _, g := range got {
			if g.Visible != VisYes || len(g.When) != 0 {
				t.Errorf("%v · %s：%+v", scopes, g.Place, g)
			}
		}
	}
}

// 读不挡写：边翻页边让后台的写入连接写，翻页成功、写不超时。WAL 和 DELETE 两种日志模式各跑一次
// （Bun 建的库是 WAL；Go 的副本经 VACUUM INTO / 拷贝，日志模式不确定，两种都得行，规格-记忆页 4.2）
func TestBrowseWhileWriting(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"WAL", "DELETE"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			file := newBunDB(t, func(db *sql.DB) {
				if _, err := db.Exec(`PRAGMA journal_mode=` + mode); err != nil {
					t.Fatal(err)
				}
			})
			if err := Migrate(bctx, file); err != nil {
				t.Fatal(err)
			}
			m, _ := openW(t, file, nil)
			for i := 0; i < 120; i++ {
				mustApply(t, m, Batch{By: BySummary, Ops: []Op{candidate(fmt.Sprintf("编的候选第 %d 条", i), "owner", "owner")}})
			}
			b, err := OpenBrowser(file, BrowserOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			stop := make(chan struct{})
			wg.Add(1)
			go func() { // 读：不停翻页、数数、取版本
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					q := BrowseQuery{Limit: 20}
					for {
						p, err := b.List(bctx, q)
						if err != nil {
							errs <- fmt.Errorf("翻页：%w", err)
							return
						}
						if p.Next == "" {
							break
						}
						q.Cursor = p.Next
					}
					if _, err := b.Counts(bctx); err != nil {
						errs <- fmt.Errorf("计数：%w", err)
						return
					}
					if _, err := b.DataVersion(bctx); err != nil {
						errs <- fmt.Errorf("版本：%w", err)
						return
					}
				}
			}()
			start := time.Now()
			for i := 0; i < 40; i++ {
				mustApply(t, m, Batch{By: BySummary, Ops: []Op{candidate(fmt.Sprintf("读的时候写的第 %d 条", i), "owner", "owner")}})
			}
			elapsed := time.Since(start)
			close(stop)
			wg.Wait()
			select {
			case err := <-errs:
				t.Fatal(err)
			default:
			}
			if elapsed > 20*time.Second {
				t.Errorf("边读边写 40 次用了 %v，写被读卡住了", elapsed)
			}
			c, err := b.Counts(bctx)
			if err != nil || c.Facts["fact"]["candidate"] != 160 {
				t.Errorf("写完以后读到 %+v %v", c.Facts, err)
			}
		})
	}
}

// 详情里的"攒够会不会自己提升"问写入侧现在的实际状态（AutoNow），不只看配置（AutoConfirm）：
// 配置写着开、整理却没开成（或只看历史）时，不能说"攒够就会自己提升"
func TestDetailOccasionsAutoNowOverridesConfig(t *testing.T) {
	t.Parallel()
	x := newBrowseDB(t, true)
	x.put(bf{id: "c1", status: "candidate", kind: "fact"})
	x.evidence("c1", "2026-10-30", "qq:private:a:b", 5, "", 1, 1)
	live := false
	opt := BrowserOptions{ConfirmOccasions: 4, AutoConfirm: true, AutoNow: func() bool { return live }}
	b := x.browser(opt)
	for _, c := range []struct {
		live, want bool
	}{{false, false}, {true, true}, {false, false}} {
		live = c.live
		d, err := b.Detail(bctx, "f:c1")
		if err != nil || d.Occasions == nil || d.Occasions.Auto != c.want {
			t.Errorf("写入侧 auto=%v 时详情应是 %v：%+v %v", c.live, c.want, d.Occasions, err)
		}
	}
	// 配置没开、写入侧却开着（不该发生）：以写入侧为准
	live = true
	d, _ := x.browser(BrowserOptions{ConfirmOccasions: 4, AutoNow: func() bool { return live }}).Detail(bctx, "f:c1")
	if d.Occasions == nil || !d.Occasions.Auto {
		t.Errorf("AutoNow 为真应盖过 AutoConfirm=false：%+v", d.Occasions)
	}
}

// Memory.AutoConfirming：只有可写并且 AutoConfirm 为真才为真；只读打开配了 AutoConfirm 也是假
func TestAutoConfirming(t *testing.T) {
	t.Parallel()
	on, _ := newWritableWith(t, autoConfirm)
	if !on.AutoConfirming() {
		t.Error("可写且 AutoConfirm：该为真")
	}
	off, _ := newWritableWith(t, nil)
	if off.AutoConfirming() {
		t.Error("可写但 AutoConfirm 没开：该为假")
	}
	ro, err := Open(context.Background(), migratedDB(t, nil), nil, nil, Options{AutoConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if ro.AutoConfirming() {
		t.Error("只读打开：该为假")
	}
}
