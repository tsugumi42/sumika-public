package store

import "testing"

func TestInTestBinary(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		`C:\Temp\app.test.exe`: true,
		"/tmp/app.test":        true,
		`D:\x\sumika.exe`:      false,
		`D:\x\sumika-ctl.exe`:  false,
		"sumika-go":            false,
		`D:\latest.exe`:        false,
	} {
		if got := inTestBinary(name); got != want {
			t.Errorf("%s：%v，应为 %v", name, got, want)
		}
	}
}
