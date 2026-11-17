"""配置：读环境变量，给出默认值。"""
import os

DEFAULT_TIMEOUT = 30  # TODO(p2) [easy]: 默认超时改成 60 秒
MAX_ITEMS = 500
DB_PATH = os.environ.get("SHELF_DB", "shelf.db")


# TODO [P1] - 支持从配置文件读取，不要只靠环境变量
def load():
    return {"timeout": DEFAULT_TIMEOUT, "max_items": MAX_ITEMS, "db": DB_PATH}
