// 生成 MingCute 全套图标的 Go 源（native/internal/assets/icons/icons_gen.go）。
// 来源是作者官方 npm 包 mingcute_icon（Apache-2.0）：只下载 tgz、在内存里按 tar 格式读出 svg/ 下的文件解析，
// 不解压到磁盘、不执行包里的任何东西；下载后用 registry 给的 sha1 核对。
//
//   bun scripts/gen-mingcute.ts                    取最新版（registry 的 latest 标签）
//   bun scripts/gen-mingcute.ts --version 2.9.72   指定版本
//   bun scripts/gen-mingcute.ts --tgz 文件.tgz      用已经下好的 tgz（不联网；sha1 仍对 registry，联不上就只提示）
//   bun scripts/gen-mingcute.ts --cache 目录        下载缓存目录（默认 data/dev/mingcute-cache，data/ 不进 git）
//
// 每个图标取 line（线性）和 fill（实心）两份，只留带颜色的路径：去掉包围框那条透明路径，颜色换成 currentColor，
// 填充规则从 <g> 或路径上带过来。含内嵌位图、渐变这类 mygo 画不了的图标不收，名字打印出来。
// 输出按名字排序，查找用二分，没有 init 开销。升级版本后重跑一次，再 gofmt，把改动的图标交给测试对一遍。
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { readTgz } from './lib/tgz'

const REGISTRY = 'https://registry.npmjs.org/mingcute_icon'
const repo = resolve(import.meta.dir, '..')
const OUT = join(repo, 'native', 'internal', 'assets', 'icons')

function arg(name: string): string | undefined {
  const i = process.argv.indexOf(name)
  return i >= 0 ? process.argv[i + 1] : undefined
}

// ---------- 取 tgz ----------
type Meta = { version: string; shasum: string; tarball: string }
async function registryMeta(version: string): Promise<Meta | null> {
  try {
    const r = await fetch(`${REGISTRY}/${version}`)
    if (!r.ok) return null
    const j: any = await r.json()
    return { version: j.version, shasum: j.dist.shasum, tarball: j.dist.tarball }
  } catch {
    return null
  }
}

async function loadTgz(): Promise<{ data: Uint8Array; meta: { version: string; sha1: string; verified: boolean } }> {
  const local = arg('--tgz')
  if (local) {
    const data = new Uint8Array(readFileSync(local))
    const sha1 = createHash('sha1').update(data).digest('hex')
    // 版本从包里的 package.json 读，不信文件名
    const pkg = JSON.parse(new TextDecoder().decode(readTgz(data).get('package/package.json') ?? new Uint8Array()) || '{}')
    const m = await registryMeta(pkg.version)
    if (m && m.shasum !== sha1) throw new Error(`sha1 对不上：文件 ${sha1}，registry ${m.shasum}`)
    if (!m) console.warn('联不上 registry，没法核对 sha1')
    return { data, meta: { version: pkg.version, sha1, verified: !!m } }
  }
  const want = arg('--version') ?? 'latest'
  const m = await registryMeta(want)
  if (!m) throw new Error(`registry 查不到 mingcute_icon@${want}`)
  const cache = arg('--cache') ?? join(repo, 'data', 'dev', 'mingcute-cache')
  mkdirSync(cache, { recursive: true })
  const file = join(cache, `mingcute_icon-${m.version}.tgz`)
  let data: Uint8Array
  if (existsSync(file)) data = new Uint8Array(readFileSync(file))
  else {
    const r = await fetch(m.tarball)
    if (!r.ok) throw new Error(`下载失败：${r.status} ${m.tarball}`)
    data = new Uint8Array(await r.arrayBuffer())
  }
  const sha1 = createHash('sha1').update(data).digest('hex')
  if (sha1 !== m.shasum) throw new Error(`sha1 对不上：下载得到 ${sha1}，registry ${m.shasum}`)
  writeFileSync(file, data)
  return { data, meta: { version: m.version, sha1, verified: true } }
}

// ---------- 把一个 svg 变成能直接放进 <svg> 的路径体 ----------
const PATH_DATA = /^[MmLlHhVvCcSsQqTtAaZz0-9eE\s,.+-]+$/

function body(svg: string): string | null {
  if (/<(defs|image|pattern|use|linearGradient|radialGradient|clipPath|mask|filter|script|style|text)\b/.test(svg)) return null
  const g = /<g\b([^>]*)>([\s\S]*?)<\/g>/.exec(svg)
  if (!g) return null
  if (!/\sfill="none"/.test(g[1])) return null // 路径没颜色时靠 <g fill="none"> 判定“不画”，不是 none 的不收
  const gRule = /fill-rule="(evenodd|nonzero)"/.exec(g[1])?.[1]
  const out: string[] = []
  for (const m of g[2].matchAll(/<path\b([^>]*?)\/>/g)) {
    const attrs = m[1]
    const d = /\sd="([^"]*)"/.exec(attrs)?.[1]
    if (d === undefined || !PATH_DATA.test(d)) return null
    const fill = /\sfill="([^"]*)"/.exec(attrs)?.[1]
    if (!fill) continue // 没有自己的颜色就是 <g fill="none"> 下面的包围框 / 水印，不画
    const rule = /fill-rule="(evenodd|nonzero)"/.exec(attrs)?.[1] ?? gRule
    const opacity = /\sopacity="([0-9.]+)"/.exec(attrs)?.[1]
    out.push(`<path fill="currentColor"${rule === 'evenodd' ? ' fill-rule="evenodd"' : ''}${opacity ? ` opacity="${opacity}"` : ''} d="${d}"/>`)
  }
  return out.length ? out.join('') : null
}

// ---------- 主流程 ----------
const { data, meta } = await loadTgz()
const files = readTgz(data)
const license = files.get('package/LICENSE')
if (!license) throw new Error('包里没有 LICENSE')

const icons = new Map<string, { line?: string; fill?: string }>()
const skipped: string[] = []
for (const [path, bytes] of files) {
  const m = /^package\/svg\/[a-z_]+\/(.+)_(line|fill)\.svg$/.exec(path)
  if (!m) continue
  const [, name, style] = m
  const b = body(new TextDecoder().decode(bytes))
  if (b === null) {
    skipped.push(`${name}_${style}`)
    continue
  }
  if (b.includes('`')) throw new Error(`${path}：路径里有反引号`)
  const e = icons.get(name) ?? {}
  if (e[style as 'line' | 'fill']) throw new Error(`${name}_${style} 在包里出现了两次（不同分类下重名）`)
  e[style as 'line' | 'fill'] = b
  icons.set(name, e)
}
// 只有一种样式的（另一种没收进来）也保留，空串表示没有
const names = [...icons.keys()].sort()

const lines: string[] = []
lines.push('// Code generated by scripts/gen-mingcute.ts; DO NOT EDIT.')
lines.push(`// 来源：npm 包 mingcute_icon@${meta.version}（MingCute Icon，Apache-2.0，https://github.com/Richard9394/MingCute，https://mingcute.com），许可证原文在同目录 LICENSE-MingCute。`)
lines.push(`// 压缩包 sha1 ${meta.sha1}${meta.verified ? '（已和 registry 核对）' : '（没核对）'}。共 ${names.length} 个图标，每个 line 线性、fill 实心两种，24×24，颜色 currentColor。`)
if (skipped.length) lines.push(`// 没收的（含 mygo 画不了的内嵌位图等）：${skipped.sort().join('、')}。`)
lines.push('')
lines.push('package icons')
lines.push('')
lines.push(`const sourceVersion = ${JSON.stringify(meta.version)}`)
lines.push('')
lines.push('// table 按名字排序，查找用二分。line、fill 是 <svg> 里面的路径；某种样式不存在时为空串。')
lines.push('var table = [...]entry{')
for (const n of names) {
  const e = icons.get(n)!
  lines.push(`\t{${JSON.stringify(n)}, \`${e.line ?? ''}\`, \`${e.fill ?? ''}\`},`)
}
lines.push('}')
lines.push('')

mkdirSync(OUT, { recursive: true })
writeFileSync(join(OUT, 'icons_gen.go'), lines.join('\n'))
writeFileSync(join(OUT, 'LICENSE-MingCute'), license)
console.log(`mingcute_icon@${meta.version}：${names.length} 个图标，跳过 ${skipped.length} 个${skipped.length ? `（${skipped.join('、')}）` : ''}`)
console.log('写到', dirname(join(OUT, 'icons_gen.go')), '，接着 gofmt -w 它')
