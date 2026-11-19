package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/paths"
)

func TestRawSearchMigrations(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"local", "qq", "fresh"} {
		t.Run(platform, func(t *testing.T) {
			p, err := paths.Init(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			file, dir, version := p.ChatDB(platform), "migrations/chat", int64(13)
			if platform == "fresh" {
				file, dir, version = p.BunEventsDB(), "migrations/events", 4
			}
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			db, err := open(file, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := migrate(db, dir, version); err != nil {
				t.Fatal(err)
			}
			insertSearchEvent(t, db, "old", "Ab中😀\x00尾")
			// A conflicting index makes the migration fail after creating the search tables.
			if _, err := db.Exec(`CREATE INDEX ix_events_time ON events(id)`); err != nil {
				t.Fatal(err)
			}
			if err := migrate(db, dir, 0); err == nil {
				t.Fatal("migration unexpectedly succeeded")
			}
			for _, name := range []string{"events_short", "events_trigram"} {
				if exists, err := tableExists(db, name); err != nil || exists {
					t.Fatalf("migration rollback left %s: %v %v", name, exists, err)
				}
			}
			if _, err := db.Exec(`DROP INDEX ix_events_time`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			var closeDB func()
			openDB := func() *sql.DB {
				if platform == "fresh" {
					st, err := OpenFresh(p)
					if err != nil {
						t.Fatal(err)
					}
					closeDB = st.Close
					return st.Events
				}
				set, err := OpenGo(p, platform)
				if err != nil {
					t.Fatal(err)
				}
				closeDB = func() {
					if err := set.Close(); err != nil {
						t.Fatal(err)
					}
				}
				st, ok := set.Platform(platform)
				if !ok {
					t.Fatal("missing platform")
				}
				return st.Events
			}
			db = openDB()
			assertSearchMatch(t, db, "events_trigram", "ab中", "old", true)
			assertShortMatch(t, db, "AB", "old", true)
			assertShortMatch(t, db, "中😀", "old", true)
			assertShortMatch(t, db, "尾", "old", false)
			insertSearchEvent(t, db, "new", "青空ABC")
			assertSearchMatch(t, db, "events_trigram", "青空a", "new", true)
			assertShortMatch(t, db, "青空", "new", true)
			insertSearchEvent(t, db, "repeated", "AaA")
			insertSearchEvent(t, db, "empty", "")
			var tokenCount int
			if err := db.QueryRow(`SELECT count(*) FROM events_short JOIN events ON events.seq=events_short.event_seq WHERE events.id='repeated' AND token='a'`).Scan(&tokenCount); err != nil || tokenCount != 1 {
				t.Fatalf("repeated token count=%d: %v", tokenCount, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM events_short JOIN events ON events.seq=events_short.event_seq WHERE events.id='empty'`).Scan(&tokenCount); err != nil || tokenCount != 0 {
				t.Fatalf("empty text token count=%d: %v", tokenCount, err)
			}
			insertSearchEvent(t, db, "later", "AB")
			if _, err := db.Exec(`UPDATE events SET created_at=2 WHERE id='old'`); err != nil {
				t.Fatal(err)
			}
			var firstID string
			if err := db.QueryRow(`SELECT events.id FROM events_short JOIN events ON events.seq=events_short.event_seq WHERE token='ab' ORDER BY events_short.created_at DESC, events_short.event_seq DESC LIMIT 1`).Scan(&firstID); err != nil || firstID != "old" {
				t.Fatalf("updated timestamp first id=%q: %v", firstID, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM events_short JOIN events ON events.seq=events_short.event_seq WHERE events.id='old' AND events_short.created_at != events.created_at`).Scan(&tokenCount); err != nil || tokenCount != 0 {
				t.Fatalf("stale timestamp token count=%d: %v", tokenCount, err)
			}
			var repeatedSeq int64
			if err := db.QueryRow(`SELECT seq FROM events WHERE id='repeated'`).Scan(&repeatedSeq); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE events SET seq=seq+1000000 WHERE id='repeated'`); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM events_short WHERE event_seq=?`, repeatedSeq).Scan(&tokenCount); err != nil || tokenCount != 0 {
				t.Fatalf("old sequence token count=%d: %v", tokenCount, err)
			}
			assertShortMatch(t, db, "aa", "repeated", true)
			if _, err := db.Exec(`UPDATE events SET text='白雲XYZ' WHERE id='new'`); err != nil {
				t.Fatal(err)
			}
			assertSearchMatch(t, db, "events_trigram", "青空a", "new", false)
			assertSearchMatch(t, db, "events_trigram", "白雲x", "new", true)
			assertShortMatch(t, db, "青空", "new", false)
			var newSeq int64
			if err := db.QueryRow(`SELECT seq FROM events WHERE id='new'`).Scan(&newSeq); err != nil {
				t.Fatal(err)
			}
			assertShortMatch(t, db, "白雲", "new", true)
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE events SET text='回滚内容' WHERE id='new'; DELETE FROM events WHERE id='old'; INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,created_at) VALUES('rolled','x','x','human','x','message','回滚内容',1)`); err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			assertSearchMatch(t, db, "events_trigram", "白雲x", "new", true)
			assertShortMatch(t, db, "白雲", "new", true)
			assertShortMatch(t, db, "AB", "old", true)
			assertShortMatch(t, db, "回滚", "rolled", false)
			if _, err := db.Exec(`DELETE FROM events WHERE id='new'`); err != nil {
				t.Fatal(err)
			}
			assertSearchRowAbsent(t, db, "events_trigram", "白雲x", newSeq)
			var shortCount int
			if err := db.QueryRow(`SELECT count(*) FROM events_short WHERE event_seq=?`, newSeq).Scan(&shortCount); err != nil || shortCount != 0 {
				t.Fatalf("deleted event retains %d short tokens: %v", shortCount, err)
			}
			closeDB()
			db = openDB()
			defer closeDB()
			assertSearchMatch(t, db, "events_trigram", "ab中", "old", true)
			assertShortMatch(t, db, "中😀", "old", true)
		})
	}
}

func insertSearchEvent(t *testing.T, db *sql.DB, id, text string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,created_at) VALUES(?, 'x', 'x', 'human', 'x', 'message', ?, 1)`, id, text); err != nil {
		t.Fatal(err)
	}
}

func assertShortMatch(t *testing.T, db *sql.DB, text, id string, want bool) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM events_short JOIN events ON events.seq=events_short.event_seq WHERE events_short.token=lower(?) AND events.id=?`, text, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if (count == 1) != want {
		t.Fatalf("short match %q id %q: count=%d want=%v", text, id, count, want)
	}
}

func assertSearchMatch(t *testing.T, db *sql.DB, table, query, id string, want bool) {
	t.Helper()
	var count int
	// table is a test-owned literal, not user input.
	if err := db.QueryRow(`SELECT count(*) FROM `+table+` JOIN events ON events.seq=`+table+`.rowid WHERE `+table+` MATCH ? AND events.id=?`, `"`+query+`"`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if (count == 1) != want {
		t.Fatalf("%s match %q id %q: count=%d want=%v", table, query, id, count, want)
	}
}

func assertSearchRowAbsent(t *testing.T, db *sql.DB, table, query string, seq int64) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+table+` MATCH ? AND rowid=?`, `"`+query+`"`, seq).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%s retains row %d for %q", table, seq, query)
	}
}
