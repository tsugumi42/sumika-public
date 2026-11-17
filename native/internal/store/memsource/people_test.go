package memsource

// 共用的人名表读取（装配和考题各调一次）：名字从昵称、各群名片、曾用名来（ADR-038），顺序要确定

import (
	"database/sql"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestReadQQPeople(t *testing.T) {
	file := filepath.Join(t.TempDir(), "events.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatalf("建一个假的 Bun 事件库：%v", err)
	}
	for _, q := range []string{
		`CREATE TABLE qq_people (uid TEXT PRIMARY KEY, nickname TEXT, cards_json TEXT, aliases_json TEXT)`,
		// 两张名片故意让键和值的顺序相反：名片按键排，不按 map 的随机顺序
		`INSERT INTO qq_people (uid, nickname, cards_json, aliases_json) VALUES ('300', '编的甲', '{"301":"另一个群的名","300":"群里的甲"}', '["以前叫编的乙"]')`,
		`INSERT INTO qq_people (uid, nickname, cards_json, aliases_json) VALUES ('400', '', NULL, NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("造 qq_people：%v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关假的 Bun 事件库：%v", err)
	}

	roster, err := ReadQQPeople(file)
	if err != nil {
		t.Fatalf("读 qq_people：%v", err)
	}
	people := roster.People
	want := map[string][]string{
		"qq:300": {"编的甲", "群里的甲", "另一个群的名", "以前叫编的乙"},
		"qq:400": nil,
	}
	sort.Slice(people, func(i, j int) bool { return people[i].ID < people[j].ID })
	if len(people) != len(want) {
		t.Fatalf("读出来 %d 个人，要 %d 个：%+v", len(people), len(want), people)
	}
	for _, p := range people {
		w, ok := want[p.ID]
		if !ok {
			t.Fatalf("多了一个人 %q", p.ID)
		}
		if !reflect.DeepEqual(p.Names, w) {
			t.Fatalf("%s 的名字是 %v，要 %v", p.ID, p.Names, w)
		}
	}
}

// 文件在但不是事件库（没有 qq_people）、文件不存在，都要报错：调用方照错误说清楚是"还没有名册"还是真读不了
func TestReadQQPeopleErrors(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(empty))
	if err != nil {
		t.Fatalf("建空库：%v", err)
	}
	if _, err := db.Exec(`CREATE TABLE notes (id INTEGER)`); err != nil {
		t.Fatalf("建空库的表：%v", err)
	}
	db.Close()
	if _, err := ReadQQPeople(empty); err == nil {
		t.Fatalf("没有 qq_people 的库也该报错")
	}
	if _, err := ReadQQPeople(filepath.Join(dir, "nope.db")); err == nil {
		t.Fatalf("文件不存在也该报错")
	}
}

// # 不能截断 URI，% 不能变成另一个路径；读不到的库也不能在别处创建文件。
func TestReadQQPeopleOddPath(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a#b%41c", "a%41c"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(root, name)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "events.db")
			u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(file)}
			db, err := sql.Open("sqlite", u.String())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE qq_people (uid TEXT, nickname TEXT, cards_json TEXT, aliases_json TEXT); INSERT INTO qq_people VALUES ('100', '编的特殊目录昵称', '{"300":"编的特殊目录名片"}', '[]')`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := peopleFiles(t, root)
			people, err := ReadQQPeople(file)
			if err != nil {
				t.Errorf("特殊目录没有读对：%v", err)
			} else if people.Name("300", "100") != "编的特殊目录名片" {
				t.Errorf("读错了名册：%+v", people)
			}
			if _, err := ReadQQPeople(filepath.Join(dir, "missing.db")); err == nil {
				t.Error("不存在的库没有报错")
			}
			if after := peopleFiles(t, root); !reflect.DeepEqual(after, before) {
				t.Errorf("只读查询多出了文件：前 %v 后 %v", before, after)
			}
		})
	}
}

func peopleFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
