package app

// 日历、天气两个工具（规格-主动联系 第十九节）的装配验收：
//   - 完整路径：模型收到工具 → 调用 → 结果进下一轮请求，不弹审批；
//   - 所在地的可见性：只有听众是主人的场合（本地私聊、QQ 主人私聊、主动联系的线）才能用默认所在地，
//     本地群聊、QQ 群、别人的 QQ 私聊不传地点时一律回同一句"不知道在哪"，所在地不出现在任何一次模型请求里；
//   - 全程假服务器，测试不联网；名字、地点、号码都是编的。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/lifetools"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/turn"
)

// 编的所在地：lifeSecret 是比城市细的那部分，lifeCity 是城市，群场合只能出现城市
const (
	lifeSecret = "私人所在地甲乙丙"
	lifeCity   = "编的城市"
	lifeFull   = lifeSecret + "，" + lifeCity + "，编的国"
	lifeYAML   = "location: {name: " + lifeFull + ", city: " + lifeCity + ", region: 编的省, country: 编的国, latitude: 31.26, longitude: 121.48, cityLatitude: 31.23, cityLongitude: 121.47, timezone: Asia/Shanghai}\n"
)

const lifeHoliday2027 = `{"year":2027,"papers":["编的通知"],"days":[
 {"name":"春节","date":"2027-01-31","isOffDay":false},
 {"name":"春节","date":"2027-02-13","isOffDay":true},
 {"name":"春节","date":"2027-02-14","isOffDay":false}]}`

// lifeFake 是假的编码、预报、假日数据服务器。编码接口把问的地名原样当名字回，所以结果里出现地名就说明用了它
type lifeFake struct {
	srv *httptest.Server
	mu  sync.Mutex
	// asked 是编码接口收到的地名，forecast 是预报接口收到的查询串
	asked    []string
	forecast []string
}

func newLifeFake(t *testing.T) *lifeFake {
	t.Helper()
	f := &lifeFake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/geo", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		f.mu.Lock()
		f.asked = append(f.asked, name)
		f.mu.Unlock()
		b, _ := json.Marshal(map[string]any{"results": []map[string]any{{"name": name, "admin1": "编的省", "country": "编的国", "latitude": 31.5, "longitude": 121.25}}})
		w.Write(b)
	})
	mux.HandleFunc("/fc", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.forecast = append(f.forecast, r.URL.RawQuery)
		f.mu.Unlock()
		fmt.Fprint(w, `{"current":{"temperature_2m":12.3,"apparent_temperature":10.1,"weather_code":3},"daily":{"time":["2026-10-28","2026-10-29"],"temperature_2m_max":[15,17],"temperature_2m_min":[6,8],"precipitation_probability_max":[20,80],"weather_code":[2,61]}}`)
	})
	mux.HandleFunc("/hol/2027.json", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, lifeHoliday2027) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *lifeFake) askedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *lifeFake) forecasts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.forecast...)
}

// attach 把假服务器接进测试环境，并写配置（所在地 + 假日数据地址）
func (f *lifeFake) attach(e *testEnv, extra string) {
	e.lifeClient = f.srv.Client()
	e.lifeGeocodeURL = f.srv.URL + "/geo"
	e.lifeForecastURL = f.srv.URL + "/fc"
	e.configure(lifeYAML + "calendar:\n  holidayUrl: " + f.srv.URL + "/hol/{year}.json\n" + extra)
}

func lifeScript(replies ...llmtest.Reply) llmtest.Script {
	return llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}, Replies: replies}
}

func (e *testEnv) bodiesCopy() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.bodies...)
}

func toolNames(t *testing.T, body string) []string {
	t.Helper()
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range req.Tools {
		out = append(out, x.Function.Name)
	}
	return out
}

func containsName(names []string, n string) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}

// 完整路径：本地私聊里主人在场，模型收到两个工具、调用、结果进下一轮；不弹审批（弹了这里会一直等）
func TestLifeToolsRoundTripLocalOwner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fake := newLifeFake(t)
	srv := httptest.NewServer(llmtest.Handler(lifeScript(
		llmtest.Call("w", "weather", `{}`),
		llmtest.Call("c", "calendar", `{"date":"2027-01-31"}`),
		llmtest.Text("看过了"),
	), &e.bodies, &e.mu))
	defer srv.Close()
	e.llm = srv
	fake.attach(e, "")
	a := e.open()
	defer a.Close()
	out := <-a.hub.Send(testChar, "今天天气怎么样，周日要上班吗")
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	bodies := e.bodiesCopy()
	if len(bodies) != 3 {
		t.Fatalf("要 3 次请求（调天气、调日历、收结果），实际 %d", len(bodies))
	}
	names := toolNames(t, bodies[0])
	if !containsName(names, "weather") || !containsName(names, "calendar") {
		t.Fatalf("模型没收到两个工具：%v", names)
	}
	// 天气结果进了第二次请求，日历结果进了第三次
	for _, want := range []string{lifeSecret, "现在 12.3°C", "降水概率 80%"} {
		if !strings.Contains(bodies[1], want) {
			t.Errorf("第二次请求缺 %q", want)
		}
	}
	for _, want := range []string{"2027-01-31 星期日", "春节调休，星期日要上班"} {
		if !strings.Contains(bodies[2], want) {
			t.Errorf("第三次请求缺 %q", want)
		}
	}
	if got := fake.askedNames(); len(got) != 0 {
		t.Fatalf("设了所在地就直接用存的坐标，不再编码：%v", got)
	}
	if got := fake.forecasts(); len(got) != 1 || !strings.Contains(got[0], "latitude=31.2600") || !strings.Contains(got[0], "longitude=121.4800") {
		t.Fatalf("主人在场不传地点该用设置里的所在地：%v", got)
	}
}

// lifeScene 是一种场合：怎么进回合，以及主人在不在听
type lifeScene struct {
	name  string
	level lifetools.PlaceGrant // 这个场合默认所在地给到哪一级
	input func(a *App) turn.Input
}

func lifeScenes() []lifeScene {
	groupIdentity := func(a *App, conv, platform, channel string) (kernel.Invocation, audience.Audience) {
		in, aud := qqClientIdentity(a)
		in.Scope.Platform, in.Scope.Channel, in.Scope.Conversation, in.Scope.Scene = platform, channel, conv, "group"
		in.Scope.AccountID = ""
		aud.Channel, aud.Conversation, aud.Scene = channel, conv, "group"
		return in, aud
	}
	respond := func(in kernel.Invocation, aud audience.Audience, a *App) turn.Input {
		return turn.Input{Identity: in, CharacterID: a.character, Conversation: in.Scope.Conversation, Scope: in.Scope.ScopeID, Audience: aud, Text: "天气怎么样"}
	}
	return []lifeScene{
		{"QQ 主人私聊", lifetools.PlaceFull, func(a *App) turn.Input { in, aud := qqClientIdentity(a); return respond(in, aud, a) }},
		{"本地群聊（主人在，PrivateOK 为真）", lifetools.PlaceCity, func(a *App) turn.Input {
			in, aud := groupIdentity(a, "console:group:fixture", "local", "console")
			aud.PrivateOK = true
			return respond(in, aud, a)
		}},
		{"QQ 群（主人发言）", lifetools.PlaceCity, func(a *App) turn.Input {
			in, aud := groupIdentity(a, "qq:group:"+testGroup, "qq", "qq")
			in.Scope.AccountID = a.account
			aud.PrivateOK = false
			return respond(in, aud, a)
		}},
		{"QQ 群（伪造 PrivateOK）", lifetools.PlaceCity, func(a *App) turn.Input {
			in, aud := groupIdentity(a, "qq:group:"+testGroup, "qq", "qq")
			in.Scope.AccountID = a.account
			aud.PrivateOK = true
			return respond(in, aud, a)
		}},
		{"别人的 QQ 私聊", lifetools.PlaceNone, func(a *App) turn.Input {
			in, aud := qqClientIdentity(a)
			in.Scope.ActorKind, in.Scope.ActorID = "human", "qq:503"
			in.Scope.Conversation = "qq:private:" + testAccount + ":503"
			aud.Conversation, aud.PrivateOK, aud.OwnerAsked = in.Scope.Conversation, false, false
			return respond(in, aud, a)
		}},
		{"别人的 QQ 私聊（伪造主人身份）", lifetools.PlaceNone, func(a *App) turn.Input {
			in, aud := qqClientIdentity(a)
			in.Scope.Conversation = "qq:private:" + testAccount + ":503"
			aud.Conversation = in.Scope.Conversation
			return respond(in, aud, a)
		}},
	}
}

func lifeRespond(t *testing.T, sc lifeScene, script llmtest.Script) (*testEnv, *lifeFake, turn.Outcome) {
	t.Helper()
	e := newEnv(t)
	fake := newLifeFake(t)
	srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	fake.attach(e, "")
	a := e.open()
	t.Cleanup(func() { a.Close() })
	return e, fake, <-a.hub.Respond(sc.input(a))
}

// 所在地的可见性：不传地点时，只有主人在的场合用默认所在地；其余场合所在地不出现在任何一次模型请求里，也不发给编码接口
func TestLifeDefaultPlaceByScene(t *testing.T) {
	t.Parallel()
	for _, sc := range lifeScenes() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			e, fake, out := lifeRespond(t, sc, lifeScript(llmtest.Call("w", "weather", `{}`), llmtest.Text("好")))
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			bodies := e.bodiesCopy()
			if len(bodies) != 2 {
				t.Fatalf("要 2 次请求，实际 %d", len(bodies))
			}
			if names := toolNames(t, bodies[0]); !containsName(names, "weather") || !containsName(names, "calendar") {
				t.Fatalf("所有场合都该看见两个工具：%v", names)
			}
			if asked := fake.askedNames(); len(asked) != 0 {
				t.Fatalf("默认所在地不该发给编码接口：%v", asked)
			}
			fc := fake.forecasts()
			switch sc.level {
			case lifetools.PlaceFull:
				if len(fc) != 1 || !strings.Contains(fc[0], "latitude=31.2600") || !strings.Contains(bodies[1], lifeFull) {
					t.Fatalf("主人本人该用完整的：%v", fc)
				}
				return
			case lifetools.PlaceCity:
				// 城市：结果里有城市；比城市细的名字、精确坐标不出现在任何一次模型请求里，发给预报接口的坐标也取整
				if len(fc) != 1 || !strings.Contains(fc[0], "latitude=31.2000") || !strings.Contains(fc[0], "longitude=121.5000") {
					t.Fatalf("群场合要用城市级坐标：%v", fc)
				}
				if !strings.Contains(bodies[1], lifeCity+"（编的省，编的国） 现在") {
					t.Fatalf("群场合该知道城市：%s", bodies[1])
				}
				for i, b := range bodies {
					for _, bad := range []string{lifeSecret, "31.26", "121.48"} {
						if strings.Contains(b, bad) {
							t.Fatalf("第 %d 次模型请求里出现了比城市细的 %s", i+1, bad)
						}
					}
				}
				return
			}
			if len(fc) != 0 {
				t.Fatalf("这个场合不该查所在地的天气：%v", fc)
			}
			for i, b := range bodies {
				if strings.Contains(b, lifeSecret) || strings.Contains(b, lifeCity) {
					t.Fatalf("第 %d 次模型请求里出现了所在地", i+1)
				}
			}
			if !strings.Contains(bodies[1], lifetools.UnknownPlace) {
				t.Fatalf("要回“不知道在哪”：%s", bodies[1])
			}
		})
	}
}

// 显式传的地点各场合都能查（地名是她自己说出来的）
func TestLifeExplicitPlaceWorksInEveryScene(t *testing.T) {
	t.Parallel()
	for _, sc := range lifeScenes() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			e, fake, out := lifeRespond(t, sc, lifeScript(llmtest.Call("w", "weather", `{"location":"公开地点"}`), llmtest.Text("好")))
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			bodies := e.bodiesCopy()
			if asked := fake.askedNames(); len(asked) != 1 || asked[0] != "公开地点" || len(bodies) != 2 || !strings.Contains(bodies[1], "公开地点（编的省，编的国） 现在 12.3°C") {
				t.Fatalf("%v", asked)
			}
			for _, b := range bodies {
				if strings.Contains(b, lifeSecret) {
					t.Fatal("显式地点不该带出所在地")
				}
			}
		})
	}
}

// 所在地没设：主人在场也回同一句“不知道在哪”，不联网
func TestLifeUnsetPlaceEvenForOwner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fake := newLifeFake(t)
	srv := httptest.NewServer(llmtest.Handler(lifeScript(llmtest.Call("w", "weather", `{}`), llmtest.Text("好")), &e.bodies, &e.mu))
	defer srv.Close()
	e.llm = srv
	e.lifeClient, e.lifeGeocodeURL, e.lifeForecastURL = fake.srv.Client(), fake.srv.URL+"/geo", fake.srv.URL+"/fc"
	e.configure("")
	a := e.open()
	defer a.Close()
	if out := <-a.hub.Send(testChar, "天气"); out.Err != nil {
		t.Fatal(out.Err)
	}
	bodies := e.bodiesCopy()
	if len(bodies) != 2 || !strings.Contains(bodies[1], lifetools.UnknownPlace) || len(fake.askedNames()) != 0 {
		t.Fatalf("%d %v", len(bodies), fake.askedNames())
	}
}

// 所在地在设置页改了马上生效（每次调用重读配置）
func TestLifeLocationChangeTakesEffectImmediately(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fake := newLifeFake(t)
	srv := httptest.NewServer(llmtest.Handler(lifeScript(llmtest.Call("w", "weather", `{}`), llmtest.Text("好")), &e.bodies, &e.mu))
	defer srv.Close()
	e.llm = srv
	fake.attach(e, "")
	a := e.open()
	defer a.Close()
	e.configure("location: {name: 新搬的地方, city: 新城, latitude: 1.5, longitude: 2.5}\n")
	if out := <-a.hub.Send(testChar, "天气"); out.Err != nil {
		t.Fatal(out.Err)
	}
	if fc := fake.forecasts(); len(fc) != 1 || !strings.Contains(fc[0], "latitude=1.5000") {
		t.Fatalf("%v", fc)
	}
	if bodies := e.bodiesCopy(); !strings.Contains(bodies[len(bodies)-1], "新搬的地方") {
		t.Fatal("结果里要是新的所在地")
	}
}

// 身份形状表：照 TestClientOwnerBoundary——缺、空、0、自己的号、类型错误、异常都不算主人在场
func TestLifeOwnerShapeBoundary(t *testing.T) {
	t.Parallel()
	a := &App{account: "501", character: "fixture"}
	a.cfg.QQ.Owner = "502"
	in, aud := qqClientIdentity(a)
	if !a.lifeOwnerShape(in, aud) {
		t.Fatal("QQ 主人私聊该算")
	}
	cases := []struct {
		name   string
		change func(*kernel.Invocation, *audience.Audience)
	}{
		{"身份缺", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.ActorID = "" }},
		{"身份 0", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.ActorID = "0" }},
		{"身份类型是别的", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.ActorKind = "human" }},
		{"身份是自己的号", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.ActorID = "501" }},
		{"system 冒充", func(i *kernel.Invocation, _ *audience.Audience) {
			i.Scope.ActorKind, i.Scope.ActorID = "system", "owner"
		}},
		{"对面不是主人号", func(i *kernel.Invocation, au *audience.Audience) {
			i.Scope.Conversation = "qq:private:501:503"
			au.Conversation = i.Scope.Conversation
		}},
		{"QQ 群", func(i *kernel.Invocation, au *audience.Audience) { i.Scope.Scene, au.Scene = "group", "group" }},
		{"会话串带群", func(i *kernel.Invocation, au *audience.Audience) {
			i.Scope.Conversation = "qq:group:90001"
			au.Conversation = i.Scope.Conversation
		}},
		{"本地群聊", func(i *kernel.Invocation, au *audience.Audience) {
			i.Scope.Channel, au.Channel, i.Scope.Scene, au.Scene = "console", "console", "group", "group"
		}},
		{"子代理调用", func(i *kernel.Invocation, _ *audience.Audience) { i.Kind = kernel.KindChild }},
		{"有父运行", func(i *kernel.Invocation, _ *audience.Audience) { i.ParentRunID = "r1" }},
		{"听众不是主人问的", func(_ *kernel.Invocation, au *audience.Audience) { au.OwnerAsked = false }},
		{"听众不是主人在场", func(_ *kernel.Invocation, au *audience.Audience) { au.PrivateOK = false }},
		{"听众会话对不上", func(_ *kernel.Invocation, au *audience.Audience) { au.Conversation = "elsewhere" }},
		{"别的角色", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.CharacterID = "other" }},
		{"别的号", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.AccountID = "504" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, y := in, aud
			tc.change(&x, &y)
			if a.lifeOwnerShape(x, y) {
				t.Fatal("不是主人在场却被当成主人")
			}
		})
	}
	for _, bad := range []string{"", "0", "501", "0502", "502x"} {
		a.cfg.QQ.Owner = config.QQID(bad)
		bi, ba := qqClientIdentity(a)
		if a.lifeOwnerShape(bi, ba) {
			t.Fatalf("主人号 %q 不合法也被认了", bad)
		}
	}
}

// 线的形状：用 turn.ThreadInput 造真的，再逐项改坏
func TestLifeThreadShapeBoundary(t *testing.T) {
	t.Parallel()
	conv := threadConvOf("t1")
	base := turn.ThreadInput(testChar, conv, []int64{1}, "x", nil)
	if !lifeThreadShape(base.Identity, base.Audience) {
		t.Fatal("线自己该算")
	}
	cases := []struct {
		name   string
		change func(*kernel.Invocation, *audience.Audience)
	}{
		{"主人身份", func(i *kernel.Invocation, _ *audience.Audience) {
			i.Scope.ActorKind, i.Scope.ActorID = "owner", "owner"
		}},
		{"别的 system", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.ActorID = "someone" }},
		{"身份缺", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.ActorKind, i.Scope.ActorID = "", "" }},
		{"权限档不对", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.ScopeID = "owner" }},
		{"权限档缺", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.ScopeID = "" }},
		{"会话对不上", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.Conversation = threadConvOf("t2") }},
		{"角色对不上", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.CharacterID = "other" }},
		{"角色缺", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.CharacterID = "" }},
		{"听众 PrivateOK", func(_ *kernel.Invocation, au *audience.Audience) { au.PrivateOK = true }},
		{"听众 OwnerAsked", func(_ *kernel.Invocation, au *audience.Audience) { au.OwnerAsked = true }},
		{"不是线的会话", func(i *kernel.Invocation, au *audience.Audience) {
			i.Scope.Conversation, au.Conversation = "console:"+testChar, "console:"+testChar
		}},
		{"自检话题", func(i *kernel.Invocation, au *audience.Audience) {
			i.Scope.Conversation = audience.SelftestConversation(testChar, audience.SelftestTopicID(testChar, ""))
			au.Conversation = i.Scope.Conversation
		}},
		{"QQ 渠道", func(i *kernel.Invocation, au *audience.Audience) { i.Scope.Channel, au.Channel = "qq", "qq" }},
		{"平台是 qq", func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.Platform = "qq" }},
		{"群场合", func(i *kernel.Invocation, au *audience.Audience) { i.Scope.Scene, au.Scene = "group", "group" }},
		{"子代理调用", func(i *kernel.Invocation, _ *audience.Audience) { i.Kind = kernel.KindChild }},
		{"有父运行", func(i *kernel.Invocation, _ *audience.Audience) { i.ParentRunID = "r1" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, y := base.Identity, base.Audience
			tc.change(&x, &y)
			if lifeThreadShape(x, y) {
				t.Fatal("伪造的线被认了")
			}
		})
	}
}

// 主动联系的线里也能用：线是替主人找主人的，默认所在地可用；线的工具清单里有这两个，且没有执行工具
func TestLifeToolsInProactiveThread(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fake := newLifeFake(t)
	script := lifeScript(llmtest.Call("w", "weather", `{}`), llmtest.Call("c", "calendar", `{"date":"2027-02-13"}`), llmtest.Text("线里想想"))
	srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	fake.attach(e, threadCfg)
	log := &attentionLog{}
	a := e.open()
	a.opt.Attention = log.add
	d := a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close() })
	if a.pthreads == nil || a.proactive == nil {
		t.Fatal("应当建导演和线的宿主")
	}
	te := &threadEnv{t: t, e: e, a: a, d: d, log: log, key: localKey("console:" + testChar)}
	te.wake()
	names := te.requestTools(1)
	if !containsName(names, "weather") || !containsName(names, "calendar") || !containsName(names, "thread_send") || containsName(names, "powershell") {
		t.Fatalf("线的工具清单：%v", names)
	}
	bodies := e.bodiesCopy()
	if len(bodies) < 3 {
		t.Fatalf("请求数 %d", len(bodies))
	}
	if fc := fake.forecasts(); len(fc) != 1 || !strings.Contains(fc[0], "latitude=31.2600") || !strings.Contains(bodies[1], lifeFull) {
		t.Fatalf("线算主人本人，该能用完整的：%v", fc)
	}
	if !strings.Contains(bodies[2], "春节，法定放假") {
		t.Fatal("日历结果没进下一轮")
	}
}

// 伪造的线调用拿不到所在地：主人身份的听众、PrivateOK 的线听众，直接调工具函数都回“不知道在哪”
func TestLifeWeatherToolRejectsForgedThreadCalls(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fake := newLifeFake(t)
	srv := httptest.NewServer(llmtest.Handler(lifeScript(llmtest.Text("x")), &e.bodies, &e.mu))
	defer srv.Close()
	e.llm = srv
	fake.attach(e, "")
	a := e.open()
	defer a.Close()
	conv := threadConvOf("f1")
	base := turn.ThreadInput(testChar, conv, []int64{1}, "x", nil)
	for name, mod := range map[string]func(*turn.ToolCall){
		"线的听众 PrivateOK": func(c *turn.ToolCall) { c.Audience.PrivateOK = true },
		"改成别的角色":         func(c *turn.ToolCall) { c.Identity.Scope.CharacterID = "other" },
		"身份是普通 human":    func(c *turn.ToolCall) { c.Identity.Scope.ActorKind, c.Identity.Scope.ActorID = "human", "x" },
	} {
		c := turn.ToolCall{CharacterID: testChar, Conversation: conv, Identity: base.Identity, Audience: base.Audience, Args: lifeArgs(`{}`)}
		mod(&c)
		if a.lifeDefaultPlace(context.Background(), c.Identity, c.Audience) != lifetools.PlaceNone {
			t.Errorf("%s：不该给默认所在地", name)
		}
	}
	// 伪造的调用身份没通过宿主的运行核对（没有这一次运行）：线的形状对也不给
	if a.lifeDefaultPlace(context.Background(), base.Identity, base.Audience) != lifetools.PlaceNone {
		t.Fatal("不是宿主正在跑的调用，不该给默认所在地")
	}
	if len(fake.askedNames()) != 0 {
		t.Fatal("不该联网")
	}
}

func lifeArgs(s string) jsjson.Value {
	v, err := jsjson.Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

// 改了 qq.owner 之后（重启接收前），旧主人的 QQ 私聊拿不到所在地，也不联网；配置读不出来同样不给
func TestLifeOldOwnerLosesPlaceAfterOwnerChange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		modify func(old string) string
	}{
		{"主人号改了", func(old string) string { return strings.Replace(old, "owner: "+testOwner, "owner: 299", 1) }},
		{"配置读不出来", func(string) string { return "qq: [这不是合法的配置" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			fake := newLifeFake(t)
			srv := httptest.NewServer(llmtest.Handler(lifeScript(llmtest.Call("w", "weather", `{}`), llmtest.Text("好")), &e.bodies, &e.mu))
			t.Cleanup(srv.Close)
			e.llm = srv
			fake.attach(e, "")
			a := e.open()
			t.Cleanup(func() { a.Close() })
			path := filepath.Join(e.data, "config", "sumika.yml")
			old, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := tc.modify(string(old))
			if changed == string(old) {
				t.Fatal("测试没改动配置")
			}
			if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
				t.Fatal(err)
			}
			out := <-a.hub.Respond(lifeScenes()[0].input(a)) // 启动时的主人，QQ 私聊
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			bodies := e.bodiesCopy()
			if fc := fake.forecasts(); len(fc) != 0 {
				t.Fatalf("旧主人不该拿到所在地：%v", fc)
			}
			for i, b := range bodies {
				if strings.Contains(b, lifeSecret) {
					t.Fatalf("第 %d 次模型请求里出现了所在地", i+1)
				}
			}
			if len(bodies) != 2 || !strings.Contains(bodies[1], lifetools.UnknownPlace) {
				t.Fatalf("要回“不知道在哪”：%d", len(bodies))
			}
		})
	}
}

// 真实入口：QQ 群里来一条 @ 她的消息，走完整的收发路径（回合的身份由 trustedInput 从库里定，AccountID 是 channel）
func TestLifeRealQQGroupGetsCity(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fake := newLifeFake(t)
	srv := httptest.NewServer(llmtest.Handler(lifeScript(llmtest.Call("w", "weather", `{}`), llmtest.Text("好")), &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	fake.attach(e, "")
	e.expectNoActions(t)
	a := e.start()
	t.Cleanup(func() { a.Close() })
	conn := e.peer.accept(t)
	e.peer.send(t, conn, groupAtFrame(e.now, testAccount, "501", testOwner, "现在天气怎么样"))
	waitFor(t, "查到天气", func() bool { return len(fake.forecasts()) == 1 })
	if fc := fake.forecasts(); !strings.Contains(fc[0], "latitude=31.2000") {
		t.Fatalf("真实 QQ 群要用城市级坐标：%v", fc)
	}
	waitFor(t, "结果进下一轮", func() bool { return len(e.bodiesCopy()) >= 2 })
	bodies := e.bodiesCopy()
	if !strings.Contains(bodies[1], lifeCity) {
		t.Fatal("群里该知道城市")
	}
	for _, b := range bodies {
		if strings.Contains(b, lifeSecret) {
			t.Fatal("群里出现了比城市细的地名")
		}
	}
}

// 形状表：“主人开放的群”认什么、不认什么。直接调判定函数，每行只改一处
func TestLifeOpenGroupShapeBoundary(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fake := newLifeFake(t)
	srv := httptest.NewServer(llmtest.Handler(lifeScript(llmtest.Text("x")), &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	fake.attach(e, "")
	a := e.open()
	t.Cleanup(func() { a.Close() })
	qq := func() (kernel.Invocation, audience.Audience) {
		conv := "qq:group:" + testGroup
		return kernel.Invocation{Kind: kernel.KindForeground, Scope: kernel.Scope{CharacterID: a.character, Conversation: conv, ScopeID: "fixture", Platform: "qq", Channel: "qq", AccountID: "channel", Scene: "group", ActorKind: "human", ActorID: "qq:503"}},
			audience.Audience{Channel: "qq", Conversation: conv, Scene: "group"}
	}
	local := func() (kernel.Invocation, audience.Audience) {
		conv := "console:group:fixture"
		return kernel.Invocation{Kind: kernel.KindForeground, Scope: kernel.Scope{CharacterID: a.character, Conversation: conv, ScopeID: "local-group", Platform: "local", Channel: "console", Scene: "group", ActorKind: "owner", ActorID: "owner"}},
			audience.Audience{Channel: "console", Conversation: conv, Scene: "group", PrivateOK: true}
	}
	for _, tc := range []struct {
		name string
		base func() (kernel.Invocation, audience.Audience)
		mod  func(*kernel.Invocation, *audience.Audience)
		want bool
	}{
		{"QQ 群：群友", qq, func(*kernel.Invocation, *audience.Audience) {}, true},
		{"QQ 群：主人", qq, func(i *kernel.Invocation, _ *audience.Audience) {
			i.Scope.ActorKind, i.Scope.ActorID = "owner", "owner"
		}, true},
		{"QQ 群：几个人一批", qq, func(i *kernel.Invocation, _ *audience.Audience) {
			i.Scope.ActorKind, i.Scope.ActorID = "mixed", "mixed"
		}, true},
		{"QQ 群：写了当前账号", qq, func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.AccountID = a.account }, true},
		{"本地群聊：主人", local, func(*kernel.Invocation, *audience.Audience) {}, true},
		{"本地群聊：别的角色", local, func(i *kernel.Invocation, _ *audience.Audience) {
			i.Scope.ActorKind, i.Scope.ActorID = "character", "other"
		}, true},
		{"不在 qq.groups 里的群", qq, func(i *kernel.Invocation, u *audience.Audience) {
			i.Scope.Conversation = "qq:group:90009"
			u.Conversation = i.Scope.Conversation
		}, false},
		{"群号是 0", qq, func(i *kernel.Invocation, u *audience.Audience) {
			i.Scope.Conversation = "qq:group:0"
			u.Conversation = i.Scope.Conversation
		}, false},
		{"群号前导零", qq, func(i *kernel.Invocation, u *audience.Audience) {
			i.Scope.Conversation = "qq:group:0" + testGroup
			u.Conversation = i.Scope.Conversation
		}, false},
		{"群号不是数字", qq, func(i *kernel.Invocation, u *audience.Audience) {
			i.Scope.Conversation = "qq:group:abc"
			u.Conversation = i.Scope.Conversation
		}, false},
		{"别的接收账号", qq, func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.AccountID = "599" }, false},
		{"缺账号", qq, func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.AccountID = "" }, false},
		{"别的角色", qq, func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.CharacterID = "other" }, false},
		{"子代理", qq, func(i *kernel.Invocation, _ *audience.Audience) { i.Kind = kernel.KindChild }, false},
		{"有父运行", qq, func(i *kernel.Invocation, _ *audience.Audience) { i.ParentRunID = "p" }, false},
		{"缺身份", qq, func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.ActorKind, i.Scope.ActorID = "", "" }, false},
		{"身份是 0", qq, func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.ActorID = "0" }, false},
		{"线的 system 身份", qq, func(i *kernel.Invocation, _ *audience.Audience) {
			i.Scope.ActorKind, i.Scope.ActorID = "system", audience.ThreadActor
		}, false},
		{"听众不是群", qq, func(_ *kernel.Invocation, u *audience.Audience) { u.Scene = "private" }, false},
		{"身份不是群", qq, func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.Scene = "private" }, false},
		{"听众会话对不上", qq, func(_ *kernel.Invocation, u *audience.Audience) { u.Conversation = "qq:group:90001" }, false},
		{"听众渠道对不上", qq, func(_ *kernel.Invocation, u *audience.Audience) { u.Channel = "console" }, false},
		{"群场合却是私聊会话", qq, func(i *kernel.Invocation, u *audience.Audience) {
			i.Scope.Conversation = "qq:private:" + a.account + ":503"
			u.Conversation = i.Scope.Conversation
		}, false},
		{"平台对不上", qq, func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.Platform = "local" }, false},
		{"本地群聊会话串不对", local, func(i *kernel.Invocation, u *audience.Audience) {
			i.Scope.Conversation = "console:" + a.character
			u.Conversation = i.Scope.Conversation
		}, false},
		{"本地群聊平台不对", local, func(i *kernel.Invocation, _ *audience.Audience) { i.Scope.Platform = "qq" }, false},
	} {
		in, aud := tc.base()
		tc.mod(&in, &aud)
		if got := a.lifeOpenGroup(in, aud); got != tc.want {
			t.Errorf("%s：%v，应是 %v", tc.name, got, tc.want)
		}
	}
	// 配置读不出来：QQ 群不给；工具层看到 Stale，本地群聊也当不知道
	path := filepath.Join(e.data, "config", "sumika.yml")
	if err := os.WriteFile(path, []byte("qq: [坏的"), 0o644); err != nil {
		t.Fatal(err)
	}
	if in, aud := qq(); a.lifeOpenGroup(in, aud) {
		t.Error("配置读不出来还给 QQ 群")
	}
	if !a.lifeSettings().Stale {
		t.Error("配置读不出来要标 Stale")
	}
}

// 只有省没有市：群里和没设是同一句；主人本人照常用
func TestLifeProvinceOnlyInGroups(t *testing.T) {
	t.Parallel()
	for _, sc := range lifeScenes() {
		if sc.level == lifetools.PlaceNone {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			fake := newLifeFake(t)
			srv := httptest.NewServer(llmtest.Handler(lifeScript(llmtest.Call("w", "weather", `{}`), llmtest.Text("好")), &e.bodies, &e.mu))
			t.Cleanup(srv.Close)
			e.llm = srv
			e.lifeClient, e.lifeGeocodeURL, e.lifeForecastURL = fake.srv.Client(), fake.srv.URL+"/geo", fake.srv.URL+"/fc"
			e.configure("location: {name: " + lifeSecret + ", region: 编的省, country: 编的国, latitude: 31.26, longitude: 121.48}\n")
			a := e.open()
			t.Cleanup(func() { a.Close() })
			if out := <-a.hub.Respond(sc.input(a)); out.Err != nil {
				t.Fatal(out.Err)
			}
			last := e.bodiesCopy()[1]
			if sc.level == lifetools.PlaceFull {
				if !strings.Contains(last, lifeSecret) {
					t.Fatal("主人本人照常用")
				}
				return
			}
			if !strings.Contains(last, lifetools.UnknownPlace) || strings.Contains(last, "编的省") || len(fake.forecasts()) != 0 {
				t.Fatalf("只有省的时候群里当不知道：%v", fake.forecasts())
			}
		})
	}
}

// 每人每天、每群每小时的次数（规格-免费接口 第六节）：主人本人不限，别人按人按群限；各场合各测一遍。
// 设成每人每天 1 次、每群每小时 1 次，一轮里连查三个不同的地点：主人的场合三次都联网，别人的场合只有第一次联网，后两次回明确文字。
func TestLifeQuotaByScene(t *testing.T) {
	t.Parallel()
	limited := "freeApi:\n  limits:\n    weather:\n      perActorDaily: 1\n      perConversationHourly: 1\n"
	owners := map[string]bool{"QQ 主人私聊": true, "本地群聊（主人在，PrivateOK 为真）": true, "QQ 群（主人发言）": true, "QQ 群（伪造 PrivateOK）": true}
	for _, sc := range lifeScenes() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			fake := newLifeFake(t)
			script := lifeScript(llmtest.Call("w1", "weather", `{"location":"地点一"}`), llmtest.Call("w2", "weather", `{"location":"地点二"}`), llmtest.Call("w3", "weather", `{"location":"地点三"}`), llmtest.Text("好"))
			srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
			t.Cleanup(srv.Close)
			e.llm = srv
			fake.attach(e, limited)
			a := e.open()
			t.Cleanup(func() { a.Close() })
			if out := <-a.hub.Respond(sc.input(a)); out.Err != nil {
				t.Fatal(out.Err)
			}
			asked := len(fake.askedNames())
			last := e.bodiesCopy()[len(e.bodiesCopy())-1]
			if owners[sc.name] {
				if asked != 3 || strings.Contains(last, "查得太多") {
					t.Fatalf("主人的场合不限次数：联网 %d 次", asked)
				}
				return
			}
			if asked != 1 || !strings.Contains(last, "查得太多") {
				t.Fatalf("别人的场合第二次起该被拒、不联网：联网 %d 次", asked)
			}
		})
	}
}

// 谁算主人（次数不限）的形状：和 lifeOwnerShape 的区别是群里主人说话也算，所以这里单独列一遍
func TestLifeQuotaOwnerShapeBoundary(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	a.cfg.QQ.Owner = "502"
	in, aud := qqClientIdentity(a)
	// 没有宿主发的运行：核对不过，谁都不是主人
	if a.lifeQuotaOwner(context.Background(), in, aud) {
		t.Fatal("宿主没发过的调用不能当主人")
	}
	// 形状层面的各种错：不看宿主核对，所以直接测形状分支会被核对挡掉；这里用 lifeThreadShape 之外的部分验证身份字段
	for name, change := range map[string]func(*kernel.Invocation){
		"身份缺":    func(i *kernel.Invocation) { i.Scope.ActorID = "" },
		"类型缺":    func(i *kernel.Invocation) { i.Scope.ActorKind = "" },
		"类型是别的":  func(i *kernel.Invocation) { i.Scope.ActorKind = "human" },
		"system": func(i *kernel.Invocation) { i.Scope.ActorKind, i.Scope.ActorID = "system", "owner" },
		"子代理":    func(i *kernel.Invocation) { i.Kind = kernel.KindChild },
		"有父运行":   func(i *kernel.Invocation) { i.ParentRunID = "r1" },
	} {
		x := in
		change(&x)
		if a.lifeQuotaOwner(context.Background(), x, aud) {
			t.Errorf("%s：不该当主人", name)
		}
	}
}
