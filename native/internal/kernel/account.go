package kernel

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// AccountRequired 说的是：这个身份的场合有没有"接收账号"。
// 只有本地主窗口（平台 local 且渠道 console，主人是内核身份）没有平台账号；其余场合一律有，
// 不看平台名——新平台没登记也按"要有账号"办，缺了就拒（不认识就拒绝）。
// 平台和渠道两个字段一起认：只认其中一个，{local, qq} 这种拼出来的身份就会被放过。
func (s Scope) AccountRequired() bool {
	return s.Platform != "local" || s.Channel != "console"
}

// ignorableInID 说的是：归一时整个丢掉的字符。按 Unicode 的类别整类处理，不逐个列“看起来一样”的字符：
// 空白、控制字符、格式字符（Cf，含零宽连接符）、Default_Ignorable_Code_Point（变体选择符、组合用连字符 CGJ、
// 韩文填充等由 Other_Default_Ignorable_Code_Point 和 Variation_Selector 两张表覆盖），
// 另加盲文空白 U+2800（看起来是空白，却是符号 So，不在上面任何一类里）。
func ignorableInID(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Variation_Selector, r) || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || r == 0x2800
}

// canonStep 是一遍归一：NFKC（全角数字等兼容字符）、丢掉 ignorableInID、转小写。
func canonStep(id string) string {
	return strings.Map(func(r rune) rune {
		if ignorableInID(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, norm.NFKC.String(id))
}

// canonID 把号、发言人 id 归成同一个比较形。一遍不够：丢掉字符以后，原来隔开的字符会碰到一起，
// 又能组合（"e‍́" 丢掉零宽连接符变成 "e"+U+0301，再 NFKC 才是 "é"），所以循环到结果不再变（幂等：归一两遍等于一遍）。
// 每一遍只会丢字符或做 NFKC、小写，实际两三遍就稳；上限 16 遍只是防御，到不了。
func canonID(id string) string {
	for range 16 {
		next := canonStep(id)
		if next == id {
			break
		}
		id = next
	}
	return id
}

// CanonID 在 canonID 之上，再反复剥掉 "渠道:" 或 "平台:" 前缀：发言人 id 的写法是 "渠道:号"（channel.Roster.Resolve），
// 账号也可能带前缀写。只用来比较，不改写存下来的 Scope。
func (s Scope) CanonID(id string) string {
	id = canonID(id)
	prefixes := [...]string{canonID(s.Channel), canonID(s.Platform)}
	for {
		cut := false
		for _, p := range prefixes {
			if p == "" {
				continue
			}
			if rest, ok := strings.CutPrefix(id, p+":"); ok {
				id, cut = canonID(rest), true // 剥掉前缀后剩下的再归一一遍，保持幂等
			}
		}
		if !cut {
			return id
		}
	}
}

// ActorIsAccount：发言人就是接收账号自己（裸号、带前缀、大小写或空白变体都算）。不看渠道：有账号就比，没账号（空）不算。
// 先比原样：账号和发言人原样相同就算，哪怕归一后是空（同一串空白、只有前缀）——旧代码的裸比较拒这种，换成归一比较不能放过它。
// 门口不比对"没有填账号"这件事；该有账号的场合缺账号由 AccountMissing 管。
func (s Scope) ActorIsAccount() bool {
	if s.AccountID != "" && s.ActorID == s.AccountID {
		return true
	}
	acct := s.CanonID(s.AccountID)
	return acct != "" && s.CanonID(s.ActorID) == acct
}

// AccountMissing：该有接收账号的场合（AccountRequired）却没有账号（空或只有空白、不可见字符）。
func (s Scope) AccountMissing() bool {
	return s.AccountRequired() && s.CanonID(s.AccountID) == ""
}
