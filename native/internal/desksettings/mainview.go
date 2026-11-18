package desksettings

import (
	"encoding/json"
	"fmt"
)

const (
	keyMainView       = "mainView"
	MainViewLegacy    = "legacy"
	MainViewWorkbench = "workbench"
)

// MainView 读桌面用哪套界面。没有这项、文件不存在，都是旧界面。
// 值不是 legacy / workbench 时返回旧界面和错误，调用方记一行日志，不让坏值把界面切走。
func (s *Store) MainView() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return MainViewLegacy, err
	}
	raw, ok := m[keyMainView]
	if !ok {
		return MainViewLegacy, nil
	}
	var view string
	if err := json.Unmarshal(raw, &view); err != nil {
		return MainViewLegacy, fmt.Errorf("%s 里的 %s 不是字符串", s.File(), keyMainView)
	}
	if view != MainViewLegacy && view != MainViewWorkbench {
		return MainViewLegacy, fmt.Errorf("%s 里的 %s 不是 %s 或 %s", s.File(), keyMainView, MainViewLegacy, MainViewWorkbench)
	}
	return view, nil
}

// SetMainView 只接受两个已知值。其他键原样留着。
func (s *Store) SetMainView(view string) error {
	if view != MainViewLegacy && view != MainViewWorkbench {
		return fmt.Errorf("界面只能是 %s 或 %s", MainViewLegacy, MainViewWorkbench)
	}
	return s.setKey(keyMainView, view)
}
