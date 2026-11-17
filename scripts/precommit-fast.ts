// 提交钩子里只跑这次改到的那一小块（AGENTS 验收：“钩子只管快检查”）；整套交给合并队列（go-verify、server-verify）。
// 2026-11-12：提交钩子改到 server/ 就跑整套 2835 条（两三分钟）、改到 .go 就 vet 整个模块（30 秒），
// 别的会话宁可不提交也不想等；同一份代码在队列、推送时还要各跑一遍。
//
//   bun scripts/precommit-fast.ts server-tests <暂存的文件…>   只跑暂存里 server/test 下的测试文件；改了 server/test 下的辅助文件（不是 .test）就跑整套；都没有就过
//   bun scripts/precommit-fast.ts go-vet <暂存的文件…>         只 vet 暂存的 .go 所在的包（含 _test.go）；改了 go.mod / go.sum 就 vet 整个模块；都没有就过
//
// 退出码照实：任何一步红都是 1。lefthook 传进来的路径相对仓库根、用正斜杠。
import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { bunParallel } from './server-verify'

type Env = Record<string, string | undefined>
const norm = (f: string) => f.replace(/\\/g, '/').replace(/^\.\//, '')

/** 暂存里属于 server/test 的测试文件，路径改成相对 server/（bun test 在 server/ 里跑） */
export function serverTestFiles(files: string[]): string[] {
  return [...new Set(files.map(norm).filter((f) => /^server\/test\/.+\.test\.tsx?$/.test(f)).map((f) => './' + f.slice('server/'.length)))].sort()
}

/** 暂存里有 server/test 下的非测试文件（helpers 等，所有测试都可能引）：要跑整套 */
export function serverNeedsAll(files: string[]): boolean {
  return files.map(norm).some((f) => /^server\/test\/.+\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f))
}

/** 暂存的 .go 所在的包目录，相对 native/，写成 ./dir 的形式（根包是 .）；testdata 下的 .go 不是包，跳过 */
export function goPackages(files: string[]): string[] {
  if (files.map(norm).some((f) => f === 'native/go.mod' || f === 'native/go.sum')) return ['./...']   // 依赖变了，哪个包都可能受影响
  const dirs = files.map(norm).filter((f) => f.startsWith('native/') && f.endsWith('.go') && !/(^|\/)testdata\//.test(f))
    .map((f) => dirname(f.slice('native/'.length)))
  return [...new Set(dirs.map((d) => (d === '.' ? '.' : './' + d)))].sort()
}

function run(cmd: string[], cwd: string, env: Env): number {
  console.log(`$ ${cmd.join(' ')}   （在 ${cwd}）`)
  const p = Bun.spawnSync(cmd, { cwd, env, stdout: 'inherit', stderr: 'inherit' })
  return p.exitCode ?? 1
}

if (import.meta.main) {
  const [mode, ...files] = process.argv.slice(2)
  const root = process.cwd()
  // 钩子里带着 GIT_DIR、GIT_INDEX_FILE：传给子进程前清掉，免得以后有测试起 git 时落到真仓库上（2026-11-11 core.bare 事故）
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_')))
  if (mode === 'server-tests') {
    // 公开导出的仓库没有 server/：lefthook 的 glob 本来也匹配不到，这里再挡一次，手动调用时也跳过
    if (!existsSync(join(root, 'server'))) { console.log('没有 server/，跳过 server 测试'); process.exit(0) }
    if (serverNeedsAll(files)) process.exit(run([process.execPath, 'test', `--parallel=${bunParallel(env)}`], join(root, 'server'), env) === 0 ? 0 : 1)
    const tests = serverTestFiles(files)
    if (!tests.length) { console.log('这次没有暂存 server/test 下的测试文件，整套在合并队列里跑'); process.exit(0) }
    process.exit(run([process.execPath, 'test', ...tests], join(root, 'server'), env) === 0 ? 0 : 1)
  } else if (mode === 'go-vet') {
    // 已删掉的文件（暂存了删除）所在目录可能整个没了：只留还在的
    const listed = goPackages(files).filter((p) => p === './...' || existsSync(join(root, 'native', p)))
    // 目录里的 .go 全被构建约束排除（只在 Linux 编、//go:build ignore）时 go vet ./dir 会报错，./... 却会跳过：先问 go list 这目录在本机有没有可编的文件
    const pkgs = listed.includes('./...') ? listed : listed.filter((p) => {
      const r = Bun.spawnSync(['go', 'list', '-e', '-f', '{{if or .GoFiles .CgoFiles .TestGoFiles .XTestGoFiles}}y{{end}}', p], { cwd: join(root, 'native'), env, stdout: 'pipe', stderr: 'pipe' })
      return r.stdout.toString().trim() === 'y'
    })
    if (!pkgs.length) { console.log('这次暂存的 .go 没有落在还存在的包里'); process.exit(0) }
    process.exit(run(['go', 'vet', ...pkgs], join(root, 'native'), env) === 0 ? 0 : 1)
  } else {
    console.error('用法：bun scripts/precommit-fast.ts server-tests|go-vet <文件…>')
    process.exit(2)
  }
}
