package main

// 默认界面字体文渊圆体：文件太大，不编进 exe 也不进 git，放在 exe 旁边的 fonts/ 里（native/README.md「界面字体」）。
// 文件名和先后顺序是公共字体服务（internal/assets/fonts）的；这里只管 exe 旁边那一个目录（开发用的 native/fonts 由别的入口用），启动要读得少，所以裁过的子集优先。
import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/assets/fonts"
	"sumika-native/internal/desksettings"
)

// bundledFamily 是注册时用的家族名，同时是默认外观里的字体名。
const bundledFamily = desksettings.DefaultFont

// bundledFont 是启动时加载文渊圆体的结果；Err 非空表示没加载上，界面退回系统字体。
type bundledFont struct {
	Loaded bool // 已经试过了
	Path   string
	Err    error
}

// loadBundledFont 从 exeDir/fonts 注册文渊圆体；register 在测试里换成替身。
func loadBundledFont(exeDir, family string, register func([]byte, string) error) bundledFont {
	dir := filepath.Join(exeDir, "fonts")
	for _, path := range fonts.WenYuanCandidates(fonts.Options{Dirs: []string{dir}, Subset: true}) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if err := register(data, family); err != nil {
			return bundledFont{Loaded: true, Path: path, Err: fmt.Errorf("注册 %s 失败：%w", filepath.Base(path), err)}
		}
		return bundledFont{Loaded: true, Path: path}
	}
	return bundledFont{Loaded: true, Err: fmt.Errorf("在 %s 里没找到 %s", dir, fonts.WenYuanSubset)}
}

// loadBundledFont 在启动时调一次。结果交给界面显示提示；失败只记日志，界面用系统字体。
func (s *shell) loadBundledFont() {
	exe, err := os.Executable()
	if err != nil {
		s.a.post(func() { s.a.look.bundled = bundledFont{Loaded: true, Err: err} })
		return
	}
	res := loadBundledFont(filepath.Dir(exe), bundledFamily, ui.RegisterFont)
	if res.Err != nil {
		s.logf("界面字体文渊圆体没加载，用系统字体：%v", res.Err)
	}
	s.a.post(func() { s.a.look.bundled = res })
}
