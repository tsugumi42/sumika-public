package desksettings

import (
	"encoding/json"
	"fmt"
)

const keyShellPanes = "shellPanes"

// ShellPanes 是新界面外壳自己的栏位偏好。和旧界面的 panes 键分开存：旧键里的 compact、thirdOpen 是旧布局写的，
// 其中 compact=true 在新壳里一打开就是头像列，主人没选过也看到了（2026-11-08 真窗口）；新壳从自己的键读，默认展开。
type ShellPanes struct {
	ListRatio float32 `json:"listRatio"` // 会话列表宽 / 主体宽；0 = 默认公式 5/14
	Compact   bool    `json:"compact"`   // 会话列表收成头像列
	SideWidth float32 `json:"sideWidth"` // 右栏宽；0 = 默认 320，夹在 292～392
	SideOpen  bool    `json:"sideOpen"`  // 右栏开着
}

// Valid 说这份偏好是不是在范围里。
func (p ShellPanes) Valid() bool {
	return inRange(p.ListRatio, 0, 1) && (p.SideWidth == 0 || inRange(p.SideWidth, 292, 392))
}

// ShellPanes 读新壳的栏位偏好。没有这项、文件不存在是默认（展开、右栏关）；坏值整份回默认并返回错误。
func (s *Store) ShellPanes() (ShellPanes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return ShellPanes{}, err
	}
	raw, ok := m[keyShellPanes]
	if !ok {
		return ShellPanes{}, nil
	}
	var p ShellPanes
	if err := json.Unmarshal(raw, &p); err != nil {
		return ShellPanes{}, fmt.Errorf("%s 里的 %s 读不懂：%w", s.File(), keyShellPanes, err)
	}
	if !p.Valid() {
		return ShellPanes{}, fmt.Errorf("%s 里的 %s 有越界的值，按默认", s.File(), keyShellPanes)
	}
	return p, nil
}

// SetShellPanes 只接受范围内的值。其他键原样留着。
func (s *Store) SetShellPanes(p ShellPanes) error {
	if !p.Valid() {
		return fmt.Errorf("栏位偏好越界：%+v", p)
	}
	return s.setKey(keyShellPanes, p)
}
