package media

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"strings"
)

const maxOpenAIVideoMultipartPartBytes = 64 << 20

type openAIVideoMultipartPart struct {
	Name     string
	FileName string
	Header   textproto.MIMEHeader
	Data     []byte
}

// ParseOpenAIVideoCreateRequestForContentType accepts both the JSON create body
// and the multipart form used by the official OpenAI SDK. Multipart bodies keep
// their file parts; text fields are validated by the same rules as JSON.
func ParseOpenAIVideoCreateRequestForContentType(body []byte, contentType string) (OpenAIVideoCreateRequest, error) {
	mediaType, params, _ := mime.ParseMediaType(strings.TrimSpace(contentType))
	if mediaType == "" || mediaType == "application/json" {
		return ParseOpenAIVideoCreateRequest(body)
	}
	if mediaType != "multipart/form-data" {
		return OpenAIVideoCreateRequest{}, &ValidationError{
			Param: "", Message: "Request body must be a JSON object or multipart/form-data.",
		}
	}
	boundary := params["boundary"]
	if boundary == "" {
		return OpenAIVideoCreateRequest{}, &ValidationError{
			Param: "", Message: "multipart/form-data requires a boundary.",
		}
	}
	parts, err := parseOpenAIVideoMultipart(body, boundary)
	if err != nil {
		return OpenAIVideoCreateRequest{}, err
	}
	fields, err := openAIVideoMultipartFields(parts)
	if err != nil {
		return OpenAIVideoCreateRequest{}, err
	}
	encodedFields, err := json.Marshal(fields)
	if err != nil {
		return OpenAIVideoCreateRequest{}, &ValidationError{Param: "", Message: "Request body must be a valid JSON object."}
	}
	parsed, err := ParseOpenAIVideoCreateRequest(encodedFields)
	if err != nil {
		return OpenAIVideoCreateRequest{}, err
	}
	normalized, normalizedType, err := writeOpenAIVideoMultipart(parts, parsed)
	if err != nil {
		return OpenAIVideoCreateRequest{}, err
	}
	parsed.NormalizedBody = normalized
	parsed.ContentType = normalizedType
	return parsed, nil
}

// RewriteOpenAIVideoMultipartModel replaces the model field while preserving
// every other multipart part, including reference files.
func RewriteOpenAIVideoMultipartModel(body []byte, contentType, model string) ([]byte, string, error) {
	mediaType, params, _ := mime.ParseMediaType(strings.TrimSpace(contentType))
	if mediaType != "multipart/form-data" {
		return nil, "", fmt.Errorf("not a multipart body")
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is missing")
	}
	parts, err := parseOpenAIVideoMultipart(body, boundary)
	if err != nil {
		return nil, "", err
	}
	seen := false
	for index := range parts {
		if parts[index].Name == "model" {
			parts[index].Data = []byte(model)
			seen = true
		}
	}
	if !seen {
		parts = append(parts, openAIVideoMultipartPart{Name: "model", Data: []byte(model)})
	}
	normalized, normalizedType, err := writeOpenAIVideoMultipartParts(parts)
	if err != nil {
		return nil, "", err
	}
	return normalized, normalizedType, nil
}

func parseOpenAIVideoMultipart(body []byte, boundary string) ([]openAIVideoMultipartPart, error) {
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	parts := make([]openAIVideoMultipartPart, 0, 8)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, &ValidationError{Param: "", Message: "Request body must be valid multipart/form-data."}
		}
		name := part.FormName()
		if name == "" {
			_ = part.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, maxOpenAIVideoMultipartPartBytes))
		_ = part.Close()
		if err != nil {
			return nil, &ValidationError{Param: "", Message: "Request body must be valid multipart/form-data."}
		}
		parts = append(parts, openAIVideoMultipartPart{
			Name: name, FileName: part.FileName(), Header: cloneMIMEHeader(part.Header), Data: data,
		})
	}
	if len(parts) == 0 {
		return nil, &ValidationError{Param: "", Message: "Request body must be valid multipart/form-data."}
	}
	return parts, nil
}

func openAIVideoMultipartFields(parts []openAIVideoMultipartPart) (map[string]any, error) {
	fields := make(map[string]any, len(parts))
	for _, part := range parts {
		if part.FileName != "" {
			continue
		}
		switch part.Name {
		case "model", "prompt", "seconds", "size", "mode", "aspect_ratio", "first_frame", "last_frame":
			fields[part.Name] = string(part.Data)
		case "images", "audios", "videos":
			var value any
			if json.Unmarshal(part.Data, &value) == nil {
				fields[part.Name] = value
			}
		}
	}
	return fields, nil
}

func writeOpenAIVideoMultipart(parts []openAIVideoMultipartPart, request OpenAIVideoCreateRequest) ([]byte, string, error) {
	updated := make([]openAIVideoMultipartPart, 0, len(parts))
	for _, part := range parts {
		if part.FileName != "" {
			updated = append(updated, part)
			continue
		}
		switch part.Name {
		case "model":
			part.Data = []byte(request.Model)
		case "prompt":
			part.Data = []byte(request.Prompt)
		case "seconds":
			part.Data = []byte(request.Seconds)
		case "size":
			part.Data = []byte(request.Size)
		case "mode":
			part.Data = []byte(request.Mode)
		case "aspect_ratio":
			part.Data = []byte(request.AspectRatio)
		}
		if len(part.Data) == 0 && part.Name != "model" && part.Name != "prompt" {
			continue
		}
		updated = append(updated, part)
	}
	return writeOpenAIVideoMultipartParts(updated)
}

func cloneMIMEHeader(source textproto.MIMEHeader) textproto.MIMEHeader {
	if source == nil {
		return nil
	}
	clone := make(textproto.MIMEHeader, len(source))
	for key, values := range source {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}

func writeOpenAIVideoMultipartParts(parts []openAIVideoMultipartPart) ([]byte, string, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for _, part := range parts {
		header := cloneMIMEHeader(part.Header)
		if header == nil {
			header = make(textproto.MIMEHeader)
		}
		header.Del("Content-Disposition")
		disposition := fmt.Sprintf(`form-data; name="%s"`, part.Name)
		if part.FileName != "" {
			disposition += fmt.Sprintf(`; filename="%s"`, part.FileName)
			if header.Get("Content-Type") == "" {
				header.Set("Content-Type", "application/octet-stream")
			}
		}
		header.Set("Content-Disposition", disposition)
		partWriter, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", &ValidationError{Param: "", Message: "Request body must be valid multipart/form-data."}
		}
		if _, err := partWriter.Write(part.Data); err != nil {
			return nil, "", &ValidationError{Param: "", Message: "Request body must be valid multipart/form-data."}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", &ValidationError{Param: "", Message: "Request body must be valid multipart/form-data."}
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

// NormalizeAgnesOpenAIVideoBody applies the channel-specific defaults and the
// legacy size enum expected by Agnes. Standard OpenAI requests keep their own
// values and are handled by the generic dialect.
func NormalizeAgnesOpenAIVideoBody(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, &ValidationError{Param: "", Message: "Request body must be a valid JSON object."}
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err == nil {
		return nil, &ValidationError{Param: "", Message: "Request body must contain exactly one JSON object."}
	}
	setDefault := func(key string, value any) error {
		if _, exists := object[key]; exists {
			return nil
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		object[key] = encoded
		return nil
	}
	for key, value := range map[string]any{
		"mode":         DefaultMode,
		"aspect_ratio": DefaultAspectRatio,
		"seconds":      DefaultSeconds,
		"n":            1,
	} {
		if err := setDefault(key, value); err != nil {
			return nil, err
		}
	}
	if raw, exists := object["size"]; exists {
		var size string
		if json.Unmarshal(raw, &size) == nil && size != "" && !agnesSizeEnum(size) {
			mapped, ok := agnesSize(size)
			if !ok {
				return nil, &ValidationError{Param: "size", Message: "size is not supported by the Agnes channel."}
			}
			encoded, err := json.Marshal(mapped)
			if err != nil {
				return nil, err
			}
			object["size"] = encoded
		}
	} else if err := setDefault("size", DefaultSize); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseOpenAIVideoCreateRequest(normalized)
	if err != nil {
		return nil, err
	}
	return parsed.NormalizedBody, nil
}

func agnesSizeEnum(value string) bool {
	switch value {
	case "720P", "1080P", "1K", "2K":
		return true
	default:
		return false
	}
}

func agnesSize(value string) (string, bool) {
	parts := strings.Split(strings.ToLower(value), "x")
	if len(parts) != 2 {
		return "", false
	}
	width, err := strconv.Atoi(parts[0])
	if err != nil || width <= 0 {
		return "", false
	}
	height, err := strconv.Atoi(parts[1])
	if err != nil || height <= 0 {
		return "", false
	}
	longest := width
	if height > longest {
		longest = height
	}
	switch {
	case longest <= 1280:
		return "720P", true
	case longest <= 1920:
		return "1080P", true
	case longest <= 2560:
		return "1K", true
	case longest <= 4096:
		return "2K", true
	default:
		return "", false
	}
}
