// 调 sumika-dev（native/bin/sumika-dev.exe，Go 写的开发工具，规格-脚本迁Go）。程序不在或是 0 字节就先编一次：
// 和 lefthook.yml 里的一行 sh 同一个做法（先编到 .new.<pid> 再换成正式名，编到一半被打断不会留下半截程序）。
// 已经在的由程序自己核对源码摘要、改过就重编。钩子环境里不认的几个变量（不重编、防重编循环、改指别的树）不往下传。
import { existsSync, renameSync, statSync } from 'node:fs'
import { join } from 'node:path'

// SUMIKA_SCAN_DATA 能把隐私扫描的名单改指到别的目录，只给测试用：带进生产调用会读到编造的或空的名单然后放行，所以默认也删掉；SUMIKA_SCAN_MAIBOT 同理（能把 MaiBot 那份名单改指别处）
const DROP = ['SUMIKA_DEV_NO_REBUILD', 'SUMIKA_DEV_REEXEC_DONE', 'SUMIKA_DEV_ROOT', 'SUMIKA_SCAN_DATA', 'SUMIKA_SCAN_MAIBOT']

export function devExe(root: string): string {
  const exe = join(root, 'native/bin/sumika-dev.exe')
  if (existsSync(exe) && statSync(exe).size > 0) return exe
  const tmp = `${exe}.new.${process.pid}`
  const p = Bun.spawnSync(['go', 'build', '-o', tmp, './cmd/sumika-dev'], { cwd: join(root, 'native'), stdout: 'inherit', stderr: 'inherit' })
  if (p.exitCode !== 0) throw new Error(`sumika-dev 编不过（退出码 ${p.exitCode}），不放行`)
  renameSync(tmp, exe)
  return exe
}

/** 调 sumika-dev 用的环境变量：从 base 里删掉钩子环境里不认的几个变量。Windows 的环境变量名不分大小写，所以按大写比较所有键（小写的 sumika_scan_data 也删）。
 *  没有“把名单指到别处”的选项了：程序只在测试构建里认 SUMIKA_SCAN_DATA，生产构建见到它直接拒绝（红）；脚本测试要编造的名单，用 makeFakeScanRoot 造一个假仓库根。 */
export function devEnv(base: Record<string, string | undefined> = process.env): Record<string, string | undefined> {
  const env: Record<string, string | undefined> = { ...base }
  for (const k of Object.keys(env)) if (DROP.includes(k.toUpperCase())) delete env[k]
  return env
}
