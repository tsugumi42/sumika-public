package projectcontext

import (
	"context"
	"fmt"
	"sumika-native/internal/approval"
	"sumika-native/internal/turn"
)

// RootIdentity 钉住宿主放行时的真实目录和文件身份。
type RootIdentity struct {
	real string
	id   fileIdentity
}

func PinRoot(path string) (*RootIdentity, error) {
	real, err := approval.RealStrict(path)
	if err != nil {
		return nil, fmt.Errorf("无法解析根 %q：%w；恢复路径后重新加载配置", path, err)
	}
	info, id, err := identify(real)
	if err != nil {
		return nil, fmt.Errorf("无法确认根 %q 身份：%w", path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("根不是目录：%s", path)
	}
	return &RootIdentity{real, id}, nil
}

// Real 是钉住时的真实目录路径。
func (p *RootIdentity) Real() string { return p.real }

func (p *RootIdentity) Check(path string) error {
	now, err := PinRoot(path)
	if err != nil {
		return err
	}
	if !samePath(p.real, now.real) || !sameIdentity(p.id, now.id) {
		return fmt.Errorf("根 %q 的真实路径或身份已改变；恢复原目录或重新加载信任配置", path)
	}
	return nil
}

type pinnedTrust struct {
	roots  []string
	pins   []*RootIdentity
	checks []func() error
	err    error
}

// NewRootsTrust 在配置加载时记录身份，之后不跟随替换的目录。
func NewRootsTrust(roots []string) (Trust, error) {
	t := &pinnedTrust{roots: append([]string(nil), roots...)}
	for _, root := range roots {
		p, err := PinRoot(root)
		if err != nil {
			return nil, err
		}
		t.pins = append(t.pins, p)
	}
	return t, nil
}

// ConfigTrust 保留配置加载时取得的身份检查，不在装配时重新授信。
func ConfigTrust(roots []string, checks []func() error) Trust {
	t, err := NewRootsTrust(roots)
	if err != nil {
		return &pinnedTrust{err: err}
	}
	p := t.(*pinnedTrust)
	p.checks = checks
	return p
}
func (t *pinnedTrust) Trusted(ctx context.Context, w turn.Workspace) (bool, error) {
	if t.err != nil {
		return false, t.err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(t.roots) == 0 {
		return false, nil
	}
	for _, check := range t.checks {
		if err := check(); err != nil {
			return false, err
		}
	}
	// 每条根都必须可确认；不能因先遇到匹配根而略过损坏的根。
	for i, root := range t.roots {
		if err := t.pins[i].Check(root); err != nil {
			return false, err
		}
	}
	p, err := PinRoot(w.Root)
	if err != nil {
		return false, err
	}
	for _, pin := range t.pins {
		if inside(pin.real, p.real) {
			return true, nil
		}
	}
	return false, nil
}
