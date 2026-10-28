package main

import (
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func localTestManager(t *testing.T, provider string) *manager {
    t.Helper()
    path := filepath.Join(t.TempDir(), "config.env")
    if err := os.WriteFile(path, []byte("LLM_PROVIDER="+provider+"\nBACKEND_PORT=11436\n"), 0600); err != nil {
        t.Fatal(err)
    }
    return &manager{configPath: path}
}

func TestLocalTestInputValidation(t *testing.T) {
    valid := localTestInput{Prompt:"1週間の歩数を整理して", MaxTokens:512}
    if err := validateLocalTestInput(valid); err != nil { t.Fatal(err) }
    for _, input := range []localTestInput{
        {Prompt:" ", MaxTokens:512},
        {Prompt:"hello",MaxTokens:1},
        {Prompt:"hello",MaxTokens:3000},
        {Prompt:strings.Repeat("x", 192001),MaxTokens:512},
        {Prompt:"hello",SystemPrompt:strings.Repeat("x",12001),MaxTokens:512},
    } {
        if err := validateLocalTestInput(input); err == nil {
            t.Errorf("accepted invalid input of length %d with max_tokens %d",len(input.Prompt),input.MaxTokens)
        }
    }
}

func TestLocalLLMTestRejectsRemoteProviderBeforeRequest(t *testing.T) {
    m := localTestManager(t,"openrouter")
    req:=httptest.NewRequest(http.MethodPost,"/api/llm/local-test", strings.NewReader(`{"prompt":"sensitive example","max_tokens":512}`))
    rec:=httptest.NewRecorder()
    m.handleLocalLLMTest(rec,req)
    if rec.Code!=http.StatusConflict {t.Fatalf("remote provider test status %d: %s",rec.Code,rec.Body.String())}
}

func TestLocalLLMTestRejectsBadRequestsWithoutStartingLLM(t *testing.T) {
    m:=localTestManager(t,"local")
    cases:=[]struct{method,body string;code int}{
        {http.MethodGet,"",http.StatusMethodNotAllowed},
        {http.MethodPost,`{"prompt":"","max_tokens":512}`,http.StatusBadRequest},
        {http.MethodPost,`{"prompt":"test","max_tokens":0}`,http.StatusBadRequest},
        {http.MethodPost,`{"prompt":"test","max_tokens":512,"url":"https://example.com"}`,http.StatusBadRequest},
        {http.MethodPost,`{"prompt":"test","max_tokens":512} {"prompt":"second","max_tokens":512}`,http.StatusBadRequest},
    }
    for _,tc:=range cases{
        req:=httptest.NewRequest(tc.method,"/api/llm/local-test",strings.NewReader(tc.body))
        rec:=httptest.NewRecorder()
        m.handleLocalLLMTest(rec,req)
        if rec.Code!=tc.code { t.Errorf("method=%s body=%q: got %d want %d (%s)",tc.method,tc.body,rec.Code,tc.code,rec.Body.String())}
    }
}
