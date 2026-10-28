package main

import (
 "net"
 "net/http"
 "strconv"
 "time"
)

// GET /api/llm/state exposes the shared model lifecycle without leaking
// requests, prompts, completions or opaque job capability IDs.
func (m *manager) handleLLMState(w http.ResponseWriter,r *http.Request){
 if r.Method!=http.MethodGet {
  w.Header().Set("Allow","GET")
  http.Error(w,"GET required",http.StatusMethodNotAllowed)
  return
 }
 cfg,err:=loadConfig(m.configPath)
 if err!=nil{http.Error(w,"local config unavailable",http.StatusServiceUnavailable);return}
 cfg=defaults(cfg)
 provider:=normalizedLLMProvider(cfg)
 m.mu.Lock()
 running:=m.backendRunningLocked()
 activeRequests:=m.activeRequests
 lastUsed:=m.lastUsed
 m.mu.Unlock()
 m.jobsMu.Lock()
 queued,loading,generating:=0,0,0
 for _,j:=range m.jobs{
  switch j.state{
   case "queued":queued++
   case "loading_model","ready":loading++
   case "generating":generating++
  }
 }
 m.jobsMu.Unlock()
 state:="unloaded"
 healthy:=false
 if provider!="local"{state="remote"
 }else if running{
  state="unavailable"
  port,err:=strconv.Atoi(cfg["BACKEND_PORT"])
  if err==nil&&port>0&&port<=65535{
   url:="http://"+net.JoinHostPort("127.0.0.1",cfg["BACKEND_PORT"])+"/health"
   client:=&http.Client{Timeout:800*time.Millisecond,CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}}
   resp,err:=client.Get(url)
   if err==nil{
    resp.Body.Close()
    if resp.StatusCode==http.StatusOK{healthy=true;state="ready"
    }else if resp.StatusCode==http.StatusServiceUnavailable{state="loading_model"}
   }
  }
 }
 if provider=="local"{
  if generating>0{state="generating"
  }else if loading>0{state="loading_model"
  }else if queued>0&&state=="unloaded"{state="queued"}
 }
 total,available,_:=systemStats()
 idleFor:=0
 if activeRequests==0&&!lastUsed.IsZero(){idleFor=int(time.Since(lastUsed).Seconds())}
 w.Header().Set("Cache-Control","no-store")
 writeJSON(w,map[string]any{
  "provider":provider,"status":state,"model":cfg["MODEL_PATH"],
  "context_size":intVal(cfg,"CONTEXT",4096),
  "backend_process_alive":running,"backend_ready":healthy,
  "active_requests":activeRequests,"queued_jobs":queued,
  "loading_jobs":loading,"generating_jobs":generating,
  "idle_seconds":idleFor,
  "auto_unload":llmAutoUnload(cfg),"idle_timeout_seconds":llmIdleTimeout(cfg),
  "load_timeout_seconds":llmLoadTimeout(cfg),"generation_timeout_seconds":llmGenerateTimeout(cfg),
  "memory_total_bytes":total,"memory_available_bytes":available,
  "sampled_at_unix_ms":time.Now().UnixMilli(),
 })
}
