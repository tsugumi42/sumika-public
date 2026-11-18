package store

// 指定文件入口供快照诊断和临时库使用，复用默认入口的 URI 转义、PRAGMA、迁移和升级护栏：
//   - OpenReadOnlyFile：只读打开一个已知的库文件，和 OpenReadOnly 一样的两道只读保险；
//   - OpenChatFile：在指定文件上建（没有就建）或升级一个平台聊天库，迁移和 OpenGo 用的是同一套；
//   - OpenTraceFile：在指定文件上建（没有就建）或升级一份共享运行记录库，迁移和 OpenGo 用的同一套（migrations/trace）。

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// OpenReadOnlyFile 只读打开一个已经存在的库文件（mode=ro、query_only、busy_timeout、外键、单连接）。
// 文件不存在就报错，不创建缺失的源。
func OpenReadOnlyFile(file string) (*sql.DB, error) {
	if _, err := os.Stat(file); err != nil {
		return nil, fmt.Errorf("没有 %s：%w", file, err)
	}
	return open(file, true)
}

// OpenChatFile 在指定文件上建（没有就建）或升级一个平台聊天库：通用 chat 轨和平台自己的轨在同一次护栏里，
// 判定、快照、换回和写版本表与 OpenGo 一样。快照放在库文件旁边的 upgrade-snapshots，不落进数据目录。
func OpenChatFile(file, platform string) (*sql.DB, error) {
	if !ValidPlatform(platform) {
		return nil, fmt.Errorf("平台名 %q 不能用：只认小写字母、数字、连字符，1 到 32 个字", platform)
	}
	tracks, err := platformTracks(platform)
	if err != nil {
		return nil, err
	}
	db, _, err := openGuarded(file, tracks, fileSnapshotDir(file), filepath.Base(file))
	if err != nil {
		return nil, fmt.Errorf("平台 %s：%w", platform, err)
	}
	return db, nil
}

// OpenTraceFile 在指定文件上建（没有就建）或升级一份共享运行记录库：迁移和 OpenGo 用的是同一套（migrations/trace）。
func OpenTraceFile(file string) (*sql.DB, error) {
	tracks, err := traceTracks()
	if err != nil {
		return nil, err
	}
	db, _, err := openGuarded(file, tracks, fileSnapshotDir(file), filepath.Base(file))
	return db, err
}
