package errors

import "testing"

func TestIsKeyFatalUpstreamError(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		msg        string
		want       bool
	}{
		// --- 真·致命:凭证失效 / 欠费 ---
		{"deepseek 401 auth failure", 401, "Authentication Fails, Your api key: ****72cb is invalid", true},
		{"openai 401 incorrect key", 401, "Incorrect API key provided: sk-abc***xyz.", true},
		{"openai 401 error code", 401, `{"error":{"code":"invalid_api_key"}}`, true},
		{"deepseek 402 insufficient balance", 402, "Insufficient Balance", true},
		{"openai 402 quota", 402, "You exceeded your current quota, please check your plan and billing details.", true},
		{"dashscope 403 ip restriction", 403, "IP access denied by API-Key restriction.", true},
		{"dashscope 403 unpurchased account", 403, "Access to model denied. Please make sure you are eligible for using the model.", true},
		// 校验路径:状态码嵌在错误文本里
		{"validation 401 text", 0, "[status 401] Authentication Fails, Your api key: **** is invalid", true},
		{"validation 401 no status prefix", 0, "Incorrect API key provided", true},

		// --- 假阳性防护:文本像"欠费"但状态码是 429/其它 ---
		{"modelscope 429 free quota wording", 429, "insufficient balance", false},
		{"modelscope validation 429 text", 0, "[status 429] insufficient balance", false},
		{"qwensg 429 window quota", 429, "You exceeded your current quota, please check your plan and billing details.", false},
		{"rate limited 429", 429, "Requests rate limit exceeded, please try again later", false},
		// --- 其它不该禁用的:模型权限(学习层)、内容审核、账号状态 ---
		{"dashscope 403 model denial", 403, "Model access denied.", false},
		{"dashscope 400 access wording", 400, "Access denied, please make sure your account has access to this model", false},
		{"moderation 400", 400, "Input text data may contain inappropriate content.", false},
		{"account standing", 400, "Your account is not in good standing", false},
		{"sensenova window quota", 429, "Allocated quota exceeded, inference tpm exhausted", false},
		// --- 校验路径读 body 失败:带 (status N) 但不是凭证措辞 ---
		{"validation read error", 0, "key is invalid (status 500), but failed to read error body: EOF", false},
		{"http 500", 500, "internal server error", false},
		{"empty", 0, "", false},
	}
	for _, tc := range cases {
		if got := IsKeyFatalUpstreamError(tc.statusCode, tc.msg); got != tc.want {
			t.Errorf("%s: IsKeyFatalUpstreamError(%d, %q) = %v, want %v", tc.name, tc.statusCode, tc.msg, got, tc.want)
		}
	}
}
