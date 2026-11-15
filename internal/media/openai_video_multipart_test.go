package media

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"
)

func buildVideoMultipart(t *testing.T) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"model": "sora-2", "prompt": "a fox", "seconds": "4", "size": "1280x720",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="input_reference"; filename="ref.png"`)
	header.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("png-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func TestParseOpenAIVideoCreateRequestMultipartPreservesReference(t *testing.T) {
	body, contentType := buildVideoMultipart(t)
	parsed, err := ParseOpenAIVideoCreateRequestForContentType(body, contentType)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Model != "sora-2" || parsed.Prompt != "a fox" || parsed.Seconds != "4" || parsed.Size != "1280x720" {
		t.Fatalf("parsed = %#v", parsed)
	}
	if !bytes.Contains(parsed.NormalizedBody, []byte("png-bytes")) {
		t.Fatalf("reference file was dropped: %q", parsed.NormalizedBody)
	}
	if !strings.HasPrefix(parsed.ContentType, "multipart/form-data; boundary=") {
		t.Fatalf("content type = %q", parsed.ContentType)
	}
}

func TestRewriteOpenAIVideoMultipartModelPreservesReference(t *testing.T) {
	body, contentType := buildVideoMultipart(t)
	rewritten, rewrittenType, err := RewriteOpenAIVideoMultipartModel(body, contentType, "upstream-model")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseOpenAIVideoCreateRequestForContentType(rewritten, rewrittenType)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Model != "upstream-model" {
		t.Fatalf("model = %q", parsed.Model)
	}
	if !bytes.Contains(rewritten, []byte("png-bytes")) {
		t.Fatal("reference file was dropped during model rewrite")
	}
}

func TestNormalizeAgnesOpenAIVideoBody(t *testing.T) {
	normalized, err := NormalizeAgnesOpenAIVideoBody([]byte(`{"model":"m","prompt":"p","size":"1280x720"}`))
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(normalized, &object); err != nil {
		t.Fatal(err)
	}
	if object["size"] != "720P" || object["mode"] != "text" || object["seconds"] != "5" ||
		object["aspect_ratio"] != "16:9" || object["n"] != float64(1) {
		t.Fatalf("agnes-normalized body = %s", normalized)
	}
}
