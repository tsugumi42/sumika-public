package memory

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 证据计次抽成写事务和记忆页共用的一个函数（规格-记忆页 4.1）：同一批证据行，事务里数和只读连接上数必须相等，
// 并且等于手算的数。名字、会话都是编的。

func TestConfirmOccasionsForShared(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want int }{{0, 3}, {-5, 3}, {1, 2}, {2, 2}, {3, 3}, {7, 7}} {
		if got := ConfirmOccasionsFor(c.in); got != c.want {
			t.Errorf("ConfirmOccasionsFor(%d) = %d，应为 %d", c.in, got, c.want)
		}
		// 写事务读的是同一个函数：配置值直接塞进去比
		w := &writer{m: &Memory{opt: Options{ConfirmOccasions: c.in}}}
		if got := w.confirmOccasions(); got != c.want {
			t.Errorf("writer.confirmOccasions()（配置 %d）= %d，应为 %d", c.in, got, c.want)
		}
	}
}

func TestTallyEvidenceSameInTxAndOnDB(t *testing.T) {
	t.Parallel()
	file := migratedDB(t, nil)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ev := func(fact, day, conv, speaker string, bySubject int) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, verdict, at) VALUES (?, ?, ?, 1, ?, ?, 'ok', 1)`,
			fact, day, conv, nullableText(speaker), bySubject); err != nil {
			t.Fatal(err)
		}
	}
	// a：本人说了两天（2 次）+ 转述的甲说了三天（含大小写和空白不同的写法，去重后 1 次）+ 一行没记说话人的转述（不算）→ 行 6，本人 2，转述 1，合计 3
	ev("a", "2026-01-01", "c1", "", 1)
	ev("a", "2026-01-02", "c1", "", 1)
	ev("a", "2026-01-03", "c1", "qq:10001", 0)
	ev("a", "2026-01-04", "c1", "qq:10001", 0)
	ev("a", "2026-01-05", "c1", " QQ:10001 ", 0)
	ev("a", "2026-01-06", "c1", "", 0)
	// b：只有两个不同的转述者，没有本人 → 行 2，本人 0，转述 2，合计 2
	ev("b", "2026-01-01", "c1", "qq:10002", 0)
	ev("b", "2026-01-01", "c2", "qq:10003", 0)
	// c：没有证据行
	want := map[string]EvidenceTally{
		"a": {Rows: 6, Self: 2, Others: 1, Occasions: 3},
		"b": {Rows: 2, Self: 0, Others: 2, Occasions: 2},
		"c": {},
	}
	ctx := context.Background()
	for id, w := range want {
		onDB, err := TallyEvidence(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		inTx, err := tallyEvidence(ctx, tx, id)
		tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		if onDB != w {
			t.Errorf("%s：只读连接上数得 %+v，手算 %+v", id, onDB, w)
		}
		if got := (EvidenceTally{Rows: inTx.rows, Self: inTx.self, Others: inTx.others, Occasions: inTx.occasions()}); got != w {
			t.Errorf("%s：事务里数得 %+v，手算 %+v", id, got, w)
		}
	}
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
