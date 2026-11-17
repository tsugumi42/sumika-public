package selfcheck

// ClassConsts 给外部测试核对本包抄的 llm 类别值
func ClassConsts() map[string]string {
	return map[string]string{"cancelled": classCancelled, "model_unknown": classModelUnknown, "budget_exhausted": classBudgetExhausted, "parse_failed": classParseFailed}
}
