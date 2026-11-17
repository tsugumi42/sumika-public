// 规格状态头还写着“待合并 / 在分支上”，而头里点名的分支早已合进 main：只提醒（check-docs 的 D14 调用）。
// 证据：2026-11-02、11-03、11-06、11-12 四天都出现过（规格-链接发送改版在 6dedba43、9326f140 合进 main 后仍写待合并）；
// AGENTS 文档分层表里已有这一行，靠自觉不够，2026-11-13 主人确认改成工具层。
import { spawnSync } from 'node:child_process'

// 文件开头的第一个 > 引用块（允许前面有标题和空行，只往下看 15 行）
export function statusHead(text: string): string {
  const lines = text.split(/\r?\n/).slice(0, 15)
  const start = lines.findIndex((l) => l.startsWith('>'))
  if (start < 0) return ''
  const out: string[] = []
  for (const l of lines.slice(start)) { if (!l.startsWith('>')) break; out.push(l) }
  return out.join('\n')
}

export const PENDING_WORDS = /待合并|待合入|在分支上|未合并|还没合|没合进/
const BRANCH = /`((?:feat|fix|chore|docs|refactor|perf|test|night|exp)\/[\w.\-\/]+)`/g

export const branchesIn = (head: string): string[] => [...new Set([...head.matchAll(BRANCH)].map((m) => m[1]))]

// 头里写着“待合并”一类字样，点名了分支，而这些分支全都已合或已删：返回这些分支，否则 null
export function staleMergeNote(text: string, merged: (branch: string) => boolean): string[] | null {
  const head = statusHead(text)
  if (!PENDING_WORDS.test(head)) return null
  const branches = branchesIn(head)
  return branches.length && branches.every(merged) ? branches : null
}

// 分支是 main 的祖先，或者分支已不存在（合完删掉了）都算“已合或已删”
export function mergedIntoMain(cwd: string, branch: string, main = 'main'): boolean {
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_')))
  const r = spawnSync('git', ['merge-base', '--is-ancestor', branch, main], { cwd, env })
  return r.status === 0 || r.status === 128
}
