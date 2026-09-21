package errors

import (
	"regexp"
	"strconv"
	"strings"
)

// 本文件解决"死 key 长期占据轮询池"的问题(2026-09-21 deepseek 组实测:
// 19 把 key 里 9 把 401 凭证失效、10 把欠费,但 401/402 都落在 failover 状态码
// (400-403,405-999)里,而失败要累计 blacklist_threshold(30)次才拉黑 →
// 页面显示 active 的死 key 一直参与轮询,每次请求最多轮 7 把却全失败,
// 用户表现为"key 很多,但一用就报错")。
//
// 判定必须**按状态码区分**——错误文本会骗人:
// 魔搭(modelscope)免费额度耗尽返回的是 **429 + "insufficient balance"**,
// 若只看文本,一次请求就能把整组 55 把还在正常服务的 key 全部打黑。
// 同理 qwensg 的 429 "You exceeded your current quota" 是限额窗口而非账号欠费。
//
// 命中后:保持 failover(换 key 重试仍然有意义),但这把 key 立即置 invalid 出池;
// CronChecker 每小时复验 invalid key,充值/换好 key 会自动回到池中。

// authSubstrings 凭证被拒的确定性措辞(仅 401 或状态码未知时放行)。
var authSubstrings = []string{
	"authentication fails", // DeepSeek: Authentication Fails, Your api key: ****xxxx is invalid
	"invalid api key",      // 通用
	"invalid_api_key",      // OpenAI error code
	"incorrect api key",    // OpenAI: Incorrect API key provided: sk-xxx
	"api key is invalid",
	"invalid authentication",
	"authentication_error",
}

// keyRestrictionSubstrings key 自身被限制(仅 403 放行):如阿里云百炼给 key 配了 IP 白名单,
// 本机出口永远用不了。注意 403 的"模型权限拒绝"(Model access denied)不在此列——那由学习层处理。
var keyRestrictionSubstrings = []string{
	"ip access denied by api-key restriction",
}

// balanceSubstrings 账号余额/配额耗尽措辞(仅 402 放行)。
var balanceSubstrings = []string{
	"insufficient balance",        // DeepSeek: Insufficient Balance
	"insufficient_quota",          // OpenAI: insufficient_quota
	"insufficient quota",          //
	"exceeded your current quota", // OpenAI: You exceeded your current quota...
	"balance is insufficient",
}

// statusCodeInMessage 用于从校验路径的错误文本里取回状态码:
// internal/channel 各通道校验失败时返回 "[status %d] %s" 或 "key is invalid (status %d), ..."。
var statusCodeInMessage = regexp.MustCompile(`[\[(]status (\d{3})`)

// IsKeyFatalUpstreamError 判断上游错误是否说明"当前 key 已不可用":
// 凭证被拒(401)、key 被限制(403)、账号欠费/配额耗尽(402)。
// statusCode 为 0 时(调用方拿不到状态码,如部分校验路径)尝试从错误文本中解析,
// 且只认凭证类措辞(余额类文本与 429 无法区分,不能据此禁用)。
func IsKeyFatalUpstreamError(statusCode int, errorMsg string) bool {
	if errorMsg == "" {
		return false
	}

	code := statusCode
	if code == 0 {
		if m := statusCodeInMessage.FindStringSubmatch(errorMsg); m != nil {
			code, _ = strconv.Atoi(m[1])
		}
	}

	errorLower := strings.ToLower(errorMsg)
	switch code {
	case 0, 401:
		return containsAny(errorLower, authSubstrings)
	case 402:
		return containsAny(errorLower, balanceSubstrings)
	case 403:
		return containsAny(errorLower, keyRestrictionSubstrings)
	}

	return false
}

func containsAny(errorLower string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(errorLower, pattern) {
			return true
		}
	}
	return false
}
