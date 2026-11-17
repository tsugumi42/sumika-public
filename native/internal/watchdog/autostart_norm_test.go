package watchdog

import "testing"

// 比较 Run 项和我们要写的命令时，路径写法不同但指向同一个文件的，按"开着"算：
// 大小写、\?\ 前缀、正斜杠、多余的 . 和 ..、没加引号、参数前后多空格。
// （os.Executable 加 EvalSymlinks 给出的写法和注册表里存的写法不一定逐字相同）
func TestAutostartOnNormalizesPaths(t *testing.T) {
	cases := []struct {
		name string
		val  string
		exe  string
		want bool
	}{
		{"逐字相同", watchedA, exeA, true},
		{"注册表里是长路径前缀", `"\\?\D:\x\sumika.exe" -watch`, exeA, true},
		{"我们的路径带长路径前缀", watchedA, `\\?\D:\x\sumika.exe`, true},
		{"注册表里是正斜杠", `"D:/x/sumika.exe" -watch`, exeA, true},
		{"我们的路径是正斜杠", watchedA, `D:/x/sumika.exe`, true},
		{"路径里有 . 和 ..", `"D:\x\.\y\..\sumika.exe" -watch`, exeA, true},
		{"没加引号", `D:\x\sumika.exe -watch`, exeA, true},
		{"参数前后多空格", `  "D:\x\sumika.exe"   -watch  `, exeA, true},
		{"盘符小写加大小写不同", `"d:\X\Sumika.EXE" -watch`, exeA, true},
		{"网络路径的长路径写法", `"\\?\UNC\srv\share\sumika.exe" -watch`, `\\srv\share\sumika.exe`, true},
		{"mygo 旧写法也一样认", `"D:/x/sumika.exe" --mygo-opened-at-login`, exeA, true},
		{"指向别的目录不算", `"D:\y\sumika.exe" -watch`, exeA, false},
		{"同名不同盘不算", `"E:\x\sumika.exe" -watch`, exeA, false},
		{"文件名只是前缀相同不算", `"D:\x\sumika.exe.bak" -watch`, exeA, false},
		{"多出的参数不算", `"D:\x\sumika.exe" -watch -demo`, exeA, false},
		{"参数大小写不同不算", `"D:\x\sumika.exe" -WATCH`, exeA, false},
		{"空值不算", ``, exeA, false},
		{"只有引号不算", `""`, exeA, false},
		{"引号没闭合不算", `"D:\x\sumika.exe -watch`, exeA, false},
		{"我们的路径是空的不算", `"" -watch`, ``, false},
	}
	for _, c := range cases {
		f := newFakeRun()
		if c.val != "" {
			f.vals["Sumika"] = c.val
		}
		if got := (Autostart{Store: f, Name: "Sumika", Exe: c.exe}).On(); got != c.want {
			t.Errorf("%s：Run=%q Exe=%q 得到 %v，该是 %v", c.name, c.val, c.exe, got, c.want)
		}
	}
}

// Migrate 也按同样的规范化认"指向这个 exe 的旧写法"；别的路径一概不碰
func TestMigrateNormalizesPaths(t *testing.T) {
	f := newFakeRun()
	f.vals["Sumika"] = `"d:/X/sumika.exe" --mygo-opened-at-login`
	a := Autostart{Store: f, Name: "Sumika", Exe: exeA}
	if changed, err := a.Migrate(); err != nil || !changed || f.vals["Sumika"] != watchedA {
		t.Fatalf("写法不同的旧项该被改成守护进程的写法：%v %v %q", changed, err, f.vals["Sumika"])
	}
	f.vals["Sumika"] = otherLegcy
	if changed, _ := a.Migrate(); changed || f.vals["Sumika"] != otherLegcy {
		t.Fatalf("别的路径的旧项不该碰：%q", f.vals["Sumika"])
	}
}
