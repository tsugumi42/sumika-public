// Package pool 是本机模型池（规格-Go本地模型 GM4）：登记表 id → 已加载的模型。第一次请求才加载，空闲卸载，
// 同时最多装 K 个，内存有个软上限；口径照 tools/local-models/server.py 的 ModelPool，差异写在规格里。
//
// 池只管记账，不碰文件也不碰 ORT：怎么加载、怎么估计占多少字节，都由调用方传进来（Config.Load、Config.Estimate；
// 真的那一份是同包的 FileLoader），测试用假的加假时钟。三条贯穿全包的规矩：
//   - 池的互斥锁只保护记账，不跨加载、不跨卸载、不跨 Run。/health 读 Snapshot，一份拷贝，不等任何长操作
//     （Local 对已就绪的进程每 30 秒 ping 一次、超时 3 秒、失败一次就杀进程，池锁挡住 /health 就会误杀健康的进程）；
//   - 加载在池自己的 goroutine 里做，不绑定任何一个请求的 ctx：第一个请求者走了，等同一次加载的别人不能跟着失败，
//     加载完的模型留在池里，由空闲卸载处理；
//   - 卸载（Session.Close）一定在没有租约的时候、在池锁外做；卸载期间它占的名额和字节仍然记着，
//     同一个 id 的新请求等它关完再加载，不会同一份模型同时装两份。
//
// 这个包和它的依赖里不许有 net、net/http、os/exec（只有 server 包可以 net/http，规格 4.2）。
package pool
