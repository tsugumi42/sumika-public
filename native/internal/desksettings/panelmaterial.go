package desksettings

import (
	"encoding/json"
	"fmt"
)

// 面板材质：新界面里浮在聊天内容上的控制层（标题栏、输入框）用什么材质（设计语言第二节第 2 层）。
const (
	keyPanelMaterial = "panelMaterial"
	// MaterialLiquid 液态玻璃（默认，照原型）；MaterialBlur 磨砂（背景模糊加半透明底，照 Bun 版）；MaterialSolid 实色。
	MaterialLiquid = "liquid"
	MaterialBlur   = "blur"
	MaterialSolid  = "solid"
)

func validMaterial(v string) bool {
	return v == MaterialLiquid || v == MaterialBlur || v == MaterialSolid
}

// PanelMaterial 读面板材质。没有这项、文件不存在，都是液态玻璃；坏值回到液态玻璃并返回错误，调用方记一行日志。
func (s *Store) PanelMaterial() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return MaterialLiquid, err
	}
	raw, ok := m[keyPanelMaterial]
	if !ok {
		return MaterialLiquid, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return MaterialLiquid, fmt.Errorf("%s 里的 %s 不是字符串", s.File(), keyPanelMaterial)
	}
	if !validMaterial(v) {
		return MaterialLiquid, fmt.Errorf("%s 里的 %s 不是 %s、%s 或 %s", s.File(), keyPanelMaterial, MaterialLiquid, MaterialBlur, MaterialSolid)
	}
	return v, nil
}

// SetPanelMaterial 只接受三个已知值。其他键原样留着。
func (s *Store) SetPanelMaterial(v string) error {
	if !validMaterial(v) {
		return fmt.Errorf("面板材质只能是 %s、%s 或 %s", MaterialLiquid, MaterialBlur, MaterialSolid)
	}
	return s.setKey(keyPanelMaterial, v)
}
