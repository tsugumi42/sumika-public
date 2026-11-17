// scripts/export-public.ts：排除、拒绝非空输出、文档改写、断链和隐私自检。仓库和数据都是编的。
import { afterAll, expect, test } from 'bun:test'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { isExcluded, parseExclude, rewriteMarkdown, runExport, type Env } from './export-public.ts'
import type { Known } from './scan-private.ts'

const dirs: string[] = []
afterAll(() => { for (const d of dirs) rmSync(d, { recursive: true, force: true }) })
const tmp = () => { const d = mkdtempSync(join(tmpdir(), 'export-public-test-')); dirs.push(d); return d }

// 测试里起 git 一律传清掉 GIT_* 的 env，再补上提交需要的身份；不继承外面会话的 GIT_DIR / GIT_WORK_TREE
function gitEnv(extra: Record<string, string> = {}): Env {
  const env: Env = { ...process.env }
  for (const k of Object.keys(env)) if (k.startsWith('GIT_')) delete env[k]
  return { ...env, GIT_AUTHOR_NAME: 'test', GIT_AUTHOR_EMAIL: 'test@example.invalid', GIT_COMMITTER_NAME: 'test', GIT_COMMITTER_EMAIL: 'test@example.invalid', ...extra }
}

function git(cwd: string, args: string[], env = gitEnv()): string {
  const p = Bun.spawnSync(['git', ...args], { cwd, env, stdout: 'pipe', stderr: 'pipe' })
  if (p.exitCode !== 0) throw new Error(`git ${args.join(' ')} 失败：${p.stderr.toString()}`)
  return p.stdout.toString()
}

const EXCLUDE = [
  '# 测试用排除表',
  '/server/   # 后台',
  '/docs/archive/',
  '/native/_spike/out/',
  '/scripts/fix-*.ts',
  '/data/',
  '/docs/notes/拆解-Codex桌面版-界面.md',
].join('\n')

const KEEP = [
  '# 留下',
  '[旧存档](docs/archive/old.md)',
  '[旧后台](server/a.txt)',
  '![旧图](docs/archive/pic.png)',
  '合并 `abcdef1` 到 main',
  '颜色 #a1b2c3d 不变',
  '哈希示例 `deadbeef` 保留',
  '（提交 1234567）',
].join('\n')

const fakeKnown: Known = { numbers: new Set(['8642097531']), names: new Set(), secrets: new Set() }
const opts = (root: string, out: string) => ({ root, out, known: fakeKnown, env: gitEnv() })

function makeRepo(extra: Record<string, string> = {}): string {
  const root = tmp()
  git(root, ['init', '-q'])
  git(root, ['config', 'user.name', 'test'])
  git(root, ['config', 'user.email', 'test@example.invalid'])
  git(root, ['config', 'core.autocrlf', 'false'])
  const files: Record<string, string> = {
    'keep.md': KEEP,
    'path.md': '本机路径 D:\\Projects\\sumika\\native，网址 https://example.invalid/D:/x 不算。',
    'docs/notes/note.md': '# 笔记\n[旧](docs/archive/old.md)\n',
    'server/a.txt': '后台',
    'docs/archive/old.md': '# 旧',
    'native/_spike/out/x.txt': '压测',
    'scripts/fix-a.ts': '// 一次性',
    'docs/notes/拆解-Codex桌面版-界面.md': '# 第三方文案',
    'scripts/public-exclude.txt': EXCLUDE,
    ...extra,
  }
  for (const [rel, text] of Object.entries(files)) {
    const p = join(root, rel)
    mkdirSync(dirname(p), { recursive: true })
    writeFileSync(p, text)
  }
  git(root, ['add', '-A'])
  git(root, ['commit', '-qm', 'init'])
  return root
}

test('排除表：根目录锚定、目录带内容、脚本通配', () => {
  const rules = parseExclude(EXCLUDE)
  expect(isExcluded('server/a.txt', rules)).toBe(true)
  expect(isExcluded('x/server/a.txt', rules)).toBe(false)
  expect(isExcluded('docs/archive/notes/x.md', rules)).toBe(true)
  expect(isExcluded('scripts/fix-old.ts', rules)).toBe(true)
  expect(isExcluded('scripts/fix-old.tsx', rules)).toBe(false)
  expect(isExcluded('docs/notes/拆解-Codex桌面版-界面.md', rules)).toBe(true)
  expect(isExcluded('keep.md', rules)).toBe(false)
})

test('导出：排除生效、私有存档链接和提交号改写、盘符只报告', () => {
  const root = makeRepo()
  const out = join(tmp(), 'out')
  const r = runExport(opts(root, out))
  expect(r.ok).toBe(true)
  expect(existsSync(join(out, 'server/a.txt'))).toBe(false)
  expect(existsSync(join(out, 'docs/archive/old.md'))).toBe(false)
  expect(existsSync(join(out, 'native/_spike/out/x.txt'))).toBe(false)
  expect(existsSync(join(out, 'scripts/fix-a.ts'))).toBe(false)
  expect(existsSync(join(out, 'docs/notes/拆解-Codex桌面版-界面.md'))).toBe(false)
  expect(r.excluded.sort()).toEqual(['docs/archive/old.md', 'docs/notes/拆解-Codex桌面版-界面.md', 'native/_spike/out/x.txt', 'scripts/fix-a.ts', 'server/a.txt'])
  const keep = readFileSync(join(out, 'keep.md'), 'utf8')
  expect(keep).toContain('旧存档（私有存档）')
  expect(keep).toContain('旧后台（私有存档）')
  expect(keep).toContain('旧图（私有存档）')
  expect(keep).not.toContain('abcdef1')
  expect(keep).toContain('颜色 #a1b2c3d 不变')
  expect(keep).toContain('哈希示例 `deadbeef` 保留')
  expect(keep).toContain('（提交）')
  expect(r.rewrites.archive).toBe(3)
  expect(r.rewrites.excluded).toBe(1)
  expect(r.rewrites.commit).toBe(2)
  expect(r.broken).toEqual([])
  expect(r.drivePaths).toEqual(['path.md:1'])
})

test('--out 非空拒绝，不删已有内容', () => {
  const root = makeRepo()
  const out = tmp()
  writeFileSync(join(out, '已有.txt'), '不要动')
  expect(() => runExport(opts(root, out))).toThrow(/不是空目录/)
  expect(readFileSync(join(out, '已有.txt'), 'utf8')).toBe('不要动')
})

test('提交号：只在上下文里的反引号/括号去掉；颜色和无关十六进制保留', () => {
  const rules = parseExclude('')
  const r = rewriteMarkdown([
    '合并 `abcdef1` 到 main',
    '颜色 #a1b2c3d 不变',
    '哈希示例 `deadbeef` 保留',
    '（提交 1234567）',
    '（abcdef2）',
    '（分支 abcdef3，合并）',
    '合并（，abcdef4，）',
  ].join('\n'), 'x.md', rules)
  expect(r.text).toContain('合并 到 main')
  expect(r.text).toContain('颜色 #a1b2c3d 不变')
  expect(r.text).toContain('哈希示例 `deadbeef` 保留')
  expect(r.text).toContain('（提交）')
  expect(r.text).toContain('（abcdef2）')
  expect(r.text).toContain('（分支，合并）')
  expect(r.text).not.toContain('（，，）')
  expect(r.counts.commit).toBe(4)
})

test('私人对话和画板链接去掉：带文字的留文字，裸地址和代码块里的也去', () => {
  const r = rewriteMarkdown([
    '见[对照图](https://claude.ai/artifact/0123abcd-ef)和裸地址 https://chatgpt.com/c/68aa-bb，结尾',
    '```',
    'open https://claude.ai/public/artifacts/xyz',
    '```',
    '公开的 [GitHub](https://github.com/x/y) 和 https://example.com 不动',
  ].join('\n'), 'x.md', [])
  expect(r.text).toContain('见对照图（私有链接）和裸地址 （私有链接），结尾')
  expect(r.text).toContain('open （私有链接）')
  expect(r.text).not.toMatch(/claude\.ai|chatgpt\.com/)
  expect(r.text).toContain('[GitHub](https://github.com/x/y) 和 https://example.com 不动')
  expect(r.counts.private).toBe(3)
})

test('断链检测报红，导出目录留着', () => {
  const root = makeRepo({ 'bad.md': '[缺](missing.md)\n' })
  const out = join(tmp(), 'out')
  const r = runExport(opts(root, out))
  expect(r.ok).toBe(false)
  expect(r.broken.some((b) => b.includes('missing.md'))).toBe(true)
  expect(existsSync(join(out, 'bad.md'))).toBe(true)
})

test('隐私扫描命中退出 1', () => {
  const root = makeRepo({ 'secret.md': '她的号是 8642097531。\n' })
  const out = join(tmp(), 'out')
  const r = runExport(opts(root, out))
  expect(r.ok).toBe(false)
  expect(r.privacy.blocking.length).toBeGreaterThan(0)
})
