"""造几本示例书。"""

SAMPLE = [
    ("Tide Atlas", "Ann", "Lee", 3),
    ("Paper Lanterns", "Bo", "Wen", 1),
]


def rows():
    # TODO(p3): 示例数据再多一点
    return list(SAMPLE)


if __name__ == "__main__":
    for r in rows():
        print(*r)
