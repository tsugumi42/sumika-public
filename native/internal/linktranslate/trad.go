package linktranslate

import "strings"

// 繁体中文和日文汉字的判断（用户 2026-11-13：不是简体中文就只放译文；2026-11-14：纯汉字的日文别标成“翻译自繁体中文”）。
// 不引入转换库，用三张字表，两两不相交（trad_test.go 的 TestScriptTablesDisjoint 查）：
//
//   - traditionalOnly：只在繁体里出现、日文不用的常用字。表里的字都有和它不同的简体写法，简体文字里不会出现，日文新字体也不写成这样
//     （日文写“経 験 発 売”这类新字体）。只有它能证明“是繁体”。
//   - sharedWithJapanese：繁体里极常用、但日文也照样这么写的字（常用漢字表里与繁体同形的 個 為 後 還 過 開 時 裏 隻，加上人名里常见的 龍 邊 齊 廣）。
//     简体里不会出现，所以能证明“不是简体”；但分不出是繁体还是日文，所以不能单独证明“是繁体”：只帮繁体凑占比，并用来决定要不要翻（和以前一样放行）。
//   - japaneseOnly：日文新字体里和繁体、简体都不同形的字（駅 発 売 読 気 経……）和日本自造的和制汉字（込 働 畑 峠……）。
//     出现了就是日文特征；表里不收和简体同形的新字体（国 学 会 体 点 来 党 双……），否则简体会被判成日语。
//
// 判断（不含假名、谚文的文字；有假名的一律先按日语）：
//   - 日文：japaneseOnly 里的字加上日式词（japaneseWords）覆盖的汉字，至少 1 个且至少占汉字总数的 10%（JaMinRatio）。
//   - 繁体：traditionalOnly 里的字至少 2 个（TradMinChars，只有它们能证明是繁体），加上 sharedWithJapanese 的字至少占汉字总数的 10%（TradMinRatio），并且不是日文。
//     共用字只帮着凑够占比：它们在繁体行文里很常用（個 為 後 還 過 開 時 约占汉字的 8%），不算进去，一句台湾口语里 traditionalOnly 只占 10% 上下，会有一半认不出。
//     依据：台港的繁体行文里两张表合起来约占汉字的 20%～30%，简体行文是 0；取 10% 一方面让短句（4～10 个汉字）里有两三个繁体字就能认出，
//     另一方面让简体文字引用一个繁体词（两三个字，汉字总数过二十）不会误判（简体里没有共用字，占比只靠 traditionalOnly 那两三个字）。
//   - 要不要翻（Needs）：繁体字和共用字加起来够 2 个、占 10%，也放行（以前一张表的判断，翻译照常）：
//     无假名的日文（“個人開催”）不是简体，不能因为拆了表就当成“已是简体”不翻。
const (
	TradMinChars = 2
	TradMinRatio = 10 // 百分比：trad*100 >= han*TradMinRatio
	JaMinRatio   = 10 // 百分比：日文特征汉字 *100 >= han*JaMinRatio
)

const traditionalOnly = "們這說從來國學會對點與關體麼驕聲" +
	"實發經當讓樣裡嗎專腦聯繫條價錢賣歡讀寫畫歲歷區圖團處總續觀觸覺證變戰爭豐顯驗髮鬥鬧麵黃齡龜" +
	"隨險靈應懷惡戲戶擇擔據數斷樂檢歸氣沒溫滿灣營獨產盡眾禮稱絕縣繼聽腳臺舉號裝譯貓輕轉辦遲鄉醫釋鐵錄閱雖雙雜" +
	"傳兒內兩卻參嚴單圍壞寶擴讚顏鬆黨妳"

// sharedWithJapanese 是日文也这么写的繁体字（日文常用漢字表里与繁体同形的字，加上人名里常见的）。
// 日文新字体只改了一部分字，言字旁（語 話 請 認 識 課 論 議 記 許 設 計 詞 試 謝）、門字框（問 開 間 聞）、車 電 馬 鳥 魚 這类繁体字日文原样在用，
// 它们只在简体里有不同的写法（语 话 请……），所以简体文字里不会出现；表里每个字都核过有不同的简体写法。
const sharedWithJapanese = "個為後還過開時裏隻龍邊齊廣" +
	"問題謝試紙語話請認識課論議記許設計詞詳誤務買進選決電車門書長見馬鳥魚優驚鮮師現難準離聞間場動運連達遠週衛"

// japaneseOnly 是日文新字体（与简体、繁体都不同形）和和制汉字。
const japaneseOnly = "駅発売読気経実変戦続歴図団処総観覚証験険応悪択拠楽検帰満営産絶県継聴挙訳軽転辺郷釈鉄録閲雑伝児両厳単囲壊拡顔関説対広歓歳価銭" +
	"乗栄浜沢竜亜恵渋歯齢蔵暦剣労戸" +
	"込働畑峠辻凪枠匂雫噂凧鰯笹栃丼"

// japaneseWords 是只有日文才这么用的词（纯汉字的日文最常见的“公告体”：发售、开催、受付……）。
// 不放中文里也常用的词（本日、公開、決定、募集、配信）。词里的字有和简体不同的新字体时本来就在 japaneseOnly 里，这里补的是和繁体同形的那些词。
var japaneseWords = []string{"開催", "受付", "締切", "先着", "完売", "入荷", "在庫", "通販", "無料", "抽選", "委託", "販売", "予約", "御礼", "頒布", "既刊", "新刊", "宜しく"}

// isTraditional 说这段文字是不是繁体中文：繁体独有字至少 2 个、与共用字合计占比够，且不是日文。有假名、谚文的文字不走这里（调用方先判断日语、韩语）。
func (c scriptCount) isTraditional() bool {
	return c.trad >= TradMinChars && (c.trad+c.shared)*100 >= c.han*TradMinRatio && !c.hanJapanese()
}

// notSimplified 说汉字里“简体里不会有”的字（繁体独有加共用）够多：无假名的日文和繁体都算。Needs 用它决定要不要翻。
func (c scriptCount) notSimplified() bool {
	n := c.trad + c.shared
	return n >= TradMinChars && n*100 >= c.han*TradMinRatio
}

// hanJapanese 说没有假名的汉字文字里日文特征够不够：新字体、和制汉字、日式词。
func (c scriptCount) hanJapanese() bool {
	return c.han*2 > c.total && c.jaHan >= 1 && c.jaHan*100 >= c.han*JaMinRatio // 汉字要占大多数：英文、韩文里夹一个“駅”不算
}

// jaWordChars 数日式词覆盖的汉字里，不在 japaneseOnly 里的那些（在里面的已经单独数过了）。
func jaWordChars(text string) int {
	n := 0
	for _, w := range japaneseWords {
		k := strings.Count(text, w)
		if k == 0 {
			continue
		}
		extra := 0
		for _, r := range w {
			if !strings.ContainsRune(japaneseOnly, r) {
				extra++
			}
		}
		n += k * extra
	}
	return n
}

// wantsSimplified 说目标语言是不是简体中文：目标写了“繁”或繁体的语言标签就不是（繁体目标本来没有“简体要不要翻”的问题，保持原行为）。
func wantsSimplified(target string) bool {
	t := strings.ToLower(target)
	for _, k := range []string{"繁", "traditional", "zh-tw", "zh_tw", "zh-hk", "zh_hk", "zh-hant", "zh_hant"} {
		if strings.Contains(t, k) {
			return false
		}
	}
	return true
}
