"""命令行入口：python src/app.py 书名 名 姓 册数"""
import sys

from config import load
from inventory import add, describe
from report import summary
from util import fmt_name


# TODO(p2): 命令行参数改用 argparse
def main(argv):
    title, first, last, copies = argv[1], argv[2], argv[3], int(argv[4])
    books = add([], title, first, last, copies)
    print(describe(books[0]))
    print("作者署名：" + fmt_name(first, last))
    print(summary(books))
    print("超时：", load()["timeout"])


if __name__ == "__main__":
    main(sys.argv)
