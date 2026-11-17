"""库存：增加书目、描述一本书。"""
from config import load
from util import clamp, fmt_name

todo_items = []  # 记录还没上架的书（变量名里有 todo，不是注释）


def add(items, title, first, last, copies):
    # TODO(p1): 并发修改时要加锁
    items.append({"title": title, "first": first, "last": last, "copies": copies})
    return items


def describe(book):
    # FIXME(p1): 库存为负时显示不对
    count = clamp(book["copies"], 0, load()["max_items"])
    return f"{book['title']} / {fmt_name(book['first'], book['last'])} x{count}"
