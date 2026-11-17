// 跟 Go 测试 dualwrite_test.go 配对：另一个进程（Bun）往 trace.db 和 memory.db 各写 N 行，和 Go 同时写。
// 打开方式照 Bun 后台的真实做法：bun:sqlite、busy_timeout 5000、写事务一开始就拿写锁（BEGIN IMMEDIATE）。
// 用法：bun dualwrite.ts <trace.db> <memory.db> <行数>
import { Database } from "bun:sqlite";

const [traceFile, memoryFile, nText] = process.argv.slice(2);
const n = Number(nText);
const trace = new Database(traceFile);
const memory = new Database(memoryFile);
for (const db of [trace, memory]) db.exec("PRAGMA busy_timeout = 5000");
const t = trace.transaction((i: number) => {
  trace.query("INSERT INTO steps(id,character_id,conversation,kind,status) VALUES(?,?,?, 'tool','done')").run(`bun-${i}`, "c1", "console:c1");
});
const m = memory.transaction((i: number) => {
  memory.query("INSERT INTO dual_rows(who,n) VALUES('bun',?)").run(i);
});
for (let i = 0; i < n; i++) {
  t.immediate(i);
  m.immediate(i);
}
trace.close();
memory.close();
console.log("done");
