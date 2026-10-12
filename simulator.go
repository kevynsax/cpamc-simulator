// Package simulator provides a secret-free, offline HTTP stand-in for cpamc.
// It never contacts providers, stores prompts, or performs real AI judgments.
package simulator

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	ClientKey     = "simulator-client-key"
	ManagementKey = "simulator-management-key"
	MaxBody       = 16 << 20
)

// Config is immutable for the life of a simulator. Restart to change scenarios.
type Config struct{ ClientKey, ManagementKey, Scenario string }

type Server struct {
	config  Config
	png     string
	started time.Time
}

func New(config Config) (*Server, error) {
	if config.ClientKey == "" {
		config.ClientKey = ClientKey
	}
	if config.ManagementKey == "" {
		config.ManagementKey = ManagementKey
	}
	if config.Scenario == "" {
		config.Scenario = "healthy"
	}
	switch config.Scenario {
	case "healthy", "low-quota", "rate-limited", "upstream-error":
	default:
		return nil, fmt.Errorf("unsupported scenario")
	}
	if config.ClientKey == config.ManagementKey {
		return nil, fmt.Errorf("client and management keys must differ")
	}
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			c := color.RGBA{40, 80, 140, 255}
			if (x/32+y/32)%2 == 0 {
				c = color.RGBA{90, 140, 190, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return &Server{config: config, png: base64.StdEncoding.EncodeToString(b.Bytes()), started: time.Now().UTC()}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Cpamc-Simulator", "true")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/live" || r.URL.Path == "/ready" || r.URL.Path == "/healthz" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fail(w, 405, "method_not_allowed")
			return
		}
		respond(w, 200, map[string]any{"ready": true, "simulator": true})
		return
	}
	management := strings.HasPrefix(r.URL.Path, "/v8/management/")
	key := s.config.ClientKey
	if management {
		key = s.config.ManagementKey
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+key)) != 1 {
		fail(w, 401, "invalid_api_key")
		return
	}
	limit := int64(1 << 20)
	if r.URL.Path == "/v1/images/edits" {
		limit = MaxBody
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/models":
		data := []map[string]any{}
		for _, m := range models {
			data = append(data, map[string]any{"id": m.id, "object": "model", "created": 0, "owned_by": m.owner})
		}
		respond(w, 200, map[string]any{"object": "list", "data": data})
	case "GET /v1/quota/view":
		s.quota(w)
	case "GET /v8/management/credentials":
		respond(w, 200, map[string]any{"files": []any{map[string]any{"id": "simulated-codex", "name": "simulated-codex", "auth_index": "simulated-codex", "provider": "codex", "label": "Simulated subscription", "disabled": false, "unavailable": false, "id_token": map[string]string{"plan_type": "plus"}, "cooldowns": []any{}}}})
	case "GET /v8/management/custom/quota/credentials":
		respond(w, 200, map[string]any{"files": []any{}})
	case "POST /v1/chat/completions":
		if !s.fault(w) {
			s.chat(w, r)
		}
	case "POST /v1/images/generations", "POST /v1/images/edits":
		if !s.fault(w) {
			s.images(w, r)
		}
	case "POST /v1/decisions":
		if !s.fault(w) {
			s.decisions(w, r)
		}
	case "POST /v1/live":
		fail(w, 501, "simulated_voice_not_supported")
	default:
		fail(w, 404, "route_not_supported")
	}
}

type model struct{ id, owner string }

var models = []model{
	{"gpt-5.6-luna", "openai"}, {"gpt-5.6-sol", "openai"}, {"gpt-5.6-terra", "openai"},
	{"gpt-image-2.5", "openai"}, {"gpt-image-2-5", "openai"},
	{"grok-imagine-image", "xai"}, {"grok-imagine-image-2.0", "xai"},
	{"meta/muse-image-1.0", "meta"}, {"gemini-3.1-flash-image", "google"},
	{"typesafe/jev-1.13", "openrouter"},
}

func knownModel(id string) bool {
	for _, m := range models {
		if id == m.id {
			return true
		}
	}
	return false
}
func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func fail(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]any{"error": map[string]string{"message": code, "type": "simulator_error", "code": code}})
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(r.Body)
	if err := d.Decode(out); err != nil {
		status := 400
		if _, ok := err.(*http.MaxBytesError); ok {
			status = 413
		}
		fail(w, status, "invalid_request")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		fail(w, 400, "invalid_request")
		return false
	}
	return true
}
func (s *Server) fault(w http.ResponseWriter) bool {
	switch s.config.Scenario {
	case "rate-limited":
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "rate_limit_exceeded")
		return true
	case "upstream-error":
		fail(w, 502, "simulated_upstream_error")
		return true
	}
	return false
}
func opaqueID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:8])
}

func (s *Server) quota(w http.ResponseWriter) {
	used := 25
	if s.config.Scenario == "low-quota" || s.config.Scenario == "rate-limited" {
		used = 99
	}
	respond(w, 200, map[string]any{"credentials": []any{map[string]any{"id": opaqueID("simulated-codex"), "provider": "codex", "disabled": false, "entries": []any{map[string]any{"url": "https://chatgpt.com/backend-api/wham/usage", "status_code": 200, "fetched_at": s.started.Format(time.RFC3339), "body": map[string]any{"plan_type": "plus", "rate_limit": map[string]any{"primary_window": map[string]any{"used_percent": used, "limit_window_seconds": 18000, "reset_at": s.started.Add(5 * time.Hour).Unix()}, "secondary_window": map[string]any{"used_percent": used, "limit_window_seconds": 604800, "reset_at": s.started.Add(7 * 24 * time.Hour).Unix()}}}}}}}, "last_run": s.started.Format(time.RFC3339), "refresh_every": "10m", "window": "simulated"})
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model          string            `json:"model"`
		Stream         bool              `json:"stream"`
		Messages       []json.RawMessage `json:"messages"`
		ResponseFormat struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Schema map[string]any `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !knownModel(req.Model) {
		fail(w, 400, "unknown_model")
		return
	}
	if len(req.Messages) == 0 {
		fail(w, 400, "messages_required")
		return
	}
	content := "Simulated response — no AI provider was contacted."
	if req.ResponseFormat.Type == "json_schema" {
		value, err := schemaSample(req.ResponseFormat.JSONSchema.Schema, 0)
		if err != nil {
			fail(w, 400, "unsupported_schema")
			return
		}
		echoTranslation(value, req.Messages)
		b, _ := json.Marshal(value)
		content = string(b)
	} else if req.ResponseFormat.Type == "json_object" {
		content = `{"en-US":"Simulated translation","es":"Traducción simulada","fr":"Traduction simulée","de":"Simulierte Übersetzung"}`
	}
	message := map[string]any{"role": "assistant", "content": content}
	if strings.Contains(req.Model, "image") {
		message["images"] = []any{map[string]any{"image_url": map[string]string{"url": "data:image/png;base64," + s.png}}}
	}
	usage := struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
		Total      int `json:"total_tokens"`
	}{8, 12, 20}
	if !req.Stream {
		respond(w, 200, map[string]any{"id": "chatcmpl-simulator", "object": "chat.completion", "created": 0, "model": req.Model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "stop"}}, "usage": usage})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(choices []any, u any) {
		body := map[string]any{"id": "chatcmpl-simulator", "object": "chat.completion.chunk", "created": 0, "model": req.Model, "choices": choices}
		if u != nil {
			body["usage"] = u
		}
		b, _ := json.Marshal(body)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	chunk([]any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": content}, "finish_reason": nil}}, nil)
	chunk([]any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, nil)
	chunk([]any{}, usage)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func (s *Server) images(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
		N      int    `json:"n"`
	}
	if r.URL.Path == "/v1/images/edits" {
		// Limit stays in memory; do not create multipart temporary files.
		reader, err := r.MultipartReader()
		if err != nil {
			fail(w, 400, "invalid_multipart")
			return
		}
		hasImage := false
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				fail(w, 400, "invalid_multipart")
				return
			}
			if part.FormName() == "image" {
				n, err := io.Copy(io.Discard, part)
				if err != nil || n == 0 {
					fail(w, 400, "image_required")
					return
				}
				hasImage = true
			} else {
				b, err := io.ReadAll(io.LimitReader(part, 4097))
				if err != nil || len(b) > 4096 {
					fail(w, 400, "invalid_field")
					return
				}
				switch part.FormName() {
				case "model":
					req.Model = string(b)
				case "prompt":
					req.Prompt = string(b)
				case "n":
					req.N, _ = strconv.Atoi(string(b))
				}
			}
			_ = part.Close()
		}
		if !hasImage {
			fail(w, 400, "image_required")
			return
		}
	} else if !decode(w, r, &req) {
		return
	}
	if !knownModel(req.Model) || !strings.Contains(req.Model, "image") {
		fail(w, 400, "unknown_model")
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		fail(w, 400, "prompt_required")
		return
	}
	if req.N == 0 {
		req.N = 1
	}
	if req.N < 1 || req.N > 3 {
		fail(w, 400, "invalid_image_count")
		return
	}
	data := []any{}
	for i := 0; i < req.N; i++ {
		data = append(data, map[string]string{"b64_json": s.png})
	}
	respond(w, 200, map[string]any{"created": 0, "data": data})
}

func (s *Server) decisions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model     string          `json:"model"`
		State     json.RawMessage `json:"state"`
		Questions map[string]struct {
			Type     string          `json:"type"`
			Options  json.RawMessage `json:"options"`
			Criteria json.RawMessage `json:"criteria"`
			Levels   json.RawMessage `json:"levels"`
		} `json:"questions"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Model != "typesafe/jev-1.13" || len(req.Questions) == 0 || len(req.Questions) > 48 || len(req.State) == 0 {
		fail(w, 400, "invalid_decisions_request")
		return
	}
	answers := map[string]any{}
	for id, q := range req.Questions {
		// Canned answers deliberately ignore state. Every answer is explicitly marked fake.
		answer := map[string]any{"type": q.Type, "simulator": true}
		switch q.Type {
		case "choice":
			options := q.Options
			if len(options) == 0 {
				options = q.Criteria
			}
			keys, err := orderedChoiceKeys(options)
			if err != nil {
				fail(w, 400, "invalid_choice_options")
				return
			}
			probabilities := map[string]int{}
			for _, key := range keys {
				probabilities[key] = 0
			}
			probabilities[keys[0]] = 1
			answer["choice"], answer["confidence"], answer["probabilities"] = keys[0], 1, probabilities
		case "noul":
			answer["noul"] = 0.9
		case "score":
			levels := q.Levels
			if len(levels) == 0 {
				levels = q.Criteria
			}
			var labels []string
			if json.Unmarshal(levels, &labels) != nil || len(labels) == 0 {
				fail(w, 400, "invalid_score_levels")
				return
			}
			legend, probabilities := map[string]string{}, map[string]int{}
			for index, label := range labels {
				if strings.TrimSpace(label) == "" {
					fail(w, 400, "invalid_score_levels")
					return
				}
				key := strconv.Itoa(index)
				legend[key], probabilities[key] = label, 0
			}
			probabilities["0"] = 1
			answer["score"], answer["confidence"], answer["probabilities"], answer["legend"] = 0, 1, probabilities, legend
		case "boolean":
			answer["value"] = true
		case "number":
			answer["value"] = 1
		default:
			fail(w, 400, "unsupported_question_type")
			return
		}
		answers[id] = answer
	}
	respond(w, 200, map[string]any{"id": "decisions-simulator", "model": req.Model, "provider": "cpamc-simulator", "answers": answers, "usage": map[string]any{"input_tokens": 8, "output_tokens": 12, "cost": 0}, "simulator": true})
}

// Keep request order rather than Go map order, so the first offered choice wins consistently.
func orderedChoiceKeys(raw json.RawMessage) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("choice options must be an object")
	}
	keys, seen := []string{}, map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid choice options")
		}
		key, ok := token.(string)
		if !ok || strings.TrimSpace(key) == "" || seen[key] {
			return nil, fmt.Errorf("invalid or duplicate choice key")
		}
		var label json.RawMessage
		if decoder.Decode(&label) != nil {
			return nil, fmt.Errorf("invalid choice label")
		}
		seen[key] = true
		keys = append(keys, key)
	}
	if _, err := decoder.Token(); err != nil || len(keys) == 0 {
		return nil, fmt.Errorf("choice options are empty")
	}
	return keys, nil
}

// echoTranslation fills a translation-shaped answer (the last user message is a JSON object with a
// "targetLanguage") from the request itself: each top-level string the schema asks for that the input
// also carries, non-empty, comes back as "[<targetLanguage>] <input>". Markup, URLs and placeholders
// stay byte-identical so callers' lossless checks pass; the text is still not a translation.
func echoTranslation(value any, messages []json.RawMessage) {
	answer, ok := value.(map[string]any)
	if !ok || len(messages) == 0 {
		return
	}
	var last struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if json.Unmarshal(messages[len(messages)-1], &last) != nil || last.Role != "user" {
		return
	}
	var input map[string]any
	if json.Unmarshal([]byte(last.Content), &input) != nil {
		return
	}
	target, _ := input["targetLanguage"].(string)
	if target == "" || len(target) > 16 {
		return
	}
	for key, current := range answer {
		source, isString := input[key].(string)
		if _, wanted := current.(string); wanted && isString && strings.TrimSpace(source) != "" {
			answer[key] = "[" + target + "] " + source
		}
	}
}

// schemaSample supports the small, local JSON-schema subset used in fixtures.
// Unsupported constraints are rejected rather than falsely claiming a valid answer.
func schemaSample(schema map[string]any, depth int) (any, error) {
	remaining := 1024
	return schemaSampleBounded(schema, depth, &remaining)
}

func schemaSampleBounded(schema map[string]any, depth int, remaining *int) (any, error) {
	*remaining = *remaining - 1
	if *remaining < 0 {
		return nil, fmt.Errorf("schema output budget exceeded")
	}
	if depth > 12 || len(schema) == 0 {
		return nil, fmt.Errorf("schema depth or empty schema")
	}
	for key := range schema {
		switch key {
		case "type", "title", "description", "default", "$schema", "properties", "required", "additionalProperties", "items", "minItems", "maxItems", "minLength", "maxLength", "minimum", "maximum", "enum", "const":
		default:
			return nil, fmt.Errorf("unsupported constraint")
		}
	}
	if v, ok := schema["const"]; ok {
		return v, nil
	}
	if v, ok := schema["enum"].([]any); ok && len(v) > 0 {
		return v[0], nil
	}
	kind, _ := schema["type"].(string)
	if kinds, ok := schema["type"].([]any); ok && len(kinds) > 0 {
		kind, _ = kinds[0].(string)
	}
	switch kind {
	case "object":
		out := map[string]any{}
		props, _ := schema["properties"].(map[string]any)
		if len(props) > 100 {
			return nil, fmt.Errorf("too many properties")
		}
		if required, ok := schema["required"].([]any); ok {
			for _, name := range required {
				key, ok := name.(string)
				if _, exists := props[key]; !ok || !exists {
					return nil, fmt.Errorf("required property missing")
				}
			}
		}
		for k, v := range props {
			child, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid property")
			}
			value, err := schemaSampleBounded(child, depth+1, remaining)
			if err != nil {
				return nil, err
			}
			out[k] = value
		}
		return out, nil
	case "array":
		count := 0
		if _, hasItems := schema["items"].(map[string]any); hasItems {
			count = 3
		}
		if max, ok := schema["maxItems"].(float64); ok && max >= 0 && max < float64(count) {
			count = int(max)
		}
		if n, ok := schema["minItems"].(float64); ok {
			if n != math.Trunc(n) || n > 32 || n < 0 {
				return nil, fmt.Errorf("invalid array size")
			}
			count = int(n)
		}
		if max, ok := schema["maxItems"].(float64); ok && (max < float64(count) || max != math.Trunc(max)) {
			return nil, fmt.Errorf("invalid array size")
		}
		if count < 0 || count > 32 {
			return nil, fmt.Errorf("invalid array size")
		}
		out := []any{}
		for i := 0; i < count; i++ {
			child, _ := schema["items"].(map[string]any)
			value, err := schemaSampleBounded(child, depth+1, remaining)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	case "string":
		value := "Simulated value"
		min, _ := schema["minLength"].(float64)
		max, hasMax := schema["maxLength"].(float64)
		if min > 1024 || min < 0 || min != math.Trunc(min) || (hasMax && (max < min || max < 0 || max != math.Trunc(max))) {
			return nil, fmt.Errorf("invalid string size")
		}
		if len(value) < int(min) {
			value += strings.Repeat("x", int(min)-len(value))
		}
		if hasMax && len(value) > int(max) {
			value = value[:int(max)]
		}
		return value, nil
	case "integer", "number":
		value := 0.0
		if min, ok := schema["minimum"].(float64); ok && min > 0 {
			value = min
		}
		if max, ok := schema["maximum"].(float64); ok && value > max {
			value = max
		}
		if min, ok := schema["minimum"].(float64); ok && value < min {
			return nil, fmt.Errorf("invalid number bounds")
		}
		if kind == "integer" && value != math.Trunc(value) {
			return nil, fmt.Errorf("non-integer bound is unsupported")
		}
		for _, k := range []string{"multipleOf", "exclusiveMinimum", "exclusiveMaximum"} {
			if _, ok := schema[k]; ok {
				return nil, fmt.Errorf("unsupported numeric constraint")
			}
		}
		return value, nil
	case "boolean":
		return false, nil
	case "null":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported schema type")
	}
}
