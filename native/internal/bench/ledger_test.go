package bench

// 网关账本：按会话前缀挑、滤掉导入的和被拒的、同一 route_id 留结局、按真实时间排（时区混着）、少列报错

import (
	"encoding/csv"
	"strings"
	"testing"
)

const ledgerHead = "time,agent,requested_model,provider,host,model,served_model,swapped,effort,input_tokens,output_tokens,cache_write_tokens,cache_read_tokens,reasoning_tokens,cost_usd,duration_ms,ttft_ms,status,error,session,kind,provider_key_id,provider_key_name,provider_account,route_id,request_id,endpoint,error_message,error_type,source,rejected,session_provider,session_account,session_official_login,caller_key_id,caller_key_name,response_id,upstream,cache_write_5m_tokens,cache_write_1h_tokens,tries,handed_on,tier_asked,tier_sent,tier_served,upstream_stop,credits"

// row 按表头拼一行，没给的列留空
func row(kv map[string]string) string {
	cols := strings.Split(ledgerHead, ",")
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = kv[c]
	}
	var b strings.Builder // 照 CSV 的规矩加引号（字段里有逗号的）
	w := csv.NewWriter(&b)
	w.Write(out)
	w.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

func TestReadLedger(t *testing.T) {
	a, b := "t2-100-b1-go-1", "t2-100-b1-bun-1"
	rows := []string{ledgerHead,
		// 倒序给；09:00Z 比 16:30+08:00（= 08:30Z）晚，按字符串比会排反
		row(map[string]string{"time": "2026-10-27T09:00:00Z", "session": a, "route_id": "r3", "status": "200", "input_tokens": "30", "ttft_ms": "900", "tries": "2"}),
		row(map[string]string{"time": "2026-10-27T16:30:00+08:00", "session": a, "route_id": "r1", "status": "200", "input_tokens": "10", "served_model": "deepseek-flash", "provider_key_name": "某号"}),
		// 同一 route_id 先失败后成功：留晚的那行
		row(map[string]string{"time": "2026-10-27T08:40:05Z", "session": a, "route_id": "r2", "status": "200", "input_tokens": "20"}),
		row(map[string]string{"time": "2026-10-27T08:40:00Z", "session": a, "route_id": "r2", "status": "502", "error_message": "上游断了"}),
		// 中途取消、没到首字
		row(map[string]string{"time": "2026-10-27T08:50:00Z", "session": b, "route_id": "r4", "status": "499", "error_message": "客户端断开"}),
		// 同一 route_id 反过来给（失败的那行在前）：照样留晚的
		row(map[string]string{"time": "2026-10-27T08:41:00Z", "session": b, "route_id": "r8", "status": "502"}),
		row(map[string]string{"time": "2026-10-27T08:41:05Z", "session": b, "route_id": "r8", "status": "200"}),
		// 不算的：从日志导进来的、被拒的、别的会话
		row(map[string]string{"time": "2026-10-27T08:45:00Z", "session": a, "route_id": "r5", "status": "0", "source": "log"}),
		row(map[string]string{"time": "2026-10-27T08:46:00Z", "session": a, "route_id": "r6", "status": "429", "rejected": "true"}),
		row(map[string]string{"time": "2026-10-27T08:47:00Z", "session": "t2-99-b1-go-1", "route_id": "r7", "status": "200"}),
	}
	got, err := ReadLedger(strings.NewReader(strings.Join(rows, "\n")+"\n"), "t2-100-")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("只留这次的两个会话：%v", got)
	}
	ga := got[a]
	if len(ga) != 3 || ga[0].RouteID != "r1" || ga[1].RouteID != "r2" || ga[2].RouteID != "r3" {
		t.Fatalf("按真实时间排、同一 route_id 只留一行：%+v", ga)
	}
	if ga[0].Input != 10 || ga[0].Model != "deepseek-flash" || ga[0].Key != "某号" || ga[0].Tries != 1 {
		t.Fatalf("字段：%+v", ga[0])
	}
	if ga[1].Status != 200 || ga[1].Input != 20 || ga[1].Error != "" {
		t.Fatalf("留结局那行：%+v", ga[1])
	}
	if ga[2].TTFTMs != 900 || ga[2].Tries != 2 {
		t.Fatalf("首字、重试次数：%+v", ga[2])
	}
	if gb := got[b]; len(gb) != 2 || gb[0].Status != 200 || gb[1].Status != 499 || gb[1].TTFTMs != 0 || gb[1].Error != "客户端断开" {
		t.Fatalf("取消的照记：%+v", gb)
	}
}

func TestFilterLedger(t *testing.T) {
	raw := strings.Join([]string{ledgerHead,
		row(map[string]string{"time": "2026-10-27T09:00:00Z", "session": "t2-100-b1-go-1", "route_id": "r1", "error_message": "带,逗号"}),
		row(map[string]string{"time": "2026-10-27T09:00:01Z", "session": "别的程序的会话", "route_id": "r2"}),
		row(map[string]string{"time": "2026-10-27T09:00:02Z", "session": "t2-99-b1-go-1", "route_id": "r3"}),
	}, "\n") + "\n"
	got, err := FilterLedger([]byte(raw), "t2-100-")
	if err != nil {
		t.Fatal(err)
	}
	// 留下的照样读得回来；别的会话一行都不带
	calls, err := ReadLedger(strings.NewReader(string(got)), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls["t2-100-b1-go-1"]) != 1 || calls["t2-100-b1-go-1"][0].Error != "带,逗号" || strings.Contains(string(got), "别的程序") {
		t.Fatalf("只留这一次的：%v\n%s", calls, got)
	}
}

func TestReadLedgerMissingColumn(t *testing.T) {
	head := strings.Replace(ledgerHead, ",session,", ",sess,", 1)
	if _, err := ReadLedger(strings.NewReader(head+"\n"), "t2-"); err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("少列要报出是哪列：%v", err)
	}
}

// 网关账本带 upstream_stop 就记下每次请求的结束原因（编程额度实跑要区分 stop 和 length）；这一列缺了不算错
func TestReadLedgerStop(t *testing.T) {
	rows := []string{ledgerHead,
		row(map[string]string{"time": "2026-11-09T08:00:00Z", "session": "t2-1-a", "route_id": "r1", "status": "200", "upstream_stop": "length"}),
		row(map[string]string{"time": "2026-11-09T08:00:05Z", "session": "t2-1-a", "route_id": "r2", "status": "200", "upstream_stop": "stop"}),
	}
	got, err := ReadLedger(strings.NewReader(strings.Join(rows, "\n")+"\n"), "t2-1-")
	if err != nil {
		t.Fatal(err)
	}
	if g := got["t2-1-a"]; len(g) != 2 || g[0].Stop != "length" || g[1].Stop != "stop" {
		t.Fatalf("结束原因：%+v", g)
	}
	short := strings.Replace(ledgerHead, ",upstream_stop", "", 1)
	cols := strings.Split(short, ",")
	line := make([]string, len(cols))
	for i, c := range cols {
		switch c {
		case "time":
			line[i] = "2026-11-09T08:00:00Z"
		case "session":
			line[i] = "t2-1-a"
		case "route_id":
			line[i] = "r1"
		}
	}
	got, err = ReadLedger(strings.NewReader(short+"\n"+strings.Join(line, ",")+"\n"), "t2-1-")
	if err != nil || len(got["t2-1-a"]) != 1 || got["t2-1-a"][0].Stop != "" {
		t.Fatalf("没有 upstream_stop 这一列也要能读：%v %+v", err, got)
	}
}
