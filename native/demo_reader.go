package main

// 演示数据（-demo 显式选，标题写"演示数据"）：把 mock.go 编的会话包成一个假的 api.Reader，
// 窗口走和生产同一条路（订阅、分页、取图、小黄脸）。编的段沿用原型的旧写法（type/qq/id），
// 在这里一次转成 api.DisplaySegment；生产不会走到这些代码。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"sumika-native/internal/api"
)

// event 是编的一条事件（原型的旧形状）
type event struct {
	Seq          int64   `json:"seq"`
	ID           string  `json:"id"`
	Channel      string  `json:"channel"`
	Conversation string  `json:"conversation"`
	ActorKind    string  `json:"actor_kind"`
	ActorID      string  `json:"actor_id"`
	Kind         string  `json:"kind"`
	Text         string  `json:"text"`
	SegmentsJSON *string `json:"segments_json"`
	CreatedAt    int64   `json:"created_at"`
	Origin       string  `json:"origin"`
	ReplyTo      string  `json:"reply_to"` // 消息里是引用的那条，通知里是通知说的那条（撤回等）
}

type person struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type qqPage struct {
	Kind   string            `json:"kind"`
	Title  string            `json:"title"`
	Events []event           `json:"events"`
	People map[string]person `json:"people"`
}

// demoSeg 仅用于演示 JSON 解码；进入正式界面前由 displayOfDemo 转成展示 DTO。
type demoSeg struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Hash     string `json:"hash"`
	Sticker  bool   `json:"sticker"`
	Summary  string `json:"summary"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	QQ       string `json:"qq"`
	Kind     string `json:"kind"`
	Super    bool   `json:"super"`
	Animated bool   `json:"animated"`
	Market   bool   `json:"market"`
}

// segBody 是编的 segments_json 根对象
type segBody struct {
	Segs    []demoSeg `json:"segs"`
	Unsent  string    `json:"unsent"`
	Aborted bool      `json:"aborted"`
	Partial bool      `json:"partial"`
	Notice  string    `json:"notice"`
}

// decodeBody 解编的 segments_json。没有、空串、null 都是零值；解析失败 Segs 清空、退回 text
func decodeBody(raw *string) (segBody, error) {
	var b segBody
	if raw == nil || *raw == "" {
		return b, nil
	}
	if err := json.Unmarshal([]byte(*raw), &b); err != nil {
		b.Segs = nil
		return b, err
	}
	return b, nil
}

func bodyOf(e event) segBody {
	b, err := decodeBody(e.SegmentsJSON)
	if err != nil {
		log.Printf("segments_json 解析失败，这一条退回 text 显示：事件 %s：%v", e.ID, err)
	}
	return b
}

func segsOf(e event) []api.DisplaySegment {
	return demoSegments(api.EventRef{}, bodyOf(e).Segs, nil)
}

// recalledIDs 一页编的事件里被撤回的消息 id（撤回通知的 reply_to）
func recalledIDs(es []event) map[string]bool {
	var out map[string]bool
	for _, e := range es {
		if e.Kind != "notice" || e.ReplyTo == "" || !strings.Contains(bodyOf(e).Notice, "recall") {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out[e.ReplyTo] = true
	}
	return out
}

// demoKey 演示会话 id → 有类型的会话身份；home: 是演示用的本地群聊，不会写进任何库
func demoKey(id string) api.ConversationKey {
	switch {
	case strings.HasPrefix(id, "local:"):
		return api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: id}
	case strings.HasPrefix(id, "home:"):
		return api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: id}
	case strings.HasPrefix(id, "qq:group:"):
		return api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: id}
	}
	return api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: id}
}

// displayOfDemo 编的旧段 → 中立显示段（和后台的投影同一个形状）
func displayOfDemo(src api.EventRef, s demoSeg) api.DisplaySegment {
	d := api.DisplaySegment{Text: s.Text, Width: s.Width, Height: s.Height, Animated: s.Animated, Summary: s.Summary, Name: s.Name, Size: s.Size}
	switch s.Type {
	case "text", "reply", "video", "forward", "card", "file":
		d.Kind = s.Type
	case "at":
		d.Kind, d.Target = "at", s.QQ
	case "face":
		d.Kind, d.Ref = "face", s.ID
		if s.Super {
			d.Kind = "superface"
		}
	case "image":
		d.Kind = "image"
		if s.Sticker || s.Market {
			d.Kind, d.Sticker = "emoji", true
		}
		if s.Hash != "" {
			d.Media = &api.MediaRef{Source: src, Hash: s.Hash}
		}
	case "mface":
		d.Kind, d.Ref = "emoji", "mface"
	case "record":
		d.Kind = "voice"
	default:
		d.Kind = s.Kind
	}
	return d
}

func demoView(key string, e event, people map[string]person, recalled map[string]bool) api.MessageView {
	k := demoKey(key)
	ref := api.EventRef{Conversation: k, Event: api.EventKey{Platform: k.Platform, EventID: e.ID}}
	v := api.MessageView{Ref: ref, Seq: e.Seq, Version: 1, ActorKind: e.ActorKind, ActorID: e.ActorID, Kind: e.Kind, Origin: e.Origin, CreatedAt: e.CreatedAt, Text: e.Text, Recalled: recalled[e.ID]}
	if p, ok := people[e.ActorID]; ok {
		v.DisplayName = p.Name
	}
	if e.Kind == "message" && e.Seq%9 == 4 {
		v.Reactions = demoReactions(e)
	}
	if e.ReplyTo != "" && e.Kind == "message" {
		v.ReplyTo, v.Reply = demoQuote(k, e.ReplyTo, people)
	}
	body := bodyOf(e)
	v.Unsent, v.Aborted, v.Partial = body.Unsent != "", body.Aborted, body.Partial
	if strings.Contains(body.Unsent, "已撤回") {
		v.Recalled = true
	}
	v.Segments = demoSegments(ref, body.Segs, people)
	return v
}

func demoSegments(ref api.EventRef, segs []demoSeg, people map[string]person) []api.DisplaySegment {
	out := make([]api.DisplaySegment, 0, len(segs))
	for _, s := range segs {
		d := displayOfDemo(ref, s)
		if d.Kind == "at" {
			if p, ok := people["qq:"+s.QQ]; ok && p.Name != "" {
				d.Name = p.Name
			} else {
				d.Name = people[s.QQ].Name
			}
		}
		out = append(out, d)
	}
	return out
}

// demoReactions 编的表情回应：小黄脸、emoji 类小黄脸和 Unicode 码位各来一点，有的表情两个人贴
func demoReactions(e event) []api.ReactionView {
	mk := func(i int, ref string) api.ReactionView {
		return api.ReactionView{ID: fmt.Sprintf("%s-r%d", e.ID, i), Ref: ref, ActorKind: "human", ActorID: fmt.Sprintf("qq:%d", 90001+i), DisplayName: mockPeople[i%len(mockPeople)], At: e.CreatedAt + int64(i+1)*1000}
	}
	return []api.ReactionView{mk(0, "14"), mk(1, "14"), mk(2, "900"), mk(3, "128077"), mk(4, "128077"), mk(5, "128077")}
}

// ReadAvatar 演示用的头像（api.AvatarReader）：按 key 画一个色块圆；五分之一的 key 当作没有，演示首字母
func (demoReader) ReadAvatar(_ context.Context, key string) (api.Result[api.MediaView], error) {
	b, err := mockAvatarPNG(key)
	if err != nil {
		return api.Result[api.MediaView]{State: api.Failed, Err: api.ErrNoAvatar}, api.ErrNoAvatar
	}
	return api.Result[api.MediaView]{State: api.Ready, Value: api.MediaView{Bytes: b, MIME: "image/png", Size: int64(len(b))}}, nil
}

// demoQuote 编的引用：原消息从编的数据里找（id 是 <会话>#<下标>）；找不到按"不在本地"，和后台读不到原消息时一样
func demoQuote(k api.ConversationKey, id string, people map[string]person) (*api.EventRef, api.Result[api.QuoteView]) {
	ref := &api.EventRef{Conversation: k, Event: api.EventKey{Platform: k.Platform, EventID: id}}
	cv, ok := mockConvOf(k.Conversation)
	if !ok {
		return ref, api.Result[api.QuoteView]{State: api.Unsupported}
	}
	for _, o := range mockData.events(cv) {
		if o.ID != id {
			continue
		}
		who := people[o.ActorID].Name
		switch o.ActorKind {
		case "owner":
			who = "主人"
		case "character":
			for _, c := range mockChars {
				if c.ID == o.ActorID {
					who = c.Name
				}
			}
		}
		return ref, api.Result[api.QuoteView]{State: api.Ready, Value: api.QuoteView{Ref: *ref, DisplayName: who, Text: o.Text, Segments: demoSegments(*ref, bodyOf(o).Segs, people)}}
	}
	return ref, api.Result[api.QuoteView]{State: api.Unsupported}
}

// buildMsg 编的事件 → 界面上的一行：先转成展示 DTO，再走生产的 msgFromView
func buildMsg(key string, e event, charOf func(id string) (character, bool), people map[string]person) *msg {
	return msgFromView(demoKey(key), demoView(key, e, people, nil), charOf)
}

// demoReader 是演示用的只读接口：数据全是 mock.go 编的
type demoReader struct{}

type demoSub[T any] struct {
	ch   chan api.Snapshot[T]
	stop context.CancelFunc
}

func (s *demoSub[T]) Updates() <-chan api.Snapshot[T] { return s.ch }
func (s *demoSub[T]) Close()                          { s.stop() }

// once 交一份快照，ctx 取消或 Close 时关通道
func once[T any](ctx context.Context, v T) api.Subscription[T] {
	ctx, stop := context.WithCancel(ctx)
	s := &demoSub[T]{ch: make(chan api.Snapshot[T], 1), stop: stop}
	s.ch <- api.Snapshot[T]{Version: 1, Result: api.Result[T]{State: api.Ready, Value: v}}
	go func() { <-ctx.Done(); close(s.ch) }()
	return s
}

var demoRuntime = api.RuntimeView{Mode: "demo", Phase: "ready", Connection: "未连接 QQ", Reason: "演示数据：全是编的"}

func (demoReader) WatchOverview(ctx context.Context, req api.OverviewRequest) (api.Subscription[api.Overview], error) {
	var out api.Overview
	for _, c := range mockChars {
		out.Characters.Items = append(out.Characters.Items, api.Result[api.CharacterView]{State: api.Ready, Value: api.CharacterView{ID: c.ID, Name: c.Name, Color: c.Color, Status: "演示"}})
	}
	for _, cv := range mockConvs {
		es := mockData.events(cv)
		last := es[len(es)-1]
		v := api.ConversationView{Key: demoKey(cv.key), Title: cv.title, Unread: cv.unread, HasUnread: cv.kind == "group" || cv.kind == "private"}
		lv := demoView(cv.key, last, nil, nil)
		v.Last = &lv
		out.Conversations.Items = append(out.Conversations.Items, v)
	}
	out.Runtime = demoRuntime
	return once(ctx, out), nil
}

// 游标就是"这一页最早那条的 seq"：编的数据只有一条时间线
func (demoReader) ReadMessages(ctx context.Context, req api.ConversationRequest, cursor api.Cursor) (api.Result[api.MessagePage], error) {
	if req.Limit <= 0 || req.Limit > 1000 {
		err := errors.New("参数错误")
		return api.Result[api.MessagePage]{State: api.Failed, Err: err}, err
	}
	var before int64
	if cursor != "" {
		n, err := strconv.ParseInt(string(cursor), 10, 64)
		if err != nil {
			return api.Result[api.MessagePage]{State: api.Failed, Err: err}, err
		}
		before = n
	}
	id := req.Key.Conversation
	es, cv, err := mockPage(id, req.Limit, before)
	if err != nil {
		return api.Result[api.MessagePage]{State: api.Failed, Err: err}, err
	}
	people := map[string]person{}
	for _, p := range cv.people {
		people[fmt.Sprintf("p%d", p)] = person{Name: mockPeople[p]}
	}
	gone := recalledIDs(es)
	var page api.MessagePage
	for _, e := range es {
		page.Items = append(page.Items, demoView(id, e, people, gone))
	}
	all := mockData.events(cv)
	if len(es) > 0 && es[0].Seq > 1 {
		page.HasMore, page.NextCursor = true, api.Cursor(strconv.FormatInt(es[0].Seq, 10))
		// 这一页最老一条再往前的那条消息（后台的 DisplayPage.Before）：往前找第一个 kind=message 的
		for i := int(es[0].Seq) - 2; i >= 0 && i < len(all); i-- {
			if all[i].Kind == "message" {
				v := demoView(id, all[i], people, gone)
				page.Before = &v
				break
			}
		}
	}
	if len(all) > 0 {
		page.HighWater = all[len(all)-1].Seq
	}
	return api.Result[api.MessagePage]{State: api.Ready, Value: page}, nil
}

func (r demoReader) WatchConversation(ctx context.Context, req api.ConversationRequest) (api.Subscription[api.ConversationSnapshot], error) {
	p, err := r.ReadMessages(ctx, req, "")
	if err != nil {
		return nil, err
	}
	return once(ctx, api.ConversationSnapshot{Conversation: api.ConversationView{Key: req.Key}, Page: p.Value}), nil
}

func (demoReader) ReadMedia(ctx context.Context, ref api.MediaRef, tier api.MediaTier) (api.Result[api.MediaView], error) {
	b, err := mockImageBytes(ref.Hash, int(tier))
	if err != nil {
		return api.Result[api.MediaView]{State: api.Failed, Err: err}, err
	}
	return api.Result[api.MediaView]{State: api.Ready, Value: api.MediaView{Bytes: b, MIME: "image/png", Size: int64(len(b))}}, nil
}

func (demoReader) ReadFaceIndex(context.Context) (api.Result[api.List[api.FaceView]], error) {
	var out api.List[api.FaceView]
	for id, f := range mockFaceTable {
		out.Items = append(out.Items, api.FaceView{ID: id, Name: f.Name, Kind: f.Kind, Char: f.Char})
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ID < out.Items[j].ID })
	return api.Result[api.List[api.FaceView]]{State: api.Ready, Value: out}, nil
}

func (demoReader) ReadFacePNG(_ context.Context, id string) (api.Result[api.MediaView], error) {
	b, err := mockFacePNG(id)
	if err != nil {
		return api.Result[api.MediaView]{State: api.Failed, Err: err}, err
	}
	return api.Result[api.MediaView]{State: api.Ready, Value: api.MediaView{Bytes: b, MIME: "image/png", Size: int64(len(b))}}, nil
}

func (demoReader) ReadSettings(context.Context) (api.Result[api.Settings], error) {
	err := errors.New("演示数据没有真实设置")
	return api.Result[api.Settings]{State: api.Unsupported, Err: err}, err
}

func (demoReader) WatchRuntime(ctx context.Context) (api.Subscription[api.RuntimeView], error) {
	return once(ctx, demoRuntime), nil
}
