// Package lifetools 是日历（calendar）和天气（weather）两个只读工具（规格-主动联系 第十九节）。
// 不是主动联系专用的：普通对话里也能调。
//
//   - calendar：查某天（默认今天，本机时区）的星期、农历、节气、传统节日（本地算，lunar-go）和法定假日、调休（联网取、缓存）；
//   - weather：先编码（Open-Meteo）再按地区选腾讯、Open-Meteo、MET Norway；地点由她传，没传时按场合用设置里的所在地（主人本人的场合完整，主人开放的群只到城市）；
//   - exchange_rate：两种货币之间的汇率（fawazahmed0、Frankfurter、ExchangeRate-API）。
//
// 天气、汇率走免费接口注册表（freeapi，规格-免费接口）：每类多个提供方，按健康度排序、挂了回退，次数按人和群在本地计数。
//
// 不传地点时能拿到所在地的哪一级由宿主给的 DefaultPlace 判（身份按 ActorKind、ActorID 认，不按名字）：
// 主人本人的场合用完整的（存的坐标，不再编码），主人开放的群只到城市（名字只有城市，坐标取整到 0.1 度），
// 其余场合回同一句 UnknownPlace，不透露所在地设没设。显式传的地点各场合都能查（地名是她自己说出来的，不是主人的私事）。
// 每次真的联网前由免费接口引擎按人、按群占一次次数（p.quota），主人本人不计。
package lifetools

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/freeapi"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/plugintrace"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// ID 是插件在内核里的名字
const ID = "life-tools"

// 工具名
const (
	ToolCalendar = "calendar"
	ToolWeather  = "weather"
)

// Settings 是每次调用时读的设置（所在地改了马上生效）
type Settings struct {
	Location   config.Location // 所在地；Set() 为假 = 没设
	HolidayURL string          // 假日数据地址，{year} 代表年份；空用默认
	Proxy      string          // 和 linkReader.proxy 同一个
	// FreeAPI 是免费接口的设置：探测、每个工具每人每群的次数、提供方上限（装配处从配置读）
	FreeAPI freeapi.Settings
	// Stale：这一份是启动时的旧值（重读配置失败）。只有主人本人的场合还能用，群里当不知道
	Stale bool
}

// PlaceGrant 是这一次调用能拿到所在地的哪一级
type PlaceGrant int

const (
	PlaceNone PlaceGrant = iota // 当不知道：别人的私聊、子代理、核不过的调用
	PlaceCity                   // 只到城市：主人开放的群（本地群聊、QQ 群）
	PlaceFull                   // 完整的：主人本人的场合（本地私聊、QQ 主人私聊、主动联系的线）
)

// Options 是装配处给的东西
type Options struct {
	CacheDir string // 假日数据缓存目录
	Settings func() Settings
	// DefaultPlace 判这一次调用能拿到所在地的哪一级。PlaceFull 只能给听众是主人的场合；nil 一律 PlaceNone
	DefaultPlace func(ctx context.Context, in kernel.Invocation, aud audience.Audience) PlaceGrant
	// QuotaOwner 判这一次调用是不是主人本人（次数不限）：宿主核对过的前台调用、没有父运行、身份是 owner/owner，或主动联系的线；nil 一律不是
	QuotaOwner func(ctx context.Context, in kernel.Invocation, aud audience.Audience) bool
	Engine     *freeapi.Engine // 免费接口引擎；nil 时自己造一个（测试）
	// FreeAPIStateFile 是引擎状态文件（统计、人工置顶和关闭、次数）；空 = 只放内存
	FreeAPIStateFile string
	Now              func() time.Time
	Logf             func(string, ...any) // 只写类别，不写所在地、网址
	// 下面几项只给测试：换掉联网用的客户端和接口地址
	Client                  *http.Client
	GeocodeURL, ForecastURL string
	Endpoints               map[string]string // 提供方 id → 换掉注册表里的地址
	WeatherTimeout          time.Duration     // 一次天气查询总共最多多久（8 秒）
	Holidays                HolidayOptions    // 只用其中的时限几项
}

// Plugin 实现 turn.Plugin
type Plugin struct {
	o   Options
	cl  *clients
	eng *freeapi.Engine
	// h 是假日数据；停掉时关掉（后台取消），再起时用 hopts 重建（mu 管 h 和 hClosed）
	mu      sync.Mutex
	h       *Holidays
	hopts   HolidayOptions
	hClosed bool
	// tracer 是宿主给的插件日志（Setup 时换上，停掉时清掉）；nil 不记
	tracer atomic.Pointer[func(plugintrace.Event)]
}

// New 造插件。CacheDir 为空报不出错但假日数据不落盘缓存会失败，所以装配处必须给
func New(o Options) *Plugin {
	if o.Settings == nil {
		o.Settings = func() Settings { return Settings{} }
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.WeatherTimeout <= 0 {
		o.WeatherTimeout = 8 * time.Second
	}
	p := &Plugin{o: o, cl: newClients(o.Client)}
	p.eng = o.Engine
	if p.eng == nil {
		p.eng = p.newEngine()
	}
	p.registerProbes()
	ho := o.Holidays
	ho.Dir, ho.Clients, ho.Now, ho.Logf = o.CacheDir, p.cl, o.Now, o.Logf
	ho.URL = func() string { return o.Settings().HolidayURL }
	ho.Proxy = func() string { return o.Settings().Proxy }
	ho.Engine, ho.Region = p.eng, p.networkRegion
	ho.Trace = p.traceHolidayURL
	p.hopts = ho
	p.h = NewHolidays(ho)
	return p
}

func (p *Plugin) ID() string { return ID }

// Holidays 给测试和装配取假日数据管理
func (p *Plugin) Holidays() *Holidays { return p.holidays() }

// holidays 是现在的假日数据（停过又起的会换一个新的）
func (p *Plugin) holidays() *Holidays {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h
}

// Decls 是三个工具的声明
var (
	CalendarDecl = tools.Declaration{
		Name:        ToolCalendar,
		Description: "查某一天的日历：星期、农历、节气、传统节日，以及法定假日和调休（放假还是调休上班）。默认今天（本机时区）。假日安排每年由国务院公布，还没公布的年份会如实告诉你，不要自己按周末猜。",
		Parameters:  `{"type":"object","additionalProperties":false,"properties":{"date":{"type":"string","description":"要查的日期，写成 2027-01-31；不写就是今天"}}}`,
	}
	WeatherDecl = tools.Declaration{
		Name:        ToolWeather,
		Description: "查某个地方现在的天气：温度、体感、天气现象，今明两天的最高最低温和降水概率。location 写地名（城市或区县）；不写时按场合用所有者的所在地（和所有者单独聊用完整的，所有者开放的群里只到城市），别的场合会告诉你不知道在哪，这时可以翻记忆或开口问。",
		Parameters:  `{"type":"object","additionalProperties":false,"properties":{"location":{"type":"string","description":"地名，比如城市名；可以聊到哪查哪"}}}`,
	}
)

// Manifest 是清单（规格-插件两层 第二节第 1 条）
func (p *Plugin) Manifest() turn.Manifest {
	return turn.Manifest{Name: "生活工具", Sub: "查日历、天气、汇率；所在地按场合给（主人本人完整，开放的群只到城市）"}
}

// Setup 实现 turn.Plugin：三个工具，所有场合都能看见（要不要给默认所在地在执行时判；每人每群的次数在执行时扣）。
// 开始后台探测免费接口；返回的函数停掉后台更新和探测
func (p *Plugin) Setup(r *turn.Registry) (func(), error) {
	all := func(audience.Audience) bool { return true }
	var undos []func()
	// 假日数据停过就关了（后台取消）；再起用同样的选项新建一个
	p.mu.Lock()
	if p.hClosed {
		p.h = NewHolidays(p.hopts)
		p.hClosed = false
	}
	p.mu.Unlock()
	p.setTrace(r.Trace)
	stop := func() {
		p.setTrace(nil)
		for i := len(undos) - 1; i >= 0; i-- {
			undos[i]()
		}
		p.eng.Close()
		p.mu.Lock()
		h := p.h
		p.hClosed = true
		p.mu.Unlock()
		h.Close()
	}
	for _, t := range []turn.Tool{
		{Decl: CalendarDecl, For: all, Exec: p.calendar},
		{Decl: WeatherDecl, For: all, Exec: p.weather},
		{Decl: ExchangeRateDecl, For: all, Exec: p.exchangeRate},
	} {
		undo, err := r.OnTool(t)
		if err != nil {
			stop()
			return nil, err
		}
		undos = append(undos, undo)
	}
	p.eng.StartProbing()
	return stop, nil
}

func fail(s string) (tools.Result, error) { return tools.Result{Text: s, IsError: true}, nil }
func ok(s string) (tools.Result, error)   { return tools.Result{Text: s}, nil }

// stringArg 取可选的文字参数：没有、null 当没传；不是文字返回 bad
func stringArg(args jsjson.Value, key string) (s string, present, bad bool) {
	if args.Kind != jsjson.Object {
		return "", false, args.Kind != jsjson.Null
	}
	v, found := args.Obj[key]
	if !found || v.Kind == jsjson.Null {
		return "", false, false
	}
	if v.Kind != jsjson.String {
		return "", false, true
	}
	return v.Str, true, false
}

// placeArg 整理地点参数：返回地点（空 = 没传）和给模型的错误文字（空 = 没问题）
func placeArg(args jsjson.Value) (place, errText string) {
	s, _, bad := stringArg(args, "location")
	if bad {
		return "", "location 要写成文字（地名），不是数字或别的类型。"
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if n := len([]rune(s)); n > config.LocationMaxRunes {
		return "", "location 太长了，写城市或区县的名字就行（最多 64 个字）。"
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", "location 里有不能用的字符，写城市或区县的名字就行。"
	}
	return s, ""
}

func (p *Plugin) weather(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	if c.Args.Kind != jsjson.Object && c.Args.Kind != jsjson.Null {
		return fail("参数要写成对象，比如 {\"location\":\"某城市\"}。")
	}
	place, bad := placeArg(c.Args)
	if bad != "" {
		return fail(bad)
	}
	pl := Place{Query: place}
	if place == "" {
		// 没传地点：场合不够、没设、只知道到省都是同一句话
		var known bool
		if pl, known = defaultPlace(p.o.Settings(), p.grant(ctx, c)); !known {
			return ok(UnknownPlace)
		}
	}
	if text, over := p.quota(ctx, ToolWeather, "天气", c); over {
		return ok(text)
	}
	text, err := p.queryWeather(ctx, pl)
	if err != nil {
		switch err.(type) {
		case notFoundError:
			return ok("没查到叫「" + cleanName(place, 30) + "」的地方，换个写法（城市名或区县名）再试，或者问一下具体是哪里。")
		case noProviderError:
			return fail(NoProvider)
		}
		p.o.Logf("天气：查询没成功：%s", errKind(err))
		return fail(QueryFailed)
	}
	return ok(text)
}

// grant 判这一次能拿到哪一级：工具收到的身份、会话要自洽，再问宿主
func (p *Plugin) grant(ctx context.Context, c turn.ToolCall) PlaceGrant {
	sc := c.Identity.Scope
	if p.o.DefaultPlace == nil || c.CharacterID != sc.CharacterID || c.Conversation != sc.Conversation || c.Audience.Conversation != c.Conversation {
		return PlaceNone
	}
	return p.o.DefaultPlace(ctx, c.Identity, c.Audience)
}

// defaultPlace 把所在地按这一级裁好。群场合只有城市的名字、省和取整的坐标，后面查天气、拼结果文字时拿不到更细的东西；
// 腾讯要的省、市从这里给（Admin1 是省，Admin2 是城市），区县不会出现在任何一家的请求里
func defaultPlace(s Settings, g PlaceGrant) (Place, bool) {
	switch g {
	case PlaceFull:
		l := s.Location
		if !l.Set() || l.Latitude == nil || l.Longitude == nil {
			return Place{}, false
		}
		return Place{Display: l.Name, Name: l.Name, Admin1: l.Region, Admin2: l.CityName, Country: l.Country, CountryCode: countryOf(l),
			Timezone: l.Timezone, Lat: *l.Latitude, Lon: *l.Longitude, HasCoord: true}, true
	case PlaceCity:
		if s.Stale {
			return Place{}, false
		}
		city, ok := s.Location.City()
		if !ok {
			return Place{}, false
		}
		return Place{Display: city.Display(), Name: city.Name, Admin1: city.Region, Admin2: city.Name, Country: city.Country, CountryCode: countryOf(s.Location),
			Timezone: s.Location.Timezone, Lat: city.Latitude, Lon: city.Longitude, HasCoord: true}, true
	}
	return Place{}, false
}

// countryOf 是所在地的国家代码：存了就用；手写时省略的，国家写的是中国或时区是 Asia/Shanghai 当 CN，别的当海外
func countryOf(l config.Location) string {
	if l.CountryCode != "" {
		return l.CountryCode
	}
	if l.Country == "中国" || l.Country == "China" || l.Timezone == "Asia/Shanghai" {
		return "CN"
	}
	return ""
}

func (p *Plugin) calendar(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	if c.Args.Kind != jsjson.Object && c.Args.Kind != jsjson.Null {
		return fail("参数要写成对象，比如 {\"date\":\"2027-01-31\"}，不写日期就是今天。")
	}
	s, present, bad := stringArg(c.Args, "date")
	if bad {
		return fail("date 要写成文字，比如 2027-01-31。")
	}
	var y, d int
	var m time.Month
	s = strings.TrimSpace(s)
	if !present || s == "" {
		y, m, d = p.o.Now().In(time.Local).Date()
	} else {
		t, err := parseDate(s)
		if err != nil {
			return fail("date 要写成 2027-01-31 这样（年-月-日，各位数写全）。")
		}
		y, m, d = t.Date()
	}
	if y < MinYear || y > MaxYear {
		return fail("只能查 1901 年到 2100 年之间的日期。")
	}
	if text, over := p.quota(ctx, ToolCalendar, "日历", c); over {
		return ok(text)
	}
	return ok(p.describe(ctx, y, m, d))
}

// parseDate 严格解析 YYYY-MM-DD
func parseDate(s string) (time.Time, error) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return time.Time{}, errBadDate
	}
	for i, r := range s {
		if i != 4 && i != 7 && (r < '0' || r > '9') {
			return time.Time{}, errBadDate
		}
	}
	return time.Parse("2006-01-02", s)
}

var errBadDate = &badDate{}

type badDate struct{}

func (*badDate) Error() string { return "日期格式不对" }

// describe 拼一天的说明：本地算的几项加放假安排
func (p *Plugin) describe(ctx context.Context, y int, m time.Month, d int) string {
	f := Facts(y, m, d)
	var b strings.Builder
	b.WriteString("日期：" + f.Date + " " + f.Weekday + "\n")
	b.WriteString("农历：" + f.LunarYear + f.LunarDay + "\n")
	if f.JieQi != "" {
		b.WriteString("节气：今天是" + f.JieQi + "\n")
	} else {
		b.WriteString("节气：今天不是节气")
		if f.PrevJieQi != "" {
			b.WriteString("；上一个 " + f.PrevJieQi)
		}
		if f.NextJieQi != "" {
			b.WriteString("，下一个 " + f.NextJieQi)
		}
		b.WriteString("\n")
	}
	if len(f.Festivals) > 0 {
		b.WriteString("节日：" + strings.Join(f.Festivals, "、") + "\n")
	}
	b.WriteString("放假安排：" + holidayText(p.holidays().Lookup(ctx, y, m, d), f, y) + "\n")
	return strings.TrimRight(b.String(), "\n")
}

func holidayText(s Status, f DayFacts, year int) string {
	switch s.Kind {
	case KindOff:
		return name(s.Name) + "，法定放假"
	case KindMakeupWork:
		return name(s.Name) + "调休，" + f.Weekday + "要上班"
	case KindWeekend:
		return "普通周末，休息"
	case KindWorkday:
		return "普通工作日，上班"
	case KindNotPublished:
		if s.OwnYear {
			return "按 " + itoa(year) + " 年的通知这天不是假日，但 " + itoa(s.Year) + " 年元旦的安排还没公布，可能调休，先不下结论"
		}
		return "这天放不放假还没公布（" + itoa(year) + " 年的假日安排国务院还没发，不能按周末规则猜）"
	}
	return "这次取不到假日数据（网络不通又没有缓存），不能按周末规则猜；过一会儿再查"
}

func name(n string) string {
	if n == "" {
		return "节假日"
	}
	return n
}

func itoa(n int) string { return strconv.Itoa(n) }
