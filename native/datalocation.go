package main

import (
	"os"
	"path/filepath"

	"github.com/ELY-Open-Source/CrychicUI/kit"
	"github.com/ELY-Open-Source/CrychicUI/typography"
	"github.com/egoist/mygo/ui"

	backend "sumika-native/internal/app"
	"sumika-native/internal/loadstate"
	"sumika-native/internal/loadui"
)

type dataLocationState struct {
	current backend.RootLocation
	edit    string
	err     error
	notice  string
	busy    bool
	save    func(string)
	load    loadstate.Loader // "正在定位数据…"晚点才出现；页面第一次看到根目录没到时起算
}

// initDataLocation 在后台启动前固定根；之后改文件不会使日志和后台切到另一套数据。
func (s *shell) initDataLocation() {
	if s.demo {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		s.a.post(func() { s.a.location.err = err })
		return
	}
	s.initDataLocationFrom(filepath.Dir(exe))
}

func (s *shell) initDataLocationFrom(exeDir string) {
	if s.demo {
		return
	}
	loc, err := backend.ResourceRootFrom(s.opt.Root, exeDir)
	if err == nil {
		s.opt.Root = loc.Dir
		s.host.opt.Root = loc.Dir
	}
	s.a.post(func() {
		s.a.location = dataLocationState{current: loc, edit: loc.Dir, err: err,
			save: func(text string) { go s.writeDataLocation(exeDir, text) }}
	})
}

func (s *shell) writeDataLocation(exeDir, text string) {
	if s.demo {
		return
	}
	err := backend.WriteRootFile(exeDir, text)
	s.a.post(func() {
		p := &s.a.location
		p.busy = false
		if err != nil {
			p.notice = "保存失败：" + err.Error()
			return
		}
		p.notice = "已保存，重启生效"
		if p.current.Source == "命令行 -root" {
			p.notice += "；下次启动请移除 -root"
		}
	})
}

// 根解析失败也独立显示：这时后台配置无法加载，不能依赖只读设置快照。
func (a *app) dataLocationInfo(c *ui.Context) {
	p := &a.location
	a.settingsGroup(c, "数据位置", func() {
		if p.err != nil {
			typography.Caption(c, p.err.Error()).TextColor(c.Theme().Danger).Wrap()
			return
		}
		if p.current.Dir == "" {
			if !p.load.Loading() {
				p.load.Begin()
			}
			lv := p.load.View(c.Now())
			loadui.Wake(c, lv)
			loadui.Line(lv, "正在定位数据…", func(s string) ui.Element { return kit.Muted(c, s) })
			return
		}
		ui.Text(c, "当前根目录："+p.current.Dir).Selectable().Wrap()
		kit.Muted(c, "来源："+p.current.Source)
	})
}

func (a *app) pageDataLocation(c *ui.Context) {
	a.dataLocationInfo(c)
	p := &a.location
	if a.demoMode || p.save == nil {
		return
	}
	ui.Text(c, "重启后使用的根目录").FontWeight(500)
	a.settingsField(c, "data-root", &p.edit, "根目录路径", nil)
	kit.Muted(c, "绝对或相对程序目录的路径；留空用程序目录，重启生效")
	a.settingsButton(c, "data-save", "保存数据位置", true, p.busy, func() {
		p.busy, p.notice = true, "正在保存…"
		p.save(p.edit)
	})
	if p.notice != "" {
		ui.Text(c, p.notice).Wrap()
	}
}
