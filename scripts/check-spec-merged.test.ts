// 规格状态头“待合并”提醒：判断本身用注入的函数测，查 git 那一步用临时仓库测（子进程清掉 GIT_*）
import { afterAll, expect, test } from 'bun:test'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { branchesIn, mergedIntoMain, staleMergeNote, statusHead } from './check-spec-merged'

const spec = (head: string) => `# 规格：某功能\n\n${head}\n\n正文 待合并 在分支上\n`
const yes = () => true
const no = () => false

test('状态头只取文件开头的第一个引用块', () => {
  expect(statusHead(spec('> 状态：a\n>\n> 原话：b'))).toBe('> 状态：a\n>\n> 原话：b')
  expect(statusHead('# 标题\n\n没有引用块\n')).toBe('')
})

test('头里写着待合并、点名的分支都已合：提醒', () => {
  expect(staleMergeNote(spec('> 状态：B 在 `feat/a` 上做完，待合并'), yes)).toEqual(['feat/a'])
  expect(staleMergeNote(spec('> 状态：在分支上 `fix/x`、`feat/y`'), yes)).toEqual(['fix/x', 'feat/y'])
})

test('分支还没合、或有一个没合：不提醒', () => {
  expect(staleMergeNote(spec('> 状态：待合并 `feat/a`'), no)).toBeNull()
  expect(staleMergeNote(spec('> 状态：待合并 `feat/a` `feat/b`'), (b) => b === 'feat/a')).toBeNull()
})

test('头里没有这类字样、没点名分支、字样只在正文：不提醒', () => {
  expect(staleMergeNote(spec('> 状态：已合 main（abc1234）`feat/a`'), yes)).toBeNull()
  expect(staleMergeNote(spec('> 状态：待合并'), yes)).toBeNull()
  expect(staleMergeNote('# t\n\n正文 待合并 `feat/a`\n', yes)).toBeNull()
  expect(staleMergeNote('', yes)).toBeNull()
})

test('反引号里的普通路径不当分支', () => {
  expect(branchesIn('> 状态：待合并，见 `native/internal/x`、`docs/规格-y.md`、`feat/z`')).toEqual(['feat/z'])
})

const dir = mkdtempSync(join(tmpdir(), 'spec-merged-'))
afterAll(() => rmSync(dir, { recursive: true, force: true }))
const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_')))
const git = (...a: string[]) => { const r = Bun.spawnSync(['git', '-c', 'user.name=t', '-c', 'user.email=t@t', '-c', 'commit.gpgsign=false', ...a], { cwd: dir, env }); if (r.exitCode !== 0) throw new Error(r.stderr.toString()); return r }
git('init', '-q', '-b', 'main')
writeFileSync(join(dir, 'a'), '1'); git('add', '.'); git('commit', '-q', '-m', 'a')
git('branch', 'feat/merged')
git('checkout', '-q', '-b', 'feat/ahead'); writeFileSync(join(dir, 'b'), '2'); git('add', '.'); git('commit', '-q', '-m', 'b'); git('checkout', '-q', 'main')

test('查 git：祖先算已合，领先的不算，不存在的按已合或已删', () => {
  expect(mergedIntoMain(dir, 'feat/merged')).toBe(true)
  expect(mergedIntoMain(dir, 'feat/ahead')).toBe(false)
  expect(mergedIntoMain(dir, 'feat/deleted')).toBe(true)
})
