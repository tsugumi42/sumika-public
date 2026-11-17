// Package sharedtest 读写仓库根 testdata/shared/ 下的共用场景（Go 后台和 Bun 后台验收同一份数据）。
//
// 只给测试用：Root 往上找仓库根；Load 读 JSON；SUMIKA_SHARED_UPDATE=1 时 Save 重新生成文件。
// 生成结果确定：字段顺序跟结构体，缩进两空格，行尾 LF，末尾一个换行，重跑不变。
package sharedtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Root 返回 testdata/shared 的绝对路径；从调用测试的工作目录往上找。
func Root(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		p := filepath.Join(dir, "testdata", "shared")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		up := filepath.Dir(dir)
		if up == dir {
			t.Fatal("找不到 testdata/shared")
		}
		dir = up
	}
}

// Update 为真时，测试把实际结果写回场景文件的 expect，而不是比对。
func Update() bool { return os.Getenv("SUMIKA_SHARED_UPDATE") == "1" }

// Files 列出 testdata/shared/<dir> 下的 .json 场景，按文件名排序。
func Files(t testing.TB, dir string) []string {
	t.Helper()
	root := filepath.Join(Root(t), filepath.FromSlash(dir))
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatalf("%s 下没有场景", dir)
	}
	return out
}

// Load 读 JSON 到 v；字段不认识就报错，免得拼错的字段悄悄不起作用。
func Load(t testing.TB, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s：%v", filepath.Base(path), err)
	}
}

// Marshal 是确定的输出格式。
func Marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(fmt.Sprint("sharedtest.Marshal：", err))
	}
	return buf.Bytes()
}

// Save 把 v 写到 path（只在 Update 时调用）。
func Save(t testing.TB, path string, v any) {
	t.Helper()
	if err := os.WriteFile(path, Marshal(v), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Same 比较两个值编码后是否逐字相同；不同时返回两份缩进文本方便看差异。
func Same(want, got any) (bool, string, string) {
	w, g := Marshal(want), Marshal(got)
	return bytes.Equal(w, g), string(w), string(g)
}
