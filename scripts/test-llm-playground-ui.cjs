// Exercise the real inline playground/memory monitor code with a minimal DOM
// and mocked network. No model access, real health data or disk writes required.
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const assert=require('node:assert/strict');
const src=fs.readFileSync(path.join(__dirname,'../admin/ui.go'),'utf8');
const script=src.match(/<script>([\s\S]*?)<\/script>/)[1];
new vm.Script(script);
const ids=[
  'aiTestInput','aiTestSystem','aiTestOutput','aiTestState','aiTestMeta',
  'aiTestRun','aiTestCancel','aiTestTarget','aiTestMaxTokens',
  'aiMemNow','aiMemLow','aiMemDelta','aiMemTotal','aiMemAxis',
  'aiMemChart','aiMemLine','aiMemDot','aiMemStart','aiMemStop','aiMemState'
];
for(const id of ids)assert.match(src,new RegExp('id="'+id+'"'));
assert.match(src,/\/api\/llm\/local-test/);
assert.match(src,/\/api\/system\/memory/);
const nodes=Object.fromEntries(ids.map(id=>[id,{
  value:'',textContent:'',disabled:false,attrs:{},
  setAttribute(k,v){this.attrs[k]=v;},
}]));
nodes.aiTestMaxTokens.value='512';
let provider='local',sent=[],responder,memQueue=[],memCounter=0,intervals=new Map(),nextInterval=0;
const gib=1073741824;
const ctx={
  el:id=>nodes[id],
  AbortController,Date,
  setInterval:(fn,delay)=>{assert.equal(delay,1000);intervals.set(++nextInterval,fn);return nextInterval;},
  clearInterval:id=>intervals.delete(id),
  j:async url=>{
    if(url==='/api/status')return {llm_provider:provider,model_path:'/models/Qwen3-8B-Q4_K_M.gguf',memory_available_bytes:8*1048576};
    if(url==='/api/config')return {CONTEXT:'16384'};
    if(url==='/api/system/memory')return {
      total_bytes:16*gib,
      available_bytes:(memQueue.length?memQueue.shift():10)*gib,
      sampled_at_unix_ms:Date.now()+(++memCounter)
    };
    throw new Error('unexpected request '+url);
  },
  fetch:(url,options)=>{sent.push({url,options});return responder(url,options);}
};
vm.createContext(ctx);
// The memory monitor helpers now span multiple source lines. Evaluate the
// complete isolated feature section rather than line-filtering function headers.
const start=script.indexOf('let aiMemTimer=');
const end=script.indexOf('async function saveVoiceProfiles()',start);
assert.ok(start>=0 && end>start, 'playground code section was not found');
const selected=script.slice(start,end);
vm.runInContext(selected,ctx);
(async()=>{
  // No memory polling or inference for empty prompt.
  await ctx.runAiTest();
  assert.match(nodes.aiTestState.textContent,/入力欄/);
  assert.equal(sent.length,0);
  assert.equal(intervals.size,0);
  ctx.fillAiTestExample();
  assert.match(nodes.aiTestInput.value,/synthetic_only/);

  // Remote provider is still rejected by the playground.
  provider='openrouter';
  await ctx.runAiTest();
  assert.equal(sent.length,0);
  assert.match(nodes.aiTestState.textContent,/OpenRouter/);
  assert.equal(intervals.size,0);

  // Successful local inference auto-starts and stops memory sampling.
  provider='local';
  memQueue=[12,7,9];
  responder=async()=>({ok:true,status:200,text:async()=>JSON.stringify({
    choices:[{message:{content:'<script>not HTML</script>'},finish_reason:'stop'}],
    usage:{prompt_tokens:321,completion_tokens:42}
  })});
  await ctx.runAiTest();
  assert.equal(sent.length,1);
  assert.equal(sent[0].url,'/api/llm/local-test');
  const body=JSON.parse(sent[0].options.body);
  assert.equal(body.max_tokens,512);
  assert.equal(body.prompt,nodes.aiTestInput.value);
  assert.equal(nodes.aiTestOutput.textContent,'<script>not HTML</script>');
  assert.match(nodes.aiTestMeta.textContent,/321 tokens/);
  assert.match(nodes.aiTestTarget.textContent,/CONTEXT 16384/);
  assert.equal(intervals.size,0);
  assert.equal(nodes.aiMemStop.disabled,true);

  // Manual watch: start before model loading, track min even after recovery.
  memQueue=[11,6,9];
  ctx.startAiMemWatch();await ctx.pollAiMemWatch();
  await ctx.pollAiMemWatch();await ctx.pollAiMemWatch();
  assert.match(nodes.aiMemNow.textContent,/9.00 GiB/);
  assert.match(nodes.aiMemLow.textContent,/6.00 GiB/);
  assert.match(nodes.aiMemDelta.textContent,/-2.00 GiB/);
  assert.match(nodes.aiMemTotal.textContent,/16.00 GiB/);
  assert.match(nodes.aiMemLine.attrs.points,/,/);
  assert.equal(intervals.size,1);
  ctx.stopAiMemWatch();
  assert.equal(intervals.size,0);
  assert.match(nodes.aiMemState.textContent,/計測終了/);

  // Bounded five-minute window; lifetime minimum must not be lost.
  ctx.startAiMemWatch();
  await ctx.pollAiMemWatch();
  let sid=vm.runInContext('aiMemSession',ctx),start=Date.now();
  for(let i=0;i<320;i++){
    ctx.aiMemRecord({total_bytes:16*gib,available_bytes:(i===1?2:8)*gib,sampled_at_unix_ms:start+i*1000},sid);
  }
  assert.ok(vm.runInContext('aiMemSamples.length',ctx)<=300);
  assert.match(nodes.aiMemLow.textContent,/2.00 GiB/);
  ctx.stopAiMemWatch();

  // Backend error and cancellation remain available.
  responder=async()=>({ok:false,status:503,text:async()=> 'model unavailable'});
  await ctx.runAiTest();
  assert.equal(nodes.aiTestOutput.textContent,'');
  assert.match(nodes.aiTestState.textContent,/HTTP 503/);
  assert.equal(intervals.size,0);

  let arrived;
  const arrival=new Promise(resolve=>{arrived=resolve;});
  responder=(_url,opts)=>new Promise((_resolve,reject)=>{
    opts.signal.addEventListener('abort',()=>{
      const e=new Error('cancelled');e.name='AbortError';reject(e);
    });
    arrived();
  });
  const inflight=ctx.runAiTest();
  await arrival;
  assert.equal(nodes.aiTestRun.disabled,true);
  assert.equal(intervals.size,1);
  ctx.cancelAiTest();
  await inflight;
  assert.match(nodes.aiTestState.textContent,/中止/);
  assert.equal(nodes.aiTestRun.disabled,false);
  assert.equal(nodes.aiTestCancel.disabled,true);
  assert.equal(intervals.size,0);
  console.log('PASS: local playground, safe memory trace, bounds, low-watermark, abort');
})().catch(e=>{console.error(e);process.exitCode=1;});
