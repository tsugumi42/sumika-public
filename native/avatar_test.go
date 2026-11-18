package main

// 头像的测试：key 的拼法、存储（取到/没有/解不开/重试）、消息左边有头像和没有两种、会话列表。数据全是编的。

import (
	"context"
	"errors"
	"image/color"
	"sync"
	"testing"

	"sumika-native/internal/api"
)

func TestAvatarKeys(t *testing.T) {
	t.Parallel()
	conv := func(platform, scene, id string) api.ConversationKey {
		return api.ConversationKey{Platform: platform, Scene: scene, Conversation: id}
	}
	for _, c := range []struct {
		k    api.ConversationKey
		want string
	}{
		{conv("qq", "group", "qq:group:90002"), "group:90002"},
		{conv("qq", "private", "qq:private:80001:90001"), "user:90001"}, // 私聊是对方的头像，不是她自己的
		{conv("qq", "group", "qq:group:12"), ""},                        // 号不合格
		{conv("qq", "group", "qq:group:9000a"), ""},
		{conv("qq", "group", "qq:group:"), ""},
		{conv("qq", "private", "qq:private:80001"), ""},
		{conv("qq", "private", "qq:private:80001:../x"), ""},
		{conv("qq", "group", "qq:private:80001:90001"), ""}, // 场合和 id 对不上
		{conv("local", "private", "console:aoi"), ""},
		{conv("local", "group", "home:cabin"), ""},
	} {
		if got := avatarKeyOfConv(c.k); got != c.want {
			t.Errorf("会话 %+v：得 %q，应为 %q", c.k, got, c.want)
		}
	}
	for _, c := range []struct{ platform, kind, id, want string }{
		{"qq", "human", "qq:90001", "user:90001"},
		{"qq", "human", "qq:", ""},
		{"qq", "human", "qq:12", ""},
		{"qq", "human", "qq:1234567890123", ""},
		{"qq", "human", "qq:90001x", ""},
		{"qq", "human", "90001", ""},        // 没有 qq: 前缀的不认
		{"qq", "human", "qq:９００01", ""},     // 全角数字
		{"qq", "character", "qq:90001", ""}, // 她自己（角色）没有 QQ 头像的 key
		{"qq", "owner", "qq:90001", ""},     // 主人：ActorID 不算，要读取层给的号
		{"local", "human", "qq:90001", ""},  // 不是 QQ
	} {
		if got := avatarKeyOfActor(c.platform, c.kind, c.id, ""); got != c.want {
			t.Errorf("%+v：得 %q，应为 %q", c, got, c.want)
		}
	}
	// 主人：号来自读取层（段里的 from.id），有号就有头像
	if got := avatarKeyOfActor("qq", "owner", "owner", "90040"); got != avatarKey("user", "90040") || got == "" {
		t.Errorf("主人带号该有头像 key：%q", got)
	}
}

// avatarReader 假的可选头像接口：有的给图，没有的回 ErrNoAvatar；数一数问了几次
type avatarReader struct {
	demoReader
	mu    sync.Mutex // 多个头像工人会同时记次数；断言在 settle 等完以后读。
	png   map[string][]byte
	asked map[string]int
}

func (r *avatarReader) ReadAvatar(_ context.Context, key string) (api.Result[api.MediaView], error) {
	r.mu.Lock()
	r.asked[key]++
	r.mu.Unlock()
	b, ok := r.png[key]
	if !ok {
		return api.Result[api.MediaView]{State: api.Failed, Err: api.ErrNoAvatar}, api.ErrNoAvatar
	}
	return api.Result[api.MediaView]{State: api.Ready, Value: api.MediaView{Bytes: b, MIME: "image/png", Size: int64(len(b))}}, nil
}

func newAvatarReader(png map[string][]byte) *avatarReader {
	return &avatarReader{png: png, asked: map[string]int{}}
}

// avatarApp 带假读取和假界面线程的 app
func avatarApp(t *testing.T, r api.Reader, ms []*msg) (*app, *uiQueue) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := chatApp(ms, true)
	q := &uiQueue{}
	a.ctx, a.cancel, a.postFn = ctx, cancel, q.post
	a.reader = r
	return a, q
}

// settle 等头像的后台活都做完、回界面线程
func settle(a *app, q *uiQueue) {
	a.avs().wg.Wait()
	q.drain()
}

func TestAvatarStore(t *testing.T) {
	t.Parallel()
	r := newAvatarReader(map[string][]byte{"user:90001": facePNG(8, 8, red), "user:90002": []byte("不是图")})
	a, q := avatarApp(t, r, nil)
	st := a.avs()
	for _, k := range []string{"user:90001", "user:90002", "user:90003", "", "user:90001"} {
		st.want(k)
	}
	if st.peek("user:90001") != nil {
		t.Fatal("还没回界面线程就不该有")
	}
	settle(a, q)
	if st.peek("user:90001") == nil {
		t.Fatal("取到的头像没放进去")
	}
	if st.peek("user:90002") != nil || st.peek("user:90003") != nil || st.peek("") != nil {
		t.Fatal("解不开的、没有的、空 key 不该有位图")
	}
	if r.asked["user:90001"] != 1 || r.asked[""] != 0 {
		t.Fatalf("同一个 key 只问一次、空 key 不问：%v", r.asked)
	}
	// 没有的记成"没有"，同一个会话里不再问；换会话（clearMissing）以后再问一次，那时取到了就有了
	st.want("user:90003")
	settle(a, q)
	if r.asked["user:90003"] != 1 {
		t.Fatalf("没有的不该重复问：%v", r.asked)
	}
	r.png["user:90003"] = facePNG(8, 8, blue)
	st.clearMissing()
	st.want("user:90003")
	settle(a, q)
	if st.peek("user:90003") == nil || r.asked["user:90003"] != 2 {
		t.Fatalf("换会话以后应再问一次并取到：%v", r.asked)
	}
	if st.peek("user:90001") == nil {
		t.Fatal("clearMissing 不该丢掉取到的")
	}
	// 数据源没有头像接口（演示、旧后台）：什么也不问，全用首字母
	a2, q2 := avatarApp(t, demoReaderOnly{}, nil)
	a2.avs().want("user:90001")
	settle(a2, q2)
	if a2.avs().peek("user:90001") != nil {
		t.Fatal("没有头像接口却有位图")
	}
}

// demoReaderOnly 只有 api.Reader，没有头像接口
type demoReaderOnly struct{ demoReader }

func (demoReaderOnly) ReadAvatar() {} // 签名不对，不满足 api.AvatarReader

// 取头像失败（超时之类）也当没有，不让界面卡在加载中
func TestAvatarFetchError(t *testing.T) {
	t.Parallel()
	a, q := avatarApp(t, newAvatarReader(nil), nil)
	st := a.avs()
	st.fetch = func(context.Context, string) ([]byte, error) { return nil, errors.New("网络断了") }
	st.want("user:90001")
	settle(a, q)
	if st.peek("user:90001") != nil || st.m["user:90001"].state != avMissing {
		t.Fatalf("%+v", st.m["user:90001"])
	}
}

func TestAvatarBesideMessages(t *testing.T) {
	ms := msgsOf(t, view("a", "human", "豆沙包", "第一句", t0), view("b", "human", "豆沙包", "第二句", t0+10000), view("c", "human", "半糖", "另一组", t0+20000))
	ms[0].avatar, ms[1].avatar, ms[2].avatar = "user:90001", "user:90001", "user:90002"
	a, q := avatarApp(t, newAvatarReader(map[string][]byte{"user:90001": facePNG(32, 32, red), "user:90002": facePNG(32, 32, blue)}), ms)
	tt, p := chatTester(a, 900, 700)
	if !tt.HasText("豆沙包的头像") {
		t.Fatal("缺少完整头像语义")
	}
	before := tt.Image()
	settle(a, q)
	p.refresh()
	for _, tc := range []struct {
		id   string
		want color.RGBA
	}{{"b", opaqueRed}, {"c", color.RGBA{R: 0, G: 0, B: 255, A: 255}}} {
		row := p.box(t, "shell:"+tc.id+":0")
		x, y := int(row.X+14+16), int(row.Y+row.H-2-16)
		got := tt.Image().RGBAAt(x, y)
		if !near(got, tc.want, 5) {
			t.Fatalf("组尾33 DIP头像中心 (%d,%d)=%v want=%v", x, y, got, tc.want)
		}
		if before.RGBAAt(x, y) == got {
			t.Fatal("加载首字未被位图替换")
		}
	}
	if a, b := p.box(t, "body:a:0"), p.box(t, "body:b:0"); a.X != b.X {
		t.Fatalf("分组正文未对齐 %+v %+v", a, b)
	}
}

// 窄窗口、很长的话、图片气泡：有头像也不伸出窗口
func TestAvatarNarrowNoOverflow(t *testing.T) {
	long := ""
	for range 12 {
		long += "今天也是摸鱼的一天，"
	}
	for _, w := range []int{821, 900} {
		ms := msgsOf(t, view("a", "human", "豆沙包", long, t0), withImage(view("b", "human", "半糖", "", t0+400_000), "mock:1:300:200"))
		ms[0].avatar, ms[1].avatar = "user:90001", "user:90002"
		a, q := avatarApp(t, newAvatarReader(map[string][]byte{"user:90001": facePNG(8, 8, red)}), ms)
		_, p := chatTester(a, w, 900)
		settle(a, q)
		p.refresh()
		for _, name := range []string{"body:a:0", "pic:b:mock:1:300:200"} {
			r := p.box(t, name)
			if r.X+r.W > float32(w)-20+0.5 {
				t.Fatalf("宽 %d：%s 伸出了窗口 %+v", w, name, r)
			}
		}
	}
}

// 头像完成后生成新快照；实际图像和行几何必须同步，缺图仍有首字。
func TestWithAvatars(t *testing.T) {
	r := newAvatarReader(map[string][]byte{"group:90002": facePNG(8, 8, red), "user:90001": facePNG(8, 8, blue)})
	a, q := avatarApp(t, r, nil)
	a.rail = 1
	a.convs = []conv{
		{Key: demoKey("qq:group:90002"), Title: "编造群", Last: lastLine{Text: "x", CreatedAt: 1}},
		{Key: demoKey("qq:private:80001:90001"), Title: "编造私聊", Last: lastLine{Text: "x", CreatedAt: 1}},
		{Key: demoKey("qq:group:90009"), Title: "没头像的群", Last: lastLine{Text: "x", CreatedAt: 1}},
	}
	tt := testerOf(300, 600, a.convList)
	before, _ := tt.Find("编造群")
	settle(a, q)
	tt.Frame()
	tt.Frame()
	after, _ := tt.Find("编造群")
	if before != after || after.H != 62 {
		t.Fatalf("头像到达改变行几何: %+v→%+v", before, after)
	}
	for _, tc := range []struct {
		name  string
		color color.RGBA
	}{{"编造群", color.RGBA{255, 0, 0, 255}}, {"编造私聊", color.RGBA{0, 0, 255, 255}}} {
		row, _ := tt.Find(tc.name)
		// 库的头像46×46，距行左10、顶8；采样图的中心。
		got := tt.Image().RGBAAt(int(row.X+33), int(row.Y+31))
		if got != tc.color {
			t.Fatalf("%s头像未更新: %v", tc.name, got)
		}
	}
	if !hasExact(tt, "没") {
		t.Fatal("缺头像没有显示首字")
	}
	if len(r.asked) != 3 {
		t.Fatalf("头像重复或漏请求: %v", r.asked)
	}
}
