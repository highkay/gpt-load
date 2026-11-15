package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/channel"
	"gpt-load/internal/dialect"
	"gpt-load/internal/health"
	"gpt-load/internal/media"
	"gpt-load/internal/mediastore"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
	"gpt-load/internal/testutil/encryptiontest"
	"gpt-load/internal/testutil/sqlitetest"
)

func newVideoContentFixture(t *testing.T, channelID channel.ID, upstreamURL string) (*gin.Engine, *mediastore.Store, *Handler) {
	t.Helper()
	return newVideoContentFixtureWithOptions(t, channelID, upstreamURL, state.FilterSet{
		Protocols: map[protocol.Protocol]struct{}{protocol.OpenAIVideos: {}},
	}, 0)
}

func newVideoContentFixtureWithOptions(t *testing.T, channelID channel.ID, upstreamURL string, filters state.FilterSet, groupConcurrency int64) (*gin.Engine, *mediastore.Store, *Handler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	keyService := encryptiontest.Service(t, "video-content-test-master-key")
	manager := state.NewManager()
	if _, err := manager.Publish(state.CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []state.GroupConfig{{
			ConnectionType: "api_key", ID: 1, Name: "video", ChannelID: channelID,
			Params: json.RawMessage(`{"base_url":"` + upstreamURL + `/v1"}`),
			Models: []state.ModelConfig{{ID: "video-model"}}, Enabled: true,
			Settings: config.Settings{state.SettingConcurrencyLimit: json.Number(strconv.FormatInt(groupConcurrency, 10))},
		}},
		Credentials: []state.CredentialConfig{{
			ID: 1, GroupID: 1, Status: state.CredentialStatusActive, Version: 1, IdentityGeneration: 1, Fingerprint: "credential-1",
		}},
		AccessKeys: []state.AccessKeyConfig{{
			ID: 1, Name: "video-client", KeyHash: keyService.Hash("gl-video"), Status: state.AccessKeyStatusActive,
			Filters: filters,
		}},
	}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	registry := state.NewCredentialRegistry()
	encrypted, err := keyService.Encrypt(`{"api_key":"key-one"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceCredentials([]state.CredentialEntry{{
		ID: 1, GroupID: 1, Version: 1, IdentityGeneration: 1, Fingerprint: "credential-1",
		Status: state.CredentialStatusActive, EncryptedValue: encrypted,
	}}); err != nil {
		t.Fatal(err)
	}
	store := mediastore.NewStore(sqlitetest.OpenMigrated(t))
	handler := NewHandler(
		manager, registry, keyService, &scriptedForwarder{}, dialect.NewSet(dialect.NewOpenAIVideos()),
		health.NewStatsStore(), health.NewMutationCoordinator(), nil, nil, nil,
	)
	handler.ConfigureMediaTasks(store)
	engine := gin.New()
	bindGatewayRoutesForTest(t, engine, handler)
	return engine, store, handler
}

func seedVideoTask(t *testing.T, store *mediastore.Store) media.Task {
	t.Helper()
	request := media.NewTaskParams{Protocol: media.ProtocolOpenAIVideos, Operation: "videos_create", Kind: media.KindVideo, ClientModel: "video-model", Mode: "text", Seconds: "5", Size: "720P", AspectRatio: "16:9"}
	task, err := store.NewTask(1, request, "")
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = store.Reserve(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	task.GroupID, task.CredentialID = 1, 1
	task.UpstreamTaskID, task.UpstreamModel = "task_up_content", "upstream-video"
	task.Status = media.StatusInProgress
	if err := store.UpdateCreateResult(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestVideoContentProxiesBinaryRangeWithPinnedCredential(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path != "/v1/videos/task_up_content/content" {
			t.Errorf("upstream path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer key-one" {
			t.Errorf("upstream authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Range") != "bytes=0-3" {
			t.Errorf("upstream range = %q", request.Header.Get("Range"))
		}
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Range", "bytes 0-3/8")
		writer.Header().Set("Accept-Ranges", "bytes")
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(writer, "abcd")
	}))
	defer upstream.Close()
	engine, store, _ := newVideoContentFixture(t, channel.OpenAICompatible, upstream.URL)
	task := seedVideoTask(t, store)

	request := httptest.NewRequest(http.MethodGet, "/v1/videos/"+task.ID+"/content", nil)
	request.Header.Set("Authorization", "Bearer gl-video")
	request.Header.Set("Range", "bytes=0-3")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("content status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != "abcd" || recorder.Header().Get("Content-Type") != "video/mp4" ||
		recorder.Header().Get("Content-Range") != "bytes 0-3/8" {
		t.Fatalf("content response = %q headers = %#v", recorder.Body.String(), recorder.Header())
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d", calls.Load())
	}
}

func TestVideoContentUsesResultURLWithoutCredential(t *testing.T) {
	var calls atomic.Int64
	mediaServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Authorization") != "" {
			t.Errorf("signed media URL received authorization %q", request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "video/mp4")
		_, _ = io.WriteString(writer, "signed")
	}))
	defer mediaServer.Close()
	engine, store, handler := newVideoContentFixture(t, channel.Agnes, "http://127.0.0.1:1")
	handler.allowPrivateVideoURLs = true
	task := seedVideoTask(t, store)
	task.Status, task.Progress, task.ResultURL = media.StatusCompleted, 100, mediaServer.URL
	if err := store.UpdateObservation(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/videos/"+task.ID+"/content", nil)
	request.Header.Set("Authorization", "Bearer gl-video")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "signed" {
		t.Fatalf("content status = %d body = %q", recorder.Code, recorder.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("media calls = %d", calls.Load())
	}
}

func TestVideoContentBlocksPrivateResultURL(t *testing.T) {
	engine, store, _ := newVideoContentFixture(t, channel.Agnes, "http://127.0.0.1:1")
	task := seedVideoTask(t, store)
	task.Status, task.Progress = media.StatusCompleted, 100
	task.ResultURL = "http://169.254.169.254/latest/meta-data"
	if err := store.UpdateObservation(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/videos/"+task.ID+"/content", nil)
	request.Header.Set("Authorization", "Bearer gl-video")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "meta-data") {
		t.Fatalf("private metadata leaked: %s", recorder.Body.String())
	}
}

func TestVideoContentAbortsTruncatedUpstreamBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		writer.WriteHeader(http.StatusOK)
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("upstream writer has no flusher")
			return
		}
		_, _ = writer.Write([]byte("partial"))
		flusher.Flush()
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			t.Error("upstream writer has no hijacker")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer upstream.Close()
	engine, store, _ := newVideoContentFixture(t, channel.OpenAICompatible, upstream.URL)
	task := seedVideoTask(t, store)
	server := httptest.NewServer(engine)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/videos/"+task.ID+"/content", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer gl-video")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("truncated upstream body was delivered as a clean EOF")
	}
}

func TestVideoContentPassesThroughNotModifiedAndRangeNotSatisfiable(t *testing.T) {
	for _, status := range []int{http.StatusNotModified, http.StatusRequestedRangeNotSatisfiable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "video/mp4")
				writer.Header().Set("ETag", `"v1"`)
				writer.Header().Set("Content-Range", "bytes 4-7/8")
				writer.WriteHeader(status)
				if status == http.StatusRequestedRangeNotSatisfiable {
					_, _ = io.WriteString(writer, "range error")
				}
			}))
			defer upstream.Close()
			engine, store, _ := newVideoContentFixture(t, channel.OpenAICompatible, upstream.URL)
			task := seedVideoTask(t, store)
			request := httptest.NewRequest(http.MethodGet, "/v1/videos/"+task.ID+"/content", nil)
			request.Header.Set("Authorization", "Bearer gl-video")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			if recorder.Code != status {
				t.Fatalf("status = %d, want %d body = %s", recorder.Code, status, recorder.Body.String())
			}
			if recorder.Header().Get("ETag") != `"v1"` {
				t.Fatalf("etag = %q", recorder.Header().Get("ETag"))
			}
			if status == http.StatusRequestedRangeNotSatisfiable {
				if recorder.Header().Get("Content-Range") != "bytes 4-7/8" || recorder.Body.String() != "range error" {
					t.Fatalf("416 response = %q headers = %#v", recorder.Body.String(), recorder.Header())
				}
			}
		})
	}
}

func TestVideoContentRejectsGroupFilteredAccessKey(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "video/mp4")
		_, _ = io.WriteString(writer, "video")
	}))
	defer upstream.Close()
	engine, store, _ := newVideoContentFixtureWithOptions(t, channel.OpenAICompatible, upstream.URL, state.FilterSet{
		Protocols: map[protocol.Protocol]struct{}{protocol.OpenAIVideos: {}},
		Groups:    map[uint]struct{}{2: {}},
	}, 0)
	task := seedVideoTask(t, store)
	request := httptest.NewRequest(http.MethodGet, "/v1/videos/"+task.ID+"/content", nil)
	request.Header.Set("Authorization", "Bearer gl-video")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("filtered content reached upstream %d times", calls.Load())
	}
}

func TestVideoContentAcquiresGroupConcurrency(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(started)
		<-release
		writer.Header().Set("Content-Type", "video/mp4")
		_, _ = io.WriteString(writer, "video")
	}))
	defer upstream.Close()
	engine, store, _ := newVideoContentFixtureWithOptions(t, channel.OpenAICompatible, upstream.URL, state.FilterSet{
		Protocols: map[protocol.Protocol]struct{}{protocol.OpenAIVideos: {}},
	}, 1)
	task := seedVideoTask(t, store)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/v1/videos/"+task.ID+"/content", nil)
		request.Header.Set("Authorization", "Bearer gl-video")
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		first <- recorder
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first content request did not reach upstream")
	}
	second := httptest.NewRequest(http.MethodGet, "/v1/videos/"+task.ID+"/content", nil)
	second.Header.Set("Authorization", "Bearer gl-video")
	secondRecorder := httptest.NewRecorder()
	engine.ServeHTTP(secondRecorder, second)
	if secondRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d body = %s", secondRecorder.Code, secondRecorder.Body.String())
	}
	close(release)
	select {
	case recorder := <-first:
		if recorder.Code != http.StatusOK || recorder.Body.String() != "video" {
			t.Fatalf("first status = %d body = %q", recorder.Code, recorder.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first content request did not finish")
	}
}

func TestAgnesVideoRetrieveURLUsesAgnesPath(t *testing.T) {
	group := state.GroupView{ResolvedTarget: channel.ResolvedTarget{
		TargetConfig: []byte(`{"base_url":"https://apihub.agnes-ai.com/v1"}`),
	}}
	task := media.Task{UpstreamTaskID: "video-1", UpstreamModel: "agnes-video-2.5-flash"}
	got, err := agnesVideoRetrieveURL(group, task)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://apihub.agnes-ai.com/agnesapi?model_name=agnes-video-2.5-flash&video_id=video-1"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestMediaSafeDialerRejectsPrivateLiteral(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	called := false
	dialer := mediaSafeDialer{dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		called = true
		return client, nil
	}}
	if _, err := dialer.DialContext(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("private literal was accepted")
	}
	if called {
		t.Fatal("private literal reached the dialer")
	}
	if _, err := dialer.DialContext(context.Background(), "tcp", "1.1.1.1:80"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("public literal did not reach the dialer")
	}
}

func TestMediaCredentialBoundChecksIdentityGeneration(t *testing.T) {
	task := media.Task{GroupID: 1, CredentialID: 1, CredentialIdentityGeneration: 2}
	if mediaCredentialBound(task, state.CredentialRef{ID: 1, GroupID: 1, IdentityGeneration: 1}) {
		t.Fatal("stale identity generation was accepted")
	}
	if !mediaCredentialBound(task, state.CredentialRef{ID: 1, GroupID: 1, IdentityGeneration: 2}) {
		t.Fatal("matching identity generation was rejected")
	}
	if !mediaCredentialBound(media.Task{GroupID: 1, CredentialID: 1}, state.CredentialRef{ID: 1, GroupID: 1, IdentityGeneration: 9}) {
		t.Fatal("legacy zero generation should remain readable")
	}
}
