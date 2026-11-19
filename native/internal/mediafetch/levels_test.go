package mediafetch

import (
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 日志分级（主人 11-13“记得改”）：下载失败算 WARN，太大、过期这类正常结局留在 INFO
func TestFinishLogLevel(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		want   string
		warn   bool
	}{
		{"别的 4xx 失败", 401, store.MediaFailed, true},
		{"404 过期", 404, store.MediaExpired, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, func(o *envOpt) { o.noRefresh = true })
			e.serve("/p", c.status, []byte("x"))
			segs := []types.Seg{imgSeg("/p")}
			seq := e.addEvent(segs, "1")
			e.start()
			e.f.Offer(seq, segs)
			e.idle()
			if r := e.row(seq, 0); r.State != c.want {
				t.Fatalf("状态 %+v，要 %s", r, c.want)
			}
			var line string
			for _, l := range strings.Split(e.allLogs(), "\n") {
				if strings.Contains(l, "没有下载") {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("没记终局：\n%s", e.allLogs())
			}
			if got := strings.HasPrefix(line, "WARN "); got != c.warn {
				t.Fatalf("级别不对（要 WARN=%v）：%s", c.warn, line)
			}
		})
	}
}

func TestOrLogfFallsBack(t *testing.T) {
	var got []string
	base := func(f string, _ ...any) { got = append(got, "base:"+f) }
	orLogf(nil, base)("a")
	orLogf(func(f string, _ ...any) { got = append(got, "warn:"+f) }, base)("b")
	if strings.Join(got, ",") != "base:a,warn:b" {
		t.Fatalf("兜底不对：%v", got)
	}
}
