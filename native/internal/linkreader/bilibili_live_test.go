package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// B 站直播间和个人主页：识别、取数（假接口）、风控和对不上身份的失败。数据全是编的。

func TestRecognizeBiliLiveAndSpace(t *testing.T) {
	ok := map[string][2]string{
		"https://live.bilibili.com/900001":                           {"live", "900001"},
		"https://space.bilibili.com/900002":                          {"user", "900002"},
		"https://space.bilibili.com/900002/":                         {"user", "900002"},
		"https://space.bilibili.com/900002/dynamic":                  {"user", "900002"},
		"https://space.bilibili.com/900002?spm_id_from=333.1007.0.0": {"user", "900002"},
	}
	for raw, want := range ok {
		l, found := Recognize(raw)
		if !found || l.Platform != "bilibili" || l.Kind != want[0] || l.ID != want[1] {
			t.Errorf("%s 识别成 %+v", raw, l)
		}
	}
	if l, _ := Recognize("https://space.bilibili.com/900002?spm_id_from=333.1007.0.0"); strings.Contains(l.Clean, "spm") {
		t.Errorf("追踪参数没去掉：%s", l.Clean)
	}
	for _, raw := range []string{"https://space.bilibili.com/", "https://space.bilibili.com/abc", "https://space.bilibili.com/900002/fans/follow", "https://space.bilibili.com/900002/x"} {
		if _, found := Recognize(raw); found {
			t.Errorf("%s 不该识别", raw)
		}
	}
}

const liveRoomJSON = `{"code":0,"data":{"uid":800001,"room_id":900001,"short_id":0,"attention":5600,"online":1200,"description":"<p>编的 &amp; 直播间简介</p>","live_status":1,"parent_area_name":"生活","area_name":"手工","title":"纸船试航直播","user_cover":"//i0.hdslb.com/bfs/live/fictional-cover.jpg","keyframe":"","live_time":"2026-11-01 18:30:00","tags":"折纸,手工"}}`
const liveMasterJSON = `{"code":0,"data":{"info":{"uname":"纸灯实验室","face":"https://i0.hdslb.com/bfs/face/fictional.jpg"},"follower_num":8800}}`

func TestFetchBiliLive(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/room/v1/Room/get_info":
			return response(200, liveRoomJSON), nil
		case "/live_user/v1/Master/info":
			return response(200, liveMasterJSON), nil
		}
		t.Fatalf("意外的请求：%s", req.URL)
		return nil, nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://live.bilibili.com/900001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Title.Value != "纸船试航直播" || r.Author.Name.Value != "纸灯实验室" || r.Text.Value != "编的 & 直播间简介" {
		t.Fatalf("标题、主播、简介：%+v %+v %q", r.Title, r.Author.Name, r.Text.Value)
	}
	if r.Category.Value != "生活 / 手工" || r.Stats["online"].Value != 1200 || r.Stats["followers"].Value != 8800 || r.Published.State != "available" {
		t.Fatalf("分区、人气、粉丝、开播时间：%+v", r)
	}
	if r.Flags["live_status"].Value != 1 || len(r.Tags.Value) != 2 {
		t.Fatalf("状态、标签：%+v %+v", r.Flags["live_status"], r.Tags)
	}
	if len(r.Media.Value) != 1 || r.Media.Value[0].URL.Value != "https://i0.hdslb.com/bfs/live/fictional-cover.jpg" {
		t.Fatalf("封面应补成 https：%+v", r.Media.Value)
	}
}

// 主播信息那一步失败，直播间照常取到，只是没有主播名
func TestFetchBiliLiveSurvivesMasterFailure(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/room/v1/Room/get_info" {
			return response(200, liveRoomJSON), nil
		}
		return response(200, `{"code":-352}`), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://live.bilibili.com/900001"))
	if err != nil || r.Title.State != "available" || r.Author.Name.State == "available" {
		t.Fatalf("应只缺主播名：%v %+v", err, r.Author.Name)
	}
	// 主播信息取不到时，直播间的 attention（关注直播间的人数）当粉丝数用
	if r.Stats["followers"].Value != 5600 || r.Stats["attention"].State == "available" {
		t.Fatalf("粉丝：%+v 关注：%+v", r.Stats["followers"], r.Stats["attention"])
	}
}

// 直播间的 attention 和主播粉丝数是同一个数：只记一份粉丝，不另出“关注”；轮播中人气是 0，不当真实人气
func TestFetchBiliLiveNoDuplicateStats(t *testing.T) {
	room := strings.NewReplacer(`"live_status":1`, `"live_status":2`, `"online":1200`, `"online":0`).Replace(liveRoomJSON)
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/room/v1/Room/get_info" {
			return response(200, room), nil
		}
		return response(200, liveMasterJSON), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://live.bilibili.com/900001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Stats["followers"].Value != 8800 || r.Stats["attention"].State == "available" || r.Stats["online"].State == "available" {
		t.Fatalf("统计：%+v", r.Stats)
	}
}

func TestFetchBiliLiveFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		kind string
	}{
		"风控":   {`{"code":-352}`, "risk_control"},
		"不存在":  {`{"code":-404}`, "not_found"},
		"身份不符": {strings.Replace(liveRoomJSON, `"room_id":900001`, `"room_id":900009`, 1), "identity_mismatch"},
		"缺标题":  {`{"code":0,"data":{"uid":1,"room_id":900001}}`, "invalid_response"},
	} {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, tc.body), nil })
		_, err := f.Fetch(context.Background(), link(t, "https://live.bilibili.com/900001"))
		fe, ok := err.(*FetchError)
		if !ok || fe.Kind != tc.kind {
			t.Errorf("%s：得到 %v，应为 %s", name, err, tc.kind)
		}
	}
}

const spaceJSON = `{"code":0,"data":{"card":{"mid":"900002","name":"纸灯实验室","face":"http://i0.hdslb.com/bfs/face/fictional.jpg","sign":"做折纸和纸灯的小实验室。","fans":8800,"attention":120,"Official":{"title":"纸灯实验室官方账号"}},"archive_count":56,"like_num":30000}}`

func TestFetchBiliSpace(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/x/web-interface/card" || req.URL.Query().Get("mid") != "900002" {
			t.Fatalf("意外的请求：%s", req.URL)
		}
		return response(200, spaceJSON), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://space.bilibili.com/900002"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "user" || r.Title.Value != "纸灯实验室" || r.Text.Value != "做折纸和纸灯的小实验室。" || r.Category.Value != "纸灯实验室官方账号" {
		t.Fatalf("名字、签名、认证：%+v", r)
	}
	if r.Stats["followers"].Value != 8800 || r.Stats["videos"].Value != 56 || r.Stats["likes"].Value != 30000 || r.Stats["articles"].State == "available" {
		t.Fatalf("统计，没返回的不冒充零：%+v", r.Stats)
	}
	if r.Author.Avatar.Value != "https://i0.hdslb.com/bfs/face/fictional.jpg" || len(r.Media.Value) != 1 {
		t.Fatalf("头像应是 https 并作为封面：%+v", r.Author.Avatar)
	}
}

func TestFetchBiliSpaceFailures(t *testing.T) {
	for name, tc := range map[string]struct{ body, kind string }{
		"风控":    {`{"code":-352}`, "risk_control"},
		"用户不存在": {`{"code":-404}`, "not_found"},
		"身份不符":  {strings.Replace(spaceJSON, `"mid":"900002"`, `"mid":"900009"`, 1), "identity_mismatch"},
		"没有名字":  {`{"code":0,"data":{"card":{"mid":"900002"}}}`, "invalid_response"},
	} {
		f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, tc.body), nil })
		_, err := f.Fetch(context.Background(), link(t, "https://space.bilibili.com/900002"))
		fe, ok := err.(*FetchError)
		if !ok || fe.Kind != tc.kind {
			t.Errorf("%s：得到 %v，应为 %s", name, err, tc.kind)
		}
	}
}
