// 导出公开版：只取 git 跟踪的文件，按 scripts/public-exclude.txt 排除，改导出目录里的文档副本，再做自检。
//   bun scripts/export-public.ts --out <空目录> [--ref HEAD] [--exclude scripts/public-exclude.txt]
// 不碰仓库里的文件；--out 不存在就建，存在但非空就拒绝。自检红了退出码 1（导出目录留着，不删）。
import { chmodSync, existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, posix, resolve } from 'node:path'
import { devEnv, devExe } from './lib/sumika-dev.ts'

const ROOT = resolve(import.meta.dir, '..')
const HASH = /(?<![0-9a-fA-F#/])[0-9a-fA-F]{7,40}(?![0-9a-fA-F])/g
const CONTEXT = /提交|合|main|commit|分支/i

export type Env = Record<string, string | undefined>
export class ExportError extends Error {}

// ───── 排除表（gitignore 风格） ─────
export type ExcludeRule = { raw: string; negate: boolean; dirOnly: boolean; re: RegExp }

/** 把一行模式变成匹配整条相对路径的正则；`**` 跨目录、`*` 不进目录、结尾 `/` 表示目录。 */
function ruleRegex(pattern: string, negate: boolean): ExcludeRule {
  let p = pattern.trim().replace(/\\/g, '/')
  if (!p) throw new ExportError('排除表里有空模式')
  if (p.startsWith('!')) { negate = !negate; p = p.slice(1) }
  const dirOnly = p.endsWith('/')
  if (dirOnly) p = p.slice(0, -1)
  const anchored = p.startsWith('/') || p.includes('/')
  if (p.startsWith('/')) p = p.slice(1)
  let body = ''
  for (let i = 0; i < p.length; i++) {
    const c = p[i]!
    if (c === '*') {
      if (p[i + 1] === '*') { i++; if (p[i + 1] === '/') { i++; body += '(?:.*/)?' } else body += '.*' }
      else body += '[^/]*'
    } else if (c === '?') body += '[^/]'
    else body += c.replace(/[.+^${}()|[\]\\]/g, '\\$&')
  }
  // 不带斜杠的模式按 gitignore 的规矩匹配任意一层；带斜杠的从仓库根算
  const prefix = anchored ? '^' : '(?:^|/)'
  const suffix = dirOnly || !p.includes('/') ? '(?:/.*)?$' : '$'
  return { raw: pattern, negate, dirOnly, re: new RegExp(prefix + body + suffix) }
}

export function parseExclude(text: string): ExcludeRule[] {
  const rules: ExcludeRule[] = []
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    // 模式和原因写在同一行：`server/   # 冻结的 Bun 后台`
    const cut = line.search(/\s+#\s/)
    const pattern = cut < 0 ? line : line.slice(0, cut).trim()
    if (!pattern) continue
    rules.push(ruleRegex(pattern, false))
  }
  return rules
}

export function isExcluded(path: string, rules: ExcludeRule[]): boolean {
  const p = path.replace(/\\/g, '/').replace(/^\.\//, '')
  let hit = false
  for (const r of rules) if (r.re.test(p)) hit = !r.negate
  return hit
}

// ───── git archive 和 tar 解包（不依赖本机的 tar） ─────
function git(root: string, args: string[], env: Env): Buffer {
  const p = Bun.spawnSync(['git', '-c', 'core.quotepath=false', ...args], { cwd: root, env, stdout: 'pipe', stderr: 'pipe' })
  if (p.exitCode !== 0) throw new ExportError(`git ${args[0]} 失败（退出码 ${p.exitCode}）：${p.stderr.toString().trim()}`)
  return p.stdout
}

type TarEntry = { path: string; type: string; mode: number; data: Buffer }

const cstr = (b: Buffer, start: number, len: number) => {
  const s = b.subarray(start, start + len)
  const z = s.indexOf(0)
  return s.subarray(0, z < 0 ? len : z).toString('utf8')
}
const octal = (b: Buffer, start: number, len: number) => {
  const s = b.subarray(start, start + len).toString('ascii').replace(/\0.*$/, '').trim()
  return s ? parseInt(s, 8) : 0
}

function pax(data: Buffer): Record<string, string> {
  const out: Record<string, string> = {}
  let off = 0
  while (off < data.length) {
    const sp = data.indexOf(0x20, off)
    if (sp < 0) break
    const len = Number(data.subarray(off, sp).toString())
    if (!Number.isFinite(len) || len <= 0 || off + len > data.length) break
    const rec = data.subarray(sp + 1, off + len - 1).toString('utf8')
    const eq = rec.indexOf('=')
    if (eq > 0) out[rec.slice(0, eq)] = rec.slice(eq + 1)
    off += len
  }
  return out
}

export function* tarEntries(buf: Buffer): Generator<TarEntry> {
  let off = 0, longName = '', nextPath = '', nextSize: number | undefined
  while (off + 512 <= buf.length) {
    const h = buf.subarray(off, off + 512)
    if (h.every((x) => x === 0)) break
    const rawName = cstr(h, 0, 100)
    const prefix = cstr(h, 345, 155)
    let name = prefix ? `${prefix}/${rawName}` : rawName
    const type = h[156] === 0 ? '0' : String.fromCharCode(h[156]!)
    const size = octal(h, 124, 12)
    const dataStart = off + 512
    if (type === 'x' || type === 'g') {
      const p = pax(buf.subarray(dataStart, dataStart + size))
      if (type === 'x') { if (p.path) nextPath = p.path; if (p.size) nextSize = Number(p.size) }
    } else if (type === 'L') {
      longName = cstr(buf.subarray(dataStart, dataStart + size), 0, size)
    } else {
      if (longName) name = longName
      if (nextPath) name = nextPath
      const real = nextSize ?? size
      yield { path: name.replace(/^\.\//, ''), type, mode: octal(h, 100, 8), data: buf.subarray(dataStart, dataStart + real) }
      longName = ''; nextPath = ''; nextSize = undefined
    }
    off = dataStart + Math.ceil(size / 512) * 512
  }
}

// ───── 文档改写 ─────
/** 链接的目标路径（去掉 <...>、标题、锚点、查询串，解开 percent 编码）。 */
function linkTarget(raw: string): string {
  let t = raw.trim()
  if (t.startsWith('<')) {
    const end = t.indexOf('>')
    if (end > 0) t = t.slice(1, end)
  } else {
    t = t.split(/\s+/)[0] ?? ''
  }
  t = t.replace(/\\/g, '/')
  const hash = t.indexOf('#')
  if (hash >= 0) t = t.slice(0, hash)
  const q = t.indexOf('?')
  if (q >= 0) t = t.slice(0, q)
  try { t = decodeURIComponent(t) } catch { /* 保留原样 */ }
  return t
}

/** 文档里的相对链接解析成导出目录里的相对路径；外面、绝对、协议链接返回 null。 */
export function resolveLink(mdRel: string, raw: string): string | null {
  const t = linkTarget(raw)
  if (!t || t.startsWith('/') || /^[a-z][a-z0-9+.-]*:/i.test(t)) return null
  // 这几处是仓库根的本地目录，文档里常写成裸路径，不按链接所在目录解析
  const rootPath = /^(?:_external|data|docs\/inbox|server|desktop|daemon|web|shell)\//.test(t)
  const p = posix.normalize(rootPath ? t : posix.join(posix.dirname(mdRel), t))
  if (p === '..' || p.startsWith('../')) return null
  return p
}

function tidy(line: string): string {
  let prev = ''
  while (line !== prev) {
    prev = line
    line = line.replace(/[（(]\s*[，,、;；:：]\s*[）)]/g, '')
    line = line.replace(/[（(]\s*[）)]/g, '')
    line = line.replace(/([（(])\s*[，,、;；:：]/g, '$1')
    line = line.replace(/[，,、;；:：]\s*([）)])/g, '$1')
    line = line.replace(/([（(])\s+/g, '$1').replace(/\s+([）)])/g, '$1')
    line = line.replace(/[ \t]{2,}/g, ' ')
    line = line.replace(/\s+([，。；：、！？])/g, '$1')
  }
  return line
}

function removeHashes(s: string): { s: string; n: number } {
  let n = 0
  const out = s.replace(HASH, () => { n++; return '' })
  return { s: out, n }
}

/** 提交号只在这几类上下文里去掉：同一行有“提交 / 合 / main / commit / 分支”，且号码在反引号或括号里。 */
export function stripCommitHashes(line: string): { line: string; n: number } {
  if (!CONTEXT.test(line)) return { line, n: 0 }
  let n = 0
  line = line.replace(/`([^`\n]*)`/g, (m, inner: string) => {
    const r = removeHashes(inner)
    if (!r.n) return m
    n += r.n
    const rest = tidy(r.s).trim()
    return rest ? `\`${rest}\`` : ''
  })
  line = line.replace(/[（(][^（）()\n]*[）)]/g, (m) => {
    const r = removeHashes(m)
    if (!r.n) return m
    n += r.n
    return tidy(r.s)
  })
  return { line: n ? tidy(line) : line, n }
}

export type RewriteCounts = { archive: number; excluded: number; commit: number; private: number; files: number }

// 主人私有的 claude.ai 画板 / 分享页、ChatGPT 对话和分享链接：别人打不开，地址本身也不该公开（2026-11-17 导出自检查出 21 处）
// 地址只认网址里的 ASCII 字符：紧跟在后面的中文标点、文字不算地址（不然“，结尾”会被一起吞掉）
const PRIVATE_URL = /https?:\/\/(?:claude\.ai|chatgpt\.com|chat\.openai\.com)\/[\w\-.~:/?#@!$&*+,;=%]*/g
function stripPrivateLinks(line: string): { line: string; n: number } {
  let n = 0
  const out = line
    .replace(/\[([^\]\n]*)\]\((https?:\/\/(?:claude\.ai|chatgpt\.com|chat\.openai\.com)\/[^)\n]*)\)/g, (_m, label: string) => { n++; return `${label}（私有链接）` })
    .replace(PRIVATE_URL, () => { n++; return '（私有链接）' })
  return { line: out, n }
}

/** 只改导出目录里的 Markdown 副本：私有存档链接变文字、被排除文件的链接变文字、私人对话和画板链接去掉、旧提交号去掉。 */
export function rewriteMarkdown(text: string, mdRel: string, rules: ExcludeRule[]): { text: string; counts: Omit<RewriteCounts, 'files'> } {
  const counts = { archive: 0, excluded: 0, commit: 0, private: 0 }
  let fence = false
  const out = text.split(/\r?\n/).map((raw) => {
    const p = stripPrivateLinks(raw) // 代码块里也去：地址出现在哪都不该公开
    counts.private += p.n
    const line = p.line
    if (/^\s*```/.test(line)) { fence = !fence; return line }
    if (fence) return line
    let next = line
    next = next.replace(/(!?)\[([^\]\n]*)\]\(([^)\n]+)\)/g, (m, _bang: string, label: string, target: string, offset: number) => {
      if ((next.slice(0, offset).match(/`/g) ?? []).length % 2 === 1) return m
      const decoded = linkTarget(target).replace(/\\/g, '/')
      const resolved = resolveLink(mdRel, target)
      const archive = (!!resolved && (resolved === 'docs/archive' || resolved.startsWith('docs/archive/'))) ||
        /(?:^|\/)docs\/archive(?:\/|$)/.test(decoded)
      const excluded = !!resolved && isExcluded(resolved, rules)
      if (!archive && !excluded) return m
      if (archive) counts.archive++
      else counts.excluded++
      return `${label}（私有存档）`
    })
    const h = stripCommitHashes(next)
    counts.commit += h.n
    return h.line
  }).join('\n')
  return { text: out, counts }
}

// ───── 自检 ─────
function walk(root: string, rel = ''): string[] {
  const out: string[] = []
  for (const e of readdirSync(join(root, rel), { withFileTypes: true })) {
    const p = rel ? `${rel}/${e.name}` : e.name
    if (e.isDirectory()) out.push(...walk(root, p))
    else out.push(p)
  }
  return out
}

export function checkLinks(out: string): string[] {
  const bad: string[] = []
  for (const rel of walk(out)) {
    if (!rel.endsWith('.md')) continue
    // 题库、第三方文档里的链接是夹具或上游原文，不是本仓库的导航
    if (rel.startsWith('scripts/bench-t2/') || rel.startsWith('native/third_party/') || rel.startsWith('testdata/')) continue
    let fence = false
    const lines = readFileSync(join(out, rel), 'utf8').split(/\r?\n/)
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i]!
      if (/^\s*```/.test(line)) { fence = !fence; continue }
      if (fence) continue
      for (const m of line.matchAll(/!?\[[^\]\n]*\]\(([^)\n]+)\)/g)) {
        const raw = m[1]!
        if ((line.slice(0, m.index).match(/`/g) ?? []).length % 2 === 1) continue
        const decoded = linkTarget(raw)
        if (!decoded || decoded.startsWith('/') || /^[a-z][a-z0-9+.-]*:/i.test(decoded)) continue
        if (/\s/.test(raw) || /:\d+$/.test(decoded)) continue
        if (decoded.startsWith('仓库根/') || /^(Go 模块缓存|网址|mygo-m3e|Telegram 源码目录)/.test(decoded)) continue
        if (decoded.includes('{{')) continue
        const resolved = resolveLink(rel, raw)
        if (!resolved) {
          bad.push(`${rel}:${i + 1} -> ${raw}（跑到导出目录外面）`)
          continue
        }
        if (existsSync(join(out, resolved))) continue
        bad.push(`${rel}:${i + 1} -> ${raw}（${resolved} 不存在）`)
      }
    }
  }
  return bad
}

// 隐私扫描交给 sumika-dev check private --tree（和提交钩子、推送钩子同一份实现）：退出码 0 干净或只有提醒、1 有要拦的、2 名单读不了
export type PrivacyResult = { code: number; output: string }
function scanPrivacy(root: string, out: string): PrivacyResult {
  const p = Bun.spawnSync([devExe(root), 'check', 'private', '--tree', out], { cwd: root, env: devEnv(), stdout: 'pipe', stderr: 'pipe' })
  return { code: p.exitCode ?? 2, output: p.stdout.toString() + p.stderr.toString() }
}

function scanDrivePaths(out: string): string[] {
  const bad: string[] = []
  for (const rel of walk(out)) {
    const buf = readFileSync(join(out, rel))
    if (buf.subarray(0, 8192).includes(0)) continue
    const lines = buf.toString('utf8').split(/\r?\n/)
    for (let i = 0; i < lines.length; i++) if (/(?<![A-Za-z])[A-Za-z]:[\\/]/.test(lines[i]!)) bad.push(`${rel}:${i + 1}`)
  }
  return bad
}

// ───── 主流程 ─────
export type ExportOptions = {
  root: string
  out: string
  ref?: string
  excludeFile?: string
  env?: Env
  privacy?: (out: string) => PrivacyResult   // 测试换成指向编造数据目录的版本；平时不传
}

export type ExportReport = {
  ok: boolean
  ref: string
  out: string
  files: number
  bytes: number
  excluded: string[]
  rewrites: RewriteCounts
  excludedHits: string[]
  broken: string[]
  privacy: PrivacyResult
  drivePaths: string[]
  warnings: string[]
}

export function runExport(o: ExportOptions): ExportReport {
  const root = resolve(o.root)
  const out = resolve(o.out)
  const ref = o.ref ?? 'HEAD'
  const env = o.env ?? process.env
  const warnings: string[] = []

  if (existsSync(out)) {
    if (!statSync(out).isDirectory()) throw new ExportError(`--out 已存在且不是目录：${out}`)
    if (readdirSync(out).length) throw new ExportError(`--out 不是空目录，拒绝覆盖：${out}`)
  } else {
    mkdirSync(out, { recursive: true })
  }
  const excludeFile = resolve(root, o.excludeFile ?? 'scripts/public-exclude.txt')
  if (!existsSync(excludeFile)) throw new ExportError(`找不到排除表：${excludeFile}`)
  const rules = parseExclude(readFileSync(excludeFile, 'utf8'))
  if (!rules.length) throw new ExportError(`排除表是空的：${excludeFile}`)

  const archive = git(root, ['archive', '--format=tar', ref], env)
  if (!archive.length) throw new ExportError(`git archive ${ref} 没有输出`)
  const excluded: string[] = []
  let files = 0, bytes = 0
  for (const e of tarEntries(archive)) {
    if (e.type !== '0') {
      if (e.type !== '5') warnings.push(`跳过非普通文件：${e.path}（type ${e.type}）`)
      continue
    }
    const rel = e.path.replace(/^\.\//, '')
    if (!rel || rel.startsWith('/') || rel.split('/').includes('..')) throw new ExportError(`archive 里有不安全的路径：${e.path}`)
    if (isExcluded(rel, rules)) { excluded.push(rel); continue }
    const dest = join(out, rel)
    mkdirSync(dirname(dest), { recursive: true })
    writeFileSync(dest, e.data)
    if (process.platform !== 'win32' && e.mode) chmodSync(dest, e.mode & 0o777)
    files++; bytes += e.data.length
  }

  const rewrites: RewriteCounts = { archive: 0, excluded: 0, commit: 0, private: 0, files: 0 }
  for (const rel of walk(out)) {
    if (!rel.endsWith('.md')) continue
    const p = join(out, rel)
    const before = readFileSync(p, 'utf8')
    const r = rewriteMarkdown(before, rel, rules)
    if (r.text !== before) { writeFileSync(p, r.text); rewrites.files++ }
    rewrites.archive += r.counts.archive
    rewrites.excluded += r.counts.excluded
    rewrites.commit += r.counts.commit
    rewrites.private += r.counts.private
  }

  const excludedHits = walk(out).filter((f) => isExcluded(f, rules))
  const broken = checkLinks(out)
  const privacy = (o.privacy ?? ((d) => scanPrivacy(root, d)))(out)
  if (privacy.code === 2) throw new ExportError(privacy.output.trim())   // 名单读不了：不导出
  const drivePaths = scanDrivePaths(out)
  return {
    ok: excludedHits.length === 0 && broken.length === 0 && privacy.code === 0,
    ref, out, files, bytes, excluded, rewrites, excludedHits, broken, privacy, drivePaths, warnings,
  }
}

// ───── 命令行 ─────
function usage(): void {
  console.log('用法：bun scripts/export-public.ts --out <空目录> [--ref HEAD] [--exclude scripts/public-exclude.txt]')
}

function printReport(r: ExportReport): void {
  const mb = (r.bytes / 1024 / 1024).toFixed(1)
  console.log(`导出 ${r.ref} -> ${r.out}`)
  console.log(`  文件 ${r.files} 个，共 ${mb} MB；排除 ${r.excluded.length} 个`)
  console.log(`  改写：docs/archive 链接 ${r.rewrites.archive} 处、被排除文件链接 ${r.rewrites.excluded} 处、旧提交号 ${r.rewrites.commit} 处、私人对话和画板链接 ${r.rewrites.private} 处（改了 ${r.rewrites.files} 个文件）`)
  if (r.warnings.length) { console.log('  警告：'); for (const w of r.warnings) console.log(`    ${w}`) }
  console.log('自检：')
  console.log(`  排除表命中：${r.excludedHits.length}${r.excludedHits.length ? '（' + r.excludedHits.slice(0, 10).join('、') + '）' : ''}`)
  console.log(`  相对链接断链：${r.broken.length}`)
  for (const b of r.broken) console.log(`    ${b}`)
  process.stdout.write(r.privacy.output)
  console.log(`  盘符绝对路径：${r.drivePaths.length} 行${r.drivePaths.length ? '（只报告，不失败）' : ''}`)
  for (const p of r.drivePaths) console.log(`    ${p}`)
  console.log(r.ok ? '自检通过' : '自检没过：见上面')
}

if (import.meta.main) {
  const args = process.argv.slice(2)
  let out = '', ref = 'HEAD', excludeFile: string | undefined
  for (let i = 0; i < args.length; i++) {
    const a = args[i]!
    if (a === '--out') out = args[++i] ?? ''
    else if (a === '--ref') ref = args[++i] ?? ''
    else if (a === '--exclude') excludeFile = args[++i]
    else if (a === '--help' || a === '-h') { usage(); process.exit(0) }
    else { console.error(`不认识的参数：${a}`); usage(); process.exit(2) }
  }
  if (!out) { usage(); process.exit(2) }
  try {
    const r = runExport({ root: ROOT, out, ref, excludeFile, env: process.env })
    printReport(r)
    process.exit(r.ok ? 0 : 1)
  } catch (e) {
    console.error(e instanceof ExportError || e instanceof Error ? e.message : String(e))
    process.exit(1)
  }
}
