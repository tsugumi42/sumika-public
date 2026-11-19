// CI 跑测试前取不进 git 的测试素材：文渊圆体（推特卡片、数字滚动的真字体测试）和 Twemoji（彩色表情的测试）。
// 公开仓库不带这两样（字体 46 MB、表情四千多个文件，.gitignore 忽略），缺了那十几条测试就跳过，CI 上就一直没测（2026-11-19 对照实验）。
//
//   bun scripts/ci-assets.ts            取到 native/fonts/、native/twemoji/；已经在、内容对得上就跳过（CI 的缓存命中时）
//
// 来源只用上游官方发布，版本和 SHA-256 钉在下面，下载后先核对再落盘，对不上就退出码 1、不留半截文件：
//   - 文渊圆体：takushun-wu/WenYuanFonts 发布 v1.100 的 WenYuanRoundedSCVF.ttf，摘要是 GitHub 给发布文件算的 digest；
//     和开发机 native/fonts/ 里那份逐字节一样（不追 v1.110：卡片测试量的是真字形，换版本度量会变，升级单独一个提交）。OFL-1.1，许可证在 native/fonts/。
//   - Twemoji：jdecked/twemoji v17.0.3。没有发布附件，只有 GitHub 按标签打的源码包；摘要是 2026-11-19 两次下载核对一致的值，
//     哪天 GitHub 重新打包导致字节变了就会红在这里（不悄悄换内容）。解包交给 update-twemoji.ts 的 --tgz（白名单、数量检查都在那边），
//     装出来的 4011 个文件和开发机 native/twemoji/ 逐个一样。
// 升级：改版本号和摘要，单独一个提交；CI 缓存的键是这个文件的哈希，改了就重新下载。
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

const ROOT = resolve(import.meta.dir, '..')

export const FONT = {
  url: 'https://github.com/takushun-wu/WenYuanFonts/releases/download/v1.100/WenYuanRoundedSCVF.ttf',
  sha256: '8572e6512ca491cea70296e22604957d35046eb56cec139fd67a5ddd6e62c657',
  dest: join(ROOT, 'native', 'fonts', 'WenYuanRoundedSCVF.ttf'),
}
export const TWEMOJI = {
  version: 'v17.0.3',
  url: 'https://github.com/jdecked/twemoji/archive/refs/tags/v17.0.3.tar.gz',
  sha256: 'a0855654b633045ae2337537e77f1bb4361162f7fcd910e613eaab1d6d9c5fca',
  dir: join(ROOT, 'native', 'twemoji'),
}

export const sha256 = (b: Uint8Array) => createHash('sha256').update(b).digest('hex')

/** 下载并核对摘要；对不上抛错，调用方不落盘 */
export async function download(url: string, want: string, fetcher: typeof fetch = fetch): Promise<Uint8Array> {
  const res = await fetcher(url, { signal: AbortSignal.timeout(300_000) })
  if (!res.ok) throw new Error(`下载失败：${res.status} ${url}`)
  const bytes = new Uint8Array(await res.arrayBuffer())
  const got = sha256(bytes)
  if (got !== want) throw new Error(`摘要对不上，没装（要 ${want}，下到的是 ${got}）：${url}`)
  return bytes
}

async function font() {
  if (existsSync(FONT.dest) && sha256(readFileSync(FONT.dest)) === FONT.sha256) return console.log(`文渊圆体：已在、摘要对得上，跳过（${FONT.dest}）`)
  const bytes = await download(FONT.url, FONT.sha256)
  mkdirSync(join(FONT.dest, '..'), { recursive: true })
  writeFileSync(FONT.dest + '.part', bytes)
  renameSync(FONT.dest + '.part', FONT.dest) // 核对过才换上，中途失败不留半截
  console.log(`文渊圆体：装好了（${(bytes.length / 1048576).toFixed(1)} MB）`)
}

async function twemoji() {
  const version = join(TWEMOJI.dir, 'VERSION')
  if (existsSync(version) && readFileSync(version, 'utf8').trim() === TWEMOJI.version) return console.log(`Twemoji：已在 ${TWEMOJI.version}，跳过`)
  const bytes = await download(TWEMOJI.url, TWEMOJI.sha256)
  const tgz = join(ROOT, 'native', `.twemoji-${TWEMOJI.version}.tar.gz`)
  writeFileSync(tgz, bytes)
  try {
    const p = Bun.spawnSync([process.execPath, join(import.meta.dir, 'update-twemoji.ts'), '--tgz', tgz, '--version', TWEMOJI.version, '--yes', '--out', TWEMOJI.dir], { stdout: 'inherit', stderr: 'inherit' })
    if (p.exitCode !== 0) throw new Error(`update-twemoji.ts 退出码 ${p.exitCode}`)
  } finally {
    rmSync(tgz, { force: true })
  }
}

if (import.meta.main) {
  let bad = false
  for (const [name, step] of [['文渊圆体', font], ['Twemoji', twemoji]] as const) {
    try {
      await step()
    } catch (e) {
      console.error(`::error::${name}：${(e as Error).message}`)
      bad = true
    }
  }
  if (bad) process.exit(1)
}
