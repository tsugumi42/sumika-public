package dbguard

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// failingChatTrack 是只有 chat 一条轨、最新号一定失败的迁移：2～latest-1 号正常建表，latest 号插不存在的表。
func failingChatTrack(latest int64) Track {
	files := map[int64]string{
		1: "-- +goose Up\nCREATE TABLE t1 (id INTEGER PRIMARY KEY);\n",
	}
	for v := int64(2); v < latest; v++ {
		files[v] = markerCompatible + "\n-- +goose Up\nCREATE TABLE t" + strconv.FormatInt(v, 10) + " (id INTEGER PRIMARY KEY);\n"
	}
	files[latest] = markerCompatible + "\n-- +goose Up\nINSERT INTO no_such_table (x) VALUES (1);\n"
	return mapTrack("chat", 1, files)
}

// 同一段“从几到几”、轨组合不同的失败快照不是同一段：先只有 chat 失败、再 chat+qq 失败，两份都要留。
func TestOpenFailedSnapshotsKeyedByTrackSet(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	calls := 0
	o := testOptions(file, &calls)
	o.Keep = 2
	base := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	o.Now = func() time.Time { return base }

	_, _, err := Open(context.Background(), file, []Track{failingChatTrack(2)}, o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonMigrateFailed {
		t.Fatalf("只有 chat 时 Reason = %s，要 migrate_failed（实际 %v）", un.Reason, err)
	}
	first := filepath.Base(un.Snapshot)
	if !strings.Contains(first, "-chat-v1-to-v2-") || !strings.HasSuffix(first, "-failed.sqlite") {
		t.Fatalf("第一份失败快照 = %s，要 chat-v1-to-v2 的 -failed", first)
	}

	o.Now = func() time.Time { return base.Add(time.Minute) }
	_, _, err = Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
	un = mustUnavailable(t, err)
	if un.Reason != ReasonMigrateFailed {
		t.Fatalf("chat+qq 时 Reason = %s，要 migrate_failed（实际 %v）", un.Reason, err)
	}
	second := filepath.Base(un.Snapshot)
	if !strings.Contains(second, "-chat+qq-v1-to-v2-") || !strings.HasSuffix(second, "-failed.sqlite") {
		t.Fatalf("第二份失败快照 = %s，要 chat+qq-v1-to-v2 的 -failed", second)
	}

	good, failed, restoreFailed := snapshotFilesByKind(t, o.SnapshotDir)
	if len(good) != 0 || len(restoreFailed) != 0 {
		t.Fatalf("普通 / 换回失败快照 = %v / %v，要都是空", good, restoreFailed)
	}
	if len(failed) != 2 {
		t.Fatalf("轨组合不同的两份失败快照都要留，实际 = %v", failed)
	}
	for _, want := range []string{first, second} {
		found := false
		for _, have := range failed {
			if have == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("失败快照 %s 被同段清理删掉了，现有 %v", want, failed)
		}
	}
}

// 换回失败的快照叫 -restorefailed：之后多次成功迁移、多次同段失败迁移，它都一份不少。
func TestOpenRestoreFailedSnapshotNeverPruned(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	o := testOptions(file, new(int))
	o.Keep = 1
	base := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	o.Now = func() time.Time { return base }
	o.Rename = func(old, new string) error {
		if new == file && strings.Contains(filepath.Base(old), ".restore-") {
			return errors.New("injected non-ephemeral rename failure")
		}
		return os.Rename(old, new)
	}
	_, _, err := Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonRestoreFailed {
		t.Fatalf("Reason = %s，要 restore_failed（实际 %v）", un.Reason, err)
	}
	restoreSnap := un.Snapshot
	if !strings.HasSuffix(restoreSnap, "-restorefailed.sqlite") {
		t.Fatalf("换回失败的快照 = %s，要 -restorefailed 后缀", filepath.Base(restoreSnap))
	}
	if _, err := os.Stat(restoreSnap); err != nil {
		t.Fatalf("换回失败的快照要留着：%v", err)
	}

	o.Rename = nil
	step := 0
	nextTime := func() time.Time {
		step++
		return base.Add(time.Duration(step) * time.Hour)
	}

	// 换回失败后原库停在 chat=2、qq=1：下一次只有 qq 待跑又失败，旧的“同从几到几”归类会误删 chat+qq 那份。
	o.Now = nextTime
	_, _, err = Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
	if un := mustUnavailable(t, err); un.Reason != ReasonMigrateFailed {
		t.Fatalf("第二次失败 Reason = %s，要 migrate_failed（实际 %v）", un.Reason, err)
	}
	if _, err := os.Stat(restoreSnap); err != nil {
		t.Fatalf("只有一条轨待跑又失败后，迁移前那份换回失败快照没了：%v", err)
	}

	// 再把 qq 迁成功，并让 chat 从 3 一路成功到 5。
	o.Now = nextTime
	db, _, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
	if err != nil {
		t.Fatalf("qq 成功迁移：%v", err)
	}
	db.Close()
	for _, latest := range []int64{3, 4, 5} {
		o.Now = nextTime
		db, _, err := Open(context.Background(), file, []Track{chatTrack(latest)}, o)
		if err != nil {
			t.Fatalf("chat 迁到 %d：%v", latest, err)
		}
		db.Close()
		if _, err := os.Stat(restoreSnap); err != nil {
			t.Fatalf("第 %d 次成功迁移后换回失败的快照没了：%v", step, err)
		}
	}

	// 同段（chat v5→v6）连续失败：普通 -failed 只留最新一份，-restorefailed 不受影响。
	for i := range 3 {
		o.Now = nextTime
		_, _, err := Open(context.Background(), file, []Track{failingChatTrack(6)}, o)
		un := mustUnavailable(t, err)
		if un.Reason != ReasonMigrateFailed {
			t.Fatalf("第 %d 次失败 Reason = %s，要 migrate_failed（实际 %v）", i+1, un.Reason, err)
		}
		if _, err := os.Stat(restoreSnap); err != nil {
			t.Fatalf("第 %d 次失败迁移后换回失败的快照没了：%v", i+1, err)
		}
	}

	good, failed, restoreFailed := snapshotFilesByKind(t, o.SnapshotDir)
	if len(good) > int(o.Keep) {
		t.Fatalf("普通快照 = %v，最多留 %d 份", good, o.Keep)
	}
	// 两段失败：qq v1→v2（换回失败后第一次），chat v5→v6（连续三次只留最新）。
	var chatFailed []string
	for _, name := range failed {
		switch {
		case strings.Contains(name, "-chat-v5-to-v6-"):
			chatFailed = append(chatFailed, name)
		case strings.Contains(name, "-qq-v1-to-v2-"):
		default:
			t.Fatalf("多出不在预期段里的失败快照：%s", name)
		}
	}
	if len(failed) != 2 || len(chatFailed) != 1 {
		t.Fatalf("失败快照 = %v，要 qq v1→v2 一份和 chat v5→v6 最新一份", failed)
	}
	if len(restoreFailed) != 1 || restoreFailed[0] != filepath.Base(restoreSnap) {
		t.Fatalf("换回失败快照 = %v，要原样保留 %s", restoreFailed, filepath.Base(restoreSnap))
	}
}

// 同一秒、同一轨组合连续两次换回失败：第二份不能拍到第一份 -restorefailed 腾出来的名字上，
// 两份都要在，内容各自是那一次迁移前的状态。
func TestOpenRestoreFailedSameSecondKeepsBothSnapshots(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	o := testOptions(file, new(int))
	o.Keep = 5
	o.Now = func() time.Time { return time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC) }

	// 先把 chat 迁到 2：两次失败的待跑轨都只剩 qq v1→v2，快照名才会撞在同一秒的同一组合上。
	db, _, err := Open(context.Background(), file, []Track{chatTrack(2)}, o)
	if err != nil {
		t.Fatalf("预先迁移 chat：%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// 主文件已经换成快照、删旁文件失败：库回到迁移前，计 restore_failed。
	failSidecarRemove := func(path string) error {
		if strings.HasSuffix(path, "-wal") {
			return errors.New("injected sidecar removal failure")
		}
		return os.Remove(path)
	}
	openRestoreFailed := func(tracks []Track) *Unavailable {
		t.Helper()
		_, _, err := Open(context.Background(), file, tracks, o)
		un := mustUnavailable(t, err)
		if un.Reason != ReasonRestoreFailed {
			t.Fatalf("Reason = %s，要 restore_failed（实际 %v）", un.Reason, err)
		}
		return un
	}

	o.Remove = failSidecarRemove
	first := openRestoreFailed(twoTrackTracks(failingQQFile()))

	// chat 再往前一步，第二次迁移前的状态就和第一次不同。
	o.Remove = nil
	db, _, err = Open(context.Background(), file, []Track{chatTrack(3)}, o)
	if err != nil {
		t.Fatalf("第二次失败前迁移 chat：%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	o.Remove = failSidecarRemove
	secondTracks := twoTrackTracks(failingQQFile())
	secondTracks[0] = chatTrack(3)
	second := openRestoreFailed(secondTracks)

	if first.Snapshot == second.Snapshot {
		t.Fatalf("两份换回失败快照拍到同一个名字：%s", filepath.Base(first.Snapshot))
	}
	if _, failed, restoreFailed := snapshotFilesByKind(t, o.SnapshotDir); len(restoreFailed) != 2 || len(failed) != 0 {
		t.Fatalf("换回失败 / -failed 快照 = %v / %v，要两份 -restorefailed、没有 -failed", restoreFailed, failed)
	}
	applied := func(path string) map[string]int64 {
		t.Helper()
		st, err := Probe(context.Background(), path, twoTrackTracks(goodQQFile()))
		if err != nil {
			t.Fatalf("探查快照 %s：%v", filepath.Base(path), err)
		}
		versions := make(map[string]int64, len(st.Tracks))
		for _, ts := range st.Tracks {
			versions[ts.Name] = ts.Applied
		}
		return versions
	}
	if got := applied(first.Snapshot); got["chat"] != 2 || got["qq"] != 1 {
		t.Fatalf("第一份快照 chat/qq = %d/%d，要 2/1", got["chat"], got["qq"])
	}
	if got := applied(second.Snapshot); got["chat"] != 3 || got["qq"] != 1 {
		t.Fatalf("第二份快照 chat/qq = %d/%d，要 3/1", got["chat"], got["qq"])
	}
}

func optionsForRel(file, rel, snapDir string) Options {
	o := testOptions(file, new(int))
	o.SnapshotDir = snapDir
	o.RelName = rel
	return o
}

// 换回失败的快照改名也失败时：快照保持普通名，旁写 .keep，之后成功 / 失败迁移轮番清理都动不了它和标记。
func TestOpenRestoreFailedSnapshotRenameFailureWritesKeepMarker(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	o := testOptions(file, new(int))
	o.Keep = 1
	base := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	o.Now = func() time.Time { return base }
	snapshotRenameFailures := 0
	o.Rename = func(old, new string) error {
		if new == file && strings.Contains(filepath.Base(old), ".restore-") {
			return errors.New("injected restore rename failure")
		}
		if strings.HasSuffix(new, "-restorefailed.sqlite") && snapshotRenameFailures == 0 {
			snapshotRenameFailures++
			return errors.New("injected snapshot rename failure")
		}
		return os.Rename(old, new)
	}
	_, _, err := Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonRestoreFailed {
		t.Fatalf("Reason = %s，要 restore_failed（实际 %v）", un.Reason, err)
	}
	if snapshotRenameFailures != 1 {
		t.Fatalf("给快照改名失败注入生效 %d 次，要只让这一次失败", snapshotRenameFailures)
	}
	keepSnap := un.Snapshot
	if !strings.HasSuffix(keepSnap, ".sqlite") || strings.HasSuffix(keepSnap, "-restorefailed.sqlite") {
		t.Fatalf("改名失败后快照 = %s，要保持普通 .sqlite 名字", filepath.Base(keepSnap))
	}
	marker := keepSnap + ".keep"
	markerBody, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("改名失败后快照旁要有 .keep 标记：%v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(markerBody), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "2026-11-16T01:00:00Z ") ||
		!strings.Contains(lines[0], "换回失败，迁移前唯一的好数据，不要删") {
		t.Fatalf(".keep 内容 = %q，要一行时间和原因", markerBody)
	}

	o.Rename = nil
	step := 0
	nextTime := func() time.Time {
		step++
		return base.Add(time.Duration(step) * time.Hour)
	}

	// 连续 4 次成功迁移，每次都会按 Keep=1 清普通快照。
	o.Now = nextTime
	db, _, err := Open(context.Background(), file, twoTrackTracks(goodQQFile()), o)
	if err != nil {
		t.Fatalf("qq 成功迁移：%v", err)
	}
	db.Close()
	for _, latest := range []int64{3, 4, 5} {
		o.Now = nextTime
		db, _, err = Open(context.Background(), file, []Track{chatTrack(latest)}, o)
		if err != nil {
			t.Fatalf("chat 迁到 %d：%v", latest, err)
		}
		db.Close()
		if _, err := os.Stat(keepSnap); err != nil {
			t.Fatalf("第 %d 次成功迁移后保留的快照没了：%v", step, err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("第 %d 次成功迁移后 .keep 标记没了：%v", step, err)
		}
	}

	// 再同段失败 2 次，失败快照清理也不能碰这份普通名快照。
	for i := range 2 {
		o.Now = nextTime
		_, _, err := Open(context.Background(), file, []Track{failingChatTrack(6)}, o)
		if un := mustUnavailable(t, err); un.Reason != ReasonMigrateFailed {
			t.Fatalf("第 %d 次失败 Reason = %s，要 migrate_failed（实际 %v）", i+1, un.Reason, err)
		}
		if _, err := os.Stat(keepSnap); err != nil {
			t.Fatalf("第 %d 次失败迁移后保留的快照没了：%v", i+1, err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("第 %d 次失败迁移后 .keep 标记没了：%v", i+1, err)
		}
	}
	after, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("读 .keep 标记：%v", err)
	}
	if string(after) != string(markerBody) {
		t.Fatalf(".keep 标记被改写了：%q → %q", markerBody, after)
	}
}

// 改名和写标记都失败时，详情要请人手动保留快照：标记路径做成目录，写不进去。
func TestOpenRestoreFailedSnapshotMarkerWriteFailureDetail(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	o := testOptions(file, new(int))
	o.Now = func() time.Time { return time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC) }
	o.Rename = func(old, new string) error {
		if strings.HasSuffix(new, "-restorefailed.sqlite") {
			// 改名失败后要走写 .keep 标记：把标记路径占成目录，让写入也失败。
			if err := os.Mkdir(keepMarkerPath(old), 0o755); err != nil {
				return err
			}
			return errors.New("injected snapshot rename failure")
		}
		if new == file && strings.Contains(filepath.Base(old), ".restore-") {
			return errors.New("injected restore rename failure")
		}
		return os.Rename(old, new)
	}
	_, _, err := Open(context.Background(), file, twoTrackTracks(failingQQFile()), o)
	un := mustUnavailable(t, err)
	if un.Reason != ReasonRestoreFailed {
		t.Fatalf("Reason = %s，要 restore_failed（实际 %v）", un.Reason, err)
	}
	want := "快照没能标记，请手动保留 " + absPath(un.Snapshot)
	if !strings.Contains(un.Detail, want) {
		t.Fatalf("Detail = %q，要包含 %q", un.Detail, want)
	}
	if _, err := os.Stat(un.Snapshot); err != nil {
		t.Fatalf("快照要留着：%v", err)
	}
}

// 清理三种目标（普通快照、失败快照、残留 .partial）都跳过有 .keep 标记的文件，标记本身也从不被清。
func TestSnapshotCleanupsSkipKeepMarked(t *testing.T) {
	dir := t.TempDir()
	o := Options{SnapshotDir: dir, RelName: "go/chat/qq.db", Keep: 1}.withDefaults()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	prefix := "go-chat-qq.db-chat-v1-to-v2-"

	marked := write(prefix+"20260101-000000.sqlite", "old data")
	markedKeep := write(prefix+"20260101-000000.sqlite.keep", "2026-11-16T01:00:00Z 换回失败，迁移前唯一的好数据，不要删\n")
	write(prefix+"20260101-000100.sqlite", "newer")
	write(prefix+"20260101-000200.sqlite", "newest")
	if removed := o.pruneSnapshots(); len(removed) != 1 || !strings.HasSuffix(removed[0], "20260101-000100.sqlite") {
		t.Fatalf("普通保留清掉 %v，只该清没标记的旧快照", removed)
	}
	if _, err := os.Stat(marked); err != nil {
		t.Fatalf(".keep 标记的普通快照被清掉了：%v", err)
	}
	if _, err := os.Stat(markedKeep); err != nil {
		t.Fatalf(".keep 标记本身被清掉了：%v", err)
	}

	markedFailed := write(prefix+"20260101-000000-failed.sqlite", "old failed data")
	markedFailedKeep := write(prefix+"20260101-000000-failed.sqlite.keep", "keep\n")
	write(prefix+"20260101-000100-failed.sqlite", "failed newer")
	write(prefix+"20260101-000200-failed.sqlite", "failed newest")
	if removed := o.pruneFailedSnapshots(); len(removed) != 1 || !strings.HasSuffix(removed[0], "20260101-000100-failed.sqlite") {
		t.Fatalf("失败快照清理清掉 %v，只该清没标记的旧快照", removed)
	}
	if _, err := os.Stat(markedFailed); err != nil {
		t.Fatalf(".keep 标记的失败快照被清掉了：%v", err)
	}
	if _, err := os.Stat(markedFailedKeep); err != nil {
		t.Fatalf("失败快照的 .keep 标记被清掉了：%v", err)
	}

	markedPartial := write(prefix+"20260101-000300.sqlite.partial", "half")
	markedPartialKeep := write(prefix+"20260101-000300.sqlite.keep", "keep\n")
	stalePartial := write(prefix+"20260101-000400.sqlite.partial", "half")
	o.removeStalePartials()
	if _, err := os.Stat(markedPartial); err != nil {
		t.Fatalf("对应快照有 .keep 标记的残留 .partial 被清掉了：%v", err)
	}
	if _, err := os.Stat(markedPartialKeep); err != nil {
		t.Fatalf("快照的 .keep 标记被清掉了：%v", err)
	}
	if _, err := os.Stat(stalePartial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("没标记的残留 .partial 要被清掉：%v", err)
	}
}

// 快照取名字时五种落点都算占用，否则同一秒的第二份会拍到刚改名走的空位上。
func TestUniqueSnapshotPathAccountsForSnapshotKinds(t *testing.T) {
	stem := "go-chat-qq.db-qq-v1-to-v2-20261116-010203"
	dir := t.TempDir()
	if got, want := uniqueSnapshotPath(dir, stem+".sqlite"), filepath.Join(dir, stem+".sqlite"); got != want {
		t.Fatalf("空目录取到 %s，要 %s", filepath.Base(got), filepath.Base(want))
	}
	for _, suffix := range []string{".sqlite", ".sqlite.partial", "-failed.sqlite", "-restorefailed.sqlite", ".sqlite.keep"} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, stem+suffix), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, want := uniqueSnapshotPath(dir, stem+".sqlite"), filepath.Join(dir, stem+"-2.sqlite"); got != want {
				t.Fatalf("%s 占用时取到 %s，要 %s", suffix, filepath.Base(got), filepath.Base(want))
			}
		})
	}
}

// 加 -failed / -restorefailed 时目标已存在就换序号，1 到 100 都占满报错，不覆盖别人。
func TestMarkedSnapshotPathSkipsOccupiedTargets(t *testing.T) {
	stem := "go-chat-qq.db-qq-v1-to-v2-20261116-010203"
	write := func(dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	plain := filepath.Join(dir, stem+".sqlite")
	write(dir, stem+"-failed.sqlite")
	got, err := markedSnapshotPath(plain, "-failed")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, stem+"-2-failed.sqlite"); got != want {
		t.Fatalf("目标被占用时取到 %s，要 %s", filepath.Base(got), filepath.Base(want))
	}

	dir = t.TempDir()
	seq := filepath.Join(dir, stem+"-2.sqlite")
	write(dir, stem+"-2-restorefailed.sqlite")
	got, err = markedSnapshotPath(seq, "-restorefailed")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, stem+"-3-restorefailed.sqlite"); got != want {
		t.Fatalf("自带序号时取到 %s，要 %s", filepath.Base(got), filepath.Base(want))
	}

	dir = t.TempDir()
	plain = filepath.Join(dir, stem+".sqlite")
	for i := 1; i <= maxSnapshotSeq; i++ {
		name := stem + "-failed.sqlite"
		if i > 1 {
			name = stem + "-" + strconv.Itoa(i) + "-failed.sqlite"
		}
		write(dir, name)
	}
	if _, err := markedSnapshotPath(plain, "-failed"); err == nil {
		t.Fatalf("1 到 %d 号都被占用时要报错", maxSnapshotSeq)
	}
}

func namesWithPrefix(names []string, prefix string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

// 两个不同 RelName 的库共用快照目录：成功迁移的保留、失败快照的清理都只动自己的。
func TestOpenSnapshotPruningStaysWithinRelName(t *testing.T) {
	t.Run("成功迁移只清自己的", func(t *testing.T) {
		dir := t.TempDir()
		snapDir := filepath.Join(dir, "snapshots")
		fileA := filepath.Join(dir, "a.db")
		fileB := filepath.Join(dir, "b.db")
		pendingChatDB(t, fileA)
		pendingChatDB(t, fileB)
		oA := optionsForRel(fileA, "go/a.db", snapDir)
		oB := optionsForRel(fileB, "go/b.db", snapDir)
		oA.Keep, oB.Keep = 1, 1
		base := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)

		oA.Now = func() time.Time { return base }
		db, _, err := Open(context.Background(), fileA, []Track{chatTrack(2)}, oA)
		if err != nil {
			t.Fatalf("A 第一次迁移：%v", err)
		}
		db.Close()

		oB.Now = func() time.Time { return base }
		db, _, err = Open(context.Background(), fileB, []Track{chatTrack(2)}, oB)
		if err != nil {
			t.Fatalf("B 第一次迁移：%v", err)
		}
		db.Close()

		// B 第二次成功迁移触发 Keep=1 的清理：只该清 B 自己的旧快照。
		oB.Now = func() time.Time { return base.Add(time.Hour) }
		db, _, err = Open(context.Background(), fileB, []Track{chatTrack(3)}, oB)
		if err != nil {
			t.Fatalf("B 第二次迁移：%v", err)
		}
		db.Close()

		good, _, _ := snapshotFilesByKind(t, snapDir)
		if got := namesWithPrefix(good, "go-a.db-"); len(got) != 1 {
			t.Fatalf("B 清理后 A 的好快照 = %v，要 1 份", got)
		}
		if got := namesWithPrefix(good, "go-b.db-"); len(got) != 1 {
			t.Fatalf("B 清理后 B 的好快照 = %v，要 1 份", got)
		}

		// A 第二次成功迁移再触发一次清理，B 仍不受影响。
		oA.Now = func() time.Time { return base.Add(2 * time.Hour) }
		db, _, err = Open(context.Background(), fileA, []Track{chatTrack(3)}, oA)
		if err != nil {
			t.Fatalf("A 第二次迁移：%v", err)
		}
		db.Close()
		good, _, _ = snapshotFilesByKind(t, snapDir)
		if got := namesWithPrefix(good, "go-a.db-"); len(got) != 1 {
			t.Fatalf("A 清理后 A 的好快照 = %v，要 1 份", got)
		}
		if got := namesWithPrefix(good, "go-b.db-"); len(got) != 1 {
			t.Fatalf("A 清理后 B 的好快照 = %v，要 1 份", got)
		}
	})

	t.Run("失败快照只清自己的", func(t *testing.T) {
		dir := t.TempDir()
		snapDir := filepath.Join(dir, "snapshots")
		fileA := filepath.Join(dir, "a.db")
		fileB := filepath.Join(dir, "b.db")
		pendingChatDB(t, fileA)
		pendingChatDB(t, fileB)
		oA := optionsForRel(fileA, "go/a.db", snapDir)
		oB := optionsForRel(fileB, "go/b.db", snapDir)
		oA.Keep, oB.Keep = 2, 2
		base := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)

		oA.Now = func() time.Time { return base }
		_, _, err := Open(context.Background(), fileA, []Track{failingChatTrack(2)}, oA)
		if un := mustUnavailable(t, err); un.Reason != ReasonMigrateFailed {
			t.Fatalf("A 失败 Reason = %s，要 migrate_failed（实际 %v）", un.Reason, err)
		}

		// B 同段失败一次：它的清理不能把 A 的失败快照当成同段删掉。
		oB.Now = func() time.Time { return base }
		_, _, err = Open(context.Background(), fileB, []Track{failingChatTrack(2)}, oB)
		if un := mustUnavailable(t, err); un.Reason != ReasonMigrateFailed {
			t.Fatalf("B 失败 Reason = %s，要 migrate_failed（实际 %v）", un.Reason, err)
		}

		// A 再失败两次：A 自己的同段只留最新，B 的仍不动。
		for i := 1; i <= 2; i++ {
			oA.Now = func() time.Time { return base.Add(time.Duration(i) * time.Hour) }
			_, _, err := Open(context.Background(), fileA, []Track{failingChatTrack(2)}, oA)
			if un := mustUnavailable(t, err); un.Reason != ReasonMigrateFailed {
				t.Fatalf("A 第 %d 次失败 Reason = %s（实际 %v）", i+1, un.Reason, err)
			}
		}

		_, failed, _ := snapshotFilesByKind(t, snapDir)
		if got := namesWithPrefix(failed, "go-a.db-"); len(got) != 1 {
			t.Fatalf("A 的失败快照 = %v，要只留最新 1 份", got)
		}
		if got := namesWithPrefix(failed, "go-b.db-"); len(got) != 1 {
			t.Fatalf("B 的失败快照 = %v，要 1 份", got)
		}
	})
}

// 锁键归一化：同一个文件用大小写不同（Windows）、相对和绝对路径并发 Open，仍然互斥。
func TestOpenLockKeyNormalizesPaths(t *testing.T) {
	assertWaits := func(t *testing.T, first, second string) {
		t.Helper()
		release := make(chan struct{})
		entered := make(chan struct{})
		o := testOptions(first, new(int))
		inner := o.OpenDB
		var once sync.Once
		o.OpenDB = func(f string, fresh bool) (*sql.DB, error) {
			once.Do(func() { close(entered) })
			<-release
			return inner(f, fresh)
		}
		firstDone := make(chan error, 1)
		go func() {
			db, _, err := Open(context.Background(), first, []Track{chatTrack(2)}, o)
			if db != nil {
				db.Close()
			}
			firstDone <- err
		}()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatal("第一个 Open 没走到 OpenDB")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		secondDB, _, err := Open(ctx, second, []Track{chatTrack(2)}, testOptions(second, new(int)))
		if secondDB != nil {
			secondDB.Close()
		}
		close(release)
		if firstErr := <-firstDone; firstErr != nil {
			t.Fatalf("第一个 Open 失败：%v", firstErr)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("第二个 Open（%s）返回 %v，要等锁等到 ctx 超时", second, err)
		}
		var un *Unavailable
		if errors.As(err, &un) {
			t.Fatalf("等锁超时不该包成 *Unavailable：%v", err)
		}
	}

	t.Run("大小写不同", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("只有 Windows 文件名不区分大小写")
		}
		file := filepath.Join(t.TempDir(), "LockCase.db")
		assertWaits(t, file, strings.ToLower(file))
	})

	t.Run("相对和绝对", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "rel.db")
		t.Chdir(filepath.Dir(file))
		assertWaits(t, file, filepath.Base(file))
	})
}

// 新建失败时这次建的主文件和 -wal / -shm / -journal 全部删掉。
func TestOpenCreateFailureRemovesNewSidecars(t *testing.T) {
	file := filepath.Join(t.TempDir(), "new.db")
	o := testOptions(file, new(int))
	o.OpenDB = func(f string, fresh bool) (*sql.DB, error) {
		for _, path := range []string{f, f + "-wal", f + "-journal"} {
			if err := os.WriteFile(path, []byte("half"), 0o600); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("injected open error")
	}
	_, _, err := Open(context.Background(), file, []Track{chatTrack(1)}, o)
	if un := mustUnavailable(t, err); un.Reason != ReasonCreateFailed {
		t.Fatalf("Reason = %s，要 create_failed（实际 %v）", un.Reason, err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(file + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("新建失败后 %s%s 要删掉：%v", file, suffix, err)
		}
	}
}

// 迁移途中取消返回取消错误本身，不包成 *Unavailable；库照样换回迁移前。
// 这个断言加在现有 TestOpenCancelDuringMigrationRestores 上，见 open_test.go。

// 探查途中取消：返回取消错误本身，不包成 probe_failed 的 *Unavailable。
func TestOpenCancelDuringProbeReturnsCanceled(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qq.db")
	legacyTwoTrackDB(t, file)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := probeStateAndDecision(ctx, file, []Track{chatTrack(2)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("探查取消错误 = %v，要 errors.Is(context.Canceled)", err)
	}
	var un *Unavailable
	if errors.As(err, &un) {
		t.Fatalf("探查取消不该包成 *Unavailable（%s）：%v", un.Reason, err)
	}

	// Open 的第一道等锁也看 ctx：取消后立刻返回取消错误，不排队等一个已经放弃的开库。
	_, _, err = Open(ctx, file, twoTrackTracks(goodQQFile()), testOptions(file, new(int)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Open 取消错误 = %v，要 errors.Is(context.Canceled)", err)
	}
	if errors.As(err, &un) {
		t.Fatalf("Open 取消不该包成 *Unavailable（%s）：%v", un.Reason, err)
	}
}

// 新建途中取消：删掉这次建的文件，返回取消错误本身，不报 create_failed。
func TestOpenCancelDuringCreateReturnsCanceled(t *testing.T) {
	file := filepath.Join(t.TempDir(), "new.db")
	ctx, cancel := context.WithCancel(context.Background())
	o := testOptions(file, new(int))
	inner := o.OpenDB
	o.OpenDB = func(f string, fresh bool) (*sql.DB, error) {
		cancel()
		return inner(f, fresh)
	}
	db, _, err := Open(ctx, file, []Track{chatTrack(2)}, o)
	if db != nil {
		db.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("新建取消错误 = %v，要 errors.Is(context.Canceled)", err)
	}
	var un *Unavailable
	if errors.As(err, &un) {
		t.Fatalf("新建取消不该包成 *Unavailable（%s）：%v", un.Reason, err)
	}
	if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("新建取消后这次建的文件要删掉：%v", err)
	}
}

// goose 版本表的 version_id 是文本：probe_failed，详情只说哪列不是整数，不回显单元格值。
func TestOpenProbeRejectsTextGooseVersion(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bad.db")
	db := openTestDB(t, file)
	execAll(t, db,
		gooseDDL,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES ('SECRET-CELL-VALUE', 1)`,
		`CREATE TABLE c (id INTEGER PRIMARY KEY)`,
	)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, _, err := Open(context.Background(), file, []Track{chatTrack(2)}, testOptions(file, &calls))
	un := mustUnavailable(t, err)
	if un.Reason != ReasonProbeFailed {
		t.Fatalf("Reason = %s，要 probe_failed（实际 %v）", un.Reason, err)
	}
	if !strings.Contains(un.Detail, "version_id 不是整数") {
		t.Fatalf("Detail = %q，要说 version_id 不是整数", un.Detail)
	}
	if strings.Contains(un.Detail, "SECRET-CELL-VALUE") {
		t.Fatalf("Detail 回显了单元格值：%q", un.Detail)
	}
	if calls != 0 {
		t.Fatalf("探查失败不该开可写连接，OpenDB 调了 %d 次", calls)
	}
}
