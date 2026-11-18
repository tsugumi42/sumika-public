/**
 * 取官方发布的 onnxruntime.dll，放到 vendor/ort/<版本>/onnxruntime.dll（规格-Go本地模型 2.3、GM2）。
 *
 * **默认干跑**：只打印要下载的地址、压缩包的期望 SHA-256 和落地位置，不联网、不写任何东西，退出码 0。
 * 加 `--yes` 才真下载。下载要主人明说才跑（AGENTS：下载先问），所以这个脚本**不挂在 package.json 的 postinstall 上**，
 * 也不并进 fetch-tools.ts（那个每次 bun install 都会跑）。
 *
 * 真下载做的事：下到内存 → 先核压缩包的 SHA-256，不对就一个字节都不写 → 在临时目录解出 lib/onnxruntime.dll → 打印它的 SHA-256
 * 并和钉在下面的 dllSha256 比一次（同一个哈希登记在 native/internal/localmodels/ort/known.go 的清单里，两处要一致；
 * 不一致退出码 1，不把它放到目标位置），对上了才改名过去。目标位置已经有文件时同样和 dllSha256 比，不一致退出码 1，
 * 提示删掉重下，不覆盖。**不执行它。** 开发和对拍都用这份（ADR-233），不从别的项目的环境里拷 dll。
 * 升版本时还要人手做两件事：
 * ① 把新 dll 的哈希换进 known.go 的清单和下面的 dllSha256（重新编译、提交）；
 * ② 在 GM6 的对拍里用它重跑一遍（规格第五节"换成官方 zip 里的 dll"一行）。
 *
 * 版本和压缩包摘要钉在下面的 PINNED：1.24.4 与 Python 参考环境同版本（对拍必须同版本）；摘要是 GitHub 给发布文件算的 digest，
 * 2026-11-01 读到的。升版本：改版本号和摘要，单独一个提交，并重跑 GM6。
 */
import { existsSync, mkdirSync, readdirSync, renameSync, rmSync, statSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'

const ROOT = resolve(import.meta.dir, '..')

const PINNED = {
  version: '1.24.4',
  platform: 'win32',
  arch: 'x64',
  asset: 'onnxruntime-win-x64-1.24.4.zip',
  url: 'https://github.com/microsoft/onnxruntime/releases/download/v1.24.4/onnxruntime-win-x64-1.24.4.zip',
  zipSha256: 'd2319fddfb6ea4db99ccc4b60c85c517bcd855721f5daa6a06d40d7cb2ee2357',
  /** 压缩包里 lib/onnxruntime.dll 的 SHA-256（2026-11-02 取回后在本机算的），和 known.go 登记的是同一个。 */
  dllSha256: 'b95efb2113b603bbbf3f191061c5516a871ed546893c820e4f3b7b6c358dbf2a',
  sizeHint: '约 71 MB',
}

const target = join(ROOT, 'vendor', 'ort', PINNED.version, 'onnxruntime.dll')

function usage(): never {
  console.error('用法：bun scripts/fetch-ort.ts [--yes]\n  不带参数：干跑，只打印要下载什么，不联网、不写文件\n  --yes：真下载（要主人明说才跑）')
  process.exit(2)
}

const args = process.argv.slice(2)
for (const a of args) if (a !== '--yes' && a !== '--help' && a !== '-h') { console.error(`不认识的参数：${a}`); usage() }
if (args.includes('--help') || args.includes('-h')) usage()
const yes = args.includes('--yes')

function printPlan() {
  console.log(`onnxruntime ${PINNED.version}（${PINNED.platform}/${PINNED.arch}）`)
  console.log(`  地址：${PINNED.url}（${PINNED.sizeHint}）`)
  console.log(`  压缩包期望的 SHA-256：${PINNED.zipSha256}`)
  console.log(`  解出 lib\\onnxruntime.dll，放到：${target}`)
  console.log(`  取回后核 lib\\onnxruntime.dll 的 SHA-256：${PINNED.dllSha256}（也登记在 known.go 里）`)
}

function findFile(dir: string, suffix: string): string | undefined {
  for (const rel of readdirSync(dir, { recursive: true, encoding: 'utf8' })) {
    const p = join(dir, rel)
    if (p.replace(/\\/g, '/').endsWith(suffix) && statSync(p).isFile()) return p
  }
}

const sha256 = (bytes: Uint8Array) => new Bun.CryptoHasher('sha256').update(bytes).digest('hex')

/** 真下载。只有带 --yes 才会走到这里。 */
async function download() {
  if (process.platform !== PINNED.platform || process.arch !== PINNED.arch) {
    throw new Error(`这个平台（${process.platform}/${process.arch}）没有钉住的包；要用就在 PINNED 里补上官方发布文件的地址和 SHA-256`)
  }
  if (existsSync(target)) {
    const have = sha256(new Uint8Array(await Bun.file(target).arrayBuffer()))
    if (have !== PINNED.dllSha256) {
      throw new Error(`${target} 已存在，但 SHA-256 和钉住的不一样（要 ${PINNED.dllSha256}，现有 ${have}）：这不是钉住的那份，不拿来用也不覆盖；确认没用的话删掉它再重新下载`)
    }
    console.log(`已存在，不重复下载：${target}（SHA-256 ${have}，和钉住的一致）`)
    return
  }
  console.log(`下载 ${PINNED.url}`)
  const res = await fetch(PINNED.url, { signal: AbortSignal.timeout(300_000) })
  if (!res.ok) throw new Error(`下载失败：${res.status} ${PINNED.url}`)
  const bytes = new Uint8Array(await res.arrayBuffer())
  const got = sha256(bytes)
  if (got !== PINNED.zipSha256) throw new Error(`压缩包的 SHA-256 对不上，什么都没写（要 ${PINNED.zipSha256}，下到的是 ${got}）`)

  const tmp = join(ROOT, 'vendor', 'ort', `.tmp-${PINNED.version}`)
  rmSync(tmp, { recursive: true, force: true })
  mkdirSync(tmp, { recursive: true })
  try {
    const zip = join(tmp, PINNED.asset)
    await Bun.write(zip, bytes)
    // 解压用 zip.js（拒绝跑出解包目录的路径）；放在这里才 import，干跑不依赖 node_modules
    const { unzipTo } = await import('./lib/zip')
    await unzipTo(zip, tmp)
    const dll = findFile(tmp, '/lib/onnxruntime.dll')
    if (!dll) throw new Error(`压缩包里没有 lib/onnxruntime.dll（解出了 ${basename(tmp)} 下的这些：${readdirSync(tmp).join('、')}）`)
    // 先在临时目录里核对，对得上才改名到目标位置；对不上就不留任何东西在目标位置（finally 会清掉临时目录）
    const dllSha = sha256(new Uint8Array(await Bun.file(dll).arrayBuffer()))
    console.log(`  lib\\onnxruntime.dll 的 SHA-256：${dllSha}`)
    if (dllSha !== PINNED.dllSha256) {
      throw new Error(`dll 的 SHA-256 和钉住的不一样（要 ${PINNED.dllSha256}），没有放到 ${target}：先查清楚再用；换了版本就同时改 known.go 的清单和这里的 dllSha256，GM6 在新 dll 上重跑`)
    }
    mkdirSync(join(ROOT, 'vendor', 'ort', PINNED.version), { recursive: true })
    renameSync(dll, target)
  } finally {
    rmSync(tmp, { recursive: true, force: true })
  }
  console.log(`完成：${target}`)
  console.log('  和钉住的、known.go 登记的一致。')
  console.log('  没有执行它。')
}

printPlan()
if (!yes) {
  console.log('\n干跑：没有联网，没有写任何文件。要真下载，等主人明说"下载"，再加 --yes。')
  process.exit(0)
}
try {
  await download()
} catch (e) {
  console.error(`出错：${(e as Error).message}`)
  process.exit(1)
}
