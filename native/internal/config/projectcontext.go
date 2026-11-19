package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
	"os"
	"sumika-native/internal/approval"
)

// ProjectContext 默认开启，但不信任的目录什么说明文件都不读；信任来自本机：配置里手写的 trustedRoots，
// 或者主人在审批卡上答过的“记住”（规格-审批分级 8.5）。
type ProjectContext struct {
	Enabled      bool           `yaml:"enabled"`
	TrustedRoots []string       `yaml:"trustedRoots"`
	UserContext  bool           `yaml:"userContext"`
	RootChecks   []func() error `yaml:"-"`
}

func (p *ProjectContext) UnmarshalYAML(b []byte) error {
	type plain ProjectContext
	value := plain{Enabled: true} // 块里不写 enabled 也是开；写 false 才关
	if err := yaml.UnmarshalWithOptions(b, &value, yaml.Strict()); err != nil {
		return fmt.Errorf("projectContext：%s", err)
	}
	for i, root := range value.TrustedRoots {
		if !filepath.IsAbs(root) || strings.TrimSpace(root) == "" {
			return fmt.Errorf("projectContext.trustedRoots[%d]：必须是绝对真实路径", i)
		}
		real, err := approval.RealStrict(root)
		if err != nil {
			return fmt.Errorf("projectContext.trustedRoots[%d]：无法确认真实路径：%w", i, err)
		}
		value.TrustedRoots[i] = real
	}
	for _, root := range value.TrustedRoots {
		info, err := os.Stat(root)
		if err != nil {
			return fmt.Errorf("projectContext.trustedRoots：%w", err)
		}
		value.RootChecks = append(value.RootChecks, func() error {
			real, err := approval.RealStrict(root)
			if err != nil {
				return fmt.Errorf("信任根 %q 无法解析：%w；恢复目录后重新加载配置", root, err)
			}
			now, err := os.Stat(real)
			if err != nil {
				return fmt.Errorf("信任根 %q 无法确认身份：%w", root, err)
			}
			if real != root || !os.SameFile(info, now) {
				return fmt.Errorf("信任根 %q 身份已改变；恢复原目录或重新加载配置", root)
			}
			return nil
		})
	}
	*p = ProjectContext(value)
	return nil
}
