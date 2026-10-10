package errors

import "testing"

// 回归护栏:不计 failure_count 的豁免必须只覆盖"非 key 健康"错误。
// 模型权限拒绝 → true(学习层管);账号级/凭证级/其它故障 → false(仍记账)。
func TestIsUnCounted(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		// 既有的"与 key 无关"类
		{"resource exhausted", "resource has been exhausted", true},
		{"reduce message length", "Please reduce the length of the messages", true},

		// 新增:模型权限拒绝(intl 403 / CN 400 两种真实措辞)
		{"dashscope-intl model denied", "Model access denied.", true},
		{
			"dashscope-cn model denied 400",
			"Access denied, please make sure your account has access to this model. For details, see: https://help.aliyun.com/",
			true,
		},
		{"validation path model denied", "[status 403] Model access denied.", true},

		// 必须继续记账的错误
		{"account unpurchased", "Access to model denied. Please make sure you are eligible for using the model.", false},
		{"arrearage", "Access denied, please make sure your account is in good standing.", false},
		{"ip restriction", "IP access denied by API-Key restriction.", false},
		{"quota window", "You exceeded your current quota", false},
		{"http 500", "internal server error", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := IsUnCounted(tc.msg); got != tc.want {
			t.Errorf("%s: IsUnCounted(%q) = %v, want %v", tc.name, tc.msg, got, tc.want)
		}
	}
}
