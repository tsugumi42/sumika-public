"""把文渊圆体可变字体裁成常用字子集，保留 wght 轴。

用法（fonttools 用 uv 临时装，不污染系统 Python）：
  uv run --no-project --with fonttools python -I scripts/subset-font.py <原字体.ttf> <输出.ttf> [只留这些字] [--rename 新名字]
第三个参数给了就只留 ASCII 加这几个字（做测试用的小字体用），不给就按下面的全集裁。
加 --rename 时把 name 表里的 family、full name、PostScript name、unique ID 改成新名字：
OFL 要求声明了保留字体名的字体，改动后不能再用原名（测试字体就这样生成）。name ID 0 的版权和出处保留，
只把保留字体名的列举换成指向同目录 OFL.txt。

保留的字：GB2312（6763 个常用简体字加符号）、Big5 常用繁体（5401 个）、JIS X 0208 的日文汉字和假名、
全角标点和符号、拉丁字母（含扩展）、希腊与西里尔字母。通用规范汉字表比 GB2312 多的 1300 多个字
没有单独列（仓库里没有这张表），要加就把字加进 EXTRA。
为了体积固定 ital 轴（界面用不到字体自带的斜体），只留 wght 轴。
子集里没有的字由 mygo 回退到系统字体显示。原文件不改。
"""
import sys

from fontTools import subset
from fontTools.varLib import instancer

# 额外要保留的字，放这里
EXTRA = ""


def decoded(codec, lead_range, trail_range):
    out = set()
    for a in lead_range:
        for b in trail_range:
            try:
                s = bytes([a, b]).decode(codec)
            except UnicodeDecodeError:
                continue
            if len(s) == 1:
                out.add(ord(s))
    return out


def rename_font(font, new):
    """把对外名字改成 new；子族固定 Regular，unique ID 和 PostScript 名跟着改。"""
    ps = new.replace(" ", "")
    names = {
        1: new, 4: new, 16: new, 21: new,  # family、full、typographic family、WWS family
        2: "Regular", 17: "Regular", 22: "Regular",
        3: f"1.000;SUMI;{ps}",  # unique ID
        6: ps, 25: ps,          # PostScript 名和可变字体前缀
    }
    table = font["name"]
    for rec in list(table.names):
        if rec.nameID == 0:
            # 版权声明保留作者和出处，只把保留字体名的列举换成指向同目录 OFL.txt
            value = rec.toUnicode().replace(
                "with Reserved Font Name 'WenYuan Rounded', '文源圆体'",
                "with Reserved Font Name(s) listed in the accompanying OFL.txt",
            )
            table.setName(value, 0, rec.platformID, rec.platEncID, rec.langID)
            continue
        if rec.nameID in (13, 14):  # 许可文本和地址原样保留
            continue
        value = names.get(rec.nameID)
        if value is None:
            old = rec.toUnicode()
            # fvar 各实例的 PostScript 名（name ID 256 以后）和本地化的家族名也要换
            if "WenYuanRoundedSCVF" in old:
                value = old.replace("WenYuanRoundedSCVF", ps)
            elif "WenYuan Rounded" in old:
                value = old.replace("WenYuan Rounded", new)
            elif "文渊圆体" in old:
                value = old.replace("文渊圆体", new)
        if value is not None:
            table.setName(value, rec.nameID, rec.platformID, rec.platEncID, rec.langID)


def main(src, dst, only=None, rename=None):
    chars = set(range(0x20, 0x7F)) | set(range(0xA0, 0x250))  # ASCII、Latin-1、Latin 扩展 A / B
    chars |= set(range(0x370, 0x400)) | set(range(0x400, 0x530))  # 希腊、西里尔
    chars |= set(range(0x2000, 0x2070)) | set(range(0x2190, 0x2200)) | set(range(0x2460, 0x2500))  # 标点、箭头、带圈数字
    chars |= set(range(0x3000, 0x3040)) | set(range(0x3040, 0x3100))  # 中日标点、平假名、片假名
    chars |= set(range(0xFF00, 0xFFF0))  # 全角
    chars |= decoded("gb2312", range(0xA1, 0xF8), range(0xA1, 0xFF))
    chars |= decoded("big5", range(0xA1, 0xC7), range(0x40, 0xFF))
    chars |= {ord(ch) for ch in EXTRA}
    chars |= decoded("shift_jis", range(0x81, 0xF0), range(0x40, 0xFD))
    if only is not None:
        chars = set(range(0x20, 0x7F)) | {ord(ch) for ch in only}
    options = subset.Options()
    options.layout_features = ["*"]
    options.name_IDs = ["*"]
    options.notdef_outline = True
    options.glyph_names = False
    options.hinting = False
    options.legacy_kern = True
    font = subset.load_font(src, options)
    subsetter = subset.Subsetter(options)
    subsetter.populate(unicodes=sorted(chars))
    subsetter.subset(font)  # 先裁字再固定 ital，字少了处理快得多
    font = instancer.instantiateVariableFont(font, {"ital": 0})
    if rename is not None:
        rename_font(font, rename)
    subset.save_font(font, dst, options)
    print(f"字符 {len(chars)} 个，输出 {dst}")


if __name__ == "__main__":
    args = sys.argv[1:]
    new_name = None
    if "--rename" in args:
        i = args.index("--rename")
        try:
            new_name = args[i + 1]
        except IndexError:
            sys.exit(__doc__)
        del args[i:i + 2]
    if len(args) not in (2, 3):
        sys.exit(__doc__)
    main(*args, rename=new_name)
