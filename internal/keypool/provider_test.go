package keypool

import (
	"errors"
	"fmt"
	"strconv"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestProvider(t *testing.T) *KeyProvider {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	if err := db.AutoMigrate(&models.APIKey{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	enc, err := encryption.NewService("")
	if err != nil {
		t.Fatalf("failed to init encryption: %v", err)
	}
	p := NewProvider(db, store.NewMemoryStore(), nil, enc)

	keys := []models.APIKey{
		{KeyValue: "sk-unrestricted", Status: models.KeyStatusActive, GroupID: 1},
		{KeyValue: "sk-only-glm", Status: models.KeyStatusActive, GroupID: 1, AllowedModels: "glm-5.2"},
		{KeyValue: "sk-only-deepseek", Status: models.KeyStatusActive, GroupID: 1, AllowedModels: "deepseek-v4-pro-0813,deepseek-v4-flash-0731"},
	}
	// AddKeys 同时负责 DB 创建与 store 缓存
	if err := p.AddKeys(1, keys); err != nil {
		t.Fatalf("failed to add keys to pool: %v", err)
	}
	return p
}

func TestSelectKeyForGroupModel(t *testing.T) {
	p := newTestProvider(t)

	// 1. 全受限组 + 不匹配模型 → ErrNoKeyForModel
	restricted := []models.APIKey{
		{KeyValue: "sk-A-glm", Status: models.KeyStatusActive, GroupID: 2, AllowedModels: "glm-5.2"},
		{KeyValue: "sk-B-deepseek", Status: models.KeyStatusActive, GroupID: 2, AllowedModels: "deepseek-v4-pro-0813"},
	}
	if err := p.AddKeys(2, restricted); err != nil {
		t.Fatalf("failed to add restricted keys: %v", err)
	}
	if _, err := p.SelectKeyForGroupModel(2, "nonexistent-model"); !errors.Is(err, ErrNoKeyForModel) {
		t.Fatalf("expected ErrNoKeyForModel, got %v", err)
	}

	// 2. 混合组: 任意模型至少可命中无限制 key
	if _, err := p.SelectKeyForGroupModel(1, "glm-5.2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < 10; i++ {
		key, err := p.SelectKeyForGroupModel(1, "glm-5.2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key.KeyValue == "sk-only-deepseek" {
			t.Fatalf("selected key not allowed to serve glm-5.2: %s", key.KeyValue)
		}
	}

	// 3. deepseek → 不可能选中 sk-only-glm
	for i := 0; i < 10; i++ {
		key, err := p.SelectKeyForGroupModel(1, "deepseek-v4-pro-0813")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key.KeyValue == "sk-only-glm" {
			t.Fatalf("selected key not allowed to serve deepseek-v4-pro-0813: %s", key.KeyValue)
		}
	}

	// 4. 空模型退化为无过滤选择（应覆盖含受限 key 在内的全部）
	for i := 0; i < 10; i++ {
		if _, err := p.SelectKeyForGroupModel(1, ""); err != nil {
			t.Fatalf("empty model fallback failed: %v", err)
		}
	}

	// 5. 轮换: glm-5.2 只有两个候选, 应都轮流被选中
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		key, _ := p.SelectKeyForGroupModel(1, "glm-5.2")
		seen[key.KeyValue] = true
	}
	if !seen["sk-unrestricted"] || !seen["sk-only-glm"] {
		t.Fatalf("rotation did not cover both eligible keys: %v", seen)
	}
}

func TestCanServeModel(t *testing.T) {
	k := &models.APIKey{AllowedModels: ""}
	if !k.CanServeModel("anything") {
		t.Fatal("empty allowed_models must allow all models")
	}

	k = &models.APIKey{AllowedModels: "glm-5.2, deepseek-v4-pro-0813"}
	if !k.CanServeModel("glm-5.2") || !k.CanServeModel("deepseek-v4-pro-0813") {
		t.Fatal("configured model should be allowed")
	}
	if k.CanServeModel("qwen3.8-max") {
		t.Fatal("non-configured model should be denied")
	}
	if !k.CanServeModel("") {
		t.Fatal("empty model should always be allowed (e.g. /models)")
	}
}

func TestModelLearner(t *testing.T) {
	l := newModelLearner()

	// 第 1 次拒绝不排除;第 2 次进入排除(基准 30 分钟)
	l.recordModelDenied(1, 902, "deepseek-v4-pro-0813")
	if l.isModelExcluded(1, 902, "deepseek-v4-pro-0813") {
		t.Fatal("first denial must not exclude")
	}
	l.recordModelDenied(1, 902, "deepseek-v4-pro-0813")
	if !l.isModelExcluded(1, 902, "deepseek-v4-pro-0813") {
		t.Fatal("key should be excluded after 2 denials")
	}
	// 其他模型不受影响
	if l.isModelExcluded(1, 902, "glm-5.2") {
		t.Fatal("other model must not be affected")
	}

	// 首次排除时长 = 基准 30 分钟
	l.mu.Lock()
	e0 := l.table["1:902"]["deepseek-v4-pro-0813"]
	first := e0.until
	l.mu.Unlock()
	if d := time.Until(first); d < 29*time.Minute || d > 30*time.Minute+time.Second {
		t.Fatalf("first exclusion duration wrong: %v", d)
	}

	// 过期后解除;再犯一次 → strikes=3,排除时长翻倍(60 分钟)
	l.mu.Lock()
	e0.until = time.Now().Add(-time.Second)
	l.mu.Unlock()
	if l.isModelExcluded(1, 902, "deepseek-v4-pro-0813") {
		t.Fatal("exclusion should expire")
	}
	l.recordModelDenied(1, 902, "deepseek-v4-pro-0813")
	l.mu.Lock()
	e1 := l.table["1:902"]["deepseek-v4-pro-0813"]
	l.mu.Unlock()
	if d := time.Until(e1.until); d < 59*time.Minute || d > 60*time.Minute+time.Second {
		t.Fatalf("escalated exclusion duration wrong (strikes=%d): %v", e1.strikes, d)
	}

	// 成功后清零
	l.recordModelSuccess(1, 902, "deepseek-v4-pro-0813")
	if l.isModelExcluded(1, 902, "deepseek-v4-pro-0813") {
		t.Fatal("success must clear exclusion")
	}
	l.mu.Lock()
	if e1.strikes != 0 {
		t.Fatalf("strikes should reset after success, got %d", e1.strikes)
	}
	l.mu.Unlock()
}

func TestSelectKeyForGroupModelLearns(t *testing.T) {
	p := newTestProvider(t)
	// 组3: 只有两个 key，其中一个反复被拒后应从路由中剔除
	keys := []models.APIKey{
		{KeyValue: "sk-L1", Status: models.KeyStatusActive, GroupID: 3},
		{KeyValue: "sk-L2", Status: models.KeyStatusActive, GroupID: 3},
	}
	if err := p.AddKeys(3, keys); err != nil {
		t.Fatalf("failed to add keys: %v", err)
	}

	seenDenied := false
	for i := 0; i < 12; i++ {
		key, err := p.SelectKeyForGroupModel(3, "deepseek-v4-pro-0813")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key.KeyValue == "sk-L1" {
			// 模拟 L1 上游拒绝两次
			p.RecordModelDenied(3, key.ID, "deepseek-v4-pro-0813")
			p.RecordModelDenied(3, key.ID, "deepseek-v4-pro-0813")
		}
		if key.KeyValue == "sk-L2" {
			seenDenied = true
		}
	}
	if !seenDenied {
		t.Fatal("expected alternating keys before exclusion")
	}

	// 三次拒绝后 L1 应被剔除，只剩 L2
	for i := 0; i < 5; i++ {
		key, err := p.SelectKeyForGroupModel(3, "deepseek-v4-pro-0813")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key.KeyValue == "sk-L1" {
			// 再补一次拒绝到阈值
			p.RecordModelDenied(3, key.ID, "deepseek-v4-pro-0813")
		}
		// 排除生效后应只见 L2
		if i >= 1 && key.KeyValue == "sk-L1" {
			t.Fatalf("learned-excluded key should not be selected")
		}
	}
}

func TestIsModelAccessDeniedError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"dashscope-intl 403", "Model access denied.", true},
		{
			"dashscope domestic 400",
			"Access denied, please make sure your account has access to this model. For details, see: https://help.aliyun.com/",
			true,
		},
		{"domestic case-insensitive", "Access Denied, please make sure your account HAS ACCESS TO THIS MODEL.", true},
		// 回归护栏:审查/欠费/配额/账号级未购买等非"模型权限"错误绝不能被学习层吞掉
		// (账号级未购买由 key_fatal_errors 立即出池,见 TestIsKeyFatalUpstreamError)
		{
			"account unpurchased (account-level, not model-scope)",
			"Access to model denied. Please make sure you are eligible for using the model.",
			false,
		},
		{"content moderation", "InternalError.Algo.DataInspectionFailed: Input text data may contain inappropriate content.", false},
		{"arrearage", "Access denied, please make sure your account is in good standing. For details, see: https://help.aliyun.com/", false},
		{"IP restriction", "IP access denied by API-Key restriction.", false},
		{"quota", "You exceeded your current quota", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := IsModelAccessDeniedError(tc.msg); got != tc.want {
			t.Errorf("%s: IsModelAccessDeniedError(%q) = %v, want %v", tc.name, tc.msg, got, tc.want)
		}
	}
}

// 复活门控:流量路径(allowRevive=false)的成功不得复活已出池的 key(fc 也不动);
// 校验路径(allowRevive=true)才允许 invalid→active 并归零 fc。
// 背景:流量成功路径恢复 fc 归零(2026-10-10)后,若不设门,在途成功会与
// 并发的 key-fatal 出池竞争,把刚出池的死 key 拉回池——即已移除的"拉黑→复活"循环变体。
func TestHandleSuccessReviveGate(t *testing.T) {
	p := newTestProvider(t)

	var key models.APIKey
	if err := p.db.Where("group_id = ? AND key_value = ?", 1, "sk-unrestricted").First(&key).Error; err != nil {
		t.Fatalf("fetch key: %v", err)
	}
	keyIDStr := fmt.Sprintf("%d", key.ID)
	keyHashKey := fmt.Sprintf("key:%d", key.ID)
	activeList := "group:1:active_keys"

	park := func() {
		if err := p.db.Model(&key).Updates(map[string]any{"status": "invalid", "failure_count": 5}).Error; err != nil {
			t.Fatalf("park db: %v", err)
		}
		if err := p.store.HSet(keyHashKey, map[string]any{"status": "invalid", "failure_count": 5}); err != nil {
			t.Fatalf("park store: %v", err)
		}
		if err := p.store.LRem(activeList, 0, keyIDStr); err != nil {
			t.Fatalf("park list: %v", err)
		}
	}
	state := func() (string, int64) {
		d, err := p.store.HGetAll(keyHashKey)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		fc, _ := strconv.ParseInt(d["failure_count"], 10, 64)
		return d["status"], fc
	}
	inActiveList := func() bool {
		ids, err := p.store.LRange(activeList, 0, -1)
		if err != nil {
			return false
		}
		for _, id := range ids {
			if id == keyIDStr {
				return true
			}
		}
		return false
	}

	// 流量路径:出池 key 的在途成功 → 不复活、fc 不动、不回候选列表
	park()
	if err := p.handleSuccess(key.ID, keyHashKey, activeList, false); err != nil {
		t.Fatalf("traffic success: %v", err)
	}
	if st, fc := state(); st != models.KeyStatusInvalid || fc != 5 {
		t.Fatalf("traffic success must not touch parked key, got status=%s fc=%d", st, fc)
	}
	if inActiveList() {
		t.Fatal("parked key must not be re-added to active list by traffic success")
	}

	// 校验路径:允许复活 + fc 归零 + 回候选列表
	if err := p.handleSuccess(key.ID, keyHashKey, activeList, true); err != nil {
		t.Fatalf("validation success: %v", err)
	}
	if st, fc := state(); st != models.KeyStatusActive || fc != 0 {
		t.Fatalf("validation success must revive and reset fc, got status=%s fc=%d", st, fc)
	}
	if !inActiveList() {
		t.Fatal("revived key must be back in active list")
	}
}
