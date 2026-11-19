// 协议生成器（规格-Bun后台第1批 4.4）：生成确定、拼错报错、提交的生成文件是新的
import { describe, expect, test } from "bun:test";
import { readFileSync, writeFileSync, mkdtempSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { GO_OUT, TS_OUT, SOURCE, genGo, genTS, load, outputs, parseField, parseType, runGen } from "../scripts/gen-rpc.ts";

const known = new Set(["Foo"]);

describe("gen-rpc", () => {
  test("同一份定义生成两次字节相同", () => {
    const a = outputs(), b = outputs();
    expect(a.map(f => f.content)).toEqual(b.map(f => f.content));
  });

  test("仓库里的生成文件和定义一致（CRLF 不算）", () => {
    for (const f of outputs()) {
      expect(readFileSync(f.path, "utf8").replace(/\r\n/g, "\n")).toBe(f.content);
    }
  });

  test("改了定义，生成结果就变（--check 靠这个拦）", () => {
    const spec = load();
    const before = genTS(spec);
    spec.types[0].fields.push("extra_field: string");
    expect(genTS(spec)).not.toBe(before);
    expect(genGo(spec)).toContain("ExtraField");
  });

  test("不认识的类型、写错的字段报错", () => {
    expect(() => parseType("Bar", known)).toThrow("不认识的类型");
    expect(() => parseType("map<int,Foo>", known)).toThrow("映射的键");
    expect(() => parseField("Bad: string", known)).toThrow("字段写法");
    expect(parseField("x?: Result<Foo[]> // 说明", known)).toEqual({
      name: "x", optional: true, doc: "说明",
      type: { k: "result", of: { k: "array", of: { k: "named", name: "Foo" } } },
    });
  });

  test("定义里多一个不认识的键要报错，类型重名也报", () => {
    const dir = mkdtempSync(join(tmpdir(), "gen-rpc-"));
    const p = join(dir, "x.json");
    const spec = JSON.parse(readFileSync(SOURCE, "utf8"));
    writeFileSync(p, JSON.stringify({ ...spec, typo: 1 }));
    expect(() => load(p)).toThrow("不认识的键");
    writeFileSync(p, JSON.stringify({ ...spec, types: [...spec.types, spec.types[0]] }));
    expect(() => load(p)).toThrow("类型重名");
  });

  test("错误码和帧上限从定义生成，两边同一个数；重码、缺上限报错", () => {
    const spec = load();
    const go = genGo(spec), ts = genTS(spec);
    expect(go).toMatch(/CodeLocalChatBusy += 1011/);
    expect(ts).toContain("LocalChatBusy: 1011,");
    expect(go).toMatch(/MaxRequestBytes += 32 << 20/);
    expect(ts).toContain("export const MAX_REQUEST_BYTES = 32 << 20;");
    const dir = mkdtempSync(join(tmpdir(), "gen-rpc-"));
    const p = join(dir, "x.json");
    const raw = JSON.parse(readFileSync(SOURCE, "utf8"));
    writeFileSync(p, JSON.stringify({ ...raw, errors: [...raw.errors, { code: 1011, name: "dup" }] }));
    expect(() => load(p)).toThrow("错误码重复");
    writeFileSync(p, JSON.stringify({ ...raw, errors: [...raw.errors, { code: 1999, name: "local_chat_busy" }] }));
    expect(() => load(p)).toThrow("错误名重复");
    writeFileSync(p, JSON.stringify({ ...raw, limits: { max_request_mib: 0, max_response_mib: 1 } }));
    expect(() => load(p)).toThrow("limits");
  });

  test("可选结构体在 Go 里是指针、标量一律 omitempty", () => {
    const go = genGo(load());
    expect(go).toMatch(/Reply +\*Result\[QuoteView\] `json:"reply,omitempty"`/);
    expect(go).toMatch(/Ref +EventRef +`json:"ref"`/);
    expect(go).toMatch(/Seq +int64 +`json:"seq,omitempty"`/);
    expect(GO_OUT.endsWith("wire_gen.go")).toBe(true);
    expect(TS_OUT.endsWith("gen.ts")).toBe(true);
  });

  test("没有 server/：只生成、检查 Go 侧那份", () => {
    const root = mkdtempSync(join(tmpdir(), "gen-rpc-noserver-"));
    mkdirSync(join(root, "protocol"), { recursive: true });
    writeFileSync(join(root, "protocol", "sumika-rpc.json"), readFileSync(SOURCE, "utf8"));
    const spec = load(join(root, "protocol", "sumika-rpc.json"));
    const rel = (p: string) => p.slice(root.length + 1).replaceAll("\\", "/");
    // 没有 server/：只生成 Go 那份
    expect(outputs(spec, root).map((f) => rel(f.path))).toEqual(["native/internal/rpcwire/wire_gen.go"]);
    expect(runGen(root, true)).toEqual({ stale: ["native/internal/rpcwire/wire_gen.go"], wrote: [], hasServer: false });
    mkdirSync(join(root, "native", "internal", "rpcwire"), { recursive: true });
    writeFileSync(join(root, "native", "internal", "rpcwire", "wire_gen.go"), genGo(spec));
    expect(runGen(root, true)).toEqual({ stale: [], wrote: [], hasServer: false });
    // server/ 一出现，恢复两边都生成
    mkdirSync(join(root, "server"));
    expect(outputs(spec, root).map((f) => rel(f.path))).toEqual(["native/internal/rpcwire/wire_gen.go", "server/src/protocol/gen.ts"]);
  });
});
