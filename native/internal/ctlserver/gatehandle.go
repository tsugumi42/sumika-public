package ctlserver

// 群号不出控制接口，群用"句柄"指（规格-本机控制接口 第六节）：
//   - gate list 的每一行带打码的会话（qq:group:***01，和日志同一口径）和句柄；
//   - gate set 认完整的 qq:group:<群号>（用户自己知道号），也认句柄；
//   - 句柄 = HMAC-SHA256(本机密钥, 完整会话号) 的前 8 个小写十六进制字符。密钥只在这台机器上（桌面程序里是设置文件的 handleKey），
//     不进任何回复和日志：群号只有十来位，没有密钥的哈希是能穷举反推的，有密钥的拿到句柄也反推不出来；同一台机器上稳定；
//   - 两个群的句柄撞了（前 8 位一样）：gate list 里两行都标 handleClash，gate set 用这个句柄回 invalid，要求写完整群号，不猜。
// 这一层是"谁能看到什么"的唯一关口：后端交上来的是完整会话号，出去之前在这里换成打码的和句柄；句柄解回会话号也只在这里，
// 解出来以后照旧走 gate set 的全部校验（档位只许 off、dry，会话只许 qq:group:<数字>），不会因为用了句柄就放宽任何一条。

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"sumika-native/internal/config"
	"sumika-native/internal/ctlproto"
)

// HandleOf 是会话 conv 的句柄
func HandleOf(key []byte, conv string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(conv))
	return hex.EncodeToString(m.Sum(nil))[:ctlproto.HandleLen]
}

// computeHandle 算句柄的函数；测试里换掉，造两个群句柄相撞的情形（真密钥下 8 个字符不好撞出来）
var computeHandle = HandleOf

// maskConv 把 qq:group:<群号> 换成 qq:group:***01（和 app.newLogf 同一个 config.MaskID）。不是这个形状的返回 false
func maskConv(conv string) (string, bool) {
	if !groupConv.MatchString(conv) {
		return "", false
	}
	return "qq:group:" + config.MaskID(conv[len("qq:group:"):]), true
}

// defaultHandleKey 没给 Options.HandleKey 时用：进程里随机一个，重启就换（测试用；桌面程序给的是存在数据目录里的稳定密钥）
func defaultHandleKey() func() ([]byte, error) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	return func() ([]byte, error) { return key, err }
}

func (s *Server) handleKey() ([]byte, error) {
	key, err := s.opt.HandleKey()
	if err != nil {
		return nil, ctlproto.Errorf(ctlproto.CodeFailed, "拿不到群句柄的密钥："+ctlproto.Clean(err.Error()))
	}
	if len(key) == 0 {
		return nil, ctlproto.Errorf(ctlproto.CodeFailed, "群句柄的密钥是空的")
	}
	return key, nil
}

// exposeGateList 后端交上来的 gate list 变成能给出去的：会话打码、加句柄、撞了的标出来、外来文字清洗。
// 会话号不是 qq:group:<数字> 的行（后端不该给）直接丢掉：既打不了码也没法用句柄指它
func (s *Server) exposeGateList(l ctlproto.GateList) (ctlproto.GateList, error) {
	key, err := s.handleKey()
	if err != nil {
		return ctlproto.GateList{}, err
	}
	count := map[string]int{}
	rows := make([]ctlproto.GateRow, 0, len(l.Groups))
	for _, g := range l.Groups {
		masked, ok := maskConv(g.Conversation)
		if !ok {
			continue
		}
		h := computeHandle(key, g.Conversation)
		count[h]++
		g.Conversation, g.Handle = masked, h
		g.Name = ctlproto.Truncate(ctlproto.Clean(g.Name), ctlproto.MaxGroupNameRunes)
		rows = append(rows, g)
	}
	for i := range rows {
		rows[i].HandleClash = count[rows[i].Handle] > 1
	}
	return ctlproto.GateList{Groups: rows}, nil
}

// exposeGateChange 同理：会话打码、加句柄
func (s *Server) exposeGateChange(c ctlproto.GateChange) (ctlproto.GateChange, error) {
	key, err := s.handleKey()
	if err != nil {
		return ctlproto.GateChange{}, err
	}
	masked, ok := maskConv(c.Conversation)
	if !ok {
		return ctlproto.GateChange{}, ctlproto.Errorf(ctlproto.CodeFailed, "后端回的会话号不是群会话，不往外给")
	}
	c.Handle, c.Conversation = computeHandle(key, c.Conversation), masked
	return c, nil
}

// resolveHandle 句柄 → 完整会话号：在后端现在认识的群里找句柄一样的。找不到、撞了都回 invalid
func (s *Server) resolveHandle(ctx context.Context, handle string) (string, error) {
	key, err := s.handleKey()
	if err != nil {
		return "", err
	}
	l, err := s.b.GateList(ctx)
	if err != nil {
		return "", err
	}
	var hit []string
	for _, g := range l.Groups {
		if groupConv.MatchString(g.Conversation) && computeHandle(key, g.Conversation) == handle {
			hit = append(hit, g.Conversation)
		}
	}
	switch len(hit) {
	case 0:
		return "", ctlproto.Errorf(ctlproto.CodeInvalid, fmt.Sprintf("没有句柄是 %s 的群：先用 gate list 看一下现在认识哪些群（句柄换过密钥或换过数据目录就不一样了）", handle))
	case 1:
		return hit[0], nil
	}
	return "", ctlproto.Errorf(ctlproto.CodeInvalid, fmt.Sprintf("句柄 %s 撞了（%d 个群的句柄一样），请改用完整的群会话身份 qq:group:<群号>", handle, len(hit)))
}
