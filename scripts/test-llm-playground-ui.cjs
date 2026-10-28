// Exercise the real inline playground code with a minimal DOM and mocked
// network. No model access or health data is required.
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const assert=require('node:assert/strict');
const src=fs.readFileSync(path.join(__dirname,'../admin/ui.go'),'utf8');
const script=src.match(/<script>([\s\S]*?)<\/script>/)[1];
new vm.Script(script);
for(const id of ['aiTestInput','aiTestSystem','aiTestOutput','aiTestState','aiTestMeta','aiTestRun','aiTestCancel','aiTestTarget','aiTestMaxTokens']){
  assert.match(src,new RegExp('id="'+id+'"'));
}
assert.match(src,/\/api\/llm\/local-test/);
const nodes=Object.fromEntries(['aiTestInput','aiTestSystem','aiTestOutput','aiTestState','aiTestMeta','aiTestRun','aiTestCancel','aiTestTarget','aiTestMaxTokens'].map(id=>[id,{value:'',textContent:'',disabled:false}]));
nodes.aiTestMaxTokens.value='512';
let provider='local',sent=[],responder;
const ctx={
  el:id=>nodes[id],
  AbortController,
  j:async url=>{
    if(url==='/api/status')return {llm_provider:provider,model_path:'/models/Qwen3-8B-Q4_K_M.gguf',memory_available_bytes:8*1048576};
    if(url==='/api/config')return {CONTEXT:'16384'};
    throw new Error('unexpected request '+url);
  },
  fetch:(url,options)=>{sent.push({url,options});return responder(url,options);}
};
vm.createContext(ctx);
const fnNames=['fillAiTestExample','cancelAiTest','runAiTest'];
const selected=script.split('\n').filter(line=>line.startsWith('let aiTestBusy=')||fnNames.some(name=>line.startsWith('function '+name+'(')||line.startsWith('async function '+name+'('))).join('\n');
vm.runInContext(selected,ctx);
(async()=>{
  // Never submit empty or malformed inputs.
  await ctx.runAiTest();
  assert.match(nodes.aiTestState.textContent,/入力欄/);
  assert.equal(sent.length,0);
  ctx.fillAiTestExample();
  assert.match(nodes.aiTestInput.value,/synthetic_only/);
  assert.match(nodes.aiTestInput.value,/Day7/);

  // Remote provider guard: fail before posting pasted health notes.
  provider='openrouter';
  await ctx.runAiTest();
  assert.equal(sent.length,0);
  assert.match(nodes.aiTestState.textContent,/OpenRouter/);

  // Local-only successful generation, output rendered as plain text.
  provider='local';
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
  assert.equal(nodes.aiTestCancel.disabled,true);

  // Errors are visible, without displaying a fabricated answer.
  responder=async()=>({ok:false,status:503,text:async()=> 'model unavailable'});
  await ctx.runAiTest();
  assert.equal(nodes.aiTestOutput.textContent,'');
  assert.match(nodes.aiTestState.textContent,/HTTP 503/);

  // Cancellation aborts an in-flight fetch and restores buttons.
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
  ctx.cancelAiTest();
  await inflight;
  assert.match(nodes.aiTestState.textContent,/中止/);
  assert.equal(nodes.aiTestRun.disabled,false);
  assert.equal(nodes.aiTestCancel.disabled,true);
  console.log('PASS: local playground input/output, remote guard, errors, usage, abort');
})().catch(e=>{console.error(e);process.exitCode=1;});
