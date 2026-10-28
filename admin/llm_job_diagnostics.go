package main

import (
 "bufio"
 "bytes"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net/http"
 "strings"
 "time"
)

const diagnosticOutputLimit = 256 << 10

// readDiagnosticStream consumes llama.cpp's OpenAI-compatible SSE response,
// retaining the text already generated even when a context deadline interrupts
// reading. No prompt or output is written to disk by this feature.
func (m *manager) readDiagnosticStream(j *llmJob, resp *http.Response) ([]byte, error) {
 if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
  // Backwards-compatible fallback for alternate OpenAI-compatible backends.
  data,err:=io.ReadAll(io.LimitReader(resp.Body,maxJobResultBytes+1))
  if err!=nil{return nil,err}
  if len(data)>maxJobResultBytes{return nil,errors.New("response too large")}
  var completion struct{
   Choices []struct{Message struct{Content string `json:"content"`} `json:"message"`;FinishReason string `json:"finish_reason"`} `json:"choices"`
   Usage struct{Prompt int `json:"prompt_tokens"`;Completion int `json:"completion_tokens"`} `json:"usage"`
  }
  if json.Unmarshal(data,&completion)==nil && len(completion.Choices)>0 {
   m.jobsMu.Lock()
   j.partialOutput=completion.Choices[0].Message.Content
   j.finishReason=completion.Choices[0].FinishReason
   j.promptTokens=completion.Usage.Prompt
   j.completionTokens=completion.Usage.Completion
   if j.partialOutput!=""{j.firstTokenAt=time.Now();j.lastTokenAt=j.firstTokenAt}
   m.jobsMu.Unlock()
  }
  return data,nil
 }
 reader:=bufio.NewReaderSize(resp.Body,4096)
 var output strings.Builder
 var finishReason string
 var model string
 var resultID string
 var created int64
 var tokenChunks int
 var promptTokens int
 var completionTokens int
 complete:=false
 for {
  line,err:=reader.ReadString('\n')
  if err!=nil&&len(line)==0{return nil,err}
  line=strings.TrimSpace(line)
  if strings.HasPrefix(line,"data:"){
   payload:=strings.TrimSpace(strings.TrimPrefix(line,"data:"))
   if payload=="[DONE]"{complete=true;break}
   if payload!=""{
    var event struct{
     ID string `json:"id"`
     Model string `json:"model"`
     Created int64 `json:"created"`
     Choices []struct{
      Delta struct{Content string `json:"content"`} `json:"delta"`
      FinishReason *string `json:"finish_reason"`
     } `json:"choices"`
     Usage *struct{Prompt int `json:"prompt_tokens"`;Completion int `json:"completion_tokens"`} `json:"usage"`
    }
    if e:=json.Unmarshal([]byte(payload),&event);e!=nil{return nil,fmt.Errorf("invalid SSE JSON: %w",e)}
    if event.ID!=""{resultID=event.ID}
    if event.Model!=""{model=event.Model}
    if event.Created>0{created=event.Created}
    if event.Usage!=nil{promptTokens=event.Usage.Prompt;completionTokens=event.Usage.Completion}
    for _,choice:=range event.Choices{
     if choice.FinishReason!=nil{finishReason=*choice.FinishReason}
     delta:=choice.Delta.Content
     if delta==""{continue}
     tokenChunks++
     now:=time.Now()
     if output.Len()+len(delta)>diagnosticOutputLimit{
      m.jobsMu.Lock();j.partialTruncated=true;m.jobsMu.Unlock()
      return nil,errors.New("generated output exceeded diagnostic limit")
     }
     output.WriteString(delta)
     m.jobsMu.Lock()
     if j.firstTokenAt.IsZero(){j.firstTokenAt=now}
     j.lastTokenAt=now
     j.partialOutput=output.String()
     j.tokenChunks=tokenChunks
     j.finishReason=finishReason
     j.promptTokens=promptTokens
     j.completionTokens=completionTokens
     m.jobsMu.Unlock()
    }
   }
  }
  if err!=nil{return nil,err}
 }
 if !complete{return nil,errors.New("stream ended without [DONE]")}
 if resultID==""{resultID=j.id}
 if created==0{created=time.Now().Unix()}
 if model==""{model=j.model}
 if finishReason==""{finishReason="stop"}
 m.jobsMu.Lock()
 j.finishReason=finishReason
 j.promptTokens=promptTokens
 j.completionTokens=completionTokens
 m.jobsMu.Unlock()
 // Return an OpenAI-compatible non-streaming response to existing consumers.
 result:=map[string]any{
  "id":resultID,"object":"chat.completion","created":created,"model":model,
  "choices":[]any{map[string]any{"index":0,"message":map[string]any{"role":"assistant","content":output.String()},"finish_reason":finishReason}},
  "usage":map[string]any{"prompt_tokens":promptTokens,"completion_tokens":completionTokens,"total_tokens":promptTokens+completionTokens},
 }
 data,err:=json.Marshal(result)
 if err!=nil{return nil,err}
 if len(data)>maxJobResultBytes{return nil,errors.New("completion response too large")}
 return bytes.Clone(data),nil
}

// The job ID is a bearer capability. Raw generated content is never included
// in ordinary status polling; it requires this explicit diagnostic request.
func (m *manager) handleJobDiagnostics(w http.ResponseWriter,r *http.Request,jobID string){
 if r.Method!=http.MethodGet{http.Error(w,"GET required",http.StatusMethodNotAllowed);return}
 m.jobsMu.Lock()
 m.cleanFinishedJobsLocked()
 j:=m.jobs[jobID]
 if j==nil{m.jobsMu.Unlock();http.NotFound(w,r);return}
 end:=time.Now()
 if j.terminal(){end=j.finished}
 generationSeconds:=0
 firstTokenMs:=int64(-1)
 sinceLastTokenSeconds:=int64(-1)
 if !j.generationStarted.IsZero(){
  generationSeconds=int(end.Sub(j.generationStarted).Seconds())
  if !j.firstTokenAt.IsZero(){firstTokenMs=j.firstTokenAt.Sub(j.generationStarted).Milliseconds()}
 }
 if !j.lastTokenAt.IsZero(){sinceLastTokenSeconds=int64(end.Sub(j.lastTokenAt).Seconds())}
 output:=j.partialOutput
 info:=map[string]any{
  "diagnostic_version":1,"request_id":jobID,"status":j.state,
  "error_code":j.errorCode,"error":j.errorMessage,
  "elapsed_seconds":int(end.Sub(j.created).Seconds()),
  "generation_seconds":generationSeconds,
  "first_token_ms":firstTokenMs,"since_last_token_seconds":sinceLastTokenSeconds,
  "token_chunks_observed":j.tokenChunks,
  "prompt_tokens":j.promptTokens,"completion_tokens_reported":j.completionTokens,
  "output_bytes":len(output),"partial_truncated":j.partialTruncated,
  "finish_reason":j.finishReason,"model":j.model,"context_size":j.contextSize,
  "raw_output_available":output!="",
  "raw_output_persisted":false,
  "min_memory_available_bytes":j.minMemoryAvailableBytes,
  "input_bytes":j.inputBytes,
  "generated_at_unix_ms":end.UnixMilli(),
 }
 if r.URL.Query().Get("include_output")=="1"{info["raw_output"]=output}
 m.jobsMu.Unlock()
 w.Header().Set("Cache-Control","no-store")
 w.Header().Set("X-Content-Type-Options","nosniff")
 writeJSON(w,info)
}

func (m *manager) markGenerationTimeout(j *llmJob) {
 m.jobsMu.Lock()
 hasOutput:=j.partialOutput!=""
 m.jobsMu.Unlock()
 if hasOutput{m.markJob(j,"timed_out","generation_timeout_partial","generation timed out with partial output",nil)
 }else{m.markJob(j,"timed_out","generation_first_token_timeout","generation timed out before first content token",nil)}
}
