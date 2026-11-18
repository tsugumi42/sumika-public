package lifetools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 手写的 2027 年数据：春节 2027-02-06（周六）；01-31 周日调休上班，02-13 周六放假，02-14 周日调休上班
const fixture2027 = `{"year":2027,"papers":["编的通知"],"days":[
 {"name":"春节","date":"2027-01-31","isOffDay":false},
 {"name":"春节","date":"2027-02-05","isOffDay":true},
 {"name":"春节","date":"2027-02-13","isOffDay":true},
 {"name":"春节","date":"2027-02-14","isOffDay":false}]}`

// 2028 年的通知里带着元旦调休的补班日 2027-12-25（周六，补班）
const fixture2028 = `{"year":2028,"papers":["编的通知"],"days":[
 {"name":"元旦","date":"2027-12-25","isOffDay":false},
 {"name":"元旦","date":"2028-01-01","isOffDay":true}]}`

type holidayRig struct {
	srv   *httptest.Server
	hits  atomic.Int64
	files map[string]func(w http.ResponseWriter, r *http.Request)
	h     *Holidays
	dir   string
	now   time.Time
}

func newHolidayRig(t *testing.T, files map[string]string) *holidayRig {
	t.Helper()
	r := &holidayRig{dir: t.TempDir(), now: time.Date(2026, 11, 9, 12, 0, 0, 0, time.Local), files: map[string]func(http.ResponseWriter, *http.Request){}}
	for name, body := range files {
		r.files[name] = func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }
	}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		if f, ok := r.files[strings.TrimPrefix(req.URL.Path, "/")]; ok {
			f(w, req)
			return
		}
		http.NotFound(w, req)
	}))
	t.Cleanup(r.srv.Close)
	r.h = NewHolidays(HolidayOptions{Dir: r.dir, URL: func() string { return r.srv.URL + "/{year}.json" },
		Clients: newClients(r.srv.Client()), Now: func() time.Time { return r.now }, SyncTimeout: 2 * time.Second})
	t.Cleanup(r.h.Close)
	return r
}

func lookup(h *Holidays, y int, m time.Month, d int) Status {
	return h.Lookup(context.Background(), y, m, d)
}

func TestHolidayAdjustedDays(t *testing.T) {
	r := newHolidayRig(t, map[string]string{"2027.json": fixture2027})
	for _, c := range []struct {
		m    time.Month
		d    int
		kind Kind
		name string
	}{
		{1, 31, KindMakeupWork, "春节"}, // 周日，调休上班
		{2, 13, KindOff, "春节"},        // 周六，放假
		{2, 14, KindMakeupWork, "春节"}, // 周日，调休上班
		{2, 5, KindOff, "春节"},         // 周五，放假
		{3, 6, KindWeekend, ""},       // 表里没有的周六：数据已发布，才能说普通周末
		{3, 8, KindWorkday, ""},       // 表里没有的周一
	} {
		s := lookup(r.h, 2027, c.m, c.d)
		if s.Kind != c.kind || s.Name != c.name {
			t.Errorf("2027-%02d-%02d：%+v，要 kind=%d name=%s", int(c.m), c.d, s, c.kind, c.name)
		}
	}
	if r.hits.Load() != 1 {
		t.Fatalf("同一年只该联网一次，实际 %d 次", r.hits.Load())
	}
}

// 没公布的年份（服务器上没有）：周末、工作日都不猜
func TestHolidayNotPublishedNeverGuesses(t *testing.T) {
	r := newHolidayRig(t, map[string]string{"2027.json": fixture2027})
	for _, d := range []int{1, 2, 3, 4} { // 2028-01-01 是周六，01-03 是周一
		if s := lookup(r.h, 2028, 1, d); s.Kind != KindNotPublished {
			t.Fatalf("2028-01-%02d：%+v", d, s)
		}
	}
	if r.hits.Load() != 1 {
		t.Fatalf("没公布的结论应记一阵子，不该每次都联网：%d 次", r.hits.Load())
	}
}

// 数据坏了的几种：不是 JSON、年份对不上、空表、缺 isOffDay、日期不合格、同一天互相矛盾；都不猜，也不落盘
func TestHolidayBrokenDataNeverGuesses(t *testing.T) {
	for name, body := range map[string]string{
		"网页":           "<html>502</html>",
		"年份对不上":        `{"year":2026,"days":[{"name":"春节","date":"2027-02-13","isOffDay":true}]}`,
		"空表":           `{"year":2027,"days":[]}`,
		"缺 isOffDay":   `{"year":2027,"days":[{"name":"春节","date":"2027-02-13"}]}`,
		"日期不合格":        `{"year":2027,"days":[{"name":"春节","date":"2027-2-13","isOffDay":true}]}`,
		"互相矛盾":         `{"year":2027,"days":[{"name":"a","date":"2027-02-13","isOffDay":true},{"name":"b","date":"2027-02-13","isOffDay":false}]}`,
		"空对象":          `{}`,
		"null":         `null`,
		"year 是文字":     `{"year":"2027","days":[{"name":"春节","date":"2027-02-13","isOffDay":true}]}`,
		"isOffDay 是文字": `{"year":2027,"days":[{"name":"春节","date":"2027-02-13","isOffDay":"true"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newHolidayRig(t, map[string]string{"2027.json": body})
			for _, d := range []int{13, 15} { // 周六、周一
				if s := lookup(r.h, 2027, 2, d); s.Kind != KindUnavailable {
					t.Fatalf("2027-02-%d：%+v", d, s)
				}
			}
			if _, err := os.Stat(filepath.Join(r.dir, "2027.json")); err == nil {
				t.Fatal("坏数据不该写进缓存")
			}
		})
	}
}

// 响应太大、被跳转、服务器出错：都是"取不到"，不猜
func TestHolidayFetchFailuresNeverGuess(t *testing.T) {
	r := newHolidayRig(t, nil)
	r.files["2027.json"] = func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(strings.Repeat("x", 2<<20))) }
	r.files["2028.json"] = func(w http.ResponseWriter, req *http.Request) { http.Redirect(w, req, "/2027.json", http.StatusFound) }
	r.files["2029.json"] = func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) }
	for _, y := range []int{2027, 2028, 2029} {
		if s := lookup(r.h, y, 3, 6); s.Kind != KindUnavailable {
			t.Fatalf("%d：%+v", y, s)
		}
	}
}

// 断网（连不上）且没有缓存：取不到；刚失败过的短时间内不再重试，查询不会被反复卡住
func TestHolidayOfflineNoCache(t *testing.T) {
	r := newHolidayRig(t, map[string]string{"2027.json": fixture2027})
	r.srv.Close() // 连接被拒绝
	start := time.Now()
	if s := lookup(r.h, 2027, 2, 13); s.Kind != KindUnavailable {
		t.Fatalf("%+v", s)
	}
	before := r.hits.Load()
	if s := lookup(r.h, 2027, 2, 13); s.Kind != KindUnavailable || r.hits.Load() != before {
		t.Fatalf("%+v", s)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("断网时查询卡太久")
	}
}

// 联网卡住：同步取有时限，不会一直等
func TestHolidaySyncFetchHasDeadline(t *testing.T) {
	block := make(chan struct{})
	r := newHolidayRig(t, nil)
	r.files["2027.json"] = func(http.ResponseWriter, *http.Request) { <-block }
	t.Cleanup(func() { close(block) })
	r.h.o.SyncTimeout = 100 * time.Millisecond
	start := time.Now()
	if s := lookup(r.h, 2027, 2, 13); s.Kind != KindUnavailable {
		t.Fatalf("%+v", s)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("卡了 %v", time.Since(start))
	}
}

// 缓存旧了：马上用旧的，后台更新；更新失败旧的照用
func TestHolidayStaleCacheServedThenRefreshed(t *testing.T) {
	r := newHolidayRig(t, map[string]string{"2027.json": fixture2027})
	lookup(r.h, 2027, 2, 13) // 取一次，写缓存
	old := `{"year":2027,"days":[{"name":"旧数据","date":"2027-02-13","isOffDay":false}]}`
	p := filepath.Join(r.dir, "2027.json")
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	aged := r.now.Add(-100 * time.Hour)
	if err := os.Chtimes(p, aged, aged); err != nil {
		t.Fatal(err)
	}
	if s := lookup(r.h, 2027, 2, 13); s.Kind != KindMakeupWork || s.Name != "旧数据" {
		t.Fatalf("先用旧缓存：%+v", s)
	}
	r.h.Wait()
	if s := lookup(r.h, 2027, 2, 13); s.Kind != KindOff || s.Name != "春节" {
		t.Fatalf("后台更新后：%+v", s)
	}
	// 再把它弄旧，这回服务器不通：旧缓存照用
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, aged, aged)
	r.srv.Close()
	r.now = r.now.Add(time.Hour) // 过了失败等待期
	for range 2 {
		if s := lookup(r.h, 2027, 2, 13); s.Name != "旧数据" {
			t.Fatalf("断网用旧缓存：%+v", s)
		}
		r.h.Wait()
	}
}

// 缓存文件本身坏了：当没有，重新取并覆盖
func TestHolidayCorruptCacheIsRefetched(t *testing.T) {
	r := newHolidayRig(t, map[string]string{"2027.json": fixture2027})
	os.WriteFile(filepath.Join(r.dir, "2027.json"), []byte("{坏了"), 0o644)
	if s := lookup(r.h, 2027, 2, 13); s.Kind != KindOff {
		t.Fatalf("%+v", s)
	}
	b, _ := os.ReadFile(filepath.Join(r.dir, "2027.json"))
	if !strings.Contains(string(b), `"春节"`) {
		t.Fatal("坏缓存没被覆盖")
	}
	// 缓存坏了又断网：不猜
	os.WriteFile(filepath.Join(r.dir, "2027.json"), []byte("{坏了"), 0o644)
	r.srv.Close()
	r.now = r.now.Add(time.Hour)
	if s := lookup(r.h, 2027, 3, 6); s.Kind != KindUnavailable {
		t.Fatalf("%+v", s)
	}
}

// 12 月下旬：补班日写在下一年的通知里，要一起看；下一年取不到、没公布都不下结论
func TestHolidayLateDecemberLooksAtNextYear(t *testing.T) {
	r := newHolidayRig(t, map[string]string{"2027.json": fixture2027, "2028.json": fixture2028})
	if s := lookup(r.h, 2027, 12, 25); s.Kind != KindMakeupWork || s.Name != "元旦" {
		t.Fatalf("周六补班：%+v", s)
	}
	if s := lookup(r.h, 2027, 12, 26); s.Kind != KindWeekend {
		t.Fatalf("周日：%+v", s)
	}
	r2 := newHolidayRig(t, map[string]string{"2027.json": fixture2027}) // 2028 还没公布
	// 周六、周日、周五：下一年的通知还没发，都不能按周末、工作日猜
	for _, d := range []int{25, 26, 31} {
		if s := lookup(r2.h, 2027, 12, d); s.Kind != KindNotPublished || s.Year != 2028 || !s.OwnYear {
			t.Fatalf("下一年没公布，2027-12-%d：%+v", d, s)
		}
	}
	if s := lookup(r2.h, 2027, 12, 11); s.Kind != KindWeekend { // 12 月上旬不用看下一年
		t.Fatalf("%+v", s)
	}
	r3 := newHolidayRig(t, map[string]string{"2027.json": fixture2027})
	r3.files["2028.json"] = func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) }
	if s := lookup(r3.h, 2027, 12, 25); s.Kind != KindUnavailable {
		t.Fatalf("下一年取不到：%+v", s)
	}
	if s := lookup(r3.h, 2027, 12, 11); s.Kind != KindWeekend { // 12 月上旬（周六）不用看下一年
		t.Fatalf("%+v", s)
	}
}

// 缓存文件按年存在数据目录里；URL 里的 {year} 被换掉
func TestHolidayURLTemplate(t *testing.T) {
	var path atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { path.Store(r.URL.Path); fmt.Fprint(w, fixture2027) }))
	defer srv.Close()
	h := NewHolidays(HolidayOptions{Dir: t.TempDir(), URL: func() string { return srv.URL + "/x/{year}/data.json" }, Clients: newClients(srv.Client())})
	defer h.Close()
	if s := h.Lookup(context.Background(), 2027, 2, 13); s.Kind != KindOff || path.Load() != "/x/2027/data.json" {
		t.Fatalf("%+v %v", s, path.Load())
	}
}
