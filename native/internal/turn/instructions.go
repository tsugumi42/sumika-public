package turn

import (
	"context"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

// InstructionResolver 组合内置文件工具；替换执行器也经过同一路径。
type InstructionResolver interface {
	Execute(context.Context, ContextIn, string, jsjson.Value, func(context.Context) (tools.Result, error)) (tools.Result, error)
}
