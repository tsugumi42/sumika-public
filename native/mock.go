package main

// 模拟数据（规格-原生界面原型 第一节）：名字、号码、聊天全是编的，不从真实库里抄。
// 十个角色、两个本地群、三个 QQ 群（一个一万条）、几个 QQ 私聊；图和表情包是现场画的色块。
// 同一个种子每次生成一样的东西，量数时好对比。

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

var mockChars = []character{
	{ID: "aoi", Name: "小葵", Color: "#5fb7c2"},
	{ID: "sumi", Name: "阿澄", Color: "#6a9fd8"},
	{ID: "lime", Name: "青柠", Color: "#8bbf5a"},
	{ID: "kuri", Name: "栗子", Color: "#c9925a"},
	{ID: "natsu", Name: "夏目", Color: "#d98a8a"},
	{ID: "hoshi", Name: "星野", Color: "#9a86d4"},
	{ID: "tsuyu", Name: "白露", Color: "#7fb3a6"},
	{ID: "chiyo", Name: "千夜", Color: "#c78ab8"},
	{ID: "waka", Name: "若叶", Color: "#a3b95c"},
	{ID: "yuki", Name: "雪菜", Color: "#8aa8c8"},
}

// 群友（QQ 那边的人），号码是占位的
var mockPeople = []string{"路过的猫", "豆沙包", "夜行列车", "半糖", "咕咕", "鱼丸", "橘子汽水", "早睡失败", "云朵", "柠檬茶", "小番茄", "不想上班", "月饼", "毛线团", "海盐", "便利店"}

type mockConv struct {
	key, title, kind string // kind：local / home（本地群聊）/ group / private
	chars            []int  // 在里面说话的角色（下标）
	people           []int  // 在里面说话的群友（下标）
	n                int    // 一共多少条
	unread           int
}

var mockConvs = func() []mockConv {
	cs := []mockConv{}
	for i, ch := range mockChars {
		n := 60 + i*40
		if i == 0 {
			n = 3000 // 第一个角色的私聊历史很长
		}
		cs = append(cs, mockConv{key: "local:" + ch.ID, title: ch.Name, kind: "local", chars: []int{i}, n: n})
	}
	cs = append(cs,
		mockConv{key: "home:cabin", title: "小屋", kind: "home", chars: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, n: 1200},
		mockConv{key: "home:draw", title: "画画组", kind: "home", chars: []int{0, 4, 7}, n: 300},
		mockConv{key: "qq:group:1001", title: "测试群", kind: "group", chars: []int{0, 1}, people: seq(16), n: 10000, unread: 37},
		mockConv{key: "qq:group:1002", title: "番剧讨论", kind: "group", chars: []int{2}, people: []int{1, 3, 5, 7, 9, 11}, n: 800, unread: 5},
		mockConv{key: "qq:group:1003", title: "摸鱼小组", kind: "group", chars: []int{0, 3}, people: []int{0, 2, 4, 6}, n: 300},
		mockConv{key: "qq:private:2001", title: "半糖", kind: "private", chars: []int{0}, people: []int{3}, n: 200, unread: 2},
		mockConv{key: "qq:private:2002", title: "夜行列车", kind: "private", chars: []int{1}, people: []int{2}, n: 90},
		mockConv{key: "qq:private:2003", title: "月饼", kind: "private", chars: []int{0}, people: []int{12}, n: 40},
	)
	return cs
}()

func seq(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = i
	}
	return s
}

// 群友、主人说的短句
var mockLines = []string{
	"今天也是摸鱼的一天", "有人看了昨晚那集吗", "笑死", "这个我知道！", "晚上一起打游戏吗", "刚下班，累死了",
	"好耶", "你们在聊啥", "等等我去拿个外卖", "这图好可爱", "hyw", "确实", "我觉得还行吧", "啊这",
	"周末有人去漫展吗？我想去但是一个人有点怂", "这个 bug 我修了一下午，最后发现是少了个逗号",
	"刚才那个链接打不开", "明天降温，记得多穿点", "草", "收到", "666", "我先睡了，晚安",
}

// 角色说的（按 Markdown 显示），有短有长
var mockReplies = []string{
	"好呀～", "诶，真的吗？", "我也想去！", "嗯嗯，记下来了", "哈哈哈哈这也太巧了",
	"今天的备份已经弄好了，放在 D 盘的 Backups 文件夹里。顺便把上周的聊天记录也整理了一下，有三条没发出去的，我标出来了，你有空看一眼就行，不急的。",
	"我查了一下，大概有三种做法：\n\n1. 直接改配置，最快但重启才生效\n2. 写个小脚本，每次跑一遍\n3. 做成设置里的开关\n\n我倾向第三种，以后不用记命令。",
	"这段可以这样写：\n\n```go\nfunc greet(name string) string {\n\treturn \"你好，\" + name\n}\n```\n\n跑一下试试？",
	"| 项 | 现在 | 改完 |\n|---|--:|--:|\n| 启动 | 2.1 秒 | 0.7 秒 |\n| 内存 | 420 MB | 160 MB |\n\n快了不少～",
	"> 早睡失败：今晚一定早睡\n\n这句话你上周也说过三次了哦",
}

type mockGen struct {
	mu    sync.Mutex
	cache map[string][]event
}

var mockData = &mockGen{cache: map[string][]event{}}

// rng 简单的线性同余，固定种子
type rng struct{ x uint64 }

func (r *rng) next() uint64   { r.x = r.x*6364136223846793005 + 1442695040888963407; return r.x >> 33 }
func (r *rng) intn(n int) int { return int(r.next() % uint64(n)) }

func (g *mockGen) events(cv mockConv) []event {
	g.mu.Lock()
	defer g.mu.Unlock()
	if es, ok := g.cache[cv.key]; ok {
		return es
	}
	r := &rng{x: uint64(len(cv.key))*7919 + uint64(cv.n)}
	now := time.Now().UnixMilli()
	es := make([]event, cv.n)
	at := now - int64(cv.n)*95_000
	for i := range es {
		at += int64(20_000 + r.intn(150_000))
		if i == cv.n-1 {
			at = now - 60_000
		}
		e := event{Seq: int64(i + 1), ID: fmt.Sprintf("%s#%d", cv.key, i), Channel: cv.kind, Conversation: cv.key, Kind: "message", CreatedAt: at}
		who := r.intn(10)
		switch {
		case cv.kind == "local" && who < 5, cv.kind == "home" && who < 3, (cv.kind == "group" || cv.kind == "private") && who < 1:
			e.Origin, e.ActorKind, e.ActorID = "owner", "owner", "owner"
			e.Text = mockLines[r.intn(len(mockLines))]
		case len(cv.people) > 0 && (cv.kind == "group" && who < 8 || cv.kind == "private" && who < 5):
			e.Origin, e.ActorKind = "member", "human"
			e.ActorID = fmt.Sprintf("p%d", cv.people[r.intn(len(cv.people))])
			e.Text = mockLines[r.intn(len(mockLines))]
		default:
			e.Origin, e.ActorKind = "character", "character"
			e.ActorID = mockChars[cv.chars[r.intn(len(cv.chars))]].ID
			e.Text = mockReplies[r.intn(len(mockReplies))]
		}
		// 十几条里来一张图或表情包（QQ 和本地群聊）
		if cv.kind != "local" && e.Origin != "character" && r.intn(14) == 0 {
			w, h := 300+r.intn(900), 300+r.intn(900)
			sticker := r.intn(2) == 0
			if sticker {
				w, h = 240, 240
			}
			label := "[图片]"
			if sticker {
				label = "[表情包]"
			}
			s := fmt.Sprintf(`{"segs":[{"type":"image","hash":"mock:%d:%d:%d","sticker":%v,"width":%d,"height":%d}]}`, i, w, h, sticker, w, h)
			e.SegmentsJSON, e.Text = &s, label
		}
		// 隔几十条来一句带小黄脸的话（不用随机数，别的消息还是原来那些）
		if e.Origin != "character" && e.SegmentsJSON == nil && i%23 == 5 {
			s := `{"segs":[{"type":"text","text":"` + e.Text + `"},{"type":"face","id":"14"},{"type":"text","text":" 然后"},{"type":"face","id":"277"},{"type":"face","id":"21"}]}`
			e.SegmentsJSON, e.Text = &s, e.Text+"[表情] 然后[表情][表情]"
		}
		// 隔十几条来一句引用前面某条的话（同样不用随机数）：引用块、点击跳转、原消息不在本地都要有的看
		if i > 20 && i%17 == 9 && e.SegmentsJSON == nil && e.Origin != "character" {
			e.ReplyTo = es[i-3-i%4].ID
		}
		es[i] = e
	}
	g.cache[cv.key] = es
	return es
}

func mockConvOf(key string) (mockConv, bool) {
	for _, cv := range mockConvs {
		if cv.key == key {
			return cv, true
		}
	}
	return mockConv{}, false
}

// page 取 before（不含）之前的最多 limit 条；before 为 0 时取最新的
func mockPage(key string, limit int, before int64) ([]event, mockConv, error) {
	cv, ok := mockConvOf(key)
	if !ok {
		return nil, cv, fmt.Errorf("没有这个会话：%s", key)
	}
	es := mockData.events(cv)
	end := len(es)
	if before > 0 {
		end = int(before) - 1
	}
	start := max(0, end-limit)
	return es[start:end], cv, nil
}

func mockLocal(id string, limit int, before int64) ([]event, error) {
	es, _, err := mockPage("local:"+id, limit, before)
	return es, err
}

func mockQQ(key string, limit int, before int64) (qqPage, error) {
	es, cv, err := mockPage(key, limit, before)
	if err != nil {
		return qqPage{}, err
	}
	kind := "group"
	if cv.kind == "private" {
		kind = "private"
	}
	people := map[string]person{}
	for _, p := range cv.people {
		people[fmt.Sprintf("p%d", p)] = person{Name: mockPeople[p]}
	}
	return qqPage{Kind: kind, Title: cv.title, Events: es, People: people}, nil
}

// mockImageBytes 一张图的 PNG 字节：按 hash 里的宽高画一块渐变，宽超过档就缩到档宽（和后台的 ?w= 一样，不放大）。
// 走和真图同一条路（字节 → decodeStatic），测试和演示都用它
func mockImageBytes(hash string, tier int) ([]byte, error) {
	var i, w, h int
	if _, err := fmt.Sscanf(strings.TrimPrefix(hash, "mock:"), "%d:%d:%d", &i, &w, &h); err != nil {
		return nil, err
	}
	if tier > 0 && w > tier { // tier 为 0 是原文件
		w, h = tier, max(h*tier/w, 1)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	base := mockChars[i%len(mockChars)].Color
	c0 := ui.Hex(base)
	r0, g0, b0 := c0.R, c0.G, c0.B
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f := float64(x+y) / float64(w+h)
			img.Set(x, y, color.RGBA{uint8(float64(r0) * (0.55 + 0.45*f)), uint8(float64(g0) * (0.55 + 0.45*f)), uint8(float64(b0)*(0.6+0.4*(1-f)) + 40*f), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// 模拟的小黄脸：名字表四项（经典、超级、emoji 类各有），图是现场画的色块圆
var mockFaceTable = map[string]faceInfo{
	"14":  {Name: "微笑", Kind: "classic"},
	"21":  {Name: "可爱", Kind: "classic"},
	"277": {Name: "汪汪", Kind: "classic"},
	"333": {Name: "骰子", Kind: "super"},
	"900": {Name: "笑哭", Kind: "emoji", Char: "😂"},
}

// mockFacePNG 一个脸的图：按 id 取颜色画一个圆。名字表之外的 id 当作没有这个图（404）
func mockFacePNG(id string) ([]byte, error) {
	var n int
	if _, ok := mockFaceTable[id]; !ok || mockFaceTable[id].Kind == "emoji" {
		return nil, errNotFound
	}
	fmt.Sscan(id, &n)
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	col := color.RGBA{uint8(90 + n*37%140), uint8(140 + n*53%100), uint8(80 + n*29%150), 255}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if dx, dy := x-32, y-32; dx*dx+dy*dy < 30*30 {
				img.Set(x, y, col)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mockAvatarPNG 演示头像：按 key 取颜色画一个 96×96 的圆；key 的哈希对 5 取余为 0 的当作没有头像
func mockAvatarPNG(key string) ([]byte, error) {
	var h uint32
	for _, r := range key {
		h = h*31 + uint32(r)
	}
	if h%5 == 0 {
		return nil, errNotFound
	}
	img := image.NewRGBA(image.Rect(0, 0, 96, 96))
	col := color.RGBA{uint8(80 + h%140), uint8(110 + h/7%120), uint8(90 + h/13%140), 255}
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			if dx, dy := x-48, y-48; dx*dx+dy*dy < 46*46 {
				img.Set(x, y, col)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
