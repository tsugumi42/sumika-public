package desksettings

// 栏位偏好与几何解耦；旧字段只在读取边界转换，保存时只写 MewTypeUI 的偏好。
import (
	"encoding/json"
	"fmt"
	"math"
)

const keyPanes = "panes"

type Panes struct {
	ListRatio  float32 `json:"listRatio"`
	Compact    bool    `json:"compact"`
	ThirdWidth float32 `json:"thirdWidth"`
	ThirdOpen  bool    `json:"thirdOpen"`
}

func DefaultPanes() Panes { return Panes{} }

func (p Panes) Clamp() Panes {
	p.ListRatio = clampWidth(p.ListRatio, 0, 1, 0)
	if p.ThirdWidth != 0 {
		p.ThirdWidth = clampWidth(p.ThirdWidth, 292, 392, 0)
	}
	return p
}

func clampWidth(v, lo, hi, def float32) float32 {
	if f := float64(v); math.IsNaN(f) || math.IsInf(f, 0) {
		return def
	}
	return min(max(v, lo), hi)
}

func (s *Store) Panes() (Panes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	def := DefaultPanes()
	m, err := s.load()
	if err != nil {
		return def, err
	}
	raw, ok := m[keyPanes]
	if !ok {
		return def, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return def, fmt.Errorf("%s 里的 %s 不是对象", s.File(), keyPanes)
	}
	p := def
	var bad []string
	if _, ok := fields["listRatio"]; ok {
		if v := field[float32](fields, "listRatio", &bad); v != nil {
			p.ListRatio = *v
		}
	} else if v := field[float32](fields, "listWidth", &bad); v != nil && *v != 280 {
		// 旧窗口固定初始宽1640；只迁用户自选宽度，280的旧默认不覆盖库的默认比例。
		p.ListRatio = *v / (1640 - 72)
	}
	if _, ok := fields["compact"]; ok {
		if v := field[bool](fields, "compact", &bad); v != nil {
			p.Compact = *v
		}
	} else if v := field[bool](fields, "listIcons", &bad); v != nil {
		p.Compact = *v
	}
	if _, ok := fields["thirdWidth"]; ok {
		if v := field[float32](fields, "thirdWidth", &bad); v != nil {
			p.ThirdWidth = *v
		}
	} else if v := field[float32](fields, "sideWidth", &bad); v != nil && *v != 320 {
		p.ThirdWidth = *v
	}
	if _, ok := fields["thirdOpen"]; ok {
		if v := field[bool](fields, "thirdOpen", &bad); v != nil {
			p.ThirdOpen = *v
		}
	} else if v := field[bool](fields, "sideOpen", &bad); v != nil {
		p.ThirdOpen = *v
	}
	p = p.Clamp()
	if len(bad) > 0 {
		return p, fmt.Errorf("%s 里的 %s 有读不懂的字段（%v），这几项按默认值", s.File(), keyPanes, bad)
	}
	return p, nil
}

// field 只在文件边界校验缺失、null和类型；调用方保留有效字段并报告读不懂的字段。
func field[T any](fields map[string]json.RawMessage, name string, bad *[]string) *T {
	raw, ok := fields[name]
	if !ok {
		return nil
	}
	var v *T
	if err := json.Unmarshal(raw, &v); err != nil || v == nil {
		*bad = append(*bad, name)
		return nil
	}
	return v
}

func (s *Store) SetPanes(p Panes) error { return s.setKey(keyPanes, p.Clamp()) }
