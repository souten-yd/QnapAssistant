package main

import (
 "context"
 "errors"
 "io"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "time"
 "encoding/json"
)

func TestStreamingDiagnosticCapturesCompleteAndPartial(t *testing.T){
 m:=testJobManager(t,"")
 t0:=time.Now().Add(-4*time.Second)
 job:=&llmJob{id:"req_test",state:"generating",created:t0,stageAt:t0,generationStarted:t0,model:"test.gguf"}
 m.jobs=map[string]*llmJob{job.id:job}
 payload:=strings.Join([]string{
  `data: {"id":"x","model":"test.gguf","choices":[{"delta":{"content":"# 要点\n"},"finish_reason":null}]}`,
  `data: {"choices":[{"delta":{"content":"休養を取る"},"finish_reason":"stop"}]}`,
  "data: [DONE]", "",
 },"\n")
 resp:=&http.Response{Header:http.Header{"Content-Type":[]string{"text/event-stream"}},Body:io.NopCloser(strings.NewReader(payload))}
 data,err:=m.readDiagnosticStream(job,resp)
 if err!=nil{t.Fatal(err)}
 var complete struct{Choices []struct{Message struct{Content string `json:"content"`} `json:"message"`} `json:"choices"`}
 if err=json.Unmarshal(data,&complete);err!=nil{t.Fatal(err)}
 if len(complete.Choices)!=1||complete.Choices[0].Message.Content!="# 要点\n休養を取る"{t.Fatalf("bad assembly: %s",data)}
 w:=httptest.NewRecorder()
 m.handleLLMJobs(w,httptest.NewRequest("GET","/api/llm/jobs/"+job.id+"/diagnostics",nil))
 if w.Code!=200{t.Fatalf("diagnostic %d: %s",w.Code,w.Body.String())}
 if strings.Contains(w.Body.String(),"休養を取る"){t.Fatal("raw health output leaked to default diagnostics")}
 w=httptest.NewRecorder()
 m.handleLLMJobs(w,httptest.NewRequest("GET","/api/llm/jobs/"+job.id+"/diagnostics?include_output=1",nil))
 if w.Code!=200||!strings.Contains(w.Body.String(),"休養を取る"){t.Fatalf("explicit raw missing: %d %s",w.Code,w.Body.String())}
 m.markGenerationTimeout(job)
 if job.errorCode!="generation_timeout_partial"{t.Fatalf("expected partial timeout, got %s",job.errorCode)}
 j2:=&llmJob{id:"second",state:"generating",created:t0,stageAt:t0}
 m.markGenerationTimeout(j2)
 if j2.errorCode!="generation_first_token_timeout"{t.Fatalf("expected first token timeout, got %s",j2.errorCode)}
}

func TestDiagnosticStreamInterruptedRetainsText(t *testing.T){
 m:=testJobManager(t,"")
 j:=&llmJob{id:"req_partial",state:"generating",created:time.Now(),stageAt:time.Now()}
 payload:="data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
 resp:=&http.Response{Header:http.Header{"Content-Type":[]string{"text/event-stream"}},Body:io.NopCloser(strings.NewReader(payload))}
 _,err:=m.readDiagnosticStream(j,resp)
 if !errors.Is(err,io.EOF){t.Fatalf("expected interrupted SSE EOF: %v",err)}
 if j.partialOutput!="partial"{t.Fatalf("lost partial output: %q",j.partialOutput)}
}
