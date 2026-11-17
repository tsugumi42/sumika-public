"""小工具函数。"""


def fmt_name(first, last):
    # TODO(p3) [easy]: 把 fmt_name 改名成 format_name，所有调用的地方一起改
    return f"{last}, {first}"


def clamp(value, low, high):
    # todo: p3 负数的情况还没处理
    return max(low, min(high, value))
