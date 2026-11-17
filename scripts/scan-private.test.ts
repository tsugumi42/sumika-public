// 隐私扫描（scripts/scan-private.ts）。号码、名字、邮箱全是编的。
import { Database } from 'bun:sqlite'
import { describe, expect, test } from 'bun:test'
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { allowKey, collect, loadKnown, maskWhere, scan, SourceError, type Known } from './scan-private.ts'

const FAKE_KEY = ['sk', 'abcdefghijklmnopqrstuv'].join('-')   // 拼出来：源码里直接写会被提交前的扫描自己拦下
const REAL = '8642097531', GROUP = '97531864', OLD = '54321'   // 编的“本机认识的号”：两个长的、一个老式 5 位
const CTX = ['2468', '1357'].join(''), CTX9 = CTX + '9', PHONE = ['139', '12345678'].join('')   // 拼出来，同上
const MAIL = ['someone.fake', 'mail.invalid.zz'].join('@')
const known: Known = { numbers: new Set([REAL, GROUP, OLD]), names: new Set(['编的昵称甲', 'Zorvex']), secrets: new Set([MAIL]) }
const kinds = (text: string) => scan([{ where: 'a.md:1', text, file: 'a.md' }], known).map((h) => `${h.block ? '拦' : '提醒'}:${h.value}`)
const blocked = (text: string) => kinds(text).some((k) => k.startsWith('拦:'))

describe('号码', () => {
  test.each([
    [`号是 ${REAL}`], [`qq_group_${GROUP}`], [`onebot_${REAL}.json`], [`"uid":"${REAL}"`], [`qq:private:${REAL}:${GROUP}`],
    [`https://example.invalid/u/${REAL}`], [`bug ${REAL}`], [`max-age=${GROUP}`], [`v${REAL}`], [`老号 ${OLD} 在群里`], [`#${GROUP}`],
  ])('认识的号码一律拦：%s', (text) => expect(blocked(text)).toBe(true))

  test('认识的号码不算在更长的数字里', () => expect(blocked(`1${REAL}9`)).toBe(false))

  test.each([
    ['Firefox bug 1882408 修了', []], ['Chromium bug 343503163', []], ['max-age=31536000', []], ['00000000-0000-0000', []],
    ['https://x.invalid/status/2120201406820884794', []], ['日期 20261111', []], ['时间戳 1789000000', []], ['版本 1.23456789.0', []],
    ['陌生数字 13572468', ['提醒:13572468']], ['另一个 2468013579', ['提醒:2468013579']],
  ])('不认识、没有号码上下文的数字不拦：%s', (text, want) => expect(kinds(text)).toEqual(want))

  // 本机库里没有的人，但写法一看就是号（AI 会话从本机 QQ 记录贴样本的情形）
  test.each([[`qq: ${CTX}`], [`群号 ${CTX}`], [`uin=${CTX9}`], [`user_id "${CTX}"`], [`好友 ${CTX9}`]])('号码上下文里的陌生数字拦：%s', (text) => {
    expect(blocked(text)).toBe(true)
  })

  test('11 位手机号拦，网址里的不算', () => {
    expect(kinds(`电话 ${PHONE}`)).toEqual([`拦:${PHONE}`])
    expect(kinds(`https://x.invalid/${PHONE}`)).toEqual([])
  })
})

describe('名字、密钥、邮箱', () => {
  test('名字和密钥照旧拦；英文名字要整词；sk- 要在词头', () => {
    expect(kinds('昨天编的昵称甲说')).toEqual(['拦:编的昵称甲'])
    expect(kinds('Zorvex said')).toEqual(['拦:Zorvex'])
    expect(kinds('AZorvexB')).toEqual([])
    expect(kinds(`key = ${FAKE_KEY}`).some((k) => k.startsWith('拦:sk-'))).toBe(true)
    expect(kinds('desktop-windows-25-cpu-5-mb-s-sustained')).toEqual([])
  })

  test.each([[MAIL.toUpperCase()], [`<${MAIL}>`], [`mailto:${MAIL}`], [MAIL.replace('@', '+work@')], [MAIL.replace('@', '%40')]])('名单里的邮箱各种写法都拦：%s', (text) => {
    expect(kinds(text).some((k) => k.startsWith('拦:'))).toBe(true)
  })

  test('别的邮箱只提醒；noreply、示例域名不提醒', () => {
    expect(kinds('联系 stranger@corp-made-up.io')).toEqual(['提醒:stranger@corp-made-up.io'])
    expect(kinds('12345+someone@users.noreply.github.com、a@example.com、b@x.invalid、noreply@anthropic.com')).toEqual([])
  })

  test('锁文件不扫，别的文件名里带 lock 照扫', () => {
    expect(scan([{ where: 'bun.lock:3', text: REAL, file: 'bun.lock' }], known)).toEqual([])
    expect(scan([{ where: 'x/unlock.md:3', text: REAL, file: 'x/unlock.md' }], known).length).toBe(1)
  })
})

describe('读 diff', () => {
  const diff = [
    'diff --git a/a.md b/a.md', 'index 1..2 100644', '--- a/a.md', '+++ b/a.md', '@@ -0,0 +1,2 @@',
    `++ b/x ${REAL}`, '+普通的一行',
    `diff --git a/qq_${GROUP}.json b/qq_${GROUP}.json`, 'new file mode 100644', '--- /dev/null', `+++ b/qq_${GROUP}.json`, '@@ -0,0 +1 @@', '+{}',
    'diff --git a/old.md b/new.md', 'similarity index 90%', 'rename from old.md', 'rename to new.md',
  ].join('\n')
  const lines = collect(diff, (f, n) => `${f}:${n}`)

  test('正文里以 ++ 开头的新增行照样扫，不当文件头', () => {
    expect(lines.find((l) => l.text.includes(REAL))).toMatchObject({ where: 'a.md:1', file: 'a.md' })
    expect(lines.find((l) => l.text === '普通的一行')?.where).toBe('a.md:2')
  })
  test('文件名、改名后的名字也作为一行扫', () => {
    expect(scan(lines, known).some((h) => h.value === GROUP && h.where.includes('文件名'))).toBe(true)
    expect(lines.some((l) => l.text === 'new.md' && l.where.includes('文件名'))).toBe(true)
  })
  test('位置里的号码和名字打码再打印', () => {
    expect(maskWhere(`qq_${GROUP}.json:1 编的昵称甲`, known)).not.toContain(GROUP)
    expect(maskWhere(`qq_${GROUP}.json:1 编的昵称甲`, known)).not.toContain('编的昵称甲')
  })
})

test('误报登记：认识的号码和私人字符串只对那个文件放行，其余按值', () => {
  const [a] = scan([{ where: 'a.md:1', text: REAL, file: 'a.md' }], known)
  const [b] = scan([{ where: 'b.md:1', text: REAL, file: 'b.md' }], known)
  expect(allowKey(a!)).not.toBe(allowKey(b!))
  const [w1] = scan([{ where: 'a.md:1', text: '陌生数字 13572468', file: 'a.md' }], known)
  const [w2] = scan([{ where: 'b.md:1', text: '陌生数字 13572468', file: 'b.md' }], known)
  expect(allowKey(w1!)).toBe(allowKey(w2!))
  // 先按值登了不认识的数字，后来它进了名单：按值的登记对“号码”类不生效
  const later: Known = { ...known, numbers: new Set([...known.numbers, '13572468']) }
  const [now] = scan([{ where: 'a.md:1', text: '陌生数字 13572468', file: 'a.md' }], later)
  expect(now!.kind).toBe('号码')
  expect(allowKey(now!)).not.toBe(allowKey(w1!))
})

// ───── 名单 ─────
function fixtures(dir: string) {
  const qq = new Database(join(dir, 'qq.db'))
  qq.run('CREATE TABLE events(actor_kind TEXT, actor_id TEXT, conversation TEXT)')
  qq.run('CREATE TABLE qq_people(uid TEXT, nickname TEXT, cards_json TEXT, aliases_json TEXT)')
  qq.run('CREATE TABLE qq_groups(gid TEXT, name TEXT)')
  qq.run('CREATE TABLE qq_sync(account TEXT)')
  qq.run("INSERT INTO events VALUES ('human','qq:1111222','qq:group:3333444'),('character','qq:9999000','qq:private:9999000:5555666')")
  qq.run(`INSERT INTO qq_people VALUES ('7777888','编的人名','{"g":"编的名片"}','["编的别名"]')`)
  qq.run("INSERT INTO qq_groups VALUES ('1212343','编的群名')")
  qq.run("INSERT INTO qq_sync VALUES ('3131313')")
  qq.close()
  const mb = new Database(join(dir, 'mb.db'))
  mb.run('CREATE TABLE person_info(user_id TEXT, person_name TEXT, user_nickname TEXT, group_cardname TEXT)')
  mb.run('CREATE TABLE chat_sessions(group_id TEXT, user_id TEXT, account_id TEXT, group_name TEXT)')
  mb.run('CREATE TABLE mai_messages(user_id TEXT, group_id TEXT, account_id TEXT)')
  mb.run('CREATE TABLE bot_platform_accounts(account_id TEXT)')
  mb.run("INSERT INTO person_info VALUES ('4545676','编的麦麦名',NULL,NULL)")
  mb.run("INSERT INTO chat_sessions VALUES ('8989010',NULL,'2323454','编的会话群')")
  mb.run("INSERT INTO mai_messages VALUES ('6262626',NULL,NULL)")
  mb.run("INSERT INTO bot_platform_accounts VALUES ('7373737')")
  mb.close()
  writeFileSync(join(dir, 'groups.json'), JSON.stringify({ '6767898': { name: '编的群表名' } }))
  writeFileSync(join(dir, 'c.yml'), [
    'qq:', '  accounts:', '    - id: 4242424 # 她的小号', '  owner: 5656565', '  port: 30010', '  groups:', '    - 5151515',
    '  # 群改前备份 sumika.yml.bak-20261108', 'magpie:', '  timeout: 12000000', 'linkReader:', '  expand:', '    conversations: ["qq:group:6464646"]', '',
  ].join('\n'))
  writeFileSync(join(dir, 'p.json'), JSON.stringify([MAIL.toUpperCase(), 'ab', 42]))
  return { qqDB: join(dir, 'qq.db'), maibotDB: join(dir, 'mb.db'), groupsJSON: join(dir, 'groups.json'), configYML: join(dir, 'c.yml'), privateJSON: join(dir, 'p.json') }
}

test('loadKnown 读全各来源；端口、注释、无关配置不当号码；聊天库昵称不进名字', () => {
  const dir = mkdtempSync(join(tmpdir(), 'scan-private-'))
  try {
    const { known: k, notes } = loadKnown(fixtures(dir))
    expect(notes).toEqual([])
    for (const n of ['1111222', '3333444', '5555666', '9999000', '7777888', '1212343', '3131313', '4545676', '8989010', '2323454', '6262626', '7373737', '6767898', '4242424', '5656565', '5151515', '6464646'])
      expect([n, k.numbers.has(n)]).toEqual([n, true])
    for (const n of ['12000000', '20261108', '30010']) expect([n, k.numbers.has(n)]).toEqual([n, false])
    for (const n of ['编的群名', '编的麦麦名', '编的群表名', '编的会话群']) expect(k.names.has(n)).toBe(true)
    for (const n of ['编的人名', '编的名片', '编的别名']) expect(k.names.has(n)).toBe(false)
    expect([...k.secrets!]).toEqual([MAIL])
  } finally { rmSync(dir, { recursive: true, force: true }) }
})

test('来源坏了报 SourceError，不带出内容；缺来源如实说', () => {
  const dir = mkdtempSync(join(tmpdir(), 'scan-private-'))
  try {
    const src = fixtures(dir)
    writeFileSync(src.privateJSON, `[${MAIL}]`)
    expect(() => loadKnown(src)).toThrow(SourceError)
    try { loadKnown(src) } catch (e) { expect(String(e)).not.toContain('someone') }
    writeFileSync(src.privateJSON, '{}')
    expect(() => loadKnown(src)).toThrow('不是数组')
    writeFileSync(src.privateJSON, '[]')
    writeFileSync(src.qqDB, 'not a database')
    expect(() => loadKnown(src)).toThrow(SourceError)
  } finally { rmSync(dir, { recursive: true, force: true }) }
  const { notes } = loadKnown({ qqDB: 'nope', maibotDB: 'nope', groupsJSON: 'nope', configYML: 'nope' })
  expect(notes).toEqual(['缺 Go 聊天库', '缺 MaiBot 库', '缺群名表', '缺配置文件'])
})

// ───── 命令行：拷进一个临时仓库跑，数据目录也是临时的 ─────
// 钩子里跑时 git 设了 GIT_DIR、GIT_INDEX_FILE 这些，不清掉临时仓库的 git 命令会落到真仓库上
const cleanEnv = () => Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_'))) as Record<string, string>

describe('命令行', () => {
  const run = (cwd: string, args: string[], env: Record<string, string> = {}) => {
    const p = Bun.spawnSync(['bun', join(cwd, 'scripts/scan-private.ts'), ...args], { cwd, env: { ...cleanEnv(), ...env } })
    return { code: p.exitCode, out: p.stdout.toString() + p.stderr.toString() }
  }
  const git = (cwd: string, ...a: string[]) => Bun.spawnSync(['git', '-c', 'user.name=编', '-c', `user.email=${a.includes('--author-mail') ? MAIL : 'x@example.com'}`, ...a.filter((x) => x !== '--author-mail')], { cwd, env: cleanEnv() })
  function repo() {
    const dir = mkdtempSync(join(tmpdir(), 'scan-private-cli-'))
    git(dir, 'init', '-q')
    mkdirSync(join(dir, 'scripts'))
    copyFileSync(join(import.meta.dir, 'scan-private.ts'), join(dir, 'scripts/scan-private.ts'))
    mkdirSync(join(dir, 'data/go/chat'), { recursive: true })
    mkdirSync(join(dir, 'data/dev'), { recursive: true })
    mkdirSync(join(dir, 'data/config'), { recursive: true })
    const src = fixtures(join(dir, 'data/dev'))
    copyFileSync(src.qqDB, join(dir, 'data/go/chat/qq.db'))
    copyFileSync(src.privateJSON, join(dir, 'data/dev/private-strings.json'))
    writeFileSync(join(dir, '.gitignore'), 'data/\n')
    git(dir, 'add', '.gitignore', 'scripts'); git(dir, 'commit', '-q', '-m', '起点')
    return { dir, env: { SUMIKA_SCAN_MAIBOT: src.maibotDB } }
  }

  test('暂存了认识的号码：退出码 1，输出打码；只有提醒：0；从子目录跑也一样', () => {
    const { dir, env } = repo()
    try {
      writeFileSync(join(dir, 'a.md'), '号 1111222\n')
      git(dir, 'add', 'a.md')
      let r = run(dir, ['--staged'], env)
      expect(r.code).toBe(1)
      expect(r.out).not.toContain('1111222')
      mkdirSync(join(dir, 'sub'))
      r = run(join(dir, 'sub'), ['--staged'], env)
      expect(r.code).toBe(1)
      writeFileSync(join(dir, 'a.md'), '陌生数字 13572468\n')
      git(dir, 'add', 'a.md')
      r = run(dir, ['--staged'], env)
      expect([r.code, r.out.includes('只有提醒')]).toEqual([0, true])
    } finally { rmSync(dir, { recursive: true, force: true }) }
  }, 30_000)

  test('没有聊天库：退出码 2 拒绝；--allow-missing 才照常跑', () => {
    const { dir, env } = repo()
    try {
      rmSync(join(dir, 'data/go/chat/qq.db'))
      writeFileSync(join(dir, 'a.md'), '普通\n')
      git(dir, 'add', 'a.md')
      expect(run(dir, ['--staged'], env).code).toBe(2)
      expect(run(dir, ['--staged', '--allow-missing'], env).code).toBe(0)
    } finally { rmSync(dir, { recursive: true, force: true }) }
  }, 30_000)

  test('推送范围：作者邮箱、提交说明、合并提交里改的都扫', () => {
    const { dir, env } = repo()
    try {
      const base = git(dir, 'rev-parse', 'HEAD').stdout.toString().trim()
      writeFileSync(join(dir, 'b.md'), '普通\n'); git(dir, 'add', 'b.md'); git(dir, 'commit', '-q', '-m', '普通', '--author-mail')
      let r = run(dir, ['--range', `${base}..HEAD`], env)
      expect([r.code, r.out.includes('作者和提交者')]).toEqual([1, true])
      git(dir, 'reset', '-q', '--hard', base)
      git(dir, 'commit', '-q', '--allow-empty', '-m', '说明里写了 3333444')
      expect(run(dir, ['--range', `${base}..HEAD`], env).code).toBe(1)
      // 合并时手改：冲突解决里加了认识的号码
      git(dir, 'reset', '-q', '--hard', base)
      git(dir, 'checkout', '-q', '-b', 'side'); writeFileSync(join(dir, 'c.md'), '一\n'); git(dir, 'add', 'c.md'); git(dir, 'commit', '-q', '-m', '旁')
      git(dir, 'checkout', '-q', '-'); writeFileSync(join(dir, 'c.md'), '二\n'); git(dir, 'add', 'c.md'); git(dir, 'commit', '-q', '-m', '主')
      git(dir, 'merge', '-q', 'side')
      writeFileSync(join(dir, 'c.md'), '合 5555666\n'); git(dir, 'add', 'c.md'); git(dir, 'commit', '-q', '-m', '合并')
      r = run(dir, ['--range', `${base}..HEAD`], env)
      expect(r.code).toBe(1)
    } finally { rmSync(dir, { recursive: true, force: true }) }
  }, 60_000)
})
