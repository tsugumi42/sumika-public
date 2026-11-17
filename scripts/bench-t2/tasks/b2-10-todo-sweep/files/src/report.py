"""报表：把库存汇总成文字。"""
from inventory import describe
from util import fmt_name


def summary(books):
    # TODO [P2] - 报表支持导出 CSV
    if not books:
        return "TODO 列表为空"
    authors = sorted({fmt_name(b["first"], b["last"]) for b in books})
    lines = [describe(b) for b in books]
    return "\n".join(lines + ["作者：" + "；".join(authors)])
