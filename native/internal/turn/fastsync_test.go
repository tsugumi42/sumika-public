package turn

import "sumika-native/internal/store"

// 测试库不等落盘（说明见 store.UseFastSyncForTests）
func init() { store.UseFastSyncForTests() }
