package main

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
)

func TestParseBackupArgs(t *testing.T) {
	o, err := parseArgs([]string{"backup", "now"})
	if err != nil || o.req.Cmd != ctlproto.CmdBackup || len(o.req.Args) != 1 || o.req.Args[0] != "now" || o.req.Backup != nil {
		t.Fatalf("%v %+v", err, o.req)
	}
	if o.wait != ctlproto.BackupDefaultWait || o.req.Wait != "1m0s" {
		t.Fatalf("backup now 默认等 60 秒：%v %q", o.wait, o.req.Wait)
	}
	o, err = parseArgs([]string{"backup", "now", "--media", "all", "--wait=5s"})
	if err != nil || o.req.Backup == nil || o.req.Backup.Media != "all" || o.wait != 5*time.Second {
		t.Fatalf("%v %+v", err, o.req)
	}
	o, err = parseArgs([]string{"backup", "now", "--media=none"})
	if err != nil || o.req.Backup.Media != "none" {
		t.Fatalf("%v %+v", err, o.req)
	}
	o, err = parseArgs([]string{"backup", "list"})
	if err != nil || o.wait != 0 || o.req.Wait != "" {
		t.Fatalf("backup list 不等：%v %+v", err, o.req)
	}
	if _, err := parseArgs([]string{"backup", "now", "--media"}); err == nil {
		t.Error("--media 后面没有内容应当报错")
	}
	o, err = parseArgs([]string{"backup", "now", "--media="})
	if err != nil || strings.TrimSpace(o.req.Backup.Media) != "" || o.req.Backup.Media == "" {
		t.Fatalf("空值要变成不合格的值交给服务端拒绝，不能当成没写：%v %+v", err, o.req.Backup)
	}
}

func TestRenderBackupList(t *testing.T) {
	var out, errOut strings.Builder
	r := ctlproto.Reply{OK: true, Data: []byte(`{"overview":{"dir":"D:\\备份\u001b[31m","total":3,"daily":2,"manual":1,"preChange":0,"bad":1,"partial":0,"foreign":2,"usedBytes":3221225472,"usedAt":1763000000000,"freeBytes":107374182400,"freeKnown":true,"alertGB":2,"dailyOn":true,"media":"stickers","rules":"每天备份：最近 7 天……"},
"rows":[{"name":"每天-20261114-0530","at":1763000000000,"reason":"daily","status":"ok","bytes":1048576,"linked":2,"classes":["self","material","media"],"media":"stickers","dbs":4},
{"name":"手动-20261113-2200","at":1762900000000,"reason":"manual","status":"bad","bytes":10,"linked":0,"classes":["self"],"media":"none","dbs":1,"problems":["库 go/chat/qq.db 的副本没通过\nintegrity_check"]}]}`)}
	renderBackup(&out, &errOut, ctlproto.Request{Cmd: "backup", Args: []string{"list"}}, r)
	got := out.String()
	for _, want := range []string{"共 3 份：每天 2、手动 1、改动前 0（其中没通过校验 1、没做完 0）", "实际占用 3.00 GB（硬链接只算一次；", "那次备份结束时算的", "提醒线 backup.alertGB = 2 GB（已超过）",
		"所在盘剩余 100.00 GB", "另有 2 份是别的数据目录写进来的", "每天自动备份：开｜媒体档：表情包和头像", "轮换规则：每天备份", "每天  1.0 MB", "她本人、素材、媒体", "校验通过", "没通过校验（不算数）", "问题：库 go/chat/qq.db"} {
		if !strings.Contains(got, want) {
			t.Errorf("少了 %q：\n%s", want, got)
		}
	}
	if strings.ContainsAny(got, "\x1b") || strings.Contains(got, "通过\nintegrity") {
		t.Fatalf("控制字符和换行没清：%q", got)
	}
	if errOut.Len() != 0 {
		t.Fatal(errOut.String())
	}
}

func TestRenderBackupListEmptyAndDirError(t *testing.T) {
	var out, errOut strings.Builder
	renderBackup(&out, &errOut, ctlproto.Request{Cmd: "backup", Args: []string{"list"}},
		ctlproto.Reply{OK: true, Data: []byte(`{"overview":{"dir":"E:\\x","freeKnown":false,"dailyOn":false,"media":"none","rules":"r"},"rows":[]}`)})
	if got := out.String(); !strings.Contains(got, "（还没有备份）") || !strings.Contains(got, "取不到") || !strings.Contains(got, "每天自动备份：关") {
		t.Fatalf("%q", got)
	}
	out.Reset()
	renderBackup(&out, &errOut, ctlproto.Request{Cmd: "backup", Args: []string{"list"}},
		ctlproto.Reply{OK: true, Data: []byte(`{"overview":{"dirError":"备份目录在数据目录里面"},"rows":[]}`)})
	if got := out.String(); !strings.Contains(got, "备份目录有问题：备份目录在数据目录里面") {
		t.Fatalf("%q", got)
	}
}

func TestRenderBackupNow(t *testing.T) {
	var out, errOut strings.Builder
	req := ctlproto.Request{Cmd: "backup", Args: []string{"now"}}
	renderBackup(&out, &errOut, req, ctlproto.Reply{OK: true, Data: []byte(`{"settled":false}`)})
	if !strings.Contains(out.String(), "还没做完") || !strings.Contains(out.String(), "backup list") {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	renderBackup(&out, &errOut, req, ctlproto.Reply{OK: true, Data: []byte(`{"settled":true,"name":"手动-20261114-0530","status":"ok","files":9,"linked":3,"bytes":2097152}`)})
	if got := out.String(); !strings.Contains(got, "手动-20261114-0530：校验通过｜9 个文件，其中 3 个和上一份共用｜2.0 MB") {
		t.Fatalf("%q", got)
	}
}

func TestParseBackupRestoreAndVerify(t *testing.T) {
	o, err := parseArgs([]string{"backup", "restore", "手动-20261115-0530", "--to", `D:\恢复出的目录`})
	if err != nil || o.req.Cmd != ctlproto.CmdBackup || len(o.req.Args) != 2 || o.req.Args[0] != "restore" || o.req.Args[1] != "手动-20261115-0530" || o.req.Backup == nil || o.req.Backup.To != `D:\恢复出的目录` {
		t.Fatalf("%v %+v", err, o.req)
	}
	if o.wait != ctlproto.BackupDefaultWait || o.req.Wait != "1m0s" {
		t.Fatalf("backup restore 默认等 60 秒：%v %q", o.wait, o.req.Wait)
	}
	o, err = parseArgs([]string{"backup", "restore", "x", "--to=D:\\y", "--wait=5s"})
	if err != nil || o.req.Backup.To != `D:\y` || o.wait != 5*time.Second || o.req.Backup.Media != "" {
		t.Fatalf("%v %+v", err, o.req.Backup)
	}
	o, err = parseArgs([]string{"backup", "verify"})
	if err != nil || len(o.req.Args) != 1 || o.req.Args[0] != "verify" || o.req.Backup != nil || o.wait != ctlproto.BackupDefaultWait {
		t.Fatalf("%v %+v", err, o.req)
	}
	o, err = parseArgs([]string{"backup", "verify", "每天-1", "--wait=9s"})
	if err != nil || len(o.req.Args) != 2 || o.req.Args[1] != "每天-1" || o.wait != 9*time.Second {
		t.Fatalf("%v %+v", err, o.req)
	}
	if _, err := parseArgs([]string{"backup", "restore", "x", "--to"}); err == nil {
		t.Error("--to 后面没有内容应当报错")
	}
	o, err = parseArgs([]string{"backup", "restore", "x", "--to="})
	if err != nil || strings.TrimSpace(o.req.Backup.To) != "" || o.req.Backup.To == "" {
		t.Fatalf("空值要变成不合格的值交给服务端拒绝，不能当成没写：%v %+v", err, o.req.Backup)
	}
	// --media 和 --to 能一起写（服务端按子命令拒绝不对的那个）
	o, err = parseArgs([]string{"backup", "restore", "x", "--media", "all", "--to", `D:\y`})
	if err != nil || o.req.Backup.Media != "all" || o.req.Backup.To != `D:\y` {
		t.Fatalf("%v %+v", err, o.req.Backup)
	}
}

func TestRenderBackupRestoreAndVerify(t *testing.T) {
	var out, errOut strings.Builder
	req := ctlproto.Request{Cmd: "backup", Args: []string{"restore", "手动-1"}}
	renderBackup(&out, &errOut, req, ctlproto.Reply{OK: true, Data: []byte(`{"settled":false,"target":"D:\\恢复\u001b[31m"}`)})
	if got := out.String(); !strings.Contains(got, "还没做完") || !strings.Contains(got, "report.json") || strings.Contains(got, "\x1b") {
		t.Fatalf("%q", got)
	}
	out.Reset()
	ok := `{"settled":true,"name":"手动-1","target":"D:\\恢复","ok":true,"files":9,"bytes":2097152,"durationMs":1500,"rowsRecorded":true,"dbs":[{"path":"go/chat/qq.db","integrity":"ok","rows":"match","tables":3},{"path":"go/trace.db","integrity":"ok","rows":"unrecorded","tables":1}],"notes":["备注"]}`
	renderBackup(&out, &errOut, req, ctlproto.Reply{OK: true, Data: []byte(ok)})
	got := out.String()
	for _, want := range []string{"核对通过：手动-1 → D:\\恢复", "9 个文件，2.0 MB，用时 1.5 秒", "库 go/chat/qq.db：完整性 ok｜行数和清单一致（3 张表）", "清单没记行数，只核对了完整性", "说明：备注", "没有改配置、没有切换、没有碰现在的数据目录", "把 --data（或 SUMIKA_DATA_DIR）指到 D:\\恢复", "密钥提醒"} {
		if !strings.Contains(got, want) {
			t.Errorf("少了 %q：\n%s", want, got)
		}
	}
	// 失败的回复：render 先写失败原因，再打印核对结果和“目录里留着未完成标记”
	out.Reset()
	errOut.Reset()
	bad := `{"settled":true,"name":"手动-1","target":"D:\\恢复","ok":false,"files":2,"dbs":[{"path":"go/chat/qq.db","integrity":"ok","rows":"mismatch","tables":3,"mismatch":["表 events：清单 3 行，恢复出的库里 5 行"]}],"problems":["恢复出的库 go/chat/qq.db 的行数和清单对不上"]}`
	render(&out, &errOut, req, ctlproto.Reply{Code: ctlproto.CodeFailed, Error: "恢复没有通过核对", Data: []byte(bad)})
	all := out.String() + errOut.String()
	for _, want := range []string{"失败（failed）：恢复没有通过核对", "核对没通过", "行数和清单对不上", "表 events：清单 3 行，恢复出的库里 5 行", "问题：恢复出的库 go/chat/qq.db", "未完成标记", "不会把它当数据目录打开"} {
		if !strings.Contains(all, want) {
			t.Errorf("少了 %q：\n%s", want, all)
		}
	}
	if strings.Contains(all, "没有改配置、没有切换") {
		t.Errorf("没通过时不该说“要用它”：\n%s", all)
	}
	// verify
	out.Reset()
	vreq := ctlproto.Request{Cmd: "backup", Args: []string{"verify"}}
	renderBackup(&out, &errOut, vreq, ctlproto.Reply{OK: true, Data: []byte(`{"settled":true,"name":"每天-1","kind":"manual","ok":true,"files":5,"bytes":1024,"durationMs":40,"dbs":[]}`)})
	if got := out.String(); !strings.Contains(got, "抽验通过：每天-1｜5 个文件，1 KB｜用时 40 毫秒") {
		t.Fatalf("%q", got)
	}
	out.Reset()
	renderBackup(&out, &errOut, vreq, ctlproto.Reply{OK: true, Data: []byte(`{"settled":false}`)})
	if got := out.String(); !strings.Contains(got, "还没做完") || !strings.Contains(got, "上次抽验") {
		t.Fatalf("%q", got)
	}
}

func TestDrillLine(t *testing.T) {
	at := time.Date(2026, 11, 15, 4, 10, 0, 0, time.Local).UnixMilli()
	for name, c := range map[string]struct {
		d    ctlproto.BackupDrill
		want []string
	}{
		"开着、没抽验过":   {ctlproto.BackupDrill{On: true}, []string{"每周自动抽验：开", "还没有计入的抽验"}},
		"关着、没抽验过":   {ctlproto.BackupDrill{}, []string{"每周自动抽验：关", "backup verify 可手动抽验", "还没有计入的抽验"}},
		"通过":        {ctlproto.BackupDrill{On: true, Have: true, At: at, Kind: "weekly", Backup: "每天-1", OK: true, DurationMs: 2500, HavePass: true, PassAt: at}, []string{"11-15 04:10（每周自动）每天-1｜通过｜用时 2.5 秒"}},
		"失败（带上次通过）": {ctlproto.BackupDrill{On: true, Have: true, At: at, Kind: "manual", Backup: "每天-1", Problems: []string{"哈希\n不对"}, DurationMs: 30, HavePass: true, PassAt: at - 86400000}, []string{"（手动）", "没通过：哈希", "上次通过 11-14 04:10"}},
		"失败（从没通过过）": {ctlproto.BackupDrill{On: true, Have: true, At: at, Backup: "x"}, []string{"没通过", "还没有通过过的抽验"}},
	} {
		got := drillLine(c.d)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s：少了 %q：%s", name, w, got)
			}
		}
		if strings.ContainsAny(got, "\n\x1b") {
			t.Errorf("%s：控制字符没清：%q", name, got)
		}
	}
}

func TestRenderBackupListWriteWait(t *testing.T) {
	var out, errOut strings.Builder
	r := ctlproto.Reply{OK: true, Data: []byte(`{"overview":{"dir":"D:\\备份","total":2,"daily":2,"dailyOn":true,"media":"stickers","rules":"r","drill":{"on":true}},
"rows":[{"name":"每天-2","at":1763000000000,"reason":"daily","status":"ok","bytes":1,"classes":["self"],"media":"stickers","dbs":2,"writeWaitSamples":40,"writeWaitMaxMs":3},
{"name":"每天-1","at":1762900000000,"reason":"daily","status":"ok","bytes":1,"classes":["self"],"media":"stickers","dbs":2,"writeWaitSamples":10,"writeWaitMaxMs":2500,"writeWaitFailed":2},
{"name":"每天-0","at":1762800000000,"reason":"daily","status":"ok","bytes":1,"classes":["self"],"media":"stickers","dbs":2}]}`)}
	renderBackup(&out, &errOut, ctlproto.Request{Cmd: "backup", Args: []string{"list"}}, r)
	got := out.String()
	for _, want := range []string{"备份期间入库的写锁等待：探了 40 次，最长等 3 毫秒", "探了 10 次，最长等 2500 毫秒，另有 2 次没探到（超过 1 秒，入库在备份期间被拖慢了）", "每周自动抽验：开｜还没有计入的抽验"} {
		if !strings.Contains(got, want) {
			t.Errorf("少了 %q：\n%s", want, got)
		}
	}
	if strings.Count(got, "备份期间入库的写锁等待") != 2 {
		t.Errorf("没有探针的那份不该有这一行：\n%s", got)
	}
}

func TestRenderDrillNotCounted(t *testing.T) {
	var out, errOut strings.Builder
	renderBackup(&out, &errOut, ctlproto.Request{Cmd: "backup", Args: []string{"verify", "旧的"}},
		ctlproto.Reply{OK: true, Data: []byte(`{"settled":true,"name":"每天-0","kind":"manual","ok":true,"files":5,"bytes":1024,"durationMs":40,"counts":false,"notCountedWhy":"别的数据目录的备份"}`)})
	if got := out.String(); !strings.Contains(got, "抽验通过：每天-0") || !strings.Contains(got, "这次不计入“上次通过”（别的数据目录的备份）") {
		t.Fatalf("%q", got)
	}
	out.Reset()
	renderBackup(&out, &errOut, ctlproto.Request{Cmd: "backup", Args: []string{"verify"}},
		ctlproto.Reply{OK: true, Data: []byte(`{"settled":true,"name":"每天-1","ok":true,"counts":true}`)})
	if strings.Contains(out.String(), "不计入") {
		t.Fatalf("计入的不该有这句：%q", out.String())
	}
	at := time.Date(2026, 11, 15, 4, 10, 0, 0, time.Local).UnixMilli()
	got := drillLine(ctlproto.BackupDrill{On: true, Have: true, At: at, Kind: "weekly", Backup: "每天-2", OK: true, HavePass: true, PassAt: at,
		HaveOther: true, OtherAt: at, OtherBackup: "每天-0", OtherOK: false, OtherWhy: "不是最新一份好的每天备份", OtherProblem: "哈希\n不对"})
	for _, want := range []string{"每天-2｜通过", "另有一次手动抽验不计入“上次通过”（不是最新一份好的每天备份）", "每天-0｜没通过：哈希"} {
		if !strings.Contains(got, want) {
			t.Errorf("少了 %q：%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") || strings.Contains(got, "哈希\n") {
		t.Errorf("控制字符没清：%q", got)
	}
	// 恢复成功以后的提醒：副本不在审批门的保护范围里
	out.Reset()
	renderBackup(&out, &errOut, ctlproto.Request{Cmd: "backup", Args: []string{"restore", "x"}},
		ctlproto.Reply{OK: true, Data: []byte(`{"settled":true,"name":"x","target":"D:\\恢复","ok":true}`)})
	if !strings.Contains(out.String(), "不在审批门的保护范围里") || !strings.Contains(out.String(), "不用了就删掉") {
		t.Fatalf("恢复完成的说明里该提醒这份副本没有保护：%q", out.String())
	}
}
