// 把一个任务交给本机的 DeepSeek Harness（dsh 桌面端自带的命令行，headless 模式：回答一个任务、结果打到标准输出、退出）。
// 用法：bun scripts/dsh-ask.ts <任务文件> [--cwd <工作目录>] [--out <结果文件>] [--write]
//   默认只读：用 scripts/dsh-readonly.yml 把权限改成 read-only（写文件被沙箱拦下），2026-10-21 在临时目录里试过：读文件放行、写文件拒绝
//   --write：不加只读补丁，用 headless 自带的权限（workspace-write：能在工作目录里写文件、跑命令，不问）；只在临时目录里用
// 模型走 Magpie（~/.dsh/profiles/headless 里主人配好的提供商，auto/deepseek-v4.1-flash），key 用本机 Magpie 的占位 magpie-local（不是真密钥）
// 任务说明从文件重定向进去（别用管道，中文会变问号）；工作目录就是它的"工作区"，只读时它也读不到工作目录以外的东西
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { parseArgs } from 'node:util'

const { values: o, positionals } = parseArgs({ allowPositionals: true, options: { cwd: { type: 'string' }, out: { type: 'string' }, write: { type: 'boolean' } } })
const task = positionals[0]
if (!task || !existsSync(task)) { console.error('用法：bun scripts/dsh-ask.ts <任务文件> [--cwd 工作目录] [--out 结果文件] [--write]'); process.exit(2) }
const DSH = process.env.DSH_CMD ?? ''
if (!DSH) { console.error('设环境变量 DSH_CMD 指向 dsh.cmd（DeepSeek Harness 自带的命令行）'); process.exit(2) }
if (!existsSync(DSH)) { console.error(`找不到 dsh：${DSH}（装在别处的话设环境变量 DSH_CMD）`); process.exit(2) }
const cwd = resolve(o.cwd ?? process.cwd())
const out = resolve(o.out ?? `${task}.out.md`)
mkdirSync(dirname(out), { recursive: true })
const args = ['--profile', 'headless', ...(o.write ? [] : ['--patch', resolve(import.meta.dir, 'dsh-readonly.yml')]), '-']
console.error(`dsh ${args.join(' ')}\n  工作目录 ${cwd}\n  任务 ${resolve(task)}\n  结果 ${out}\n  权限 ${o.write ? 'workspace-write（能写）' : 'read-only'}`)

const t0 = Date.now()
const p = Bun.spawn(['cmd', '/c', DSH, ...args], {
  cwd, env: { ...process.env, MAGPIE_GATEWAY_KEY: process.env.MAGPIE_GATEWAY_KEY ?? 'magpie-local' },
  stdin: Bun.file(resolve(task)), stdout: 'pipe', stderr: 'pipe', windowsHide: true,
})
const [stdout, stderr] = await Promise.all([new Response(p.stdout).text(), new Response(p.stderr).text()])
const code = await p.exited
writeFileSync(out, stdout)
writeFileSync(out.replace(/\.md$/, '') + '.log.txt', stderr)
console.error(`退出码 ${code}，用时 ${Math.round((Date.now() - t0) / 1000)} 秒，结果 ${[...stdout].length} 字 → ${out}`)
if (!stdout.trim()) console.error('结果是空的，看 .log.txt：' + readFileSync(out.replace(/\.md$/, '') + '.log.txt', 'utf8').slice(0, 400))
process.exit(code)
