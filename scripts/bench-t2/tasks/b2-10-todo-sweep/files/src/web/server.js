// 简单的 HTTP 服务（示例，不依赖第三方包）
const http = require("http");

const PORT = 8080; // TODO(P1) 上线前必须换掉硬编码的端口
const todoList = [];

/* TODO p2: 加上请求日志 */
const server = http.createServer((req, res) => {
  if (req.url === "/draft") {
    res.end("TODO: 页面还没做");
    return;
  }
  res.end("ok " + todoList.length);
});

server.listen(PORT);
