// 查 .claude/skills/mygo-ui/SKILL.md 有没有和 native/ 里的代码对不上：
// 1. 超过 150 行；
// 2. 反引号里的 internal/… 路径在 native/ 下没有对应目录；
// 3. 反引号里的 包名.名字 在 native/internal/<包名>/ 的非 _test.go 文件里找不到 func 或 type；
//    包名不是本仓库的（例如 ui）跳过，输出“没核对”；
// 4. 写的 mygo vX.Y.Z 和 native/go.mod 里 github.com/egoist/mygo 的版本不一致，或一处都没写。
// 仓库根跑：bun scripts/check-ui-skill.ts
// 测试：bun test scripts/check-ui-skill.test.ts
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'

export interface CheckDeps {
  pathExists: (p: string) => boolean // p 相对仓库根，例如 native/internal/winshell
  hasName: (pkg: string, name: string) => boolean
}

export interface CheckResult {
  errors: string[]
  skipped: string[]
  packages: number
  names: number
}

const NAME_SPAN = /^([a-z][a-z0-9]*)\.([A-Z][A-Za-z0-9_]*)$/
const INTERNAL_PATH = /^internal\/[A-Za-z0-9_./-]+$/

// 只读文本和两个回调，方便用编的数据测。
export function checkUiSkill(skill: string, goMod: string, deps: CheckDeps): CheckResult {
  const errors: string[] = []
  const skipped = new Set<string>()
  const pkgs = new Set<string>()
  const names = new Set<string>()
  const lineOf = (index: number) => skill.slice(0, index).split('\n').length

  const lines = skill.replace(/\r?\n$/, '').split(/\r?\n/)
  if (lines.length > 150) errors.push(`入口页 ${lines.length} 行，超过 150 行`)

  const spans: { text: string; index: number }[] = []
  for (const m of skill.matchAll(/`([^`\n]+)`/g)) {
    spans.push({ text: m[1].trim(), index: m.index ?? 0 })
  }

  const seenPaths = new Set<string>()
  for (const s of spans) {
    if (!INTERNAL_PATH.test(s.text)) continue
    const p = `native/${s.text.replace(/\/$/, '')}`
    if (seenPaths.has(p)) continue
    seenPaths.add(p)
    if (!deps.pathExists(p)) {
      errors.push(`第 ${lineOf(s.index)} 行：${s.text} 在 native/ 下不存在`)
    } else {
      pkgs.add(s.text.replace(/^internal\//, '').replace(/\/$/, ''))
    }
  }

  for (const s of spans) {
    const m = NAME_SPAN.exec(s.text)
    if (!m) continue
    const [, pkg, name] = m
    const pkgPath = `native/internal/${pkg}`
    if (!deps.pathExists(pkgPath)) {
      skipped.add(s.text)
      continue
    }
    if (!deps.hasName(pkg, name)) {
      errors.push(`第 ${lineOf(s.index)} 行：${s.text} 在 native/internal/${pkg}/ 下找不到`)
      continue
    }
    pkgs.add(pkg)
    names.add(s.text)
  }

  const want = /github\.com\/egoist\/mygo\s+v(\d+\.\d+\.\d+)/.exec(goMod)?.[1]
  if (!want) {
    errors.push('native/go.mod 里找不到 github.com/egoist/mygo 的版本')
  } else {
    const found = [...skill.matchAll(/mygo\s+v(\d+\.\d+\.\d+)/g)]
    if (found.length === 0) {
      errors.push('入口页没写版本，应写 mygo vX.Y.Z')
    }
    for (const m of found) {
      if (m[1] !== want) {
        errors.push(`第 ${lineOf(m.index ?? 0)} 行：mygo v${m[1]} 和 go.mod 的 v${want} 不一致`)
      }
    }
  }

  return { errors, skipped: [...skipped], packages: pkgs.size, names: names.size }
}

function hasDecl(dir: string, name: string): boolean {
  let files: string[]
  try {
    files = readdirSync(dir)
  } catch {
    return false
  }
  const funcRe = new RegExp(`\\bfunc\\s+(?:\\([^)]*\\)\\s*)?${name}\\s*\\(`)
  const typeRe = new RegExp(`\\btype\\s+${name}\\s`)
  for (const f of files) {
    if (!f.endsWith('.go') || f.endsWith('_test.go')) continue
    const text = readFileSync(join(dir, f), 'utf8')
    if (funcRe.test(text) || typeRe.test(text)) return true
  }
  return false
}

if (import.meta.main) {
  const root = resolve(import.meta.dir, '..')
  const skillPath = join(root, '.claude/skills/mygo-ui/SKILL.md')
  let skill = ''
  let goMod = ''
  try {
    skill = readFileSync(skillPath, 'utf8')
    goMod = readFileSync(join(root, 'native/go.mod'), 'utf8')
  } catch (e) {
    console.log(`读不到要查的文件：${e instanceof Error ? e.message : e}`)
    process.exit(1)
  }
  const r = checkUiSkill(skill, goMod, {
    pathExists: (p) => {
      try {
        return statSync(join(root, p)).isDirectory()
      } catch {
        return false
      }
    },
    hasName: (pkg, name) => hasDecl(join(root, 'native/internal', pkg), name),
  })
  if (r.errors.length) {
    console.log(`入口页和代码对不上：\n${r.errors.map((e) => `- ${e}`).join('\n')}`)
    if (r.skipped.length) console.log(`没核对：${r.skipped.join('、')}`)
    process.exit(1)
  }
  console.log(`核对了 ${r.packages} 个包、${r.names} 个名字，跳过 ${r.skipped.length} 个`)
  if (r.skipped.length) console.log(`没核对：${r.skipped.join('、')}`)
}
