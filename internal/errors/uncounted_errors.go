package errors

import "strings"

// unCountedSubstrings contains a list of substrings that indicate an error
// that must NOT count toward a key's failure_count (blacklist_threshold):
// these failures are not key-health signals.
//   - 模型权限拒绝(该 key/账号对该模型不被上游放行):是 (key,model) 维度的能力信号,
//     由学习层定向排除(2 次后指数退避)。若计 failure_count,一把正常服务其他模型的
//     健康 key 会被"不支持的模型"的 403 一路攒到阈值误判出池
//     (2026-09-26 qwenchina key 2426 实测:558 次 qwen3.8-flash 成功,被其他模型 403 攒到 fc=30;
//      2026-10-10 qwensg 复查同样的机制)。措辞覆盖 dashscope intl 403 "Model access denied."
//      与 CN 400 "make sure your account has access to this model"。
//     注意:账号级未购买 "access to model denied ... eligible" 不在此列,它是账号级问题,
//     由 key_fatal_errors 立即出池(见 keyRestrictionSubstrings)。
var unCountedSubstrings = []string{
	"resource has been exhausted",
	"please reduce the length of the messages",
	"model access denied",
	"make sure your account has access to this model",
}

// IsUnCounted checks if the given error message contains substrings
func IsUnCounted(errorMsg string) bool {
	if errorMsg == "" {
		return false
	}

	errorLower := strings.ToLower(errorMsg)

	for _, pattern := range unCountedSubstrings {
		if strings.Contains(errorLower, pattern) {
			return true
		}
	}

	return false
}
