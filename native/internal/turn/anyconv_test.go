package turn

import "sumika-native/internal/store"

// anyConv 把一个库包成 store.Stores、不看会话串属于哪个平台：这个包的测试里会话串是随手起的（group:queue、private:x 这类），
// 不是登记过的渠道，store.Single 会照规矩报错。选对库（本地、QQ 各进各的库，没登记的报错）的验收在 split_test.go 用真开的两个平台库测
type anyConv struct{ st *store.Store }

func (a anyConv) For(string) (*store.Store, error)             { return a.st, nil }
func (a anyConv) ForConversation(string) (*store.Store, error) { return a.st, nil }
func (a anyConv) Each() []store.Named                          { return []store.Named{{Platform: "test", Store: a.st}} }
func (a anyConv) TraceStore() *store.Store                     { return a.st }
