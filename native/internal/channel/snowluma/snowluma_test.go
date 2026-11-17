package snowluma

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/contract"
	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
)

func TestContract(t *testing.T) {
	contract.Adapter(t, func() types.Adapter {
		return New(func(ctx context.Context) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() })
	})
}

func TestSendBeforeConnect(t *testing.T) {
	a := New(func(ctx context.Context) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() })
	r, err := a.Send(context.Background(), "100", types.Part{ID: "p", Conv: groupConv(), Kind: types.PartMessage, Body: types.TextBody("在的")})
	if err != nil || r.PartID != "p" || r.Status != types.ReceiptFailed || r.Err != "没连上" || r.Validate() != nil {
		t.Fatalf("没连上的文字：receipt = %#v, err = %v", r, err)
	}
	pr, err := a.Poke(context.Background(), "100", types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "90002"}, "90003")
	if err != nil || pr.Status != types.ReceiptFailed || pr.Err != "没连上" || pr.PartID != "" || pr.Validate() != nil {
		t.Fatalf("没连上的戳：receipt = %#v, err = %v", pr, err)
	}
	if _, err := a.Send(context.Background(), "100", types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyImage}}}); !errors.Is(err, types.ErrUnsupported) {
		t.Fatalf("图片：%v，要 ErrUnsupported", err)
	}
}

func TestTranslate(t *testing.T) {
	at := time.Unix(100, 0).UTC()
	base := types.Inbound{Kind: types.InboundMessage, Account: "100", Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: "100", ID: "200"}, PlatformID: "300", From: types.User{ID: "200", Nick: "虚构甲"}, Time: at}
	group := base
	group.Conv = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "400"}
	group.From.Card = "虚构乙"
	tests := []struct {
		name, frame string
		in          *types.Inbound
		notice      *types.Notice
	}{
		{"private", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[{"type":"text","data":{"text":"你好"}}]}`, func() *types.Inbound {
			x := base
			x.Segs = []types.Seg{{Kind: types.SegText, Text: "你好"}}
			return &x
		}(), nil},
		{"group", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"group_id":400,"message_id":300,"sender":{"nickname":"虚构甲","card":"虚构乙"},"message":[]}`, func() *types.Inbound { x := group; x.Segs = []types.Seg{}; return &x }(), nil},
	}
	segmentCases := []struct {
		name, raw string
		want      types.Seg
	}{
		{"at", `{"type":"at","data":{"qq":200}}`, types.Seg{Kind: types.SegAt, Target: "200"}},
		{"reply", `{"type":"reply","data":{"id":300}}`, types.Seg{Kind: types.SegReply, Target: "300"}},
		{"image", `{"type":"image","data":{"file":"fake"}}`, types.Seg{Kind: types.SegImage, File: "fake"}},
		// 表情包照 Bun 认：sub_type 不是 0、4、9，或者 summary 不是"[图片]"，或者商城表情的地址、文件名
		{"image-sub0", `{"type":"image","data":{"file":"fake","sub_type":0,"summary":"[图片]"}}`, types.Seg{Kind: types.SegImage, File: "fake"}},
		{"image-sub4", `{"type":"image","data":{"file":"fake","sub_type":"4"}}`, types.Seg{Kind: types.SegImage, File: "fake"}},
		{"image-sub9", `{"type":"image","data":{"file":"fake","sub_type":9,"summary":" "}}`, types.Seg{Kind: types.SegImage, File: "fake"}},
		{"image-bun-flag", `{"type":"image","data":{"file":"fake","sticker":true}}`, types.Seg{Kind: types.SegImage, File: "fake"}},
		{"sticker-sub1", `{"type":"image","data":{"file":"fake","sub_type":1,"summary":"[动画表情]"}}`, types.Seg{Kind: types.SegEmoji, File: "fake"}},
		{"sticker-sub7", `{"type":"image","data":{"file":"fake","sub_type":7}}`, types.Seg{Kind: types.SegEmoji, File: "fake"}},
		{"sticker-summary", `{"type":"image","data":{"file":"fake","summary":"[动画表情]"}}`, types.Seg{Kind: types.SegEmoji, File: "fake"}},
		{"sticker-market-url", `{"type":"image","data":{"url":"https://gxh.vip.qq.com/club/item/parcel/0/fake"}}`, types.Seg{Kind: types.SegEmoji, URL: "https://gxh.vip.qq.com/club/item/parcel/0/fake"}},
		{"sticker-market-file", `{"type":"image","data":{"file":"ab-0123456789ABCDEF0123456789abcdef.gif"}}`, types.Seg{Kind: types.SegEmoji, File: "ab-0123456789ABCDEF0123456789abcdef.gif"}},
		// 图片的地址和文件名照 2026-10-30 录的真帧形状存（url、file、sub_type、summary）；敌对的地址、文件名整个不存，种类照认
		{"image-real-shape", `{"type":"image","data":{"url":"https://multimedia.example.invalid/download?appid=1&fileid=编的&rkey=编的","file":"0123ABCD.jpg","sub_type":0,"summary":""}}`, types.Seg{Kind: types.SegImage, URL: "https://multimedia.example.invalid/download?appid=1&fileid=编的&rkey=编的", File: "0123ABCD.jpg"}},
		{"image-url-upper-scheme", `{"type":"image","data":{"url":"HTTPS://example.invalid/a"}}`, types.Seg{Kind: types.SegImage, URL: "HTTPS://example.invalid/a"}},
		{"image-url-file-scheme", `{"type":"image","data":{"url":"file:///C:/Users/x/a.jpg","file":"a.jpg"}}`, types.Seg{Kind: types.SegImage, File: "a.jpg"}},
		{"image-url-base64", `{"type":"image","data":{"url":"base64://aGVsbG8="}}`, types.Seg{Kind: types.SegImage}},
		{"image-url-local-path", `{"type":"image","data":{"url":"C:\\Users\\x\\a.jpg"}}`, types.Seg{Kind: types.SegImage}},
		{"image-url-newline", `{"type":"image","data":{"url":"https://example.invalid/a\nb"}}`, types.Seg{Kind: types.SegImage}},
		{"image-url-number", `{"type":"image","data":{"url":12345,"file":12345}}`, types.Seg{Kind: types.SegImage, File: "12345"}},
		{"image-url-too-long", `{"type":"image","data":{"url":"https://example.invalid/` + strings.Repeat("a", types.MediaRefMax) + `"}}`, types.Seg{Kind: types.SegImage}},
		{"image-file-dotdot", `{"type":"image","data":{"file":"../../a.jpg"}}`, types.Seg{Kind: types.SegImage}},
		{"image-file-backslash", `{"type":"image","data":{"file":"..\\a.jpg"}}`, types.Seg{Kind: types.SegImage}},
		{"image-file-drive", `{"type":"image","data":{"file":"C:a.jpg"}}`, types.Seg{Kind: types.SegImage}},
		{"image-file-only-dots", `{"type":"image","data":{"file":".."}}`, types.Seg{Kind: types.SegImage}},
		{"image-file-control", `{"type":"image","data":{"file":"a\u0000.jpg"}}`, types.Seg{Kind: types.SegImage}},
		{"image-file-too-long", `{"type":"image","data":{"file":"` + strings.Repeat("a", types.MediaRefMax+1) + `"}}`, types.Seg{Kind: types.SegImage}},
		{"image-data-not-object", `{"type":"image","data":"https://example.invalid/a"}`, types.Seg{Kind: types.SegImage}},
		{"face", `{"type":"face","data":{"id":14}}`, types.Seg{Kind: types.SegFace, Ref: "14"}},
		{"image-sub4-string-decimal", `{"type":"image","data":{"file":"fake","sub_type":"4.0"}}`, types.Seg{Kind: types.SegImage, File: "fake"}},
		{"image-sub4-decimal", `{"type":"image","data":{"file":"fake","sub_type":4.0}}`, types.Seg{Kind: types.SegImage, File: "fake"}},
		{"sticker-sub-word", `{"type":"image","data":{"file":"fake","sub_type":"abc"}}`, types.Seg{Kind: types.SegEmoji, File: "fake"}},
		{"superface-sub3-decimal", `{"type":"face","data":{"id":14,"sub_type":3.0}}`, types.Seg{Kind: types.SegSuperface, Ref: "14"}},
		{"face-bun-flag", `{"type":"face","data":{"id":14,"super":true}}`, types.Seg{Kind: types.SegFace, Ref: "14"}},
		{"superface-sub3", `{"type":"face","data":{"id":14,"sub_type":3}}`, types.Seg{Kind: types.SegSuperface, Ref: "14"}},
		{"superface-result", `{"type":"face","data":{"id":14,"resultId":"1"}}`, types.Seg{Kind: types.SegSuperface, Ref: "14"}},
		{"mface", `{"type":"mface","data":{"emojiId":"fake"}}`, types.Seg{Kind: types.SegEmoji, Ref: "fake"}},
		{"forward", `{"type":"forward","data":{"id":"fake"}}`, types.Seg{Kind: types.SegForward, Ref: "fake"}},
		{"xml-card", `{"type":"xml","data":{"data":"<msg brief=\"fake\"/>"}}`, types.Seg{Kind: types.SegCard, Raw: `<msg brief="fake"/>`, CardType: "xml"}},
		{"unknown", `{"type":"mystery","data":{"x":1}}`, types.Seg{Kind: types.SegOther, Raw: `{"type":"mystery","data":{"x":1}}`}},
	}
	for _, tc := range segmentCases {
		x := base
		x.Segs = []types.Seg{tc.want}
		tests = append(tests, struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{tc.name, `{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_id":300,"sender":{"nickname":"虚构甲"},"message":[` + tc.raw + `]}`, &x, nil})
	}
	tests = append(tests,
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"self-sent-user-is-peer", `{"post_type":"message_sent","time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"message":[]}`, &types.Inbound{Kind: types.InboundSelf, Account: "100", Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: "100", ID: "200"}, PlatformID: "300", From: types.User{ID: "100"}, Time: at, Segs: []types.Seg{}}, nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"self-no-peer", `{"post_type":"message_sent","time":100,"self_id":100,"user_id":100,"message_type":"private","message_id":300,"message":[]}`, &types.Inbound{Kind: types.InboundSelf, Account: "100", Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: "100", ID: ""}, PlatformID: "300", From: types.User{ID: "100"}, Time: at, Segs: []types.Seg{}}, nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"self-target-is-self", `{"post_type":"message_sent","time":100,"self_id":100,"user_id":100,"target_id":100,"peer_id":200,"message_type":"private","message_id":300,"message":[]}`, &types.Inbound{Kind: types.InboundSelf, Account: "100", Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: "100", ID: "200"}, PlatformID: "300", From: types.User{ID: "100"}, Time: at, Segs: []types.Seg{}}, nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"temp-session-carries-group", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"group_id":400,"message_type":"private","sub_type":"group","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"group-by-message-type", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"group_id":400,"message_type":"group","message_id":300,"sender":{"nickname":"虚构甲","card":"虚构乙"},"message":[]}`, func() *types.Inbound { x := group; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"sender-user-id", `{"post_type":"message","time":100,"self_id":100,"message_type":"private","message_id":300,"sender":{"user_id":200,"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"private-poke-to-her", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"notice_type":"notify","sub_type":"poke","target_id":100}`, nil, &types.Notice{Kind: types.NoticePoke, Account: "100", Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: "100", ID: "200"}, Time: at, Actor: "200", Target: "100"}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"private-poke-her-echo", `{"post_type":"notice","time":100,"self_id":100,"user_id":100,"notice_type":"notify","sub_type":"poke","target_id":200}`, nil, &types.Notice{Kind: types.NoticePoke, Account: "100", Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: "100", ID: "200"}, Time: at, Actor: "100", Target: "200"}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"private-poke-self-self", `{"post_type":"notice","time":100,"self_id":100,"user_id":100,"notice_type":"notify","sub_type":"poke","target_id":100}`, nil, nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"time-float", `{"post_type":"message","time":100.5,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound {
			x := base
			x.Time = time.Unix(100, 5e8).UTC()
			x.Segs = []types.Seg{}
			return &x
		}(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"time-string", `{"post_type":"message","time":"100","self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Time = at; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"time-missing", `{"post_type":"message","self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Time = time.Time{}; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"time-zero", `{"post_type":"message","time":0,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Time = time.Time{}; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"time-millis", `{"post_type":"message","time":100000000000,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Time = time.Time{}; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"message-string", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":"你好","raw_message":"你好"}`, func() *types.Inbound {
			x := base
			x.Time = at
			x.Segs = []types.Seg{{Kind: types.SegText, Text: "你好"}}
			return &x
		}(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"message-null-raw", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":null,"raw_message":"[CQ:face,id=14]"}`, func() *types.Inbound {
			x := base
			x.Time = at
			x.Segs = []types.Seg{{Kind: types.SegText, Text: "[CQ:face,id=14]"}}
			return &x
		}(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"message-missing", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"}}`, func() *types.Inbound { x := base; x.Time = at; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"notice-actor-zero", `{"post_type":"notice","time":100,"self_id":100,"user_id":0,"operator_id":201,"group_id":400,"notice_type":"group_ban","sub_type":"ban","duration":0}`, nil, &types.Notice{Kind: types.NoticeOther, Account: "100", Conv: group.Conv, Time: at, Actor: "201", Raw: `{"post_type":"notice","time":100,"self_id":100,"user_id":0,"operator_id":201,"group_id":400,"notice_type":"group_ban","sub_type":"ban","duration":0}`}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"recall-operator-zero", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":0,"group_id":400,"notice_type":"group_recall","message_id":300}`, nil, &types.Notice{Kind: types.NoticeRecall, Account: "100", Conv: group.Conv, Time: at, Actor: "200", MsgID: "300"}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"reaction", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":200,"group_id":400,"notice_type":"group_msg_emoji_like","message_id":300,"sub_type":"add","likes":[{"emoji_id":"76","count":1}]}`, nil, &types.Notice{Kind: types.NoticeReaction, Account: "100", Conv: group.Conv, Time: at, Actor: "200", MsgID: "300", Ref: "76"}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"reaction-remove", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":200,"group_id":400,"notice_type":"group_msg_emoji_like","message_id":300,"sub_type":"remove","likes":[{"emoji_id":"76","count":1}]}`, nil, &types.Notice{Kind: types.NoticeOther, Account: "100", Conv: group.Conv, Time: at, Actor: "200", Raw: clip(`{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":200,"group_id":400,"notice_type":"group_msg_emoji_like","message_id":300,"sub_type":"remove","likes":[{"emoji_id":"76","count":1}]}`)}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"reaction-is-add-false", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":200,"group_id":400,"notice_type":"group_msg_emoji_like","message_id":300,"is_add":false,"likes":[{"emoji_id":"76","count":1}]}`, nil, &types.Notice{Kind: types.NoticeOther, Account: "100", Conv: group.Conv, Time: at, Actor: "200", Raw: clip(`{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":200,"group_id":400,"notice_type":"group_msg_emoji_like","message_id":300,"is_add":false,"likes":[{"emoji_id":"76","count":1}]}`)}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"reaction-two-likes", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":200,"group_id":400,"notice_type":"group_msg_emoji_like","message_id":300,"sub_type":"add","likes":[{"emoji_id":"76","count":1},{"emoji_id":"66","count":1}]}`, nil, &types.Notice{Kind: types.NoticeOther, Account: "100", Conv: group.Conv, Time: at, Actor: "200", Raw: clip(`{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":200,"group_id":400,"notice_type":"group_msg_emoji_like","message_id":300,"sub_type":"add","likes":[{"emoji_id":"76","count":1},{"emoji_id":"66","count":1}]}`)}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"reaction-no-likes", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":200,"group_id":400,"notice_type":"group_msg_emoji_like","message_id":300}`, nil, &types.Notice{Kind: types.NoticeOther, Account: "100", Conv: group.Conv, Time: at, Actor: "200", Raw: clip(`{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":200,"group_id":400,"notice_type":"group_msg_emoji_like","message_id":300}`)}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"time-negative", `{"post_type":"message","time":-5,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Time = time.Time{}; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"time-word", `{"post_type":"message","time":"abc","self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Time = time.Time{}; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"time-bool", `{"post_type":"message","time":true,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Time = time.Time{}; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"time-null", `{"post_type":"message","time":null,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Time = time.Time{}; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"message-object", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":{"type":"text","data":{"text":"虚构乙"}},"raw_message":"你好"}`, func() *types.Inbound {
			x := base
			x.Time = at
			x.Segs = []types.Seg{{Kind: types.SegText, Text: "你好"}}
			return &x
		}(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"message-number", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":5,"raw_message":"你好"}`, func() *types.Inbound {
			x := base
			x.Time = at
			x.Segs = []types.Seg{{Kind: types.SegText, Text: "你好"}}
			return &x
		}(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"segment-not-object", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":["abc",{"type":"text","data":{"text":"你好"}}]}`, func() *types.Inbound {
			x := base
			x.Time = at
			x.Segs = []types.Seg{{Kind: types.SegOther, Raw: `"abc"`}, {Kind: types.SegText, Text: "你好"}}
			return &x
		}(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"segment-data-not-object", `{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[{"type":"text","data":"abc"},{"type":"face","data":{"id":14}}]}`, func() *types.Inbound {
			x := base
			x.Time = at
			x.Segs = []types.Seg{{Kind: types.SegText}, {Kind: types.SegFace, Ref: "14"}}
			return &x
		}(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"private-recall-her-echo", `{"post_type":"notice","time":100,"self_id":100,"user_id":100,"notice_type":"friend_recall","message_id":300}`, nil, &types.Notice{Kind: types.NoticeRecall, Account: "100", Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: "100", ID: ""}, Time: at, Actor: "100", MsgID: "300"}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"private-notice-user-zero", `{"post_type":"notice","time":100,"self_id":100,"user_id":0,"target_id":200,"notice_type":"notify","sub_type":"profile_like"}`, nil, &types.Notice{Kind: types.NoticeOther, Account: "100", Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: "100", ID: "200"}, Time: at, Raw: `{"post_type":"notice","time":100,"self_id":100,"user_id":0,"target_id":200,"notice_type":"notify","sub_type":"profile_like"}`}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"no-post-type", `{"time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300,"sender":{"nickname":"虚构甲"},"message":[]}`, func() *types.Inbound { x := base; x.Segs = []types.Seg{}; return &x }(), nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"no-post-type-no-message", `{"time":100,"self_id":100,"user_id":200,"message_type":"private","message_id":300}`, nil, nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"self", `{"post_type":"message_sent","time":100,"self_id":100,"user_id":100,"target_id":200,"message_id":300,"message":[]}`, &types.Inbound{Kind: types.InboundSelf, Account: "100", Conv: base.Conv, PlatformID: "300", From: types.User{ID: "100"}, Time: at, Segs: []types.Seg{}}, nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"group_recall", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"operator_id":201,"group_id":400,"notice_type":"group_recall","message_id":300}`, nil, &types.Notice{Kind: types.NoticeRecall, Account: "100", Conv: group.Conv, Time: at, Actor: "201", MsgID: "300"}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"friend_recall", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"notice_type":"friend_recall","message_id":300}`, nil, &types.Notice{Kind: types.NoticeRecall, Account: "100", Conv: base.Conv, Time: at, Actor: "200", MsgID: "300"}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"poke", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"group_id":400,"notice_type":"notify","sub_type":"poke","target_id":100}`, nil, &types.Notice{Kind: types.NoticePoke, Account: "100", Conv: group.Conv, Time: at, Actor: "200", Target: "100"}},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"input_status 认不出对方", `{"post_type":"notice","time":100,"self_id":100,"notice_type":"notify","sub_type":"input_status"}`, nil, nil},
		struct {
			name, frame string
			in          *types.Inbound
			notice      *types.Notice
		}{"group_card", `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"group_id":400,"notice_type":"group_card","card_new":"虚构乙"}`, nil, &types.Notice{Kind: types.NoticeOther, Account: "100", Conv: group.Conv, Time: at, Actor: "200", Raw: `{"post_type":"notice","time":100,"self_id":100,"user_id":200,"group_id":400,"notice_type":"group_card","card_new":"虚构乙"}`}},
	)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in, n, st, err := translate([]byte(tc.frame))
			if err != nil || st != nil || !reflect.DeepEqual(in, tc.in) || !reflect.DeepEqual(n, tc.notice) {
				t.Fatalf("got %#v %#v %#v %v; want %#v %#v", in, n, st, err, tc.in, tc.notice)
			}
			in2, n2, st2, err2 := translate([]byte(tc.frame))
			if !reflect.DeepEqual(in, in2) || !reflect.DeepEqual(n, n2) || !reflect.DeepEqual(st, st2) || !reflect.DeepEqual(err, err2) {
				t.Fatal("same frame changed")
			}
		})
	}
}

func TestRepeatTranslation(t *testing.T) {
	frame := []byte(`{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_id":300,"message":[]}`)
	a, _, _, err := translate(frame)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := translate(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("同一帧两次翻译不同")
	}
}

func TestReceive(t *testing.T) {
	frames := make(chan []byte, 2)
	frames <- []byte(`invalid`)
	frames <- []byte(`{"post_type":"message","time":100,"self_id":100,"user_id":200,"message_id":300,"message":[]}`)
	a := New(func(ctx context.Context) ([]byte, error) {
		select {
		case b := <-frames:
			return b, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	sink := &signalSink{got: make(chan types.Inbound, 1)}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, sink) }()
	select {
	case in := <-sink.got:
		if in.PlatformID != "300" {
			t.Fatalf("id = %q", in.PlatformID)
		}
	case <-time.After(time.Second):
		t.Fatal("no message")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// 帧里没有可用的时间：接收端拿到的是收到的时间，不是零值，也不是 1970 年
func TestReceiveFillsMissingTime(t *testing.T) {
	frames := make(chan []byte, 1)
	frames <- []byte(`{"post_type":"message","self_id":100,"user_id":200,"message_type":"private","message_id":300,"message":[]}`)
	a := New(func(ctx context.Context) ([]byte, error) {
		select {
		case b := <-frames:
			return b, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &signalSink{got: make(chan types.Inbound, 1)}
	before := time.Now()
	go func() { _ = a.Run(ctx, sink) }()
	select {
	case in := <-sink.got:
		if in.Time.Before(before) || in.Time.After(time.Now()) {
			t.Fatalf("时间 = %v，应该是收到的时间（%v 之后）", in.Time, before)
		}
	case <-time.After(time.Second):
		t.Fatal("没收到消息")
	}
}

// 被踢下线：通知没有会话，同时有一条固定类别的状态；lifecycle 只有 connect、enable 算连上。
func TestTranslateKickedAndLifecycle(t *testing.T) {
	at := time.Unix(100, 0).UTC()
	kicked := []struct{ name, frame string }{
		{"bot_offline", `{"post_type":"notice","notice_type":"bot_offline","time":100,"self_id":100,"tag":"编的标签","message":"编的原因"}`},
		{"kicked_offline", `{"post_type":"notice","notice_type":"kicked_offline","time":100,"self_id":100,"tag":"编的标签"}`},
	}
	for _, tc := range kicked {
		in, n, st, err := translate([]byte(tc.frame))
		wantN := &types.Notice{Kind: types.NoticeKicked, Account: "100", Time: at}
		wantS := &types.Status{Kind: types.StatusKicked, Account: "100", Time: at}
		if err != nil || in != nil || !reflect.DeepEqual(n, wantN) || !reflect.DeepEqual(st, wantS) {
			t.Fatalf("%s：got %#v %#v %#v %v", tc.name, in, n, st, err)
		}
	}
	for _, sub := range []string{"connect", "enable"} {
		_, _, st, err := translate([]byte(`{"post_type":"meta_event","meta_event_type":"lifecycle","time":100,"self_id":100,"sub_type":"` + sub + `"}`))
		if err != nil || st == nil || st.Kind != types.StatusConnected {
			t.Fatalf("lifecycle %s：got %#v %v，要连上", sub, st, err)
		}
	}
	in, n, st, err := translate([]byte(`{"post_type":"meta_event","meta_event_type":"lifecycle","time":100,"self_id":100,"sub_type":"disable"}`))
	if err != nil || in != nil || n != nil || st != nil {
		t.Fatalf("lifecycle disable：got %#v %#v %#v %v，什么都不该翻", in, n, st, err)
	}
}

// 一帧同时有通知和状态（被踢下线）时两样都交给接收端
func TestDispatchSendsNoticeAndStatus(t *testing.T) {
	rec := &fake.Recorder{}
	(&Adapter{}).dispatch(context.Background(), rec, []byte(`{"post_type":"notice","notice_type":"bot_offline","time":100,"self_id":100,"message":"编的原因"}`))
	if ns, ss := rec.Notices(), rec.Statuses(); len(ns) != 1 || ns[0].Kind != types.NoticeKicked || len(ss) != 1 || ss[0].Kind != types.StatusKicked {
		t.Fatalf("通知 %#v，状态 %#v；要各一条被踢", ns, ss)
	}
}

// 心跳间隔只认 1 秒到 1 小时：太短看门狗一直误判，太长乘 3 溢出、计时器立刻到点
func TestHeartbeatIntervalBounds(t *testing.T) {
	for _, tc := range []struct {
		interval string
		want     time.Duration
	}{
		{"30000", 30 * time.Second},
		{`"5000"`, 5 * time.Second},
		{"1000", time.Second},
		{"3600000", time.Hour},
		{"999", 0},
		{"1", 0},
		{"3600001", 0},
		{"9000000000000000", 0},
		{"-5000", 0},
		{"null", 0},
	} {
		frame := []byte(`{"post_type":"meta_event","meta_event_type":"heartbeat","time":100,"self_id":100,"interval":` + tc.interval + `}`)
		if got := heartbeatInterval(frame); got != tc.want {
			t.Fatalf("interval %s：看门狗用 %v，要 %v", tc.interval, got, tc.want)
		}
		if _, _, st, _ := translate(frame); st == nil || st.Interval != tc.want {
			t.Fatalf("interval %s：心跳状态 %#v，间隔要 %v", tc.interval, st, tc.want)
		}
	}
}

type signalSink struct {
	fake.Recorder
	got chan types.Inbound
}

func (s *signalSink) Inbound(_ context.Context, in types.Inbound) error { s.got <- in; return nil }
