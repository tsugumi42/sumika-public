// scripts/lib/sumika-dev.ts 和 lefthook.yml 里“隐私扫描”那一行：环境里带着 SUMIKA_SCAN_DATA（能把名单改指到别的目录，只给测试用）时，
// 生产路径不被改道，仍读“真实位置”的名单。真实位置在这里是一个编造的临时仓库：它带一份和真程序一模一样的源码和程序（摘要对得上，不会重编），
// 名单里只有一个编造号码；SUMIKA_SCAN_DATA 指向一个没有名单的空目录，被改道的话扫描会读到空名单、退出 2 或放行，而不是拦下。
import { Database } from 'bun:sqlite'
import { afterAll, beforeAll, expect, test } from 'bun:test'
import { copyFileSync, cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { devEnv, devExe } from './lib/sumika-dev.ts'

const repoRoot = join(import.meta.dir, '..')
const dirs: string[] = []
afterAll(() => { for (const d of dirs) rmSync(d, { recursive: true, force: true }) })
const tmp = () => { const d = mkdtempSync(join(tmpdir(), 'sumika-dev-test-')); dirs.push(d); return d }
const FAKE_NUMBER = '8642097531' // 编的

const cleanEnv = (extra: Record<string, string> = {}): Record<string, string> => {
  const env: Record<string, string> = {}
  for (const [k, v] of Object.entries(process.env)) if (v !== undefined && !k.startsWith('GIT_')) env[k] = v
  return { ...env, ...extra }
}

let fake = ''
let emptyData = ''

beforeAll(() => {
  // 先让真程序编到最新（正常路径，第一次可能要重编，慢）
  const real = devExe(repoRoot)
  const v = Bun.spawnSync([real, 'version'], { env: devEnv(), stdout: 'pipe', stderr: 'pipe' })
  if (v.exitCode !== 0) throw new Error(`sumika-dev 起不来：${v.stderr.toString()}`)
  // 假仓库：同样的源码（SourceDirs 加 go.mod、go.sum）和同一个程序，摘要对得上
  fake = tmp()
  mkdirSync(join(fake, 'native/bin'), { recursive: true })
  for (const f of ['go.mod', 'go.sum']) copyFileSync(join(repoRoot, 'native', f), join(fake, 'native', f))
  for (const d of ['cmd/sumika-dev', 'internal/devtools', 'internal/jsjson', 'internal/netsafe'])
    cpSync(join(repoRoot, 'native', d), join(fake, 'native', d), { recursive: true })
  copyFileSync(real, join(fake, 'native/bin/sumika-dev.exe'))
  // 编造的“真实位置”：名单里一个编造号码
  mkdirSync(join(fake, 'data/dev/human-replies'), { recursive: true })
  writeFileSync(join(fake, 'data/dev/human-replies/groups.json'), JSON.stringify({ g: FAKE_NUMBER }))
  // 聊天库缺了程序就拒绝扫描（退出 2），所以放一个只有表头的空库
  mkdirSync(join(fake, 'data/go/chat'), { recursive: true })
  const db = new Database(join(fake, 'data/go/chat/qq.db'))
  db.run('CREATE TABLE events(actor_kind TEXT, actor_id TEXT, conversation TEXT)')
  db.run('CREATE TABLE qq_people(uid TEXT)')
  db.run('CREATE TABLE qq_groups(gid TEXT, name TEXT)')
  db.close()
  writeFileSync(join(fake, '.gitignore'), 'data/\nnative/\n')
  writeFileSync(join(fake, 'a.md'), `号 ${FAKE_NUMBER}\n`)
  const git = (...a: string[]) => {
    const p = Bun.spawnSync(['git', ...a], { cwd: fake, env: cleanEnv(), stdout: 'pipe', stderr: 'pipe' })
    if (p.exitCode !== 0) throw new Error(`git ${a.join(' ')} 失败：${p.stderr.toString()}`)
  }
  git('init', '-q')
  git('add', '.gitignore', 'a.md')
  emptyData = tmp() // 没有任何名单的空目录：被 SUMIKA_SCAN_DATA 改道到这里，扫描读不到号码
}, 300_000)

// lefthook.yml 里“隐私扫描”那一行（run: 后面双引号里的整行 sh）
function hookLine(): string {
  const yml = readFileSync(join(repoRoot, 'lefthook.yml'), 'utf8')
  const m = /\n {4}隐私扫描:\r?\n(?: {6}#[^\n]*\n)*? {6}run: "([^\n]*)"\r?\n/.exec(yml)
  if (!m) throw new Error('lefthook.yml 里找不到“隐私扫描”那一行')
  return m[1]!
}
const runSh = (line: string, env: Record<string, string>) => {
  const p = Bun.spawnSync(['sh', '-c', line], { cwd: fake, env, stdout: 'pipe', stderr: 'pipe' })
  return { code: p.exitCode ?? -1, out: p.stdout.toString() + p.stderr.toString() }
}
const polluted = () => cleanEnv({ SUMIKA_SCAN_DATA: emptyData, SUMIKA_DEV_NO_REBUILD: '1', SUMIKA_DEV_ROOT: join(emptyData, 'nowhere') })

test('钩子那一行清掉环境里的 SUMIKA_SCAN_DATA：仍读真实位置的名单，拦下编造号码', () => {
  const r = runSh(hookLine(), polluted())
  expect(r.code).toBe(1)
  expect(r.out).toContain('拦 号码')
  expect(r.out).not.toContain(FAKE_NUMBER)
  expect(r.out).not.toContain('SUMIKA_SCAN_DATA')
})

test('改坏检查：钩子那一行不清它，扫描就读空名单、拒绝（这个测试能看出漏清）', () => {
  const r = runSh(hookLine().replace(' -u SUMIKA_SCAN_DATA', ''), polluted())
  expect(r.code).toBe(2)
  expect(r.out).toContain('警告：名单目录被 SUMIKA_SCAN_DATA 改到了')
})

test('sumika-dev.ts 的生产路径默认删掉 SUMIKA_SCAN_DATA：仍读真实位置的名单', () => {
  const env = devEnv(undefined, { ...cleanEnv(), SUMIKA_SCAN_DATA: emptyData })
  expect(env.SUMIKA_SCAN_DATA).toBeUndefined()
  const spawnEnv = Object.fromEntries(Object.entries(env).filter(([, v]) => v !== undefined)) as Record<string, string>
  const p = Bun.spawnSync([devExe(fake), 'check', 'private', '--staged'], { cwd: fake, env: spawnEnv, stdout: 'pipe', stderr: 'pipe' })
  const out = p.stdout.toString() + p.stderr.toString()
  expect(p.exitCode).toBe(1)
  expect(out).toContain('拦 号码')
  expect(out).not.toContain('SUMIKA_SCAN_DATA')
})

test('只有测试显式传 testDataDir 才保留，程序会警告', () => {
  const env = devEnv({ testDataDir: emptyData }, { ...process.env })
  expect(env.SUMIKA_SCAN_DATA).toBe(emptyData)
  const p = Bun.spawnSync([devExe(fake), 'check', 'private', '--staged'], { cwd: fake, env: cleanEnv({ SUMIKA_SCAN_DATA: emptyData }), stdout: 'pipe', stderr: 'pipe' })
  const out = p.stdout.toString() + p.stderr.toString()
  expect(p.exitCode).toBe(2)
  expect(out).toContain('警告：名单目录被 SUMIKA_SCAN_DATA 改到了')
  expect(out).toContain('只该在测试里用')
})
