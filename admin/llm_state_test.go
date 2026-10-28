package main

import (
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "testing"
)

func TestLLMStateReadOnlyAndNoJobContent(t *testing.T) {
 m:=testJobManager(t,"")
 w:=httptest.NewRecorder()
 m.handleLLMState(w,httptest.NewRequest(http.MethodGet,"/api/llm/state",nil))
 if w.Code!=200{t.Fatalf("status: %d %s",w.Code,w.Body.String())}
 var state map[string]any
 if err:=json.Unmarshal(w.Body.Bytes(),&state);err!=nil{t.Fatal(err)}
 if state["status"]!="unloaded"||state["provider"]!="local"{t.Fatalf("bad global state: %v",state)}
 if state["load_timeout_seconds"]!=float64(600)||state["generation_timeout_seconds"]!=float64(1800){t.Fatalf("wrong timeouts: %v",state)}
 if _,ok:=state["request_id"];ok{t.Fatal("global endpoint must not list private job IDs")}
 w=httptest.NewRecorder()
 m.handleLLMState(w,httptest.NewRequest(http.MethodPost,"/api/llm/state",nil))
 if w.Code!=http.StatusMethodNotAllowed{t.Fatalf("POST allowed: %d",w.Code)}
}
