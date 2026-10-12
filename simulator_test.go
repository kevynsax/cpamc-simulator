package simulator

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func request(t *testing.T, s *Server, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func server(t *testing.T, scenario string) *Server {
	t.Helper()
	s, err := New(Config{Scenario: scenario})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestRoutesAndAuth(t *testing.T) {
	s := server(t, "")
	for _, path := range []string{"/live", "/ready", "/healthz"} {
		if w := request(t, s, "GET", path, "", ""); w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
	for _, path := range []string{"/v1/models", "/v1/quota/view", "/v8/management/credentials", "/v8/management/custom/quota/credentials"} {
		key := ClientKey
		wrong := ManagementKey
		if strings.Contains(path, "management") {
			key, wrong = wrong, key
		}
		for _, k := range []string{"", wrong} {
			if w := request(t, s, "GET", path, k, ""); w.Code != 401 {
				t.Fatal(path, w.Code)
			}
		}
		w := request(t, s, "GET", path, key, "")
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) || w.Header().Get("X-Cpamc-Simulator") != "true" {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if w := request(t, s, "POST", "/v1/live", ClientKey, ""); w.Code != 501 {
		t.Fatal(w.Code)
	}
	if w := request(t, s, "GET", "/unknown", ClientKey, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestChat(t *testing.T) {
	s := server(t, "")
	cases := []struct {
		body     string
		status   int
		contains string
	}{
		{`{"model":"gpt-5.6-luna","messages":[{}]}`, 200, "Simulated response"},
		{`{"model":"gpt-5.6-luna","messages":[{}],"stream":true}`, 200, "data: [DONE]"},
		{`{"model":"gemini-3.1-flash-image","messages":[{"role":"user","content":[{"type":"text","text":"redraw"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]}]}`, 200, "data:image/png;base64,"},
		{`{"model":"gpt-5.6-luna","messages":[{}],"response_format":{"type":"json_schema","json_schema":{"name":"verdict","strict":true,"schema":{"type":"object","additionalProperties":false,"required":["label","confidence","reason"],"properties":{"label":{"type":"string","enum":["on-topic","off-topic","abuse"]},"confidence":{"type":"number"},"reason":{"type":"string"}}}}}}`, 200, `\"label\":\"on-topic\"`},
		{`{"model":"gpt-5.6-luna","messages":[{}],"response_format":{"type":"json_object"}}`, 200, "Simulated translation"},
		{`{"model":"gpt-5.6-luna","messages":[{}],"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object","properties":{"rows":{"type":"array"},"ok":{"type":"boolean"}}}}}}`, 200, `\"rows\":[]`},
		{`{"model":"gpt-5.6-luna","messages":[{}],"response_format":{"type":"json_schema","json_schema":{"schema":{"$ref":"x"}}}}`, 400, "unsupported_schema"},
		{`{"model":"unknown","messages":[{}]}`, 400, "unknown_model"}, {`{"model":"gpt-5.6-luna"}`, 400, "messages_required"}, {`null`, 400, "unknown_model"}, {`{}`, 400, "unknown_model"}, {`{`, 400, "invalid_request"}, {`{} {}`, 400, "invalid_request"},
	}
	for _, tc := range cases {
		w := request(t, s, "POST", "/v1/chat/completions", ClientKey, tc.body)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	w := request(t, s, "POST", "/v1/chat/completions", ClientKey, `{"messages":["`+strings.Repeat("x", MaxBody)+`"]}`)
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
}
func TestTranslationEcho(t *testing.T) {
	s := server(t, "")
	input, _ := json.Marshal(map[string]any{"action": "translate", "targetLanguage": "de", "subject": "Olá", "bodyHtml": `<p>Oi <span data-field="firstName">Nome</span> <a href="https://x.test/?a=1&amp;b=2">link</a> {{projectName}}</p>`, "text": ""})
	messages, _ := json.Marshal([]any{map[string]string{"role": "system", "content": "task"}, map[string]string{"role": "user", "content": string(input)}})
	body := `{"model":"gpt-5.6-luna","messages":` + string(messages) + `,"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object","properties":{"subject":{"type":"string"},"bodyHtml":{"type":"string"},"text":{"type":"string"},"subjects":{"type":"array","items":{"type":"string"}}}}}}}`
	w := request(t, s, "POST", "/v1/chat/completions", ClientKey, body)
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Choices) != 1 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(out.Choices[0].Message.Content), &answer); err != nil {
		t.Fatal(err)
	}
	if answer["subject"] != "[de] Olá" || answer["bodyHtml"] != `[de] <p>Oi <span data-field="firstName">Nome</span> <a href="https://x.test/?a=1&amp;b=2">link</a> {{projectName}}</p>` || answer["text"] != "Simulated value" {
		t.Fatalf("%v", answer)
	}
	// Without a targetLanguage the generic fixture is unchanged.
	plain := strings.Replace(body, `\"targetLanguage\":\"de\",`, "", 1)
	w = request(t, s, "POST", "/v1/chat/completions", ClientKey, plain)
	if w.Code != 200 || strings.Contains(w.Body.String(), "[de]") || !strings.Contains(w.Body.String(), "Simulated value") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestImages(t *testing.T) {
	s := server(t, "")
	w := request(t, s, "POST", "/v1/images/generations", ClientKey, `{"model":"gpt-image-2.5","prompt":"fixture","n":2,"size":"1536x1024","quality":"low"}`)
	var out struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Data) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	raw, _ := base64.StdEncoding.DecodeString(out.Data[0].B64)
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil || img.Bounds().Dx() != 256 {
		t.Fatal(err)
	}
	var b bytes.Buffer
	form := multipart.NewWriter(&b)
	_ = form.WriteField("model", "grok-imagine-image-2.0")
	_ = form.WriteField("prompt", "fixture")
	file, _ := form.CreateFormFile("image", "reference.png")
	_, _ = file.Write(raw)
	_ = form.Close()
	r := httptest.NewRequest("POST", "/v1/images/edits", &b)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+ClientKey)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, model := range []string{"gpt-image-2.5", "grok-imagine-image-2.0", "meta/muse-image-1.0"} {
		if w := request(t, s, "POST", "/v1/images/generations", ClientKey, `{"model":"`+model+`","prompt":"fixture"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"b64_json":"iVBOR`) {
			t.Fatal(model, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"model":"gpt-image-2.5"}`, `{"model":"bad","prompt":"fixture"}`, `{"model":"gpt-5.6-luna","prompt":"fixture"}`, `{"model":"gpt-image-2.5","prompt":"fixture","n":4}`} {
		if w := request(t, s, "POST", "/v1/images/generations", ClientKey, body); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := request(t, s, "POST", "/v1/images/edits", ClientKey, `{}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestDecisionsAndFaults(t *testing.T) {
	body := `{"model":"typesafe/jev-1.13","state":{},"questions":{"q":{"type":"choice","options":{"on-topic":"x","abuse":"y"}},"b":{"type":"noul"}}}`
	w := request(t, server(t, ""), "POST", "/v1/decisions", ClientKey, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"choice":"on-topic"`) || !strings.Contains(w.Body.String(), `"noul":0.9`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, b := range []string{`{}`, `{"model":"typesafe/jev-1.13","state":{},"questions":{"q":{"type":"bad"}}}`} {
		if w := request(t, server(t, ""), "POST", "/v1/decisions", ClientKey, b); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	for scenario, status := range map[string]int{"rate-limited": 429, "upstream-error": 502} {
		s := server(t, scenario)
		for _, path := range []string{"/v1/chat/completions", "/v1/images/generations", "/v1/images/edits", "/v1/decisions"} {
			if w := request(t, s, "POST", path, ClientKey, body); w.Code != status {
				t.Fatal(scenario, path, w.Code)
			}
		}
	}
	if w := request(t, server(t, "low-quota"), "GET", "/v1/quota/view", ClientKey, ""); !strings.Contains(w.Body.String(), `"used_percent":99`) {
		t.Fatal(w.Body.String())
	}
	if _, err := New(Config{Scenario: "bogus"}); err == nil {
		t.Fatal("invalid scenario accepted")
	}
	if _, err := New(Config{ClientKey: "same", ManagementKey: "same"}); err == nil {
		t.Fatal("same keys accepted")
	}
}
func TestCannedDecisionsContract(t *testing.T) {
	s := server(t, "")
	body := `{"model":"typesafe/jev-1.13","state":{"private":"ignored-fixture"},"questions":{"legacy":{"type":"choice","options":{"z-first":"first","a-second":"second"}},"native":{"type":"choice","criteria":{"z-first":"first","a-second":"second"}},"noul":{"type":"noul"},"score":{"type":"score","criteria":["low","medium","high"]},"legacy-score":{"type":"score","levels":["low","high"]},"bool":{"type":"boolean"},"number":{"type":"number"}}}`
	w := request(t, s, "POST", "/v1/decisions", ClientKey, body)
	var out struct {
		Answers   map[string]map[string]any `json:"answers"`
		Simulator bool                      `json:"simulator"`
		Model     string                    `json:"model"`
		Provider  string                    `json:"provider"`
		Usage     struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if !out.Simulator || out.Model != "typesafe/jev-1.13" || out.Provider != "cpamc-simulator" || out.Usage.Cost != 0 || w.Header().Get("X-Cpamc-Simulator") != "true" {
		t.Fatal("missing fixture metadata")
	}
	for _, id := range []string{"legacy", "native"} {
		answer := out.Answers[id]
		if answer["choice"] != "z-first" || answer["confidence"] != float64(1) {
			t.Fatal("first presented choice must win", answer)
		}
		probabilities := answer["probabilities"].(map[string]any)
		if probabilities["z-first"] != float64(1) || probabilities["a-second"] != float64(0) {
			t.Fatal(probabilities)
		}
	}
	if out.Answers["noul"]["noul"] != 0.9 || out.Answers["bool"]["value"] != true || out.Answers["number"]["value"] != float64(1) {
		t.Fatal("missing canned scalar answers")
	}
	for _, id := range []string{"score", "legacy-score"} {
		answer := out.Answers[id]
		if answer["score"] != float64(0) || answer["confidence"] != float64(1) || answer["legend"].(map[string]any)["0"] != "low" || answer["probabilities"].(map[string]any)["0"] != float64(1) {
			t.Fatal(answer)
		}
	}
	for _, answer := range out.Answers {
		if answer["simulator"] != true {
			t.Fatal("answer not marked fake", answer)
		}
	}
	if strings.Contains(w.Body.String(), "ignored-fixture") {
		t.Fatal("state reflected in response")
	}
	for i := 0; i < 5; i++ {
		otherState := strings.Replace(body, "ignored-fixture", "different-fixture", 1)
		other := request(t, s, "POST", "/v1/decisions", ClientKey, otherState)
		if other.Code != 200 || other.Body.String() != w.Body.String() {
			t.Fatal("canned answer depends on state or nondeterministic map order")
		}
	}
}

func TestInvalidCannedDecisionsQuestions(t *testing.T) {
	for _, question := range []string{
		`{"type":"choice"}`, `{"type":"choice","options":{}}`, `{"type":"choice","options":[]}`,
		`{"type":"choice","options":{"":"blank"}}`, `{"type":"choice","options":{"a":"one","a":"duplicate"}}`,
		`{"type":"score"}`, `{"type":"score","levels":[]}`, `{"type":"score","criteria":{}}`,
		`{"type":"score","levels":["low",""]}`, `{"type":"score","levels":[0,1]}`,
	} {
		body := `{"model":"typesafe/jev-1.13","state":{},"questions":{"q":` + question + `}}`
		w := request(t, server(t, ""), "POST", "/v1/decisions", ClientKey, body)
		if w.Code != 400 {
			t.Fatal(question, w.Code, w.Body.String())
		}
	}
}

func TestSchemaOutputBudgetAndConstraints(t *testing.T) {
	schema := map[string]any{"type": "string"}
	for i := 0; i < 8; i++ {
		schema = map[string]any{"type": "array", "minItems": float64(32), "items": schema}
	}
	if _, err := schemaSample(schema, 0); err == nil {
		t.Fatal("exponential fixture allocation accepted")
	}
	for _, schema := range []map[string]any{
		{"type": "object", "required": []any{"missing"}},
		{"type": "array", "minItems": float64(4), "maxItems": float64(2)},
		{"type": "array", "minItems": 0.5},
		{"type": "string", "minLength": 2.5},
		{"type": "integer", "minimum": 0.5},
		{"type": "object", "minProperties": float64(2)},
	} {
		if _, err := schemaSample(schema, 0); err == nil {
			t.Fatal("unsupported constraints accepted", schema)
		}
	}
}

func TestHTTPConcurrent(t *testing.T) {
	ts := httptest.NewServer(server(t, ""))
	defer ts.Close()
	done := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		go func() {
			r, _ := http.NewRequest("GET", ts.URL+"/v1/models", nil)
			r.Header.Set("Authorization", "Bearer "+ClientKey)
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				done <- false
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			done <- resp.StatusCode == 200
		}()
	}
	for i := 0; i < 20; i++ {
		if !<-done {
			t.Fatal("HTTP request failed")
		}
	}
}
