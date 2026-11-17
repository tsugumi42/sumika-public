package taskrun

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"sumika-native/internal/alert"
)

// 样本都是编的：号码、地址、令牌不是真的；运行时拼出来，免得源码里躺着长得像真的字面量
var (
	fakeNum  = strings.Repeat("8", 11)
	fakeSK   = "sk-" + strings.Repeat("q", 20)
	fakeLong = strings.Repeat("Ab1", 10)
)

func TestDetailMasksSecrets(t *testing.T) {
	cases := []struct{ name, in, secret string }{
		{"QQ 号", "发给 " + fakeNum + " 失败", fakeNum},
		{"URL", "取 https://example.com/a/b?token=abcdef 失败", "example.com"},
		{"带用户名的 URL", "取 http://user:pw@host.example:8080/x 失败", "pw@host"},
		{"邮箱", "联系 someone@example.org", "someone@example.org"},
		{"会话名", "会话 qq:group:123456:abc 发送失败", "123456"},
		{"私聊会话名", "qq:private:90001:90002", "90001"},
		{"sk 令牌", "key " + fakeSK + " 无效", fakeSK},
		{"Bearer", "Authorization: Bearer abc.def-ghi", "abc.def-ghi"},
		{"长令牌", "value " + fakeLong + "==", fakeLong},
		{"IP", "连不上 192.168.1.20:3425", "192.168.1.20"},
		{"本机路径", `读 C:\Users\someone\secret\a.txt 失败`, "someone"},
		{"正斜杠路径", "读 D:/Data/聊天/x.db 失败", "聊天"},
	}
	for _, c := range cases {
		got := Detail("network", c.in)
		if strings.Contains(got, c.secret) {
			t.Errorf("%s：%q 里还有 %q", c.name, got, c.secret)
		}
		if got == "" {
			t.Errorf("%s：不该整句丢掉", c.name)
		}
	}
}

func TestDetailOmitsModelOutput(t *testing.T) {
	out := `输出不是合法 JSON：[{"id":"x","reason":"情境是想蹭别人的好处，语气口语"}]`
	got := Detail("parse_failed", out)
	if strings.Contains(got, "情境") || strings.Contains(got, "reason") || !strings.Contains(got, "输出不是合法 JSON") || !strings.Contains(got, "原文已略") {
		t.Errorf("模型输出没略掉：%q", got)
	}
	got = Detail("loop_detected", "思考打转：「别人的话」挨着出现 5 次")
	if strings.Contains(got, "别人的话") || !strings.Contains(got, "思考打转") {
		t.Errorf("打转的原文没略掉：%q", got)
	}
	// 别的类别不略（这是调用方自己的错误文字）
	if got := Detail("network", "连接被重置：对端关闭"); !strings.Contains(got, "对端关闭") {
		t.Errorf("别的类别不该略原文：%q", got)
	}
}

func TestDetailBoundsAndIdempotent(t *testing.T) {
	long := strings.Repeat("失败原因很长。", 400)
	got := Detail("network", long)
	if len(got) > MaxDetailBytes || !utf8.ValidString(got) {
		t.Errorf("超长：%d 字节，合法 UTF-8=%v", len(got), utf8.ValidString(got))
	}
	for _, in := range []string{long, "a\x00b\nc\rd\xff", "value " + fakeSK + " 于 https://x.example/1 12345678", `输出不是合法 JSON：[{...}]`} {
		for _, class := range []string{"network", "parse_failed"} {
			once := Detail(class, in)
			if twice := Detail(class, once); twice != once {
				t.Errorf("清两遍不等于清一遍：%q → %q → %q", in, once, twice)
			}
		}
	}
	if Detail("network", "a\x00b\nc") != "a b c" {
		t.Errorf("控制字符要并成空格：%q", Detail("network", "a\x00b\nc"))
	}
}

type fakeSink struct {
	mu   sync.Mutex
	rows []Row
	err  error
}

func (f *fakeSink) AddTaskRun(r Row) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, r)
	return f.err
}

func TestDegradeLogsRecordsAndAlerts(t *testing.T) {
	var logs []string
	sink := &fakeSink{}
	hub := alert.New()
	now := time.Unix(1000, 0)
	d := &Degrader{Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }, Alerts: hub, Sink: sink, Now: func() time.Time { return now }}
	err := errors.New("发给 " + fakeNum + " 失败 https://example.com/x")
	d.Degrade(ModuleSend, ClassUnsupported, err, Opts{Outcome: OutcomeSkipped, Alert: &alert.Alert{Key: "k", Title: "t", Body: "b"}})
	d.Degrade(ModuleSend, ClassUnsupported, err, Opts{Alert: &alert.Alert{Key: "k", Title: "t", Body: "b2"}})
	if len(logs) != 2 || !strings.Contains(logs[0], "send · unsupported") || strings.Contains(logs[0], fakeNum) || strings.Contains(logs[0], "example.com") {
		t.Fatalf("日志只写'模块 · 类别'，不带原文：%q", logs)
	}
	if open := hub.Open(); len(open) != 1 || open[0].Key != "k" {
		t.Fatalf("同一个键只开一条提醒：%+v", open)
	}
	if len(sink.rows) != 2 || sink.rows[0].Outcome != OutcomeSkipped || sink.rows[1].Outcome != OutcomeDegraded || sink.rows[0].Class != ClassUnsupported {
		t.Fatalf("记了两行，默认结局是 degraded：%+v", sink.rows)
	}
	for _, r := range sink.rows {
		if strings.Contains(r.Detail, fakeNum) || strings.Contains(r.Detail, "example.com") || r.Ms >= 0 {
			t.Errorf("详情要脱敏、没有耗时记 -1：%+v", r)
		}
	}
	// 没有提醒项就不 Raise
	hub2 := alert.New()
	d.Alerts = hub2
	d.Degrade(ModuleLink, ClassNetwork, nil, Opts{})
	if len(hub2.Open()) != 0 {
		t.Error("没给 Alert 不该提醒")
	}
}

func TestDegradeUsesInjectedRedactThenOwn(t *testing.T) {
	sink := &fakeSink{}
	d := &Degrader{Sink: sink, Redact: func(s string) string { return strings.ReplaceAll(s, "秘密词", "***") }}
	d.Degrade(ModuleLink, ClassNetwork, errors.New("含 秘密词 和 12345678"), Opts{})
	got := sink.rows[0].Detail
	if strings.Contains(got, "秘密词") || strings.Contains(got, "12345678") {
		t.Errorf("装配传入的脱敏和自己的脱敏都要过：%q", got)
	}
}

func TestNilDegraderAndSinkFailure(t *testing.T) {
	var d *Degrader
	d.Degrade(ModuleSend, ClassUnsupported, errors.New("x"), Opts{}) // 不能 panic
	d.Start(ModuleSend).End(OutcomeOK, "", nil)
	var logs []string
	sink := &fakeSink{err: errors.New("库坏了")}
	d2 := &Degrader{Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }, Sink: sink}
	d2.Degrade(ModuleSend, ClassUnsupported, nil, Opts{}) // 写库失败只记日志
	if len(logs) != 2 || !strings.Contains(logs[1], "记运行记录失败") {
		t.Errorf("写库失败要留一行日志：%q", logs)
	}
}

func TestSpanEndRecordsDurationAndDropsClassOnOK(t *testing.T) {
	sink := &fakeSink{}
	clock := time.Unix(2000, 0)
	d := &Degrader{Sink: sink, Now: func() time.Time { return clock }}
	sp := d.Start(ModuleLink)
	sp.Attempts, sp.RootID = 3, "root-1"
	clock = clock.Add(1500 * time.Millisecond)
	sp.End(OutcomeOK, "network", nil) // ok 带类别：类别必须被丢掉
	d.Start(ModuleLink).End(OutcomeTimeout, ClassTimeout, errors.New("超时"))
	if len(sink.rows) != 2 {
		t.Fatalf("%+v", sink.rows)
	}
	r := sink.rows[0]
	if r.Ms != 1500 || r.Class != "" || r.Attempts != 3 || r.RootID != "root-1" || !r.At.Equal(time.Unix(2000, 0)) {
		t.Errorf("成功行：%+v", r)
	}
	if sink.rows[1].Class != ClassTimeout || sink.rows[1].Outcome != OutcomeTimeout {
		t.Errorf("失败行：%+v", sink.rows[1])
	}
}
