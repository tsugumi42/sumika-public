/**
 * 下载 Sumika 自带的 rg（ripgrep）和 fd 到 vendor/bin。
 * `bun install` 之后自动运行（package.json 的 postinstall），也可以手动 `bun run setup`。
 * 来源只有两个项目自己的官方 GitHub 发布页；已经有就跳过。Sumika 启动时把 vendor/bin 放到 PATH 最前面，
 * pi 的 grep / find 工具就会用这一份，不依赖系统里装没装。
 * 版本和压缩包的 SHA-256 钉在下面的 PINNED 里，下载后先核对再解压（2026-10-26 外部审计 F05：原来取 releases/latest、不核对，
 * bun.lock 锁不住，换台机器装出来的版本不一样，也和"发布满 1 天再升"（ADR-173、ADR-213）对不上）。摘要取自 GitHub 给每个发布文件算的 digest，
 * ripgrep 另有官方的 .sha256 文件，两边核对过一致。升级：改版本号和摘要，单独一个提交。
 */
import { existsSync, mkdirSync, readdirSync, renameSync, rmSync, statSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { unzipTo } from './lib/zip'

const ROOT = resolve(import.meta.dir, '..')
const BIN = join(ROOT, 'vendor', 'bin')
const EXE = process.platform === 'win32' ? '.exe' : ''
const ARCH = process.arch === 'arm64' ? 'aarch64' : 'x86_64'
const TRIPLE = process.platform === 'win32' ? `${ARCH}-pc-windows-msvc` : process.platform === 'darwin' ? `${ARCH}-apple-darwin` : `${ARCH}-unknown-linux-musl`
const EXT = process.platform === 'win32' ? 'zip' : 'tar.gz'

const TOOLS = [
  { bin: 'rg', repo: 'BurntSushi/ripgrep', asset: (v: string) => `ripgrep-${v}-${TRIPLE}.${EXT}`, tagPrefix: '' },
  { bin: 'fd', repo: 'sharkdp/fd', asset: (v: string) => `fd-v${v}-${TRIPLE}.${EXT}`, tagPrefix: 'v' },
]

/** 钉住的版本和各平台压缩包的 SHA-256（2026-10-26 从 GitHub 发布文件的 digest 取；ripgrep 15.2.0 发布于 2026-07-15，fd 10.5.0 于 2026-08-26） */
const PINNED: Record<string, { version: string; sha256: Record<string, string> }> = {
  rg: {
    version: '15.2.0',
    sha256: {
      'x86_64-pc-windows-msvc': '71b2fef860abe467217a538ff31de02f5258807c0129f771846f87bd029aafc5',
      'aarch64-pc-windows-msvc': 'e4abca10c3a64ebea742667dd7009449d49403db5460dd6873e389fa2945360f',
    },
  },
  fd: {
    version: '10.5.0',
    sha256: {
      'x86_64-pc-windows-msvc': 'a227701b8551c35a9931d9f6da75503cf86d88e182d71fb849a70864c5d57cd7',
      'aarch64-pc-windows-msvc': 'a2bcddcfd259b05357a77bbc6cd671fdb30f63fd266a0e748305890a8c5ceaa6',
    },
  },
}

function findFile(dir: string, name: string): string | undefined {
  for (const rel of readdirSync(dir, { recursive: true, encoding: 'utf8' })) {
    const p = join(dir, rel)
    if (basename(p) === name && statSync(p).isFile()) return p
  }
}

async function fetchTool(t: (typeof TOOLS)[number]) {
  const target = join(BIN, t.bin + EXE)
  if (existsSync(target)) return console.log(`${t.bin}：已存在，跳过`)
  const pin = PINNED[t.bin]!
  const want = pin.sha256[TRIPLE]
  if (!want) throw new Error(`这个平台（${TRIPLE}）没有钉住的摘要，不下载；要用就在 PINNED 里补上官方发布文件的 SHA-256`)
  const version = pin.version
  const asset = t.asset(version)
  const url = `https://github.com/${t.repo}/releases/download/${t.tagPrefix}${version}/${asset}`
  console.log(`${t.bin}：下载 ${version}（${url}）`)
  const res = await fetch(url, { signal: AbortSignal.timeout(120_000) })
  if (!res.ok) throw new Error(`下载失败：${res.status} ${url}`)
  const tmp = join(BIN, `.tmp-${t.bin}`)
  rmSync(tmp, { recursive: true, force: true })
  mkdirSync(tmp, { recursive: true })
  const archive = join(tmp, asset)
  const bytes = new Uint8Array(await res.arrayBuffer())
  const got = new Bun.CryptoHasher('sha256').update(bytes).digest('hex')
  if (got !== want) { rmSync(tmp, { recursive: true, force: true }); throw new Error(`摘要对不上，没装（要 ${want}，下到的是 ${got}）`) }
  await Bun.write(archive, bytes)
  // Windows 上是 zip：用内置的 zip.js 解，不起 tar（ADR-078；后台和脚本直接起外部程序头几次要卡几秒）。别的系统是 tar.gz，照旧用 tar
  if (EXT === 'zip') await unzipTo(archive, tmp)
  else {
    const tar = Bun.spawnSync(['tar', '-xf', archive, '-C', tmp])
    if (tar.exitCode !== 0) throw new Error(`解压失败：${tar.stderr.toString()}`)
  }
  const exe = findFile(tmp, t.bin + EXE)
  if (!exe) throw new Error(`压缩包里没有 ${t.bin + EXE}`)
  renameSync(exe, target)
  rmSync(tmp, { recursive: true, force: true })
  const check = Bun.spawnSync([target, '--version'])
  console.log(`${t.bin}：完成，${check.stdout.toString().split('\n')[0]}`)
}

mkdirSync(BIN, { recursive: true })
// 下载失败只警告、不让 bun install 失败：Sumika 照样能用，只是少了两个搜索工具
for (const t of TOOLS) {
  try {
    await fetchTool(t)
  } catch (e) {
    console.warn(`${t.bin}：${(e as Error).message}。搜内容 / 找文件工具暂时不可用，联网后运行 bun run setup 重试。`)
  }
}
