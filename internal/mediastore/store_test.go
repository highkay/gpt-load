package mediastore

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gpt-load/internal/media"
	"gpt-load/internal/testutil/sqlitetest"
)

func TestStoreReserveGetUpdateAndExpiry(t *testing.T) {
	store := NewStore(sqlitetest.OpenMigrated(t))
	now := time.Unix(1_800_000_000, 0)
	store.SetClock(func() time.Time { return now })
	store.SetTTL(time.Hour)

	request := media.NewTaskParams{Protocol: "openai-videos", Operation: "videos_create", Kind: "video", ClientModel: "video-model", Mode: "text", Seconds: "5", Size: "720P", AspectRatio: "16:9"}
	task, err := store.NewTask(7, request, "")
	if err != nil {
		t.Fatal(err)
	}
	created, newRow, err := store.Reserve(context.Background(), task)
	if err != nil || !newRow {
		t.Fatalf("Reserve() = %#v, %t, %v", created, newRow, err)
	}
	got, err := store.Get(context.Background(), 7, created.ID)
	if err != nil || got.ID != created.ID {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	if _, err := store.Get(context.Background(), 8, created.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("cross-access-key Get() error = %v", err)
	}

	created.GroupID, created.CredentialID = 3, 4
	created.UpstreamTaskID, created.UpstreamModel = "upstream-1", "upstream-model"
	created.Status, created.Progress = media.StatusInProgress, 42
	if err := store.UpdateCreateResult(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(context.Background(), 7, created.ID)
	if err != nil || got.GroupID != 3 || got.CredentialID != 4 || got.UpstreamTaskID != "upstream-1" || got.Progress != 42 {
		t.Fatalf("updated task = %#v, %v", got, err)
	}

	now = now.Add(2 * time.Hour)
	if _, err := store.Get(context.Background(), 7, created.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("expired Get() error = %v", err)
	}
	deleted, err := store.DeleteExpired(context.Background())
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteExpired() = %d, %v", deleted, err)
	}
}

func TestStoreIdempotencyReservationReturnsExisting(t *testing.T) {
	store := NewStore(sqlitetest.OpenMigrated(t))
	now := time.Unix(1_800_000_000, 0)
	store.SetClock(func() time.Time { return now })
	store.SetTTL(time.Hour)
	request := media.NewTaskParams{Protocol: "openai-videos", Operation: "videos_create", Kind: "video", ClientModel: "video-model", Mode: "text", Seconds: "5", Size: "720P", AspectRatio: "16:9"}
	hash, err := media.IdempotencyHash(1, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.NewTask(1, request, hash)
	if err != nil {
		t.Fatal(err)
	}
	created, newRow, err := store.Reserve(context.Background(), first)
	if err != nil || !newRow {
		t.Fatalf("first Reserve() = %#v, %t, %v", created, newRow, err)
	}
	second, err := store.NewTask(1, request, hash)
	if err != nil {
		t.Fatal(err)
	}
	existing, newRow, err := store.Reserve(context.Background(), second)
	if err != nil || newRow {
		t.Fatalf("second Reserve() = %#v, %t, %v", existing, newRow, err)
	}
	if existing.ID != created.ID {
		t.Fatalf("existing ID = %q, want %q", existing.ID, created.ID)
	}

	now = now.Add(2 * time.Hour)
	reused, err := store.NewTask(1, request, hash)
	if err != nil {
		t.Fatal(err)
	}
	reused, newRow, err = store.Reserve(context.Background(), reused)
	if err != nil || !newRow {
		t.Fatalf("expired idempotency key was not reusable: %#v, %t, %v", reused, newRow, err)
	}
	if reused.ID == created.ID {
		t.Fatal("expired idempotency row was returned")
	}
}

func TestStoreMarkCancelledAndCleanup(t *testing.T) {
	store := NewStore(sqlitetest.OpenMigrated(t))
	now := time.Unix(1_800_000_000, 0)
	store.SetClock(func() time.Time { return now })
	store.SetTTL(time.Hour)
	request := media.NewTaskParams{Protocol: "openai-videos", Operation: "videos_create", Kind: "video", ClientModel: "video-model", Mode: "text", Seconds: "5", Size: "720P", AspectRatio: "16:9"}
	task, err := store.NewTask(1, request, "")
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = store.Reserve(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCancelled(context.Background(), 1, task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), 1, task.ID)
	if err != nil || got.Status != media.StatusCancelled {
		t.Fatalf("cancelled task = %#v, %v", got, err)
	}
	if err := store.Cleanup(context.Background(), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), 1, task.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("cleanup did not remove expired task: %v", err)
	}
}

func TestStoreUpdateObservationDoesNotOverwriteCancelled(t *testing.T) {
	store := NewStore(sqlitetest.OpenMigrated(t))
	now := time.Unix(1_800_000_000, 0)
	store.SetClock(func() time.Time { return now })
	store.SetTTL(time.Hour)
	request := media.NewTaskParams{Protocol: "openai-videos", Operation: "videos_create", Kind: "video", ClientModel: "video-model", Mode: "text", Seconds: "5", Size: "720P", AspectRatio: "16:9"}
	task, err := store.NewTask(1, request, "")
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = store.Reserve(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCancelled(context.Background(), 1, task.ID); err != nil {
		t.Fatal(err)
	}
	task.Status = media.StatusInProgress
	task.Progress = 10
	if err := store.UpdateObservation(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), 1, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != media.StatusCancelled || got.Progress != 0 {
		t.Fatalf("late observation overwrote terminal state: %#v", got)
	}
}

func TestStorePersistsCredentialIdentityGeneration(t *testing.T) {
	store := NewStore(sqlitetest.OpenMigrated(t))
	now := time.Unix(1_800_000_000, 0)
	store.SetClock(func() time.Time { return now })
	store.SetTTL(time.Hour)
	request := media.NewTaskParams{Protocol: "openai-videos", Operation: "videos_create", Kind: "video", ClientModel: "video-model"}
	task, err := store.NewTask(1, request, "")
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = store.Reserve(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	task.CredentialIdentityGeneration = 9
	if err := store.UpdateCreateResult(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), 1, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CredentialIdentityGeneration != 9 {
		t.Fatalf("identity generation = %d, want 9", got.CredentialIdentityGeneration)
	}
}

func TestStoreReserveIdempotencyRaceKeepsOneWinner(t *testing.T) {
	store := NewStore(sqlitetest.OpenMigrated(t))
	now := time.Unix(1_800_000_000, 0)
	store.SetClock(func() time.Time { return now })
	store.SetTTL(time.Hour)
	request := media.NewTaskParams{Protocol: "openai-videos", Operation: "videos_create", Kind: "video", ClientModel: "video-model"}
	hash, err := media.IdempotencyHash(1, "race-key")
	if err != nil {
		t.Fatal(err)
	}
	expired, err := store.NewTask(1, request, hash)
	if err != nil {
		t.Fatal(err)
	}
	expired.ExpiresAtMS = now.Add(-time.Minute).UnixMilli()
	if err := store.db.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var createdCount atomic.Int64
	results := make(chan media.Task, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			task, taskErr := store.NewTask(1, request, hash)
			if taskErr != nil {
				t.Error(taskErr)
				return
			}
			got, created, reserveErr := store.Reserve(context.Background(), task)
			if reserveErr != nil {
				t.Error(reserveErr)
				return
			}
			if created {
				createdCount.Add(1)
			}
			results <- got
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	ids := make(map[string]struct{})
	for got := range results {
		ids[got.ID] = struct{}{}
	}
	if len(ids) != 1 || createdCount.Load() != 1 {
		t.Fatalf("race results = %d ids, %d created, want 1 and 1", len(ids), createdCount.Load())
	}
	var count int64
	if err := store.db.Model(&media.Task{}).Where("idempotency_key = ?", hash).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idempotency rows = %d, want 1", count)
	}
}

func TestStoreReadsConfiguredTTL(t *testing.T) {
	t.Setenv("MEDIA_TASK_TTL", "2h")
	store := NewStore(sqlitetest.OpenMigrated(t))
	if store.TTL() != 2*time.Hour {
		t.Fatalf("TTL() = %s", store.TTL())
	}
}
