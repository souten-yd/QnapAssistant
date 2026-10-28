package main

import (
 "bytes"
 "context"
 "crypto/rand"
 "encoding/hex"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net"
 "net/http"
 "strconv"
 "strings"
 "time"
)

const maxLLMJobs = 5 // One running, at most four waiting.
const maxJobRequestBytes = 256 << 10
const maxJobResultBytes = 256 << 10
const jobRetention = 15 * time.Minute

type llmJob struct {
 id string
 created time.Time
 stageAt time.Time
 finished time.Time
 state string
 errorCode string
 errorMessage string
 request []byte
 result []byte
 cancel context.CancelFunc
 ctx context.Context
 model string
 contextSize int
 inputBytes int
 generationStarted time.Time
 firstTokenAt time.Time
 lastTokenAt time.Time
 partialOutput string
 partialTruncated bool
 tokenChunks int
 promptTokens int
 completionTokens int
 finishReason string
}

func (j *llmJob) terminal() bool {
 return j.state == "completed" || j.state == "failed" || j.state == "timed_out" || j.state == "cancelled"
}

func generateJobID() (string, error) {
 b := make([]byte, 24)
 if _, err := rand.Read(b); err != nil { return "", err }
 return "req_" + hex.EncodeToString(b), nil
}

func parseJobRequest(body []byte) ([]byte,error) {
 var payload map[string]json.RawMessage
 if err:=json.Unmarshal(body,&payload);err!=nil{return nil,errors.New("invalid JSON object")}
 if payload==nil{return nil,errors.New("JSON object required")}
 var messages []json.RawMessage
 if err:=json.Unmarshal(payload["messages"],&messages);err!=nil||len(messages)==0||len(messages)>64{
  return nil,errors.New("messages must contain 1 to 64 messages")
 }
 for _,raw:=range messages{
  var entry struct{Role string `json:"role"`;Content json.RawMessage `json:"content"`}
  if err:=json.Unmarshal(raw,&entry);err!=nil{return nil,errors.New("invalid message object")}
  if entry.Role!="system"&&entry.Role!="user"&&entry.Role!="assistant"{return nil,errors.New("unsupported message role")}
  var content string
  if err:=json.Unmarshal(entry.Content,&content);err!=nil||strings.TrimSpace(content)==""{
   return nil,errors.New("messages require nonempty text content")
  }
 }
 var stream bool
 if b,ok:=payload["stream"];ok {
  if err:=json.Unmarshal(b,&stream);err!=nil||stream{return nil,errors.New("asynchronous jobs require stream=false")}
 }
 payload["stream"]=json.RawMessage("false")
 if b,ok:=payload["max_tokens"];ok{
  var n int
  if err:=json.Unmarshal(b,&n);err!=nil||n<1||n>2048{return nil,errors.New("max_tokens must be between 1 and 2048")}
 }else{payload["max_tokens"]=json.RawMessage("512")}
 if b,ok:=payload["model"];ok{
  var name string
  if err:=json.Unmarshal(b,&name);err!=nil{return nil,errors.New("invalid model identifier")}
  if name=="auto"||name==""{delete(payload,"model")}
 }
 if _,ok:=payload["tools"];ok{return nil,errors.New("tools are not supported for local asynchronous jobs")}
 if _,ok:=payload["tool_choice"];ok{return nil,errors.New("tools are not supported for local asynchronous jobs")}
 return json.Marshal(payload)
}

func (m *manager) ensureJobQueueLocked(){
 if m.jobs==nil{m.jobs=make(map[string]*llmJob)}
 if m.jobQueue==nil{
  m.jobQueue=make(chan *llmJob,maxLLMJobs)
  go m.jobWorker(m.jobQueue)
 }
}

func (m *manager) cleanFinishedJobsLocked(){
 now:=time.Now()
 for id,j:=range m.jobs{
  if j.terminal()&&now.Sub(j.finished)>jobRetention{
   j.request=nil;j.result=nil
   delete(m.jobs,id)
  }
 }
}

func (m *manager) markJob(j *llmJob,state,code,msg string,result []byte){
 m.jobsMu.Lock()
 defer m.jobsMu.Unlock()
 if j.terminal(){return}
 j.state=state;j.stageAt=time.Now()
 j.errorCode=code;j.errorMessage=msg
 if state=="completed"{j.result=result}
 if j.terminal(){j.finished=time.Now();j.request=nil}
}

func (m *manager) jobWorker(queue <-chan *llmJob){
 for j:=range queue{
  m.jobsMu.Lock()
  cancelled:=j.terminal()
  m.jobsMu.Unlock()
  if !cancelled{m.runJob(j)}
 }
}

func (m *manager) runJob(j *llmJob){
 defer j.cancel()
 // Acquire a lease BEFORE model loading to prevent idle-unload racing startup.
 release:=m.beginLLMUse()
 defer release()
 m.markJob(j,"loading_model","","",nil)
 cfg,err:=loadConfig(m.configPath)
 if err!=nil{m.markJob(j,"failed","config_unavailable","local configuration unavailable",nil);return}
 cfg=defaults(cfg)
 if normalizedLLMProvider(cfg)!="local"{
  m.markJob(j,"failed","provider_changed","local provider is no longer selected",nil);return
 }
 if err=m.ensureReady(j.ctx);err!=nil{
  if errors.Is(j.ctx.Err(),context.Canceled){m.markJob(j,"cancelled","cancelled","cancelled by client",nil)
  }else if errors.Is(err,context.DeadlineExceeded){m.markJob(j,"timed_out","load_timeout","model load timed out",nil)
  }else{m.markJob(j,"failed","model_start_failed",err.Error(),nil)}
  return
 }
 m.markJob(j,"ready","","",nil)
 latest,err:=loadConfig(m.configPath)
 if err!=nil||normalizedLLMProvider(defaults(latest))!="local"{
  m.markJob(j,"failed","provider_changed","provider changed while loading",nil);return
 }
 cfg=defaults(latest)
 port:=cfg["BACKEND_PORT"]
 portNum,err:=strconv.Atoi(port)
 if err!=nil||portNum<1||portNum>65535{
  m.markJob(j,"failed","invalid_backend","invalid loopback port",nil);return
 }
 req,err:=http.NewRequestWithContext(j.ctx,http.MethodPost,
  "http://"+net.JoinHostPort("127.0.0.1",port)+"/v1/chat/completions",bytes.NewReader(j.request))
 if err!=nil{m.markJob(j,"failed","invalid_request",err.Error(),nil);return}
 req.Header.Set("Content-Type","application/json")
 if err=applyThinkingMode(req,cfg);err!=nil{
  m.markJob(j,"failed","thinking_config",err.Error(),nil);return
 }
 // Request internal SSE so partial text remains available after a timeout.
 rewritten,err:=io.ReadAll(io.LimitReader(req.Body,maxJobRequestBytes+1))
 if err!=nil||len(rewritten)>maxJobRequestBytes{m.markJob(j,"failed","invalid_request","streaming request too large",nil);return}
 var streamPayload map[string]json.RawMessage
 if err=json.Unmarshal(rewritten,&streamPayload);err!=nil{m.markJob(j,"failed","invalid_request","stream request malformed",nil);return}
 streamPayload["stream"]=json.RawMessage("true")
 rewritten,err=json.Marshal(streamPayload)
 if err!=nil{m.markJob(j,"failed","invalid_request","stream request failed",nil);return}
 req.Body=io.NopCloser(bytes.NewReader(rewritten))
 req.ContentLength=int64(len(rewritten))
 m.jobsMu.Lock();j.generationStarted=time.Now();m.jobsMu.Unlock()
 m.markJob(j,"generating","","",nil)
 generateTimeout:=time.Duration(llmGenerateTimeout(cfg))*time.Second
 ctx,cancel:=context.WithTimeout(j.ctx,generateTimeout)
 defer cancel()
 req=req.WithContext(ctx)
 client:=&http.Client{CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse},
  Transport:&http.Transport{Proxy:nil,DialContext:(&net.Dialer{Timeout:10*time.Second}).DialContext}}
 defer client.CloseIdleConnections()
 resp,err:=client.Do(req)
 if err!=nil{
  if errors.Is(j.ctx.Err(),context.Canceled){m.markJob(j,"cancelled","cancelled","cancelled by client",nil)
  }else if errors.Is(ctx.Err(),context.DeadlineExceeded){m.markGenerationTimeout(j)
  }else{m.markJob(j,"failed","backend_unreachable",err.Error(),nil)}
  return
 }
 defer resp.Body.Close()
 data,err:=m.readDiagnosticStream(j,resp)
 if err!=nil{
  if errors.Is(j.ctx.Err(),context.Canceled){m.markJob(j,"cancelled","cancelled","cancelled by client",nil)
  }else if errors.Is(ctx.Err(),context.DeadlineExceeded){m.markGenerationTimeout(j)
  }else{m.markJob(j,"failed","response_read_error",err.Error(),nil)}
  return
 }
 if resp.StatusCode!=http.StatusOK{
  m.markJob(j,"failed",fmt.Sprintf("backend_http_%d",resp.StatusCode),
    fmt.Sprintf("local model HTTP %d",resp.StatusCode),nil);return
 }
 var completion struct{Choices []json.RawMessage `json:"choices"`}
 if err=json.Unmarshal(data,&completion);err!=nil||len(completion.Choices)==0{
  m.markJob(j,"failed","invalid_backend_response","local LLM response was not a completion",nil);return
 }
 if errors.Is(j.ctx.Err(),context.Canceled){m.markJob(j,"cancelled","cancelled","cancelled by client",nil);return}
 m.markJob(j,"completed","","",data)
}

func (m *manager) handleLLMJobs(w http.ResponseWriter,r *http.Request){
 w.Header().Set("Cache-Control","no-store")
 w.Header().Set("X-Content-Type-Options","nosniff")
 // A third-party website cannot submit/poll health data via browser CORS.
 if origin:=r.Header.Get("Origin");origin!=""{
  ohost:=strings.TrimPrefix(strings.TrimPrefix(origin,"http://"),"https://")
  if strings.ContainsAny(ohost,"/")||ohost!=r.Host{
   http.Error(w,"cross-origin LLM jobs are not allowed",http.StatusForbidden);return
  }
 }
 if r.URL.Path=="/api/llm/jobs" {
  if r.Method!=http.MethodPost{w.Header().Set("Allow","POST");http.Error(w,"POST required",http.StatusMethodNotAllowed);return}
  m.createLLMJob(w,r);return
 }
 if !strings.HasPrefix(r.URL.Path,"/api/llm/jobs/"){http.NotFound(w,r);return}
 sub:=strings.TrimPrefix(r.URL.Path,"/api/llm/jobs/")
 diagnostic:=strings.HasSuffix(sub,"/diagnostics")
 if diagnostic{sub=strings.TrimSuffix(sub,"/diagnostics")}
 result:=strings.HasSuffix(sub,"/result")
 if result{sub=strings.TrimSuffix(sub,"/result")}
 if sub==""||strings.Contains(sub,"/"){http.NotFound(w,r);return}
 if diagnostic{m.handleJobDiagnostics(w,r,sub);return}
 m.jobsMu.Lock()
 m.cleanFinishedJobsLocked()
 j:=m.jobs[sub]
 if j==nil{m.jobsMu.Unlock();http.NotFound(w,r);return}
 if result {
  if r.Method!=http.MethodGet{m.jobsMu.Unlock();http.Error(w,"GET required",http.StatusMethodNotAllowed);return}
  if j.state!="completed"{
   status:=j.state
   m.jobsMu.Unlock()
   if status=="failed"||status=="timed_out"||status=="cancelled"{http.Error(w,"job "+status,http.StatusGone)
   }else{http.Error(w,"job still processing",http.StatusAccepted)}
   return
  }
  data:=append([]byte(nil),j.result...)
  m.jobsMu.Unlock()
  w.Header().Set("Content-Type","application/json")
  _,_=w.Write(data);return
 }
 if r.Method==http.MethodDelete{
  if !j.terminal(){j.cancel();j.state="cancelled";j.stageAt=time.Now();j.finished=j.stageAt;j.request=nil;j.errorCode="cancelled";j.errorMessage="cancelled by client"}
  m.jobsMu.Unlock();writeJSON(w,map[string]any{"id":sub,"status":"cancelled"});return
 }
 if r.Method!=http.MethodGet{m.jobsMu.Unlock();http.Error(w,"GET or DELETE required",http.StatusMethodNotAllowed);return}
 now:=time.Now()
 state:=j.state;elapsed:=int(now.Sub(j.created).Seconds());stageElapsed:=int(now.Sub(j.stageAt).Seconds())
 if j.terminal(){elapsed=int(j.finished.Sub(j.created).Seconds());stageElapsed=0}
 errorCode,errorMessage:=j.errorCode,j.errorMessage
 model,contextSize:=j.model,j.contextSize
 m.jobsMu.Unlock()
 total,avail,_:=systemStats()
 m.mu.Lock();alive:=m.backendRunningLocked();m.mu.Unlock()
 writeJSON(w,map[string]any{
  "request_id":sub,"status":state,
  "elapsed_seconds":elapsed,"stage_elapsed_seconds":stageElapsed,
  "model":model,"context_size":contextSize,
  "backend_process_alive":alive,
  "memory_total_bytes":total,"memory_available_bytes":avail,
  "sampled_at_unix_ms":now.UnixMilli(),
  "error_code":errorCode,"error":errorMessage,
 })
}

func (m *manager) createLLMJob(w http.ResponseWriter,r *http.Request){
 cfg,err:=loadConfig(m.configPath)
 if err!=nil{http.Error(w,"local config unavailable",http.StatusServiceUnavailable);return}
 cfg=defaults(cfg)
 if normalizedLLMProvider(cfg)!="local"{http.Error(w,"local-only jobs; OpenRouter is not allowed",http.StatusConflict);return}
 body,err:=io.ReadAll(http.MaxBytesReader(w,r.Body,maxJobRequestBytes))
 if err!=nil{http.Error(w,"request too large",http.StatusRequestEntityTooLarge);return}
 payload,err:=parseJobRequest(body)
 if err!=nil{http.Error(w,err.Error(),http.StatusBadRequest);return}
 id,err:=generateJobID()
 if err!=nil{http.Error(w,"could not allocate job ID",http.StatusInternalServerError);return}
 ctx,cancel:=context.WithCancel(context.Background())
 now:=time.Now()
 j:=&llmJob{id:id,state:"queued",created:now,stageAt:now,request:payload,cancel:cancel,ctx:ctx,
  model:cfg["MODEL_PATH"],contextSize:intVal(cfg,"CONTEXT",4096),inputBytes:len(payload)}
 m.jobsMu.Lock()
 m.cleanFinishedJobsLocked()
 active:=0
 for _,entry:=range m.jobs{if !entry.terminal(){active++}}
 if active>=maxLLMJobs{m.jobsMu.Unlock();cancel();w.Header().Set("Retry-After","10");http.Error(w,"local job queue is full",http.StatusTooManyRequests);return}
 m.ensureJobQueueLocked()
 m.jobs[id]=j
 m.jobQueue<-j
 m.jobsMu.Unlock()
 w.Header().Set("Location","/api/llm/jobs/"+id)
 w.Header().Set("Content-Type","application/json; charset=utf-8")
 w.WriteHeader(http.StatusAccepted)
 writeJSON(w,map[string]any{"request_id":id,"status":"queued","status_url":"/api/llm/jobs/"+id,
  "result_url":"/api/llm/jobs/"+id+"/result"})
}

func llmLoadTimeout(c config) int {
 n:=intVal(c,"LLM_LOAD_TIMEOUT_SECONDS",600)
 if n<30||n>3600{return 600}
 return n
}
func llmGenerateTimeout(c config) int {
 n:=intVal(c,"LLM_GENERATE_TIMEOUT_SECONDS",1800)
 if n<30||n>3600{return 1800}
 return n
}
