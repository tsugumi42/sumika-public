package openaichat

import "sumika-native/internal/jsjson"

// 结构化的思考（OpenRouter 式网关的 delta.reasoning_details，openai-completions.js 66～137 行）：是重放用的元数据，不是给人看的思考文字。
// pi 一块一块收下、合并，存在思考块的签名里，下一次请求原样带回 assistant.reasoning_details（这时不再带 reasoning_content 那类字段）

// isReasoningDetail 照 isOpenAIReasoningDetail：对象；id 是字符串或 null、format 是字符串、index 是数（不给都行）；
// 类型只认 reasoning.summary（summary 是字符串）、reasoning.encrypted（data 是字符串）、reasoning.text（text 是字符串，signature 是字符串或 null）。
// 和 pi 不一样（7.1 A49）：reasoning.text 没有 text、只带字符串签名的也认——OpenRouter 转 Claude 的流最后一块就是这样，
// pi 整块丢掉，签名跟着丢，下一次请求带回去的思考没有签名
func isReasoningDetail(d jsjson.Value) bool {
	if d.Kind != jsjson.Object {
		return false
	}
	has := func(k string) (jsjson.Value, bool) { v, ok := d.Obj[k]; return v, ok }
	if v, ok := has("id"); ok && v.Kind != jsjson.String && v.Kind != jsjson.Null {
		return false
	}
	if v, ok := has("format"); ok && v.Kind != jsjson.String {
		return false
	}
	if v, ok := has("index"); ok && v.Kind != jsjson.Number {
		return false
	}
	t, _ := has("type")
	if t.Kind != jsjson.String {
		return false
	}
	str := func(k string) bool { v, ok := has(k); return ok && v.Kind == jsjson.String }
	switch t.Str {
	case "reasoning.summary":
		return str("summary")
	case "reasoning.encrypted":
		return str("data")
	case "reasoning.text":
		if v, ok := has("signature"); ok && v.Kind != jsjson.String && v.Kind != jsjson.Null {
			return false
		}
		if _, ok := has("text"); !ok {
			return str("signature")
		}
		return str("text")
	}
	return false
}

func setKey(o *jsjson.Value, k string, v jsjson.Value) {
	if _, ok := o.Obj[k]; !ok {
		o.Keys = append(o.Keys, k)
	}
	o.Obj[k] = v
}

func delKey(o *jsjson.Value, k string) {
	if _, ok := o.Obj[k]; !ok {
		return
	}
	delete(o.Obj, k)
	for i, x := range o.Keys {
		if x == k {
			o.Keys = append(o.Keys[:i:i], o.Keys[i+1:]...)
			break
		}
	}
}

// falsy 是 JS 的假值（这几个字段只会是字符串、数、null）
func falsy(v jsjson.Value, ok bool) bool {
	return !ok || v.Kind == jsjson.Null || (v.Kind == jsjson.String && v.Str == "") || (v.Kind == jsjson.Number && v.Num == 0) || (v.Kind == jsjson.Bool && !v.Bool)
}

// fillMissing 照 fillMissingCommonReasoningDetailFields：id ??=、format ||=、index ??=。
// JS 里赋成 undefined 的键 JSON.stringify 时不写，所以来源没有这个键时不加；format 原来是假值、来源又没有时这个键就没了
func fillMissing(target *jsjson.Value, src jsjson.Value) {
	for _, k := range []string{"id", "index"} {
		if v, ok := target.Obj[k]; !ok || v.Kind == jsjson.Null {
			if sv, sok := src.Obj[k]; sok {
				setKey(target, k, sv)
			} else {
				delKey(target, k) // 原来是 null、来源又没有：赋成 undefined，不写
			}
		}
	}
	if v, ok := target.Obj["format"]; falsy(v, ok) {
		if sv, sok := src.Obj["format"]; sok {
			setKey(target, "format", sv)
		} else {
			delKey(target, "format")
		}
	}
}

func copyObj(v jsjson.Value) jsjson.Value {
	out := jsjson.Value{Kind: jsjson.Object, Keys: append([]string(nil), v.Keys...), Obj: map[string]jsjson.Value{}}
	for k, x := range v.Obj {
		out.Obj[k] = x
	}
	return out
}

// appendReasoningDetail 照 appendOpenAIReasoningDetail：连着的 reasoning.text 拼成一条（signature 先到先得）、连着的 summary 拼成一条，别的另起一条
func appendReasoningDetail(list []jsjson.Value, d jsjson.Value) []jsjson.Value {
	t := d.Obj["type"].Str
	if n := len(list); n > 0 && (t == "reasoning.text" || t == "reasoning.summary") && list[n-1].Obj["type"].Str == t {
		last := &list[n-1]
		field := "text"
		if t == "reasoning.summary" {
			field = "summary"
		}
		setKey(last, field, jsjson.Value{Kind: jsjson.String, Str: last.Obj[field].Str + d.Obj[field].Str})
		if t == "reasoning.text" {
			if v, ok := last.Obj["signature"]; falsy(v, ok) {
				if sv, sok := d.Obj["signature"]; sok {
					setKey(last, "signature", sv)
				} else {
					delKey(last, "signature")
				}
			}
		}
		fillMissing(last, d)
		return list
	}
	return append(list, copyObj(d))
}
