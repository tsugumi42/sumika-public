// 画师确认页的生成脚本（规格-画师原图 第七节）：读关注列表 following.json，按 native/internal/artists/clues.json 的线索表打分分类，
// 写 classified.json 和自包含的 review.html（不联网）。线索表和 Go 的判断函数是同一份，--check 用 testdata/clue-samples.json 对一遍。
//   bun scripts/artist-review.ts --dir <含 following.json 的目录>   生成（已有的 classified.json、review.html 先备份成 .v1 / .prev 再写）
//   bun scripts/artist-review.ts --check                           只对样例，两边得分和命中线索不一样就退出码 1
// 关注列表是私人数据，只放 data/dev/（被 .gitignore 忽略）；这个脚本里不放任何真实账号。
import { copyFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

const ROOT = resolve(import.meta.dir, '..')
const CLUES = join(ROOT, 'native/internal/artists/clues.json')
const SAMPLES = join(ROOT, 'native/internal/artists/testdata/clue-samples.json')
const TEMPLATE = join(ROOT, 'scripts/artist-review.template.html')

export interface Profile {
  name?: string
  bio?: string
  location?: string
  links?: string[]
  mediaCount?: number
  statuses?: number
}
interface Clue {
  name: string
  weight: number
  scope: 'profile' | 'tweet'
  pattern: string
}
interface Table {
  clues: Clue[]
  ratio: { name: string; minRatio: number; minStatuses: number; weight: number }[]
  thresholds: { autoPending: number; followingArtist: number; followingMaybe: number }
}

export function loadTable(path = CLUES): { table: Table; res: RegExp[] } {
  const table = JSON.parse(readFileSync(path, 'utf8')) as Table
  // Go 用 RE2 的 (?i)，这里用 i + u 标志；线索表里只写两边都认的写法
  return { table, res: table.clues.map((c) => new RegExp(c.pattern, 'iu')) }
}

export function judge(t: { table: Table; res: RegExp[] }, p: Profile, tweet = ''): { score: number; clues: { name: string; weight: number }[] } {
  const profile = [p.name ?? '', p.bio ?? '', p.location ?? '', ...(p.links ?? [])].join('\n')
  const clues: { name: string; weight: number }[] = []
  let score = 0
  t.table.clues.forEach((c, i) => {
    const text = c.scope === 'tweet' ? tweet : profile
    if (text !== '' && t.res[i].test(text)) {
      clues.push({ name: c.name, weight: c.weight })
      score += c.weight
    }
  })
  const media = p.mediaCount ?? -1
  const statuses = p.statuses ?? 0
  if (statuses > 0 && media >= 0) {
    const ratio = media / statuses
    for (const r of t.table.ratio) {
      if (statuses >= r.minStatuses && ratio >= r.minRatio) {
        clues.push({ name: r.name, weight: r.weight })
        score += r.weight
        break
      }
    }
  }
  return { score, clues }
}

export function classify(t: { table: Table }, score: number): '画师' | '可能是' | '不是' {
  if (score >= t.table.thresholds.followingArtist) return '画师'
  if (score >= t.table.thresholds.followingMaybe) return '可能是'
  return '不是'
}

function check(): number {
  const t = loadTable()
  const samples = JSON.parse(readFileSync(SAMPLES, 'utf8')) as (Profile & { tweet: string; score: number; clues: string[] })[]
  let bad = 0
  samples.forEach((s, i) => {
    const j = judge(t, { name: s.name, bio: s.bio, location: s.location, links: s.links, mediaCount: s.mediaCount, statuses: s.statuses }, s.tweet)
    const names = j.clues.map((c) => c.name)
    if (j.score !== s.score || JSON.stringify(names) !== JSON.stringify(s.clues)) {
      bad++
      console.error(`样例 ${i}：脚本得到 ${j.score} ${JSON.stringify(names)}，样例文件里是 ${s.score} ${JSON.stringify(s.clues)}`)
    }
  })
  console.log(bad === 0 ? `线索表样例 ${samples.length} 条，和 Go 一致` : `${bad} 条不一致`)
  return bad === 0 ? 0 : 1
}

interface User {
  id: string
  screen_name: string
  name?: string
  bio?: string
  bio_urls?: string[]
  profile_url_field?: string[]
  location?: string
  followers?: number
  verified?: boolean
}

function generate(dir: string): void {
  const src = join(dir, 'following.json')
  if (!existsSync(src)) throw new Error(`${src} 不存在`)
  const t = loadTable()
  const users = (JSON.parse(readFileSync(src, 'utf8')) as { users: User[] }).users
  const rows = users.map((u) => {
    const links = [...(u.bio_urls ?? []), ...(u.profile_url_field ?? [])].map(String)
    const j = judge(t, { name: u.name, bio: u.bio, location: u.location, links })
    return {
      id: String(u.id),
      screen_name: u.screen_name,
      name: u.name ?? '',
      bio: u.bio ?? '',
      bio_urls: u.bio_urls ?? [],
      profile_url_field: u.profile_url_field ?? [],
      location: u.location ?? '',
      followers: u.followers ?? 0,
      verified: u.verified ?? false,
      score: j.score,
      hits: j.clues,
      suggested: classify(t, j.score),
    }
  })
  const tally = { 画师: 0, 可能是: 0, 不是: 0 }
  rows.forEach((r) => tally[r.suggested]++)
  for (const f of ['classified.json', 'review.html']) {
    const p = join(dir, f)
    if (existsSync(p)) {
      const bak = join(dir, f.replace(/\.(json|html)$/, '.v1.$1'))
      if (!existsSync(bak)) copyFileSync(p, bak) // 第一次覆盖前留一份旧版；已经有了就不再盖
    }
  }
  writeFileSync(join(dir, 'classified.json'), JSON.stringify({ total: rows.length, tally, rows }, null, 1))
  // 页面里每行带一个小写的搜索串；JSON 里的 < 换成 <，免得数据里出现 </script> 把页面截断
  const data = rows.map((r) => ({
    id: r.id,
    screen_name: r.screen_name,
    name: r.name,
    bio: r.bio,
    followers: r.followers,
    score: r.score,
    hits: r.hits,
    suggested: r.suggested,
    search: `${r.name} ${r.screen_name} ${r.bio}`.toLowerCase(),
  }))
  const bs = String.fromCharCode(92) // 反斜杠
  const json = JSON.stringify(data)
    .split('<').join(bs + 'u003c')
    .split(String.fromCharCode(0x2028)).join(bs + 'u2028')
    .split(String.fromCharCode(0x2029)).join(bs + 'u2029')
  const tpl = readFileSync(TEMPLATE, 'utf8')
  if (!tpl.includes('/*DATA*/[]')) throw new Error('模板里没有数据占位')
  writeFileSync(join(dir, 'review.html'), tpl.replace('/*DATA*/[]', () => json))
  console.log(`共 ${rows.length} 人：画师 ${tally.画师}、可能是 ${tally.可能是}、不是 ${tally.不是}`)
}

if (import.meta.main) {
  const args = process.argv.slice(2)
  if (args.includes('--check')) process.exit(check())
  const i = args.indexOf('--dir')
  if (i < 0 || !args[i + 1]) {
    console.error('用法：bun scripts/artist-review.ts --dir <目录> 或 --check')
    process.exit(2)
  }
  if (check() !== 0) process.exit(1) // 先确认脚本和 Go 的线索表一致，再生成
  generate(resolve(args[i + 1]))
}
