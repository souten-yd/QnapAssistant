package main

import (
 "bytes"
 "context"
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "os"
 "os/exec"
 "path/filepath"
 "strings"
 "testing"
 "time"
)

func testJobManager(t *testing.T, extra string) *manager {
 t.Helper()
 configPath:=filepath.Join(t.TempDir(),"config.env")
 if err:=os.WriteFile(configPath,[]byte("LLM_PROVIDER=local\nLLM_LOAD_TIMEOUT_SECONDS=600\nLLM_GENERATE_TIMEOUT_SECONDS=1800\n"+extra),0600);err!=nil{t.Fatal(err)}
 return &manager{configPath:configPath,lastUsed:time.Now()}
}
func jobRequest() string {
 return `{"messages":[{"role":"user","content":"Hello"}],"max_tokens":64,"stream":false}`
}
func TestParseLLMJobRequest(t *testing.T) {
 b,err:=parseJobRequest([]byte(jobRequest()));if err!=nil{t.Fatal(err)}
 var value map[string]any
 if err:=json.Unmarshal(b,&value);err!=nil{t.Fatal(err)}
 if value["stream"]!=false||value["max_tokens"]!=float64(64){t.Fatalf("unexpected payload: %v",value)}
 for _,bad:=range []string{"null","[]",`{"messages":[]}`,`{"messages":[1],"stream":true}`,
   `{"messages":[1],"max_tokens":2049}`,`{"messages":[1],"tools":[]}`,`{"messages":[1],"max_tokens":"invalid"}`} {
  if _,err:=parseJobRequest([]byte(bad));err==nil{t.Errorf("accepted bad JSON: %s",bad)}
 }
}
func TestLLMJobsRejectCloudAndOversized(t *testing.T){
 m:=testJobManager(t,"LLM_PROVIDER=openrouter\n")
 w:=httptest.NewRecorder()
 m.handleLLMJobs(w,httptest.NewRequest(http.MethodPost,"/api/llm/jobs",strings.NewReader(jobRequest())))
 if w.Code!=http.StatusConflict{t.Fatalf("cloud provider accepted: %d %s",w.Code,w.Body.String())}
 m=testJobManager(t,"")
 for _,tc:=range []struct{body string;code int}{
  {`{"messages":[]}`,http.StatusBadRequest},{strings.Repeat("x",maxJobRequestBytes+1),http.StatusRequestEntityTooLarge},
 }{
  w=httptest.NewRecorder()
  m.handleLLMJobs(w,httptest.NewRequest(http.MethodPost,"/api/llm/jobs",strings.NewReader(tc.body)))
  if w.Code!=tc.code{t.Errorf("bad request got %d want %d",w.Code,tc.code)}
 }
 w=httptest.NewRecorder()
 req:=httptest.NewRequest(http.MethodPost,"/api/llm/jobs",strings.NewReader(jobRequest()))
 req.Header.Set("Origin","https://evil.example")
 m.handleLLMJobs(w,req)
 if w.Code!=http.StatusForbidden{t.Fatalf("cross-origin allowed: %d",w.Code)}
}
func TestLLMJobLifeCycleAndCancellation(t *testing.T){
 llm:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path=="/health"{w.WriteHeader(200);return}
  if r.URL.Path=="/v1/chat/completions"{
   defer r.Body.Close()
   var payload map[string]any
   if err:=json.NewDecoder(r.Body).Decode(&payload);err!=nil{t.Error(err)}
   if payload["stream"]!=true{t.Error("internal streaming not enabled")}
   w.Header().Set("Content-Type","application/json")
   _,_=w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":2}}`))
   return
  }
  http.NotFound(w,r)
 }))
 defer llm.Close()
 // A tracked process signals that llama.cpp is already running; the test
 // HTTP server stands in for its health/completion endpoints.
 port:=strings.TrimPrefix(llm.URL,"http://127.0.0.1:")
 m:=testJobManager(t,"BACKEND_PORT="+port+"\n")
 proc:=exec.Command("sleep","10")
 if err:=proc.Start();err!=nil{t.Fatal(err)}
 m.cmd=proc
 defer func(){_ = proc.Process.Kill();_ = proc.Wait()}()
 w:=httptest.NewRecorder()
 m.handleLLMJobs(w,httptest.NewRequest(http.MethodPost,"/api/llm/jobs",bytes.NewReader([]byte(jobRequest()))))
 if w.Code!=http.StatusAccepted{t.Fatalf("create: %d %s",w.Code,w.Body.String())}
 var admitted struct{RequestID string `json:"request_id"`}
 if err:=json.Unmarshal(w.Body.Bytes(),&admitted);err!=nil{t.Fatal(err)}
 if len(admitted.RequestID)!=52{t.Fatalf("unguessable ID missing: %q",admitted.RequestID)}
 path:="/api/llm/jobs/"+admitted.RequestID
 var state string
 for i:=0;i<80;i++{
  time.Sleep(50*time.Millisecond)
  w=httptest.NewRecorder()
  m.handleLLMJobs(w,httptest.NewRequest(http.MethodGet,path,nil))
  if w.Code!=200{t.Fatalf("poll: %d %s",w.Code,w.Body.String())}
  var status struct{
   State string `json:"status"`
   Elapsed int `json:"elapsed_seconds"`
   StageElapsed int `json:"stage_elapsed_seconds"`
  }
  if err:=json.Unmarshal(w.Body.Bytes(),&status);err!=nil{t.Fatal(err)}
  state=status.State
  if state=="completed"{break}
 }
 if state!="completed"{t.Fatalf("job did not finish, state=%s",state)}
 w=httptest.NewRecorder()
 m.handleLLMJobs(w,httptest.NewRequest(http.MethodGet,path+"/result",nil))
 if w.Code!=200||!strings.Contains(w.Body.String(),`"OK"`){t.Fatalf("result: %d %s",w.Code,w.Body.String())}
 w=httptest.NewRecorder()
 m.handleLLMJobs(w,httptest.NewRequest(http.MethodDelete,path,nil))
 if w.Code!=200{t.Errorf("delete after completion: %d",w.Code)}
}
func TestLLMJobQueueLimitAndCancel(t *testing.T){
 m:=testJobManager(t,"")
 m.jobs=make(map[string]*llmJob)
 now:=time.Now()
 for i:=0;i<maxLLMJobs;i++{
  id,_:=generateJobID()
  ctx,cancel:=context.WithCancel(context.Background())
  m.jobs[id]=&llmJob{id:id,ctx:ctx,cancel:cancel,created:now,stageAt:now,state:"queued"}
 }
 w:=httptest.NewRecorder()
 m.handleLLMJobs(w,httptest.NewRequest(http.MethodPost,"/api/llm/jobs",strings.NewReader(jobRequest())))
 if w.Code!=http.StatusTooManyRequests{t.Fatalf("expected bounded queue 429, got %d",w.Code)}
 for id:=range m.jobs{
  w=httptest.NewRecorder()
  m.handleLLMJobs(w,httptest.NewRequest(http.MethodDelete,"/api/llm/jobs/"+id,nil))
  if w.Code!=200{t.Fatalf("delete: %d",w.Code)}
 }
}
