package modelmeta

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/llm"
)

// 取自 models.dev 真实形状的小样本（模型名是编的）：原厂、转售商、坏数据都有
const sample = `{
 "acme": {"id":"acme","models":{
   "alpha-1": {"id":"alpha-1","limit":{"context":1050000,"output":128000}},
   "beta-2":  {"id":"beta-2","limit":{"context":400000,"input":272000,"output":128000}},
   "emb-1":   {"id":"emb-1","type":"embedding","limit":{"context":8000,"output":0}},
   "pic-1":   {"id":"pic-1","type":"image","limit":{"context":0,"output":0}},
   "old-1":   {"id":"old-1","status":"deprecated","limit":{"context":64000,"output":8000}}
 }},
 "openai": {"id":"openai","models":{
   "gamma-3": {"id":"gamma-3","limit":{"context":1050000,"output":128000}}
 }},
 "resellerA": {"id":"resellerA","models":{
   "gamma-3": {"id":"gamma-3","limit":{"context":1050000,"output":50000}},
   "delta-4": {"id":"delta-4","limit":{"context":500000,"output":500000}},
   "eps-5":   {"id":"eps-5","limit":{"context":200000,"output":64000}},
   "bad-1":   {"id":"bad-1","limit":{"context":"many","output":1}},
   "ctl\u0007x": {"id":"ctl","limit":{"context":1,"output":1}}
 }},
 "resellerB": {"id":"resellerB","models":{
   "delta-4": {"id":"delta-4","limit":{"context":500000,"output":65536}},
   "eps-5":   {"id":"eps-5","limit":{"context":200000,"output":32000}},
   "zeta-6-1": {"id":"zeta-6-1","limit":{"context":128000,"output":16000}},
   "gamma-3-pro": {"id":"gamma-3-pro","limit":{"context":9,"output":1}}
 }}
}`

func catalogOf(t *testing.T, raw string) *Catalog {
	t.Helper()
	e, err := decodeCatalog([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return NewCatalog(e)
}

func TestCatalogFiltersAndDisambiguates(t *testing.T) {
	c := catalogOf(t, sample)
	check := func(id string, ctx, out int, mixed bool) {
		t.Helper()
		f, ok := c.Lookup(id)
		if !ok || f.Context != ctx || f.Output != out || f.Mixed != mixed {
			t.Errorf("%s：%+v ok=%v，想要 窗口 %d 输出 %d 不一致 %v", id, f, ok, ctx, out, mixed)
		}
	}
	check("alpha-1", 1050000, 128000, false)
	// 原厂（openai）有条目：只看原厂的，转售商说的 50000 不算
	check("gamma-3", 1050000, 128000, false)
	// 网关的写法 provider/model 去掉前缀再查；不按前缀模糊匹配（gamma-3 不能配到 gamma-3-pro）
	check("gw/gamma-3", 1050000, 128000, false)
	// 没有原厂条目、各家说法不一致：取保守的最小值并标出来
	check("eps-5", 200000, 32000, true)
	// 输出不小于窗口的（转售商把输出写成窗口）当输出未知；另一家有可信值就用它
	check("delta-4", 500000, 65536, false)
	// 小数点写成横线
	check("vendor/zeta-6.1", 128000, 16000, false)
	// 非聊天模型、窗口为 0 的、读不懂的、id 含控制字符的都不进目录
	for _, id := range []string{"emb-1", "pic-1", "bad-1", "ctl", "nope"} {
		if _, ok := c.Lookup(id); ok {
			t.Errorf("%s 不该查到", id)
		}
	}
	// deprecated 不隐藏（只是资料）
	check("old-1", 64000, 8000, false)
	if f, ok := c.Lookup("gamma-3-pro"); !ok || f.Context != 9 {
		t.Errorf("前缀相同的模型要单独查：%+v", f)
	}
	var nilCat *Catalog
	if _, ok := nilCat.Lookup("alpha-1"); ok {
		t.Error("空目录查不到")
	}
}

func TestDecodeCatalogRejectsNonJSONAndSkipsBadEntries(t *testing.T) {
	if _, err := decodeCatalog([]byte("<html>")); err == nil {
		t.Fatal("不是 JSON 要报错")
	}
	e, err := decodeCatalog([]byte(`{"p":{"models":{"a":{"id":"a","limit":{"context":-5,"output":1}},"b":{"id":"b","limit":{"context":10,"output":2}}}},"q":7,"r":{"models":"x"}}`))
	if err != nil || len(e) != 1 || e[0].ID != "b" {
		t.Fatalf("负数和类型错的只丢那一条：%+v %v", e, err)
	}
}

// 取值顺序逐字段：用户 > 服务商 > models.dev > 默认；服务商报了 27.2 万、资料库写 105 万时取 27.2 万
func TestResolveOrderPerField(t *testing.T) {
	c := catalogOf(t, sample)
	// 网关只报了窗口、没报输出：窗口取网关的 272000（不被资料库的 1050000 盖掉），输出用资料库补的 128000
	m := llm.Model{ID: "gw/gamma-3", ContextLength: 272000}
	r := Resolve(m, Override{}, c)
	if r.Window() != 272000 || r.OutputLimit() != 128000 || r.WindowSource() != llm.SourceProvider || r.OutputSource() != llm.SourceModelsDev {
		t.Fatalf("逐字段：%+v 窗口 %d/%s 输出 %d/%s", r.Limits, r.Window(), r.WindowSource(), r.OutputLimit(), r.OutputSource())
	}
	if r.Limits.ProviderWindow != 272000 || r.Limits.DevWindow != 1050000 || r.Limits.DevOutput != 128000 {
		t.Fatalf("两边的数都要记下：%+v", r.Limits)
	}
	// 用户覆盖盖过一切，只盖写了的那项
	r = Resolve(m, Override{MaxOutputTokens: 4000}, c)
	if r.Window() != 272000 || r.OutputLimit() != 4000 || r.OutputSource() != llm.SourceUser || r.WindowSource() != llm.SourceProvider {
		t.Fatalf("覆盖：%+v", r.Limits)
	}
	r = Resolve(m, Override{ContextWindow: 100000}, c)
	if r.Window() != 100000 || r.WindowSource() != llm.SourceUser {
		t.Fatalf("窗口覆盖：%+v", r.Limits)
	}
	// 服务商什么都没报：资料库补空
	r = Resolve(llm.Model{ID: "alpha-1"}, Override{}, c)
	if r.Window() != 1050000 || r.OutputLimit() != 128000 || r.WindowSource() != llm.SourceModelsDev || r.WindowUncertain() {
		t.Fatalf("资料库补空：%+v", r.Limits)
	}
	// 都没有：保守默认并标未知，窗口不可靠（压缩线更保守）
	r = Resolve(llm.Model{ID: "devin/unknown"}, Override{}, c)
	if r.Window() != 128000 || r.OutputLimit() != llm.FallbackMaxOutput || r.WindowSource() != llm.SourceDefault || r.OutputSource() != llm.SourceDefault || !r.WindowUncertain() {
		t.Fatalf("默认：%+v", r.Limits)
	}
	// 资料库各家说法不一致的窗口：不可靠
	if r := Resolve(llm.Model{ID: "eps-5"}, Override{}, c); !r.WindowUncertain() || r.Window() != 200000 {
		t.Fatalf("不一致：%+v", r.Limits)
	}
	// 没有资料库（离线又没缓存）：服务商报的照用，没报的走默认
	r = Resolve(llm.Model{ID: "x", ContextWindow: 64000, MaxOutputTokens: 8000}, Override{}, nil)
	if r.Window() != 64000 || r.OutputLimit() != 8000 || r.WindowSource() != llm.SourceProvider {
		t.Fatalf("无资料库：%+v", r.Limits)
	}
}

// 重复 Resolve（改了覆盖、缓存刷新后重算）结果和一次算出来的一样：服务商的原值不能被上一次补的值冒充
func TestResolveIsIdempotentAndRevisable(t *testing.T) {
	c := catalogOf(t, sample)
	once := Resolve(llm.Model{ID: "alpha-1"}, Override{}, c)
	twice := Resolve(once, Override{}, c)
	if once.Limits != twice.Limits || once.Window() != twice.Window() || once.OutputLimit() != twice.OutputLimit() {
		t.Fatalf("重复调用不一样：%+v / %+v", once.Limits, twice.Limits)
	}
	// 先补了资料库的值，再来一次没有资料库：回到默认，而不是把上次补的当服务商报的
	back := Resolve(once, Override{}, nil)
	if back.WindowSource() != llm.SourceDefault || back.Limits.ProviderWindow != 0 {
		t.Fatalf("上次补的值被当成了服务商的：%+v", back.Limits)
	}
	// 去掉覆盖回到原来的来源
	over := Resolve(once, Override{ContextWindow: 5000}, c)
	if again := Resolve(over, Override{}, c); again.Window() != 1050000 || again.WindowSource() != llm.SourceModelsDev {
		t.Fatalf("去掉覆盖应该恢复：%+v", again.Limits)
	}
}

func newTestStore(t *testing.T, handler http.HandlerFunc) (*Store, *httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	dir := filepath.Join(t.TempDir(), "cache")
	s := &Store{Path: filepath.Join(dir, "models-dev.json"), URL: srv.URL, Enabled: true, HTTP: srv.Client(), Now: time.Now}
	return s, srv, s.Path
}

func TestStoreRefreshCachesAndRevalidates(t *testing.T) {
	var gets, conditional atomic.Int32
	s, _, path := newTestStore(t, func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(sample))
	})
	if s.Catalog() != nil || !s.Due() {
		t.Fatal("没缓存时该刷新")
	}
	var changes atomic.Int32
	s.OnChange = func() { changes.Add(1) }
	if err := s.Refresh(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if f, ok := s.Catalog().Lookup("alpha-1"); !ok || f.Context != 1050000 {
		t.Fatalf("刷新后查不到：%+v", f)
	}
	if st := s.State(); st.FromCache || st.Entries == 0 || st.FetchedAt.IsZero() || st.Error != "" {
		t.Fatalf("状态：%+v", st)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("缓存没写出来：%v", err)
	}
	// 没过期不联网
	if s.Due() {
		t.Fatal("刚刷新过不该再到期")
	}
	if err := s.Refresh(context.Background(), false); err != nil || gets.Load() != 1 {
		t.Fatalf("没到期不该联网：%d 次 %v", gets.Load(), err)
	}
	// 手动刷新：条件请求，304 只更新时间，目录不通知变化
	if err := s.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if conditional.Load() != 1 || changes.Load() != 1 {
		t.Fatalf("条件请求 %d 次，变化通知 %d 次（第一次取到算一次，304 不算）", conditional.Load(), changes.Load())
	}
	if f, ok := s.Catalog().Lookup("alpha-1"); !ok || f.Context != 1050000 {
		t.Fatal("304 之后目录要还在")
	}
	// 重启：从缓存读，任意年龄都用
	s2 := &Store{Path: path, Enabled: true, Now: func() time.Time { return time.Now().Add(1000 * time.Hour) }}
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	if f, ok := s2.Catalog().Lookup("gamma-3"); !ok || f.Output != 128000 || !s2.State().FromCache || !s2.Due() {
		t.Fatalf("缓存重启后要能用且到期该刷新：%+v %+v", f, s2.State())
	}
}

// 联网失败：保留旧缓存、记错误、退避；离线启动照样能用缓存；手动刷新不受退避限制
func TestStoreFailureKeepsCacheAndBacksOff(t *testing.T) {
	var fail atomic.Bool
	var gets atomic.Int32
	s, _, path := newTestStore(t, func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(sample))
	})
	now := time.Now()
	s.Now = func() time.Time { return now }
	if err := s.Refresh(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	now = now.Add(5 * time.Hour)
	if err := s.Refresh(context.Background(), false); err == nil {
		t.Fatal("服务出错该返回错误")
	}
	if _, ok := s.Catalog().Lookup("alpha-1"); !ok || s.State().Error == "" {
		t.Fatalf("旧缓存要保留并记错误：%+v", s.State())
	}
	before := gets.Load()
	now = now.Add(time.Minute)
	if s.Due() {
		t.Fatal("失败后 5 分钟内不再试")
	}
	_ = s.Refresh(context.Background(), false)
	if gets.Load() != before {
		t.Fatal("退避期间不该联网")
	}
	now = now.Add(10 * time.Minute)
	if !s.Due() {
		t.Fatal("过了退避该再试")
	}
	fail.Store(false)
	if err := s.Refresh(context.Background(), true); err != nil || s.State().Error != "" {
		t.Fatalf("手动刷新成功后错误要清掉：%v %+v", err, s.State())
	}
	// 服务根本连不上（关掉）：启动读缓存，Refresh 报错但不影响用
	s3 := &Store{Path: path, URL: "http://127.0.0.1:1", Enabled: true, Now: time.Now}
	if err := s3.Load(); err != nil {
		t.Fatal(err)
	}
	if err := s3.Refresh(context.Background(), true); err == nil {
		t.Fatal("连不上该报错")
	}
	if _, ok := s3.Catalog().Lookup("alpha-1"); !ok {
		t.Fatal("连不上时缓存照用")
	}
}

func TestStoreRejectsBadBodiesAndKeepsOld(t *testing.T) {
	var mode atomic.Int32
	huge := strings.Repeat(" ", maxBody+10)
	s, _, _ := newTestStore(t, func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 0:
			w.Write([]byte(sample))
		case 1:
			w.Write([]byte("<html>maintenance</html>"))
		case 2:
			w.Write([]byte("{}"))
		case 3:
			w.Write([]byte(huge))
		case 4:
			w.Write([]byte(`{"p":{"models":{"e":{"id":"e","type":"embedding","limit":{"context":5}}}}}`))
		}
	})
	if err := s.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	for m := int32(1); m <= 4; m++ {
		mode.Store(m)
		if err := s.Refresh(context.Background(), true); err == nil {
			t.Fatalf("模式 %d：坏回复该报错", m)
		}
		if _, ok := s.Catalog().Lookup("alpha-1"); !ok {
			t.Fatalf("模式 %d：旧缓存被冲掉了", m)
		}
	}
}

func TestStoreCorruptCacheIsQuarantinedNotUsedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models-dev.json")
	if err := os.WriteFile(path, []byte(`{"v":1,"entries":[`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: path, Enabled: true, Now: time.Now}
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if s.Catalog() != nil {
		t.Fatal("坏缓存不能当空表用")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("坏缓存要挪开，不能留在原处被下次再读")
	}
	matches, _ := filepath.Glob(path + ".bad-*")
	if len(matches) != 1 {
		t.Fatalf("坏文件改名隔离留着查原因：%v", matches)
	}
	// 没有文件不算错
	if err := (&Store{Path: filepath.Join(dir, "none.json")}).Load(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreDisabledNeverFetches(t *testing.T) {
	var gets atomic.Int32
	s, _, _ := newTestStore(t, func(w http.ResponseWriter, r *http.Request) { gets.Add(1) })
	s.Enabled = false
	if s.Due() || s.Refresh(context.Background(), true) == nil || gets.Load() != 0 {
		t.Fatal("关掉就一次都不联网，手动刷新也报错")
	}
}

func TestStoreBadProxyIsAnErrorNotACrash(t *testing.T) {
	s := &Store{Enabled: true, URL: "http://127.0.0.1:1", Proxy: "::not a url", Now: time.Now}
	if err := s.Refresh(context.Background(), true); err == nil || !strings.Contains(err.Error(), "代理") {
		t.Fatalf("坏代理地址：%v", err)
	}
}
