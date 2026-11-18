// 把社区版 Twemoji（jdecked/twemoji，图形 CC-BY 4.0）的 SVG 下载到 native/twemoji/：软件自带的表情图（ADR-150）。
// 正式程序从程序旁的 twemoji/ 读，开发时从 native/twemoji/ 读（native/internal/assets），目录不进 git，打包时整个拷到 exe 旁。
//
//   bun scripts/update-twemoji.ts                      干跑：只打印会下载哪个版本、放到哪，不联网下载、不写任何东西
//   bun scripts/update-twemoji.ts --yes                取最新发布，换掉 native/twemoji/
//   bun scripts/update-twemoji.ts --yes --version v17.0.3   指定发布标签
//   bun scripts/update-twemoji.ts --yes --out 目录      放到别的目录（测试用；默认 native/twemoji）
//   bun scripts/update-twemoji.ts --tgz 文件.tgz --yes   用已经下好的压缩包（不联网，标签用 --version 说明）
//
// 做法：下载发布压缩包到内存，只读 assets/svg/ 下名字是码位的 .svg 和根目录的 LICENSE-GRAPHICS，不执行里面的任何东西；
// 文件名和内容过白名单（码位小写十六进制用 - 连，内容以 <svg 开头），数量不到 3000 就不动旧目录；
// 先写到“目标目录.new”，写完再换掉旧的（旧的挪成“目标目录.old”，换成功后删），中途失败旧目录原样还在；
// 目录里留一个 VERSION 文件写发布标签。
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { readTgz } from './lib/tgz'

const repo = resolve(import.meta.dir, '..')
const REPO = 'jdecked/twemoji'
const MIN_FILES = 3000

function arg(name: string): string | undefined {
  const i = process.argv.indexOf(name)
  return i >= 0 ? process.argv[i + 1] : undefined
}
const yes = process.argv.includes('--yes')
const out = resolve(arg('--out') ?? join(repo, 'native', 'twemoji'))

async function latestTag(): Promise<string> {
  const r = await fetch(`https://api.github.com/repos/${REPO}/releases/latest`, { headers: { accept: 'application/vnd.github+json', 'user-agent': 'sumika-update-twemoji' } })
  if (!r.ok) throw new Error(`查最新发布失败：${r.status}`)
  const j: any = await r.json()
  if (typeof j.tag_name !== 'string' || !/^v\d+\.\d+\.\d+$/.test(j.tag_name)) throw new Error(`最新发布的标签不对劲：${j.tag_name}`)
  return j.tag_name
}

const local = arg('--tgz')
const tag = arg('--version') ?? (local ? undefined : await latestTag())
if (!tag || !/^v\d+\.\d+\.\d+$/.test(tag)) throw new Error('用 --tgz 时要用 --version vX.Y.Z 说明是哪个发布')
const url = `https://github.com/${REPO}/archive/refs/tags/${tag}.tar.gz`
const current = existsSync(join(out, 'VERSION')) ? readFileSync(join(out, 'VERSION'), 'utf8').trim() : '（没有）'

console.log(`目标目录：${out}`)
console.log(`现有版本：${current}；要取的：${tag}（${local ? `本地文件 ${local}` : url}）`)
if (!yes) {
  console.log('干跑，没下载也没写。确定要换就加 --yes。')
  process.exit(0)
}

const data = local ? new Uint8Array(readFileSync(local)) : await (async () => {
  const r = await fetch(url, { headers: { 'user-agent': 'sumika-update-twemoji' } })
  if (!r.ok) throw new Error(`下载失败：${r.status} ${url}`)
  return new Uint8Array(await r.arrayBuffer())
})()

const NAME = /^[0-9a-f]+(-[0-9a-f]+)*\.svg$/
const svgs = new Map<string, Uint8Array>()
let license: Uint8Array | undefined
for (const [path, bytes] of readTgz(data)) {
  const parts = path.split('/') // 第一段是 twemoji-17.0.3 这样的顶层目录
  if (parts.length === 4 && parts[1] === 'assets' && parts[2] === 'svg' && NAME.test(parts[3])) {
    if (!new TextDecoder().decode(bytes.subarray(0, 200)).includes('<svg')) throw new Error(`${path} 不像 SVG`)
    svgs.set(parts[3], bytes)
  } else if (parts.length === 2 && parts[1] === 'LICENSE-GRAPHICS') license = bytes
}
if (svgs.size < MIN_FILES) throw new Error(`只读到 ${svgs.size} 个 SVG（至少 ${MIN_FILES}），压缩包的目录结构可能变了，没动旧目录`)
if (!svgs.has('1f44d.svg')) throw new Error('里面没有 1f44d.svg（程序靠它判断目录有效），没动旧目录')
if (!license) throw new Error('里面没有 LICENSE-GRAPHICS，没动旧目录')

const fresh = `${out}.new`
const old = `${out}.old`
rmSync(fresh, { recursive: true, force: true })
mkdirSync(fresh, { recursive: true })
for (const [name, bytes] of svgs) writeFileSync(join(fresh, name), bytes)
writeFileSync(join(fresh, 'LICENSE-GRAPHICS'), license)
writeFileSync(join(fresh, 'VERSION'), tag + '\n')
rmSync(old, { recursive: true, force: true })
if (existsSync(out)) renameSync(out, old)
renameSync(fresh, out)
rmSync(old, { recursive: true, force: true })
console.log(`Twemoji ${tag}：${svgs.size} 个 SVG，写到 ${out}`)
