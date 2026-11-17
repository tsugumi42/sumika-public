package lifetools

// 生活工具的插件日志（规格-插件两层 第二点五节第 5 条）：每次接口尝试一条。地名、接口都是编的，接口是本机假服务器

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/freeapi"
	"sumika-native/internal/plugintrace"
)

type lifeSink struct {
	mu   sync.Mutex
	rows []plugintrace.Event
}

func (s *lifeSink) add(e plugintrace.Event) { s.mu.Lock(); s.rows = append(s.rows, e); s.mu.Unlock() }
func (s *lifeSink) bySite() map[string]plugintrace.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]plugintrace.Event{}
	for _, e := range s.rows {
		out[e.Site] = e
	}
	return out
}

func TestTraceEveryProviderAttempt(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoCN), "tencent-weather": status(503), "open-meteo": body(fcOK)})
	sink := &lifeSink{}
	r.p.setTrace(sink.add)
	weatherText(t, r, "编的市")
	got := sink.bySite()
	geo, tq, om := got["open-meteo-geo"], got["tencent-weather"], got["open-meteo"]
	if geo.Result != plugintrace.OK || geo.Route != "geocode" || !geo.Timed || !strings.HasPrefix(geo.URL, r.srv.URL+"/open-meteo-geo") {
		t.Fatalf("编码那次：%+v", geo)
	}
	if tq.Result != plugintrace.Fail || tq.Kind != "5xx" || !strings.Contains(tq.Error, "503") || tq.Route != "weather" {
		t.Fatalf("腾讯挂了那次：%+v", tq)
	}
	if om.Result != plugintrace.OK || om.Route != "weather" {
		t.Fatalf("回退成功那次：%+v", om)
	}
	if len(sink.rows) != 3 {
		t.Fatalf("应当三次尝试三行：%+v", sink.rows)
	}
}

// 查无结果是提供方的正常回答：记 ok 加 note notfound
func TestTraceNotFoundIsOK(t *testing.T) {
	r := newAPIRig(t, map[string]http.HandlerFunc{"open-meteo-geo": body(geoCN), "tencent-weather": body(tqEmpty), "open-meteo": body(fcOK)})
	sink := &lifeSink{}
	r.p.setTrace(sink.add)
	weatherText(t, r, "编的市")
	if tq := sink.bySite()["tencent-weather"]; tq.Result != plugintrace.OK || tq.Note != "notfound" {
		t.Fatalf("查无此地记 ok + notfound：%+v", tq)
	}
}

// 错误文字里的网址只留协议、主机、路径（查询串里可能有坐标）；取消记 skip
func TestAttemptEventScrubsURLsAndCanceled(t *testing.T) {
	e := attemptEvent("open-meteo", "weather", "https://api.example.test/v1?lat=1&lon=2", time.Second, freeapi.KNet,
		errors.New(`Get "https://api.example.test/v1?lat=31.2&lon=121.5": dial tcp: refused`))
	if strings.Contains(e.Error, "lat=") || !strings.Contains(e.Error, "https://api.example.test/v1") || e.Kind != "net" {
		t.Fatalf("错误里的网址要清洗：%+v", e)
	}
	if c := attemptEvent("x", "fx", "", 0, freeapi.KCanceled, nil); c.Result != plugintrace.Skip {
		t.Fatalf("取消记 skip：%+v", c)
	}
}
