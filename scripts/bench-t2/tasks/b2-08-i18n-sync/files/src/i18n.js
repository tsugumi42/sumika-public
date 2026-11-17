import zh from '../locales/zh.json' with { type: 'json' };

let messages = zh;

export function setMessages(m) {
  messages = m;
}

// 取不到译文时退回 zh，再退回键名
export function t(key, vars = {}) {
  const text = messages[key] ?? zh[key] ?? key;
  return text.replace(/\{(\w+)\}/g, (_, name) => String(vars[name] ?? ''));
}
