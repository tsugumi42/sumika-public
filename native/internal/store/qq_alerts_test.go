package store

import (
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/paths"
)

// 写一行读回来：字段原样（有 Detail 的、没有的两种），按 at、id 升序，账号和 since 各自过滤
func TestQQAlerts(t *testing.T) {
	t.Parallel()
	s, _ := goSet(t)
	qq, _ := s.Platform("qq")
	detail := "编的细节"
	rows := []QQAlert{
		{At: 2000, Kind: "down", Account: "100", Text: "掉线"},
		{At: 1000, Kind: "up", Account: "100", Text: "恢复"},
		{At: 2000, Kind: "send-failed", Account: "100", Text: "没发出去", Detail: &detail},
		{At: 2000, Kind: "kicked", Account: "200", Text: "被踢"},
	}
	for i, a := range rows {
		id, err := qq.AddQQAlert(a)
		if err != nil {
			t.Fatalf("写第 %d 行：%v", i, err)
		}
		if id != int64(i+1) {
			t.Fatalf("第 %d 行返回的 id 是 %d，要 %d", i, id, i+1)
		}
	}
	// 100 号的三行按 at、id 排：先 1000 的，再两条 2000 的（写的先后）
	want := []QQAlert{
		{ID: 2, At: 1000, Kind: "up", Account: "100", Text: "恢复"},
		{ID: 1, At: 2000, Kind: "down", Account: "100", Text: "掉线"},
		{ID: 3, At: 2000, Kind: "send-failed", Account: "100", Text: "没发出去", Detail: &detail},
	}
	got, err := qq.QQAlerts("100", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("100 号的提醒不对：\n要 %+v\n有 %+v", want, got)
	}
	if got, err := qq.QQAlerts("100", 2000); err != nil || !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("since = 2000 只要 2000 及以后的：%+v %v", got, err)
	}
	if got, err := qq.QQAlerts("100", 2001); err != nil || len(got) != 0 {
		t.Fatalf("since = 2001 以后没有了，得空：%+v %v", got, err)
	}
	// 别的号只有自己那条
	if got, err := qq.QQAlerts("200", 0); err != nil || len(got) != 1 || got[0].Kind != "kicked" || got[0].Account != "200" {
		t.Fatalf("200 号：%+v %v", got, err)
	}
	if got, err := qq.QQAlerts("999", 0); err != nil || len(got) != 0 {
		t.Fatalf("没记过的号得空：%+v %v", got, err)
	}
}

// 本地库没有 qq_alerts：两个方法都报错说清，不 panic，不悄悄给空
func TestQQAlertsLocalStoreRefuses(t *testing.T) {
	t.Parallel()
	s, _ := goSet(t)
	local, _ := s.Platform("local")
	if _, err := local.AddQQAlert(QQAlert{At: 1, Kind: "down", Account: "100", Text: "掉线"}); err == nil || !strings.Contains(err.Error(), "QQ 平台的库") {
		t.Fatalf("本地库写提醒要报没这张表，得到 %v", err)
	}
	if _, err := local.QQAlerts("100", 0); err == nil || !strings.Contains(err.Error(), "QQ 平台的库") {
		t.Fatalf("本地库读提醒要报没这张表，得到 %v", err)
	}
}

// kind 只认 Bun 那五个；拼错的报错，而且什么都不写
func TestQQAlertKindChecked(t *testing.T) {
	t.Parallel()
	s, _ := goSet(t)
	qq, _ := s.Platform("qq")
	for _, kind := range []string{"", "DOWN", "send_failed", "重启"} {
		if _, err := qq.AddQQAlert(QQAlert{At: 1, Kind: kind, Account: "100", Text: "编的"}); err == nil {
			t.Fatalf("kind %q 不该写得进去", kind)
		}
	}
	if got, err := qq.QQAlerts("100", 0); err != nil || len(got) != 0 {
		t.Fatalf("报错的 kind 一行都不该写进去：%+v %v", got, err)
	}
}

// 关了再开：迁移不用重跑、不报错，写进去的还在
func TestQQAlertsSurviveReopen(t *testing.T) {
	t.Parallel()
	s, p := goSet(t)
	qq, _ := s.Platform("qq")
	if _, err := qq.AddQQAlert(QQAlert{At: 1000, Kind: "down", Account: "100", Text: "掉线"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatalf("再开一次不该报错：%v", err)
	}
	defer s2.Close()
	qq2, _ := s2.Platform("qq")
	got, err := qq2.QQAlerts("100", 0)
	if err != nil || len(got) != 1 || got[0].Text != "掉线" || got[0].At != 1000 {
		t.Fatalf("再开以后数据要还在：%+v %v", got, err)
	}
}

// 旧程序留下的 QQ 库只跑到平台的 0001：正式打开时补跑后面的迁移（0002 提醒、0003 历史归档表），qq_people 的旧数据还在，
// 提醒表能写能读，平台版本表记到最新（上面那个再开测试的起点已经是版本 2，没测到这一段）
func TestQQAlertsLegacyQQDBUpgrades(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.ChatDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// 通用迁移照 OpenGo 的做法全跑；平台迁移用同一个调用只跑到版本 1（旧程序那一版，不抄迁移代码）
	ev, err := openGo(p.ChatDB("qq"), "migrations/chat", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateWith(ev, "migrations/qq", 1, "goose_db_version_qq"); err != nil {
		ev.Close()
		t.Fatal(err)
	}
	if _, err := ev.Exec(`INSERT INTO qq_people (uid, nickname) VALUES ('100', '编的昵称')`); err != nil {
		ev.Close()
		t.Fatal(err)
	}
	// 先确认造出来的真是旧库：平台版本 1，没有 qq_alerts，不然这条测试就是在测普通的再开
	if v := qqPlatformVersion(t, ev); v != 1 {
		ev.Close()
		t.Fatalf("造出来的旧库平台版本是 %d，要 1", v)
	}
	if ok, err := tableExists(ev, "qq_alerts"); err != nil || ok {
		ev.Close()
		t.Fatalf("旧库不该有 qq_alerts：%v %v", ok, err)
	}
	if err := ev.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatalf("旧库打开要能补跑 0002：%v", err)
	}
	defer s.Close()
	qq, _ := s.Platform("qq")
	var nick string
	if err := qq.Events.QueryRow(`SELECT nickname FROM qq_people WHERE uid = '100'`).Scan(&nick); err != nil {
		t.Fatalf("升级以后旧库里 100 号不见了：%v", err)
	}
	if nick != "编的昵称" {
		t.Fatalf("升级以后 100 号的昵称变了：%q", nick)
	}
	if _, err := qq.AddQQAlert(QQAlert{At: 1000, Kind: "down", Account: "100", Text: "掉线"}); err != nil {
		t.Fatalf("补跑 0002 以后要能写提醒：%v", err)
	}
	got, err := qq.QQAlerts("100", 0)
	if err != nil || len(got) != 1 || got[0].Kind != "down" || got[0].At != 1000 {
		t.Fatalf("补跑 0002 以后要能读提醒：%+v %v", got, err)
	}
}

// qqPlatformVersion 是 QQ 平台迁移表里记下的最大版本（和 OpenGo 用的那张表同名）
func qqPlatformVersion(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow(`SELECT coalesce(max(version_id), 0) FROM goose_db_version_qq`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
