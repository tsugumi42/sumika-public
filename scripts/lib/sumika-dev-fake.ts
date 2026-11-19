// 脚本测试要用“编造的名单”跑真的 sumika-dev check private 时，造一个假仓库根：带一份和真程序一模一样的源码和程序（摘要对得上，不会重编）、
// git init、data/ 里放编造的名单。程序按自己所在的仓库根找名单，不用 SUMIKA_SCAN_DATA（那个只在测试构建里认，生产构建见到就拒绝）。
import { copyFileSync, cpSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { Database } from 'bun:sqlite'
import { devExe } from './sumika-dev'

export type FakeScanOpts = {
  groups?: Record<string, string> // data/dev/human-replies/groups.json 的内容（号码放这里）
  privateStrings?: string[] // data/dev/private-strings.json（推送前 --range 要求它不为空）
  chatDb?: boolean // 放一个只有表头的聊天库（--staged 缺它会拒绝）
}

/** 在 dir 里造假仓库根，返回里面的 sumika-dev 程序路径。dir 要是个新的空目录。 */
export function makeFakeScanRoot(repoRoot: string, dir: string, opts: FakeScanOpts = {}): string {
  const real = devExe(repoRoot)
  mkdirSync(join(dir, 'native/bin'), { recursive: true })
  for (const f of ['go.mod', 'go.sum']) copyFileSync(join(repoRoot, 'native', f), join(dir, 'native', f))
  for (const d of ['cmd/sumika-dev', 'internal/devtools', 'internal/jsjson', 'internal/netsafe'])
    cpSync(join(repoRoot, 'native', d), join(dir, 'native', d), { recursive: true })
  const exe = join(dir, 'native/bin/sumika-dev.exe')
  copyFileSync(real, exe)
  mkdirSync(join(dir, 'data/dev/human-replies'), { recursive: true })
  writeFileSync(join(dir, 'data/dev/human-replies/groups.json'), JSON.stringify(opts.groups ?? {}))
  if (opts.privateStrings) writeFileSync(join(dir, 'data/dev/private-strings.json'), JSON.stringify(opts.privateStrings))
  if (opts.chatDb) {
    mkdirSync(join(dir, 'data/go/chat'), { recursive: true })
    const db = new Database(join(dir, 'data/go/chat/qq.db'))
    db.run('CREATE TABLE events(actor_kind TEXT, actor_id TEXT, conversation TEXT)')
    db.run('CREATE TABLE qq_people(uid TEXT)')
    db.run('CREATE TABLE qq_groups(gid TEXT, name TEXT)')
    db.close()
  }
  writeFileSync(join(dir, '.gitignore'), 'data/\nnative/\n')
  const env = Object.fromEntries(Object.entries(process.env).filter(([k, v]) => v !== undefined && !k.startsWith('GIT_'))) as Record<string, string>
  const p = Bun.spawnSync(['git', 'init', '-q'], { cwd: dir, env, stdout: 'pipe', stderr: 'pipe' })
  if (p.exitCode !== 0) throw new Error(`git init 失败：${p.stderr.toString()}`)
  return exe
}
