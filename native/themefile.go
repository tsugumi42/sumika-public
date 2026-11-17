package main

// 文件与对话框属于宿主；主题编辑页只交出快照与动作，不在构建期间读写磁盘。
import (
	"errors"
	"os"

	"github.com/egoist/mygo"
	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/desksettings"
)

func (s *shell) loadAppearance(st *desksettings.Store) {
	s.loadBundledFont()
	settings, err := st.Appearance()
	light, night, compileErr := settings.Compile()
	if compileErr != nil {
		panic(compileErr)
	} // Store 已校验，默认预设也必须可编译。
	upgraded := errors.Is(err, desksettings.ErrThemeUpgraded) || errors.Is(err, desksettings.ErrTextReset)
	if err != nil {
		s.logf("读主题失败，使用默认：%v", err)
	}
	if upgraded {
		// 旧格式只提醒这一次：换好的（默认配色 + 原来的模式）写回去，下次启动就不再报
		if werr := st.SetAppearance(settings); werr != nil {
			s.logf("存主题失败：%v", werr)
		}
	}
	s.a.post(func() {
		a := s.a
		// 启动读取晚到时，用户已经改过的外观不能被旧快照覆盖。
		if a.look.revision == 0 {
			a.look.settings, a.look.light, a.look.night, a.look.current = settings, light, night, nil
			if err != nil {
				a.look.notice = "读主题失败，已用默认：" + err.Error()
			}
		}
		a.look.save = func(v desksettings.Appearance, revision uint64) { go s.writeAppearance(st, v, revision) }
		if a.look.revision > 0 {
			a.look.save(a.look.settings, a.look.revision)
		}
	})
}

func (s *shell) writeAppearance(st *desksettings.Store, v desksettings.Appearance, revision uint64) {
	s.themeMu.Lock()
	defer s.themeMu.Unlock()
	if revision <= s.themeWritten {
		return
	}
	if err := st.SetAppearance(v); err != nil {
		s.logf("存主题失败：%v", err)
		s.a.post(func() {
			if s.a.look.revision == revision {
				s.a.look.notice = "主题已应用，但没有存上：" + err.Error()
			}
		})
		return
	}
	s.themeWritten = revision
}

var themeFilters = []mygo.FileFilter{{Name: "MewTypeUI 主题 JSON", Extensions: []string{"json"}}}

func readTheme(file string) (theme.Definition, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return theme.Definition{}, err
	}
	return theme.Import(b)
}

func writeTheme(file string, d theme.Definition) error {
	b, err := theme.Export(d)
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(b, '\n'), 0o644)
}

// chooseTheme 的 bool 为真表示导入；对话框取消时不改主题，也不写文件。
func (s *shell) chooseTheme(importing bool, d theme.Definition) {
	a := s.a
	a.look.busy = true
	window := a.win
	go func() {
		var err error
		var imported theme.Definition
		canceled := false
		if importing {
			var files []string
			files, err = mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: window, Title: "导入主题", Filters: themeFilters})
			if err == nil {
				canceled = len(files) == 0
				if !canceled {
					imported, err = readTheme(files[0])
				}
			}
		} else {
			var file string
			file, err = mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: window, Title: "导出主题", DefaultPath: "sumika-theme.json", Filters: themeFilters})
			if err == nil {
				canceled = file == ""
				if !canceled {
					err = writeTheme(file, d)
				}
			}
		}
		if a.base().Err() != nil {
			return
		}
		a.post(func() {
			a.look.busy = false
			if canceled {
				return
			}
			if err == nil && importing {
				err = a.importLook(imported)
			}
			if err != nil {
				a.look.notice = "主题文件操作失败：" + err.Error()
				return
			}
			if !importing {
				a.look.notice = "主题已导出"
			}
		})
	}()
}
