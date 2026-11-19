package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"sumika-native/internal/dbguard"
)

// 记忆库的迁移轨（规格-库版本与升级护栏 第 8 条）。定义放这里、不 import store：
// 角色记忆库和主人共用记忆库用 memory 轨（嵌入的 migrations，goose 默认表）；
// 旁路索引 memory-v2.db 用 memory-index 轨（只有版本号，没有迁移文件）。

// migrationFS 是护栏读的迁移文件来源。生产就是内嵌的 migrations；只有测试会换成会失败的版本（失败注入），
// 生产代码不读环境变量、不加开关。替换的测试不能 t.Parallel：并行测试都在它恢复之后才跑。
var migrationFS fs.FS = migrationsFS

// memoryTracks 是角色记忆库和主人共用记忆库的迁移轨。
// AcceptNoGoose：Bun 建的老库有基础表、没有 goose 表，按 0 版迁移，不判成认不出的库。
func memoryTracks() ([]dbguard.Track, error) {
	floor, ok := dbguard.Floors["memory"]
	if !ok {
		return nil, fmt.Errorf("迁移轨 memory 没有护栏起点号")
	}
	sub, err := fs.Sub(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("迁移轨 memory 找不到目录 migrations：%w", err)
	}
	return []dbguard.Track{{
		Name: "memory", FS: sub, Floor: floor, AcceptNoGoose: true,
	}}, nil
}

// indexTracks 是旁路索引的轨：只有版本号 1，没有 goose 表、没有迁移文件。
func indexTracks() []dbguard.Track {
	return []dbguard.Track{{
		Name: "memory-index", Version: 1, Floor: dbguard.Floors["memory-index"],
	}}
}

// inspectMemoryGuard 只读探查一个记忆库；太新时错误里写明“记忆库被较新的 Sumika 升级过”，
// 和 App 的“文件在却打不开就整个不启动”同一个规则。有待跑迁移不算错：启动时不迁移这个库。
func inspectMemoryGuard(ctx context.Context, file string, tracks []dbguard.Track) (dbguard.Decision, error) {
	dec, err := dbguard.Inspect(ctx, file, tracks)
	if err != nil {
		var u *dbguard.Unavailable
		if errors.As(err, &u) && u.Reason == dbguard.ReasonTooNew {
			return dec, fmt.Errorf("记忆库被较新的 Sumika 升级过：%w", err)
		}
		return dec, err
	}
	return dec, nil
}
