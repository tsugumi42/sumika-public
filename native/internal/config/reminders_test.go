package config

import (
	"slices"
	"strings"
	"testing"
)

func TestRemindersDefaultsAndExample(t *testing.T) {
	d := Defaults().Reminders
	if !d.Enabled || d.MaxActive != 20 || d.MinIntervalMinutes != 60 || d.ThreadMaxActive != 3 || d.QQ || !slices.Equal(d.DefaultTargets, []string{"local"}) {
		t.Fatalf("默认值和规格第十四节不一样：%+v", d)
	}
	if bad := d.check(); len(bad) != 0 {
		t.Fatalf("默认值不该报错：%v", bad)
	}
}

func TestRemindersCheck(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(*Reminders)
		want string
	}{
		{"条数 0", func(r *Reminders) { r.MaxActive = 0 }, "maxActive"},
		{"下限 0", func(r *Reminders) { r.MinIntervalMinutes = 0 }, "minIntervalMinutes"},
		{"线里比总数多", func(r *Reminders) { r.ThreadMaxActive = 21 }, "threadMaxActive"},
		{"默认目标空", func(r *Reminders) { r.DefaultTargets = nil }, "defaultTargets"},
		{"默认目标不认识", func(r *Reminders) { r.DefaultTargets = []string{"group"} }, "defaultTargets[0]"},
		{"默认发 QQ 没开", func(r *Reminders) { r.DefaultTargets = []string{"local", "qq"} }, "reminders.qq"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := DefaultReminders()
			c.mut(&r)
			if bad := strings.Join(r.check(), "\n"); !strings.Contains(bad, c.want) {
				t.Fatalf("要提到 %q：%s", c.want, bad)
			}
		})
	}
	r := DefaultReminders()
	r.QQ, r.DefaultTargets = true, []string{"local", "qq"}
	if bad := r.check(); len(bad) != 0 {
		t.Fatalf("开了 QQ 能默认发：%v", bad)
	}
}

func TestRemindersFromYAML(t *testing.T) {
	t.Setenv("SUMIKA_CONFIG", write(t, `
reminders:
  enabled: false
  maxActive: 5
  qq: true
  defaultTargets: [qq]
`))
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := c.Reminders
	if r.Enabled || r.MaxActive != 5 || !r.QQ || !slices.Equal(r.DefaultTargets, []string{"qq"}) || r.MinIntervalMinutes != 60 {
		t.Fatalf("%+v", r)
	}
}
