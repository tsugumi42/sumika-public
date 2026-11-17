package store

import (
	"strings"
	"testing"
)

// 测试里的地址、号、文字都是编的

const (
	urlA   = "https://multimedia.nt.qq.com.cn/download?appid=1&fileid=编的A&rkey=编的rkey甲"
	urlB   = "https://gchat.qpic.cn/gchatpic_new/1/2/3/0?term=2"
	hashX  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashY  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	kindIm = "image"
)

// 一条带了各种字节的 segments_json：中文、不转义的 & < >、emoji、U+2028、控制字符转义、未知字段、小数、空白、未知的键
func nastyBody(segs string) string {
	return `{"segs":` + segs + `,"from":{"id":"10001","nick":"编的昵称 & <b>","card":""},"tags":["backfill"],"line":"编的一行\u001f 😀  ","x_new":{"a":[1,2,3.50,1e3],"b":null},"import_meta":{"version":1,"legacy_segments":[{"hash":""}]}}`
}

func segImage(extra string) string {
	return `{"kind":"image","text":"","url":"` + urlA + `","file":"编的.jpg","x_seg":{"k":[1,2.50]}` + extra + `}`
}

// M11：回填只动目标段的 hash，别的字节原样；带了未知字段、特殊字符、小数的 segments_json 前后逐字节比较
func TestSpliceSegHashKeepsOtherBytes(t *testing.T) {
	t.Parallel()
	other := `{"kind":"text","text":"编的话 & <> 😀 \u001f"}`
	seg := segImage("")
	body := nastyBody(`[` + other + `,` + seg + `,{"kind":"emoji","url":"` + urlB + `"}]`)
	got, ok := spliceSegHash(body, 1, kindIm, MediaURLSig(urlA), hashX)
	if !ok {
		t.Fatal("条件都对却没写")
	}
	want := strings.Replace(body, seg, strings.TrimSuffix(seg, "}")+`,"hash":"`+hashX+`"}`, 1)
	if got != want {
		t.Fatalf("字节对不上：\n要 %s\n有 %s", want, got)
	}
	// 去掉插入的那一段，必须回到原文
	if back := strings.Replace(got, `,"hash":"`+hashX+`"`, "", 1); back != body {
		t.Fatalf("去掉 hash 以后和原文不一样：\n%s\n%s", back, body)
	}
}

func TestSpliceSegHashShapes(t *testing.T) {
	t.Parallel()
	sig := MediaURLSig(urlA)
	imageEmptyHash := `{"kind":"image","hash":"","url":"` + urlA + `"}`
	cases := []struct {
		name  string
		body  string
		idx   int
		kind  string
		sig   string
		hash  string
		want  string // 空 = 不该写
		check bool
	}{
		{"已有空 hash 键：换值", nastyBody(`[` + imageEmptyHash + `]`), 0, "image", sig, hashX, strings.Replace(nastyBody(`[`+imageEmptyHash+`]`), `"hash":""`, `"hash":"`+hashX+`"`, 1), true},
		{"hash 是 null：当成没有", `{"segs":[{"kind":"image","hash":null,"url":"` + urlA + `"}]}`, 0, "image", sig, hashX, `{"segs":[{"kind":"image","hash":"` + hashX + `","url":"` + urlA + `"}]}`, true},
		{"已经有 hash：不盖", nastyBody(`[{"kind":"image","hash":"` + hashY + `","url":"` + urlA + `"}]`), 0, "image", sig, hashX, "", true},
		{"hash 不是字符串：不盖", nastyBody(`[{"kind":"image","hash":5,"url":"` + urlA + `"}]`), 0, "image", sig, hashX, "", true},
		{"下标越界", nastyBody(`[` + segImage("") + `]`), 1, "image", sig, hashX, "", true},
		{"下标为负", nastyBody(`[` + segImage("") + `]`), -1, "image", sig, hashX, "", true},
		{"种类对不上", nastyBody(`[` + segImage("") + `]`), 0, "emoji", sig, hashX, "", true},
		{"那一段是文字", nastyBody(`[{"kind":"text","text":"x","url":"` + urlA + `"}]`), 0, "image", sig, hashX, "", true},
		{"地址变了", nastyBody(`[` + segImage("") + `]`), 0, "image", MediaURLSig(urlB), hashX, "", true},
		{"没有地址", nastyBody(`[{"kind":"image","file":"x.jpg"}]`), 0, "image", sig, hashX, "", true},
		{"没有 segs", `{"from":{"id":"1"}}`, 0, "image", sig, hashX, "", true},
		{"segs 不是数组", `{"segs":{"kind":"image"}}`, 0, "image", sig, hashX, "", true},
		{"不是 JSON", `这不是 JSON`, 0, "image", sig, hashX, "", true},
		{"空串", ``, 0, "image", sig, hashX, "", true},
		{"hash 不合法也不写", nastyBody(`[` + segImage("") + `]`), 0, "image", sig, "ZZ", "", true},
		{"段里带空白", `{ "segs" : [ { "kind" : "image" , "url" : "` + urlA + `" } ] }`, 0, "image", sig, hashX, `{ "segs" : [ { "kind" : "image" , "url" : "` + urlA + `","hash":"` + hashX + `" } ] }`, true},
	}
	for _, c := range cases {
		got, ok := spliceSegHash(c.body, c.idx, c.kind, c.sig, c.hash)
		if c.want == "" {
			if ok || got != "" {
				t.Errorf("%s：不该写，却写了 %q", c.name, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s：\n要 %s\n有 %s（ok=%v）", c.name, c.want, got, ok)
		}
	}
}

// 段里的 hash 在最后一位以外的位置、segs 不是第一个键：照样只改目标
func TestSpliceSegHashFindsSegsAnywhere(t *testing.T) {
	t.Parallel()
	body := `{"from":{"segs":[1,2,3]},"tags":[],"segs":[{"hash":"","kind":"emoji","url":"` + urlA + `"}]}`
	got, ok := spliceSegHash(body, 0, "emoji", MediaURLSig(urlA), hashX)
	if !ok || got != strings.Replace(body, `"hash":""`, `"hash":"`+hashX+`"`, 1) {
		t.Fatalf("%v %s", ok, got)
	}
}

func newMediaEvent(t *testing.T, st *Store, conv, platformID string, createdAt int64, segs string) int64 {
	t.Helper()
	seq, err := st.AppendEvent(NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:10001", Origin: "peer", Kind: "message",
		Text: "t", Segments: rawJSON(nastyBody(segs)), CreatedAt: createdAt}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if platformID != "" {
		if _, err := st.Events.Exec(`INSERT INTO channel_msgs (channel, conversation, platform_id, event_seq) VALUES ('qq', ?, ?, ?)`, conv, platformID, seq); err != nil {
			t.Fatal(err)
		}
	}
	return seq
}

// rawJSON 让 JSString 原样写一段已经写好的 JSON（测试要精确的字节）
type rawJSON string

func (r rawJSON) MarshalJSON() ([]byte, error) { return []byte(r), nil }

func bodyOf(t *testing.T, st *Store, seq int64) string {
	t.Helper()
	var b string
	if err := st.Events.QueryRow(`SELECT segments_json FROM events WHERE seq = ?`, seq).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

// M11 的库一半：真的写进库、通知界面、前后字节只差一个 hash；段变了、已经有了就不写
func TestSetSegHashInDB(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	st, _ := set.Platform("qq")
	conv := "qq:private:10000:10001"
	sub := st.SubscribeChanges()
	defer sub.Close()
	seq := newMediaEvent(t, st, conv, "-90010", 1_700_000_000_000, `[`+segImage("")+`]`)
	before := bodyOf(t, st, seq)
	<-sub.Wake()
	sub.Take()

	ok, err := st.SetSegHash(seq, 0, kindIm, MediaURLSig(urlB), hashX)
	if err != nil || ok {
		t.Fatalf("地址指纹不对不该写：%v %v", ok, err)
	}
	if bodyOf(t, st, seq) != before {
		t.Fatal("没写却改了库")
	}
	ok, err = st.SetSegHash(seq, 0, kindIm, MediaURLSig(urlA), hashX)
	if err != nil || !ok {
		t.Fatalf("应该写成：%v %v", ok, err)
	}
	after := bodyOf(t, st, seq)
	if strings.Replace(after, `,"hash":"`+hashX+`"`, "", 1) != before || after == before {
		t.Fatalf("前后不只差一个 hash：\n%s\n%s", before, after)
	}
	select {
	case <-sub.Wake():
		if !sub.Take()[conv] {
			t.Fatal("通知里没有这个会话")
		}
	default:
		t.Fatal("写了 hash 没通知界面")
	}
	if ok, err := st.SetSegHash(seq, 0, kindIm, MediaURLSig(urlA), hashY); err != nil || ok {
		t.Fatalf("已经有 hash 不许盖：%v %v", ok, err)
	}
	if bodyOf(t, st, seq) != after {
		t.Fatal("第二次写改了库")
	}
	if ok, err := st.SetSegHash(seq+99, 0, kindIm, MediaURLSig(urlA), hashX); err != nil || ok {
		t.Fatalf("事件不在：%v %v", ok, err)
	}
	if _, err := st.SetSegHash(seq, 0, kindIm, MediaURLSig(urlA), "短"); err == nil {
		t.Fatal("hash 不合法要报错")
	}
	// 事件没有 segments_json
	nullSeq, err := st.AppendEvent(NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:10001", Kind: "message", Text: "t", CreatedAt: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SetSegHash(nullSeq, 0, kindIm, MediaURLSig(urlA), hashX); err != nil || ok {
		t.Fatalf("没有 segments_json：%v %v", ok, err)
	}
}

func TestMediaFetchRowsAndRef(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	st, _ := set.Platform("qq")
	conv := "qq:group:90002"
	seq := newMediaEvent(t, st, conv, "-77", 1_700_000_000_000, `[`+segImage("")+`]`)
	if c, id, ok, err := st.EventRef(seq); err != nil || !ok || c != conv || id != "-77" {
		t.Fatalf("EventRef：%q %q %v %v", c, id, ok, err)
	}
	noID := newMediaEvent(t, st, conv, "", 1_700_000_000_000, `[`+segImage("")+`]`)
	if c, id, ok, err := st.EventRef(noID); err != nil || !ok || c != conv || id != "" {
		t.Fatalf("没有平台号的事件：%q %q %v %v", c, id, ok, err)
	}
	if _, _, ok, err := st.EventRef(seq + 100); err != nil || ok {
		t.Fatalf("不存在：%v %v", ok, err)
	}
	row := MediaFetch{EventSeq: seq, Idx: 0, Kind: kindIm, URLSig: MediaURLSig(urlA), State: MediaPending, Tries: 1, NextAt: 5, UpdatedAt: 6}
	if err := st.PutMediaFetch(row); err != nil {
		t.Fatal(err)
	}
	row.State, row.Hash, row.Tries, row.LastError = MediaDone, hashX, 2, ""
	if err := st.PutMediaFetch(row); err != nil {
		t.Fatal(err)
	}
	got, err := st.MediaFetchRows(seq)
	if err != nil || len(got) != 1 || got[0] != row {
		t.Fatalf("读回来不一样：%+v %v", got, err)
	}
	if err := st.PutMediaFetch(MediaFetch{EventSeq: 0, State: "x"}); err == nil {
		t.Fatal("没有事件号要报错")
	}
}

// 补扫挑谁：有地址、没 hash、没有终局状态的；导入来的旧段（没地址）、已有 hash、终局状态、时间太早、JSON 坏的都不挑
func TestMediaCandidates(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	st, _ := set.Platform("qq")
	conv := "qq:private:10000:10001"
	const now = 1_700_000_000_000
	pick := newMediaEvent(t, st, conv, "1", now, `[`+segImage("")+`]`)
	hashed := newMediaEvent(t, st, conv, "2", now, `[{"kind":"image","hash":"`+hashX+`","url":"`+urlA+`"}]`)
	imported := newMediaEvent(t, st, conv, "3", now, `[{"kind":"image","hash":""}]`)
	expired := newMediaEvent(t, st, conv, "4", now, `[`+segImage("")+`]`)
	failed := newMediaEvent(t, st, conv, "5", now, `[{"kind":"text","text":"x"},{"kind":"emoji","url":"`+urlB+`"}]`)
	pending := newMediaEvent(t, st, conv, "6", now, `[`+segImage("")+`]`)
	tooOld := newMediaEvent(t, st, conv, "7", now-10*86_400_000, `[`+segImage("")+`]`)
	text := newMediaEvent(t, st, conv, "8", now, `[{"kind":"text","text":"x","url":"`+urlA+`"}]`)
	// 没有任何角色看到的（陌生人私聊、自检期间的）：有地址没 hash 也挑
	stranger, err := st.AppendEvent(NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:10009", Origin: "peer", Kind: "message", Text: "t",
		Segments: rawJSON(nastyBody(`[` + segImage("") + `]`)), CreatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 一个事件两张图，一张终局、一张还要做：还是要挑
	mixed := newMediaEvent(t, st, conv, "9", now, `[`+segImage("")+`,`+segImage("")+`]`)
	if _, err := st.Events.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, segments_json, created_at) VALUES ('bad','qq',?, 'human','x','message','t','{坏的', ?)`, conv, now); err != nil {
		t.Fatal(err)
	}
	put := func(seq int64, idx int, state string) {
		if err := st.PutMediaFetch(MediaFetch{EventSeq: seq, Idx: idx, Kind: kindIm, URLSig: "x", State: state, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	put(expired, 0, MediaExpired)
	put(failed, 1, MediaFailed)
	put(pending, 0, MediaPending)
	put(mixed, 0, MediaDone)

	var got []int64
	after := int64(0)
	for {
		page, err := st.MediaCandidates(now-7*86_400_000, after, 2, 0) // 小页：翻页要接得上
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			got = append(got, e.Seq)
			after = e.Seq
		}
	}
	want := []int64{pick, failed, pending, stranger, mixed}
	if len(got) != len(want) {
		t.Fatalf("挑出 %v，要 %v（hashed=%d imported=%d expired=%d tooOld=%d text=%d stranger=%d）", got, want, hashed, imported, expired, tooOld, text, stranger)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("挑出 %v，要 %v", got, want)
		}
	}
}

// FinishSegHash：回填和 media_fetch 的行在同一个事务里 —— 回填成了行在，回填没成（段变了、事件没了）行也在
func TestFinishSegHashWritesRowInSameTx(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	st, _ := set.Platform("qq")
	conv := "qq:private:10000:10001"
	seq := newMediaEvent(t, st, conv, "1", 1_700_000_000_000, `[`+segImage("")+`]`)
	row := func(s int64) MediaFetch {
		return MediaFetch{EventSeq: s, Idx: 0, Kind: kindIm, URLSig: MediaURLSig(urlA), State: MediaDone, UpdatedAt: 5, Hash: hashX}
	}
	ok, err := st.FinishSegHash(seq, 0, kindIm, MediaURLSig(urlA), hashX, row(seq))
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if rows, _ := st.MediaFetchRows(seq); rows[0] != row(seq) {
		t.Fatalf("行：%+v", rows)
	}
	if !strings.Contains(bodyOf(t, st, seq), `"hash":"`+hashX+`"`) {
		t.Fatal("没回填")
	}
	// 段已经有 hash：回填不成，行照写
	seq2 := newMediaEvent(t, st, conv, "2", 1_700_000_000_000, `[{"kind":"image","hash":"`+hashY+`","url":"`+urlA+`"}]`)
	before := bodyOf(t, st, seq2)
	ok, err = st.FinishSegHash(seq2, 0, kindIm, MediaURLSig(urlA), hashX, row(seq2))
	if err != nil || ok || bodyOf(t, st, seq2) != before {
		t.Fatalf("%v %v", ok, err)
	}
	if rows, _ := st.MediaFetchRows(seq2); rows[0].State != MediaDone {
		t.Fatalf("行：%+v", rows)
	}
	// 事件不在：不报错，行照写
	if ok, err = st.FinishSegHash(9999, 0, kindIm, MediaURLSig(urlA), hashX, row(9999)); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	if rows, _ := st.MediaFetchRows(9999); len(rows) != 1 {
		t.Fatalf("行：%+v", rows)
	}
	// 行写不进（没有状态）：整个事务回滚，hash 也不回填
	seq3 := newMediaEvent(t, st, conv, "3", 1_700_000_000_000, `[`+segImage("")+`]`)
	before3 := bodyOf(t, st, seq3)
	bad := row(seq3)
	bad.State = ""
	if _, err = st.FinishSegHash(seq3, 0, kindIm, MediaURLSig(urlA), hashX, bad); err == nil {
		t.Fatal("行不合法要报错")
	}
	if bodyOf(t, st, seq3) != before3 {
		t.Fatal("行写不进时 hash 不该回填（同一个事务）")
	}
}
