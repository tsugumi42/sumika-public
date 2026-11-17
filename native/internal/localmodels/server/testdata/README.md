# server/testdata

`python-http.json`：Python 版（`tools/local-models/server.py` 的 `create_app`，令牌、正文上限、Pydantic 校验、路由里的检查都是真的，
配一个假的池）对各种请求的状态码。Go 版的 `TestWhitespaceZoo` 逐条比 `zoo`，`TestStrictBody` 逐条比 `shapes`；
Go 版比 Python 版更严的地方在 `server_test.go` 的 `stricterThanPython` 里逐条登记，多一条少一条都红。

输入全是编的。再生成（要按 `tools/local-models/requirements.txt` 用 uv 建的独立环境；
`--models-root` 只用来让 `load_specs` 核对模型文件在不在，不读内容）：

```
python -I tools/local-models/parity_ref.py http --models-root <模型根> --out native/internal/localmodels/server/testdata
```

再生成以后先看 `git diff`：FastAPI、Pydantic 的版本变了，状态码可能跟着变（`python-http.json` 开头记了各包版本）。
