package desksettings

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/paths"
)

func newDir(t *testing.T) string {
	t.Helper()
	p, err := paths.Init(filepath.Join(t.TempDir(), "数据"))
	if err != nil {
		t.Fatal(err)
	}
	return p.Root
}

func writeFile(t *testing.T, s *Store, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.File()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.File(), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func leftovers(t *testing.T, s *Store) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(s.File()))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// 默认关：文件、目录都不存在也不报错
func TestAutoReceiveDefaultsOff(t *testing.T) {
	for name, dir := range map[string]string{"数据目录已初始化": newDir(t), "数据目录不存在": filepath.Join(t.TempDir(), "没有")} {
		on, err := New(dir).AutoReceive()
		if on || err != nil {
			t.Errorf("%s：默认应该是关且没有错误，得到 %v %v", name, on, err)
		}
	}
}

func TestSetAndReadRoundTrip(t *testing.T) {
	s := New(newDir(t))
	for _, want := range []bool{true, false, true} {
		if err := s.SetAutoReceive(want); err != nil {
			t.Fatal(err)
		}
		got, err := s.AutoReceive()
		if got != want || err != nil {
			t.Fatalf("写 %v 后读到 %v %v", want, got, err)
		}
		// 换一个 Store 实例读同一个文件：存的是文件，不是内存
		got, err = New(s.dir).AutoReceive()
		if got != want || err != nil {
			t.Fatalf("写 %v 后重新打开读到 %v %v", want, got, err)
		}
	}
	if l := leftovers(t, s); len(l) != 0 {
		t.Fatalf("写完留下了临时文件：%v", l)
	}
}

// 不认识的键原样留着；写出来的是缩进的合法 JSON
func TestSetKeepsUnknownKeys(t *testing.T) {
	s := New(newDir(t))
	writeFile(t, s, `{"futureSwitch": {"a": [1, 2]}, "autoReceive": true}`)
	if err := s.SetAutoReceive(false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(s.File())
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("写出来的不是 JSON：%v\n%s", err, b)
	}
	if string(m["autoReceive"]) != "false" {
		t.Fatalf("autoReceive = %s", m["autoReceive"])
	}
	var fut struct{ A []int }
	if err := json.Unmarshal(bytes.ToLower(m["futureSwitch"]), &fut); err != nil || len(fut.A) != 2 {
		t.Fatalf("没认识的键被改坏了：%s", m["futureSwitch"])
	}
}

// 形状表：坏文件读成关并报错，不会读成开；写一次就换成好的
func TestCorruptFileReadsOffAndIsReplacedOnSet(t *testing.T) {
	cases := map[string]string{
		"不是 JSON":      "not json",
		"空文件":          "",
		"数组":           "[]",
		"null":         "null",
		"字符串当布尔":       `{"autoReceive": "yes"}`,
		"数字当布尔":        `{"autoReceive": 1}`,
		"null 当布尔":     `{"autoReceive": null}`,
		"截断的 JSON 里有开": `{"autoReceive": true`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			s := New(newDir(t))
			writeFile(t, s, content)
			on, err := s.AutoReceive()
			if on {
				t.Fatalf("坏文件被读成了开")
			}
			if err == nil {
				t.Fatalf("坏文件没有报错")
			}
			if err := s.SetAutoReceive(true); err != nil {
				t.Fatalf("用户点了开，坏文件应该被换掉：%v", err)
			}
			if on, err := s.AutoReceive(); !on || err != nil {
				t.Fatalf("换掉以后读到 %v %v", on, err)
			}
		})
	}
}

// 数据目录没初始化（没有身份证）：不写、不建 go/，也不动已有的东西
func TestUninitializedDataDirIsNeverCreated(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "没有这个目录")
	s := New(missing)
	if err := s.SetAutoReceive(true); err == nil {
		t.Fatal("数据目录不存在时写成功了")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("数据目录被建出来了：%v", err)
	}
	// 目录在、没有身份证
	bare := t.TempDir()
	if err := New(bare).SetAutoReceive(true); err == nil {
		t.Fatal("没有身份证的目录里写成功了")
	}
	if _, err := os.Stat(filepath.Join(bare, "go")); !os.IsNotExist(err) {
		t.Fatalf("go/ 被建出来了：%v", err)
	}
}

// 用户的 sumika.yml 一个字节都不动，修改时间也不变
func TestSumikaYmlUntouched(t *testing.T) {
	dir := newDir(t)
	cfg := filepath.Join(dir, "config", "sumika.yml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	const body = "# 用户手写的注释\nqq:\n  accounts:\n    - id: \"90001\"\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(cfg, old, old); err != nil {
		t.Fatal(err)
	}
	s := New(dir)
	for _, on := range []bool{true, false} {
		if err := s.SetAutoReceive(on); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(cfg)
	st, _ := os.Stat(cfg)
	if string(got) != body || !st.ModTime().Equal(old) {
		t.Fatalf("sumika.yml 被动过：%q %v", got, st.ModTime())
	}
	if s.File() == cfg {
		t.Fatal("开关文件和 sumika.yml 是同一个文件")
	}
}

// 改名失败（目标位置是个目录）：报错，不留临时文件，原来的东西还在
func TestFailedRenameLeavesNoTempFile(t *testing.T) {
	s := New(newDir(t))
	if err := os.MkdirAll(s.File(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAutoReceive(true); err == nil {
		t.Fatal("目标是目录时写成功了")
	}
	if l := leftovers(t, s); len(l) != 0 {
		t.Fatalf("失败后留下了临时文件：%v", l)
	}
}

// 托盘和命令行同时改：每次读到的都是完整的文件，没有错误
func TestConcurrentSetsStayValid(t *testing.T) {
	s := New(newDir(t))
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(on bool) {
			defer wg.Done()
			if err := s.SetAutoReceive(on); err != nil {
				t.Errorf("写：%v", err)
			}
			if _, err := s.AutoReceive(); err != nil {
				t.Errorf("读到了坏文件：%v", err)
			}
		}(i%2 == 0)
	}
	wg.Wait()
	if l := leftovers(t, s); len(l) != 0 {
		t.Fatalf("留下了临时文件：%v", l)
	}
}

// ───────────── 句柄密钥 ─────────────
// 形状表（行 = 文件的样子，列 = HandleKey 的结果）：
//   没有文件 → 生成 32 字节、写进 go/desktop-settings.json，下次（新的 Store）读到的是同一个
//   文件有别的键、没有 handleKey → 生成并保留别的键（autoReceive 不丢）
//   handleKey 不是 64 个十六进制字符（空、短、长、非十六进制、不是字符串） → 当没有，重新生成
//   文件读不懂 → 换成新的（和 SetAutoReceive 一致）
//   数据目录没有身份证 → 报错，不建 go/ 也不写文件
//   两个线程同时第一次要 → 拿到同一个
//   SetAutoReceive 之后 → 密钥不变；文件读坏被 SetAutoReceive 换掉 → 已经在用的密钥带着，不换

func TestHandleKeyIsCreatedOnceAndStays(t *testing.T) {
	dir := newDir(t)
	s := New(dir)
	k1, err := s.HandleKey()
	if err != nil || len(k1) != HandleKeyLen {
		t.Fatalf("%x %v", k1, err)
	}
	if bytes.Equal(k1, make([]byte, HandleKeyLen)) {
		t.Fatal("密钥是全零")
	}
	k2, _ := s.HandleKey()
	k3, err := New(dir).HandleKey() // 新的 Store（重启）：从文件读
	if err != nil || !bytes.Equal(k1, k2) || !bytes.Equal(k1, k3) {
		t.Fatalf("同一个数据目录的密钥应该稳定：%x %x %x %v", k1, k2, k3, err)
	}
	other, _ := New(newDir(t)).HandleKey()
	if bytes.Equal(k1, other) {
		t.Fatal("两个数据目录的密钥不该一样")
	}
	// 调用方拿到的是副本，改它不影响里面存的
	k1[0] ^= 0xff
	if k4, _ := s.HandleKey(); bytes.Equal(k1, k4) {
		t.Fatal("返回的应是副本")
	}
}

func TestHandleKeyKeepsOtherKeysAndSurvivesSetAutoReceive(t *testing.T) {
	s := New(newDir(t))
	writeFile(t, s, `{"autoReceive": true, "以后的开关": 7}`)
	k, err := s.HandleKey()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	b, _ := os.ReadFile(s.File())
	if json.Unmarshal(b, &m) != nil || string(m["autoReceive"]) != "true" || string(m["以后的开关"]) != "7" {
		t.Fatalf("别的键不能丢：%s", b)
	}
	if err := s.SetAutoReceive(false); err != nil {
		t.Fatal(err)
	}
	if k2, _ := New(s.dir).HandleKey(); !bytes.Equal(k, k2) {
		t.Fatal("改开关以后密钥变了")
	}
	if on, err := s.AutoReceive(); on || err != nil {
		t.Fatalf("%v %v", on, err)
	}
}

func TestHandleKeyRegeneratesWhenTheStoredValueIsNotAKey(t *testing.T) {
	good := strings.Repeat("ab", 32)
	for name, content := range map[string]string{
		"空串":     `{"handleKey": ""}`,
		"太短":     `{"handleKey": "abcd"}`,
		"太长":     `{"handleKey": "` + good + `00"}`,
		"不是十六进制": `{"handleKey": "` + strings.Repeat("zz", 32) + `"}`,
		"不是字符串":  `{"handleKey": 123}`,
		"null":   `{"handleKey": null}`,
		"文件读不懂":  `这不是 JSON`,
		"整个是个数组": `[1,2]`,
	} {
		s := New(newDir(t))
		writeFile(t, s, content)
		k, err := s.HandleKey()
		if err != nil || len(k) != HandleKeyLen {
			t.Errorf("%s：%x %v", name, k, err)
			continue
		}
		if k2, _ := New(s.dir).HandleKey(); !bytes.Equal(k, k2) {
			t.Errorf("%s：重新生成以后应存下来", name)
		}
	}
	// 合格的值原样用
	s := New(newDir(t))
	writeFile(t, s, `{"handleKey": "`+good+`"}`)
	if k, err := s.HandleKey(); err != nil || hex.EncodeToString(k) != good {
		t.Fatalf("合格的密钥应原样用：%x %v", k, err)
	}
}

func TestHandleKeyNeedsAnInitialisedDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "没初始化")
	if _, err := New(dir).HandleKey(); err == nil {
		t.Fatal("没有身份证的目录应该报错")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("不该建出目录：%v", err)
	}
	empty := t.TempDir() // 存在但没有身份证
	if _, err := New(empty).HandleKey(); err == nil {
		t.Fatal("没有身份证的目录应该报错")
	}
	if _, err := os.Stat(filepath.Join(empty, "go")); !os.IsNotExist(err) {
		t.Fatalf("不该建 go/：%v", err)
	}
}

func TestHandleKeyConcurrentFirstUse(t *testing.T) {
	s := New(newDir(t))
	var wg sync.WaitGroup
	keys := make([][]byte, 8)
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys[i], _ = s.HandleKey()
		}()
	}
	wg.Wait()
	for i, k := range keys {
		if len(k) != HandleKeyLen || !bytes.Equal(k, keys[0]) {
			t.Fatalf("第 %d 个拿到的和第 0 个不同：%x %x", i, k, keys[0])
		}
	}
}

// 设置文件被读坏，用户随后点了开关把它换掉：已经在用的句柄密钥带着走（不然句柄会换一茬）
func TestHandleKeySurvivesTheFileBeingReplacedAfterCorruption(t *testing.T) {
	s := New(newDir(t))
	k, err := s.HandleKey()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s, "坏了")
	if err := s.SetAutoReceive(true); err != nil {
		t.Fatal(err)
	}
	if k2, err := New(s.dir).HandleKey(); err != nil || !bytes.Equal(k, k2) {
		t.Fatalf("密钥应带过去：%x %x %v", k, k2, err)
	}
}
