package errors

import "strings"

// terminalSubstrings 标记"换 key 重试也无济于事"的上游确定性失败:
// 与请求内容/参数相关、与 key/模型无关,重试只会复制相同错误,
// 并按 failover 逻辑给每把健康 key 都计一次失败。
var terminalSubstrings = []string{
	// 阿里云百炼内容审核(DataInspectionFailed):
	// "Input text data may contain inappropriate content."
	"may contain inappropriate content",
}

// IsTerminalUpstreamError 判断上游错误是否为终态:
// 终态错误不重试、不计失败、不参与自动学习,原文透传给客户端。
func IsTerminalUpstreamError(errorMsg string) bool {
	if errorMsg == "" {
		return false
	}
	errorLower := strings.ToLower(errorMsg)
	for _, pattern := range terminalSubstrings {
		if strings.Contains(errorLower, pattern) {
			return true
		}
	}
	return false
}