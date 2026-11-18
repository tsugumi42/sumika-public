package jsstr

import "testing"

// 和 Bun 的 fileURLToPath(…, { windows: true }) 跑出来的一样（2026-10-25 在本机跑的）
func TestFileURLToPath(t *testing.T) {
	cases := [][2]string{
		{"file:///C:/x/a.md", `C:\x\a.md`},
		{"file:///c:/a%20b/c.txt", `c:\a b\c.txt`},
		{"file://server/share/a.txt", `\\server\share\a.txt`},
		{"file://localhost/C:/x", `C:\x`},
		{"file:///C:/a%2Fb", `ERR File URL path must not include encoded \ or / characters`},
		{"file:///x/y", "ERR File URL path must be absolute"},
		{"file:///C:/中文/a.md", `C:\中文\a.md`},
		{"file:///C:/a%E4%B8%AD.md", `C:\a中.md`},
		{"file:///D:", "D:"},
	}
	for _, c := range cases {
		got, err := FileURLToPath(c[0])
		if err != nil {
			got = "ERR " + err.Error()
		}
		if got != c[1] {
			t.Errorf("%s：Go %q，Node %q", c[0], got, c[1])
		}
	}
}
