package main

import "strings"

// The shell is applied last: it restyles the page, adds a header with tabs, a
// monitoring dashboard and a debug page, and moves the existing cards into the
// tabs at load time. Existing element IDs, injection anchors and scripts stay
// untouched, so every older feature keeps working.

const shellHeadAnchor = `</head>`
const shellTitleAnchor = `<h1>QnapAssistant</h1>`

// shellCardMap: original card heading → tab, new heading. Order inside a tab follows this list.
var shellCardMap = []struct{ Heading, Tab, Title string }{
	{"Runtime", "home", "音声サービス"},
	{"LLM provider", "model", "AIの接続先"},
	{"Local GGUF models", "model", "モデルの選択とダウンロード"},
	{"Model residency / auto-unload", "model", "自動停止とタイムアウト"},
	{"Local llama.cpp settings", "model", "llama.cpp の詳細設定"},
	{"OpenRouter", "openrouter", "OpenRouter（外部AI）"},
	{"Voice client profiles", "voice", "音声クライアント"},
	{"Voice response", "voice", "音声の応答"},
	{"ローカルAI 入出力テスト", "debug", "ローカルAI 入出力テスト"},
	{"Logs", "debug", "ログ"},
	{"QnapAssistant Update", "update", "アップデート"},
}

const shellCSS = `<style id="qa-shell-style">
:root{--bg:#0e1116;--panel:#161b22;--panel2:#1c222b;--line:#2a313c;--text:#e6edf3;--muted:#8b97a6;--accent:#4aa3ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--info:#58a6ff}
body{background:var(--bg);color:var(--text);font-family:system-ui,-apple-system,"Hiragino Sans","Noto Sans JP",sans-serif;font-size:14px;line-height:1.55}
.wrap{max-width:1180px;padding:0 16px 40px}
.qa-header{position:sticky;top:0;z-index:20;background:rgba(14,17,22,.92);backdrop-filter:blur(8px);border-bottom:1px solid var(--line);margin:0 -16px 16px;padding:10px 16px 0}
.qa-brand{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.qa-brand h1{font-size:18px;margin:0}.qa-ver{color:var(--muted);font-size:12px}
.qa-dot{width:10px;height:10px;border-radius:50%;background:var(--muted);display:inline-block}.qa-dot.ok{background:var(--ok)}.qa-dot.busy{background:var(--warn);animation:qa-pulse 1.2s infinite}.qa-dot.bad{background:var(--bad)}
@keyframes qa-pulse{50%{opacity:.35}}
.qa-headstate{color:var(--muted);font-size:13px;margin-left:auto}
.qa-tabs{display:flex;gap:2px;overflow-x:auto;margin-top:8px;scrollbar-width:none}.qa-tabs::-webkit-scrollbar{display:none}
.qa-tabs button{background:none;border:0;border-bottom:2px solid transparent;border-radius:0;color:var(--muted);padding:9px 12px;white-space:nowrap;font-size:14px;cursor:pointer}
.qa-tabs button[aria-selected=true]{color:var(--text);border-bottom-color:var(--accent);font-weight:600}
.qa-panel[hidden]{display:none}
.card{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:16px;margin:0 0 14px}
.card h3{margin:0 0 10px;font-size:15px}
button{cursor:pointer;margin:2px 4px 2px 0}button:hover{border-color:var(--accent)}
button.qa-primary{background:var(--accent);border-color:var(--accent);color:#04111f;font-weight:600}
button.qa-danger{border-color:#5a2a2a;color:#ffb4ae}
.qa-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:12px}
.qa-tile{background:var(--panel2);border:1px solid var(--line);border-radius:10px;padding:12px}
.qa-tile .k{color:var(--muted);font-size:12px}.qa-tile .v{font-size:22px;font-weight:700;margin-top:2px}.qa-tile .s{color:var(--muted);font-size:12px}
.qa-bar{height:6px;background:#2a313c;border-radius:4px;overflow:hidden;margin-top:6px}.qa-bar>i{display:block;height:100%;background:var(--accent);width:0;transition:width .4s}
.qa-bar.warn>i{background:var(--warn)}.qa-bar.bad>i{background:var(--bad)}
.qa-hero{display:flex;gap:16px;align-items:center;flex-wrap:wrap}
.qa-pill{display:inline-flex;align-items:center;gap:8px;font-size:20px;font-weight:700}
.qa-sub{color:var(--muted)}
.qa-actions{margin-left:auto;display:flex;flex-wrap:wrap}
.qa-table{width:100%;border-collapse:collapse;font-size:13px}.qa-table th,.qa-table td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line);white-space:nowrap}
.qa-table th{color:var(--muted);font-weight:500}.qa-scroll{overflow-x:auto}
.qa-ok{color:var(--ok)}.qa-warn{color:var(--warn)}.qa-bad{color:var(--bad)}.qa-info{color:var(--info)}
.qa-checks{list-style:none;padding:0;margin:0}.qa-checks li{display:grid;grid-template-columns:22px 1fr;gap:4px 8px;padding:8px 0;border-bottom:1px solid var(--line)}
.qa-checks .hint{grid-column:2;color:var(--muted);font-size:12px}
.qa-spark{width:100%;height:44px;display:block;margin-top:6px}
.qa-muted{color:var(--muted)}.qa-row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
.qa-row label{display:flex;gap:6px;align-items:center;color:var(--muted)}.qa-row input[type=checkbox]{width:auto}
.qa-row select,.qa-row input[type=search]{width:auto;min-width:120px}
pre#logs{max-height:60vh;overflow:auto;font-size:12px;white-space:pre-wrap;word-break:break-all;background:#0b0e13;border:1px solid var(--line);border-radius:8px;padding:10px}
details.qa-adv>summary{cursor:pointer;color:var(--muted);margin:4px 0 10px}
@media (max-width:640px){.qa-tile .v{font-size:18px}.qa-pill{font-size:17px}.qa-actions{margin-left:0}.qa-headstate{width:100%;margin-left:0}}
</style>`

const shellHTML = `<header class="qa-header"><div class="qa-brand"><span id="qaDot" class="qa-dot"></span><h1>QnapAssistant</h1><span id="qaVer" class="qa-ver"></span><span id="qaHeadState" class="qa-headstate">状態を確認中…</span></div>
<nav class="qa-tabs" role="tablist" aria-label="メニュー">
<button role="tab" data-tab="home">ダッシュボード</button><button role="tab" data-tab="model">AIモデル</button><button role="tab" data-tab="openrouter">OpenRouter</button><button role="tab" data-tab="voice">音声</button><button role="tab" data-tab="debug">診断</button><button role="tab" data-tab="update">更新</button>
</nav></header>
<section class="qa-panel" data-panel="home">
<div class="card"><div class="qa-hero"><div><div class="qa-pill"><span id="qaStateDot" class="qa-dot"></span><span id="qaState">確認中…</span></div><div id="qaStateSub" class="qa-sub"></div></div>
<div class="qa-actions"><button class="qa-primary" onclick="qaAct('start')">起動</button><button onclick="qaAct('restart')">再起動</button><button class="qa-danger" onclick="qaAct('stop')">停止</button></div></div></div>
<div class="card"><h3>現在の処理</h3><div id="qaJob" class="qa-muted">処理中のジョブはありません。</div></div>
<div class="qa-grid" style="margin-bottom:14px">
<div class="qa-tile"><div class="k">CPU使用率</div><div class="v" id="qaCpu">–</div><div class="s" id="qaLoad"></div><svg class="qa-spark" id="qaCpuSpark" viewBox="0 0 120 40" preserveAspectRatio="none"></svg></div>
<div class="qa-tile"><div class="k">メモリ使用</div><div class="v" id="qaMem">–</div><div class="s" id="qaMemSub"></div><div class="qa-bar" id="qaMemBar"><i></i></div><svg class="qa-spark" id="qaMemSpark" viewBox="0 0 120 40" preserveAspectRatio="none"></svg></div>
<div class="qa-tile"><div class="k">入力の読み込み速度</div><div class="v" id="qaPp">–</div><div class="s">トークン/秒（直近）</div></div>
<div class="qa-tile"><div class="k">回答の生成速度</div><div class="v" id="qaTg">–</div><div class="s">トークン/秒（直近）</div></div>
<div class="qa-tile"><div class="k">待ち行列</div><div class="v" id="qaQueue">–</div><div class="s" id="qaQueueSub"></div></div>
</div>
<div class="card"><h3>最近の処理（SelfCareなどからの依頼）</h3><div class="qa-scroll"><table class="qa-table" id="qaRecent"><thead><tr><th>終了</th><th>結果</th><th>合計</th><th>読込待ち</th><th>最初の出力まで</th><th>生成速度</th><th>トークン(入/出)</th></tr></thead><tbody></tbody></table></div><div id="qaRecentEmpty" class="qa-muted">まだ記録がありません（QnapAssistantの再起動で消えます）。</div></div>
</section>
<section class="qa-panel" data-panel="model" hidden></section>
<section class="qa-panel" data-panel="openrouter" hidden></section>
<section class="qa-panel" data-panel="voice" hidden></section>
<section class="qa-panel" data-panel="debug" hidden>
<div class="card"><h3>セルフチェック</h3><p class="qa-muted" style="margin-top:0">設定・CPU・モデル・メモリ・空き容量・応答・最近の失敗をまとめて確認し、対処法を表示します。</p><button class="qa-primary" onclick="qaSelfCheck()">チェックを実行</button><span id="qaCheckAt" class="qa-muted"></span><ul class="qa-checks" id="qaChecks"></ul></div>
<div class="card"><h3>診断レポート</h3><p class="qa-muted" style="margin-top:0">CPU・メモリ・設定（APIキー等は伏せ字）・モデル一覧・処理履歴・ログ末尾をJSONにまとめます。不具合の相談時に添付してください。</p><button onclick="location.href='/api/debug/report?download=1'">ダウンロード</button><button onclick="qaCopyReport()">クリップボードにコピー</button><span id="qaReportState" class="qa-muted"></span></div>
<div class="card"><h3>処理履歴（詳細）</h3><div class="qa-scroll"><table class="qa-table" id="qaHistory"><thead><tr><th>終了</th><th>結果</th><th>モデル</th><th>合計</th><th>キュー</th><th>ロード</th><th>最初の出力まで</th><th>生成</th><th>速度</th><th>入力</th></tr></thead><tbody></tbody></table></div></div>
</section>
<section class="qa-panel" data-panel="update" hidden></section>`

const shellScript = `<script>
(function(){
const CARD_MAP=` + "__CARD_MAP__" + `;
const TABS=['home','model','openrouter','voice','debug','update'];
const ERR={generation_first_token_timeout:'入力の読み込み中に時間切れ',generation_timeout_partial:'生成中に時間切れ（途中まで出力）',load_timeout:'モデルの読み込みが時間切れ',model_start_failed:'モデルを起動できません',backend_unreachable:'llama.cppに接続できません',cancelled:'取り消し',provider_changed:'接続先が変更されました',config_unavailable:'設定を読めません',invalid_request:'依頼の形式が不正',invalid_backend_response:'応答の形式が不正',response_read_error:'応答の受信に失敗'};
const STATUS={completed:['完了','qa-ok'],failed:['失敗','qa-bad'],timed_out:['時間切れ','qa-bad'],cancelled:['取消','qa-warn']};
const $=id=>document.getElementById(id);
const esc=s=>String(s==null?'':s).replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
function dur(s){if(s==null||s<0)return '–';s=Math.round(s);if(s<60)return s+'秒';let m=Math.floor(s/60);if(m<60)return m+'分'+(s%60)+'秒';return Math.floor(m/60)+'時間'+(m%60)+'分'}
function gib(b){return (b/1073741824).toFixed(1)+' GiB'}
function rate(v){return v>0?(v>=10?v.toFixed(0):v.toFixed(2)):'–'}
function clock(ms){if(!ms)return '–';let d=new Date(ms);return d.toLocaleTimeString('ja-JP',{hour:'2-digit',minute:'2-digit'})+(new Date().toDateString()===d.toDateString()?'':' ('+(d.getMonth()+1)+'/'+d.getDate()+')')}
function errText(e){return ERR[e]||(e&&e.startsWith('backend_http_')?'llama.cppがHTTP '+e.slice(13)+'を返しました':e||'')}

// ---- layout: move existing cards into tabs ----
const panels={};document.querySelectorAll('.qa-panel').forEach(p=>panels[p.dataset.panel]=p);
const cards=[...document.querySelectorAll('.wrap > .card')];
for(const m of CARD_MAP){
 const card=cards.find(c=>{let h=c.querySelector('h3');return h&&h.textContent.trim()===m.Heading});
 if(!card)continue;
 card.querySelector('h3').textContent=m.Title;
 panels[m.Tab].appendChild(card);
 if(m.Heading==='Local llama.cpp settings'||m.Heading==='Model residency / auto-unload'){
  const h=card.querySelector('h3'),d=document.createElement('details');d.className='qa-adv';
  d.innerHTML='<summary>設定を表示（通常は変更不要）</summary>';
  while(h.nextSibling)d.appendChild(h.nextSibling);card.appendChild(d);
 }
}
// The dashboard hero now controls the LLM; keep only the voice row of the old Runtime card.
{const st=$('status');if(st&&st.parentElement&&st.parentElement.closest('.qa-panel'))st.parentElement.hidden=true;}
cards.filter(c=>!c.closest('.qa-panel')).forEach(c=>panels.model.appendChild(c)); // unknown cards stay reachable
const relabel={"act('start')":'起動・確認',"act('stop')":'停止',"act('restart')":'再起動',"voiceAct('start')":'音声を起動',"voiceAct('stop')":'音声を停止',"voiceAct('restart')":'音声を再起動',"logs('llama')":'llama.cpp',"logs('admin')":'管理サービス'};
document.querySelectorAll('button[onclick]').forEach(b=>{let t=relabel[b.getAttribute('onclick')];if(t)b.textContent=t});
// Plain Japanese labels for the remaining English texts (IDs and handlers are unchanged).
const words={'Apply provider':'接続先を保存','Select local model':'このモデルを使う','Download to Public':'ダウンロード','Provider':'接続先','Download URL':'ダウンロードURL','Filename (.gguf)':'ファイル名（.gguf）','Local Qwen / llama.cpp':'ローカル（NAS内のllama.cpp）','Save residency':'保存','Auto unload':'使わないときは自動停止','Idle seconds':'停止までの待ち時間（秒）','Disabled / resident':'常駐（停止しない）','Enabled':'自動停止する','Model load timeout (seconds)':'モデル読み込みの時間制限（秒）','Generation timeout (seconds)':'回答生成の時間制限（秒）'};
document.querySelectorAll('.qa-panel button, .qa-panel label, .qa-panel option').forEach(n=>{const t=n.textContent.trim();if(words[t])n.textContent=words[t]});
document.querySelectorAll('.qa-panel .hint').forEach(h=>{const t=h.textContent.trim();
 if(t.startsWith('Switching provider'))h.textContent='接続先を切り替えると、ローカルのllama.cppは停止します。音声の会話履歴は接続先に関係なく保持されます。';
 if(t.startsWith('候補を選ぶと')){const d=document.createElement('details');d.className='qa-adv';
  d.innerHTML='<summary>モデル選びの説明</summary>';const p=document.createElement('div');p.className='hint';p.textContent=t;d.appendChild(p);
  const s=document.createElement('div');s.className='hint';s.textContent='候補を選ぶとURL等が自動で入ります。「ダウンロード」の後、上の一覧で選んで「このモデルを使う」を押してください。';
  h.replaceWith(s,d);}
});

// ---- tabs ----
function show(tab){if(!TABS.includes(tab))tab='home';
 document.querySelectorAll('.qa-tabs button').forEach(b=>b.setAttribute('aria-selected',String(b.dataset.tab===tab)));
 for(const k in panels)panels[k].hidden=k!==tab;
 if(location.hash!=='#'+tab)history.replaceState(null,'','#'+tab);
 poll();
 if(tab==='debug'&&!$('qaChecks').children.length)qaSelfCheck();
 if(tab==='debug')qaLogs();
 window.scrollTo(0,0);}
window.qaShowTab=show;
document.querySelectorAll('.qa-tabs button').forEach(b=>b.onclick=()=>show(b.dataset.tab));

// ---- dashboard ----
const hist={cpu:[],mem:[]};
function spark(id,arr,max){let svg=$(id);if(!svg||arr.length<2)return;let n=arr.length,pts=arr.map((v,i)=>(i*120/(n-1)).toFixed(1)+','+(40-Math.max(0,Math.min(1,v/max))*38-1).toFixed(1)).join(' ');
 svg.innerHTML='<polyline fill="none" stroke="var(--accent)" stroke-width="1.5" points="'+pts+'"/>'}
function bar(id,frac){let b=$(id);if(!b)return;b.querySelector('i').style.width=Math.round(Math.min(1,frac)*100)+'%';b.className='qa-bar'+(frac>.9?' bad':frac>.75?' warn':'')}
function stateOf(d){const L=d.llm,log=d.llama_log,cur=(d.jobs||{}).current;
 if(L.provider!=='local')return ['外部AI（OpenRouter）を使用中','ok',L.openrouter_model||''];
 if(cur){if(cur.status==='generating'){if(log.processing&&log.prompt_progress>=0)return ['入力を読み込み中 '+Math.round(log.prompt_progress*100)+'%','busy',''];return ['回答を生成中','busy','']}
  if(cur.status==='loading_model'||cur.status==='ready')return ['モデルを読み込み中','busy',''];}
 if(log.processing)return ['処理中','busy',''];
 if(L.backend_process_alive)return ['準備完了','ok','起動から '+dur(L.backend_uptime_s)];
 return [L.auto_unload?'待機中（依頼が来ると自動で起動）':'停止中',L.auto_unload?'ok':'bad',''];}
function render(d){
 const [text,cls,extra]=stateOf(d);const L=d.llm,S=d.system,log=d.llama_log,J=d.jobs||{};
 $('qaVer').textContent=d.version&&d.version!=='unknown'?'v'+d.version:'';
 ['qaDot','qaStateDot'].forEach(i=>$(i).className='qa-dot '+cls);
 $('qaState').textContent=text;$('qaHeadState').textContent=text;
 $('qaStateSub').textContent=[L.provider==='local'?(L.model||'モデル未選択'):'',L.provider==='local'?'コンテキスト '+L.context_size:'',L.provider==='local'?'スレッド '+(L.threads||'?'):'',extra].filter(Boolean).join(' ・ ');
 if(S.cpu_percent>=0){$('qaCpu').textContent=S.cpu_percent.toFixed(0)+'%';hist.cpu.push(S.cpu_percent)}
 $('qaLoad').textContent=(S.load||[]).length?'負荷 '+S.load.map(v=>v.toFixed(2)).join(' / ')+'（'+S.cpu_count+'コア）':'';
 if(S.memory_total_bytes){const used=S.memory_total_bytes-S.memory_available_bytes,f=used/S.memory_total_bytes;
  $('qaMem').textContent=Math.round(f*100)+'%';$('qaMemSub').textContent='空き '+gib(S.memory_available_bytes)+' / '+gib(S.memory_total_bytes)+(S.swap_total_bytes?' ・ スワップ使用 '+gib(S.swap_total_bytes-S.swap_free_bytes):'');
  bar('qaMemBar',f);hist.mem.push(f*100)}
 for(const k in hist)if(hist[k].length>60)hist[k].shift();
 spark('qaCpuSpark',hist.cpu,100);spark('qaMemSpark',hist.mem,100);
 $('qaPp').textContent=rate(log.prompt_tokens_per_sec);$('qaTg').textContent=rate(log.gen_tokens_per_sec);
 $('qaQueue').textContent=(J.queued||0)+'件';$('qaQueueSub').textContent=J.current?'実行中 1件':'実行中なし';
 const cur=J.current;let html='';
 if(cur){const st={queued:'順番待ち',loading_model:'モデルを読み込み中',ready:'準備完了',generating:'生成中'}[cur.status]||cur.status;
  const st2=cur.status==='generating'&&log.processing&&log.prompt_progress>=0?'入力を読み込み中':st;
  html+='<div class="qa-row"><b>'+esc(st2)+'</b><span class="qa-muted">経過 '+dur(cur.elapsed_seconds)+' ・ この段階 '+dur(cur.stage_elapsed_seconds)+' ・ 入力 '+Math.round((cur.input_bytes||0)/1024)+' KB</span></div>';
  if(log.processing&&log.prompt_progress>=0){const remain=log.prompt_tokens_per_sec>0?(log.prompt_tokens_total-log.prompt_tokens_done)/log.prompt_tokens_per_sec:null;
   html+='<div style="margin-top:8px">入力の読み込み '+log.prompt_tokens_done+' / 約'+log.prompt_tokens_total+' トークン（'+Math.round(log.prompt_progress*100)+'%）'+(remain!=null?' ・ 残り約 '+dur(remain):'')+'</div><div class="qa-bar"><i style="width:'+Math.round(log.prompt_progress*100)+'%"></i></div>';}
  if(cur.token_chunks>0)html+='<div style="margin-top:8px">出力 '+cur.token_chunks+' 片'+(cur.since_last_token_seconds>=0?' ・ 最後の出力から '+dur(cur.since_last_token_seconds):'')+'</div>';
  html+='<div class="qa-muted" style="margin-top:6px">llama.cppは256トークンごとに進捗を記録します。数値が長く変わらなくても、CPU使用率が高ければ処理は続いています。</div>';
 }else html='<span class="qa-muted">処理中のジョブはありません。</span>';
 $('qaJob').innerHTML=html;
 const rows=(d.recent_jobs||[]);
 $('qaRecent').querySelector('tbody').innerHTML=rows.slice(0,8).map(r=>{const [t,c]=STATUS[r.status]||[r.status,''];
  return '<tr><td>'+clock(r.finished_unix_ms)+'</td><td class="'+c+'" title="'+esc(r.error_code)+'">'+esc(t)+(r.error_code&&r.status!=='completed'?' <span class="qa-muted">'+esc(errText(r.error_code))+'</span>':'')+'</td><td>'+dur(r.total_seconds)+'</td><td>'+dur(r.queue_seconds)+'</td><td>'+dur(r.first_token_seconds)+'</td><td>'+(r.tokens_per_second>0?r.tokens_per_second+' t/s':'–')+'</td><td>'+(r.prompt_tokens||'–')+' / '+(r.completion_tokens||'–')+'</td></tr>'}).join('');
 $('qaRecentEmpty').hidden=rows.length>0;
 $('qaHistory').querySelector('tbody').innerHTML=rows.map(r=>{const [t,c]=STATUS[r.status]||[r.status,''];
  return '<tr><td>'+clock(r.finished_unix_ms)+'</td><td class="'+c+'">'+esc(t)+(r.error_code&&r.status!=='completed'?'<br><span class="qa-muted">'+esc(r.error_code)+'</span>':'')+'</td><td>'+esc(r.model)+'</td><td>'+dur(r.total_seconds)+'</td><td>'+dur(r.queue_seconds)+'</td><td>'+dur(r.load_seconds)+'</td><td>'+dur(r.first_token_seconds)+'</td><td>'+dur(r.generate_seconds)+'</td><td>'+(r.tokens_per_second>0?r.tokens_per_second+' t/s':'–')+'</td><td>'+Math.round((r.input_bytes||0)/1024)+' KB</td></tr>'}).join('')||'<tr><td colspan="10" class="qa-muted">記録はありません。</td></tr>';
}
let timer=null,busy=false;
async function poll(){if(busy)return;busy=true;try{const r=await fetch('/api/monitor',{cache:'no-store'});if(r.ok)render(await r.json())}catch(e){$('qaHeadState').textContent='管理サービスに接続できません';$('qaDot').className='qa-dot bad'}finally{busy=false}}
// Dashboard/debug: every 3 s. Other tabs: every 10 s (keeps the header state current). Hidden page: paused.
function loop(){clearTimeout(timer);const fast=!panels.home.hidden||!panels.debug.hidden;timer=setTimeout(async()=>{if(!document.hidden)await poll();loop()},document.hidden?15000:fast?3000:10000)}
window.qaAct=async function(x){const labels={start:'起動',stop:'停止',restart:'再起動'};if(x==='stop'&&!confirm('ローカルAIを停止します。処理中の依頼は失敗します。よろしいですか？'))return;
 $('qaState').textContent=labels[x]+'しています…';try{await act(x)}catch(e){alert(labels[x]+'に失敗しました: '+e.message)}poll()};

// ---- debug ----
window.qaSelfCheck=async function(){const ul=$('qaChecks');ul.innerHTML='<li class="qa-muted">確認中…</li>';
 try{const d=await (await fetch('/api/debug/selfcheck',{cache:'no-store'})).json();const icon={ok:['✔','qa-ok'],warn:['!','qa-warn'],fail:['✖','qa-bad'],info:['i','qa-info']};
  ul.innerHTML=d.checks.map(c=>{const [i,cl]=icon[c.status]||['?',''];return '<li><span class="'+cl+'"><b>'+i+'</b></span><span><b>'+esc(c.name)+'</b> <span class="qa-muted">'+esc(c.detail)+'</span></span>'+(c.hint?'<span class="hint">'+esc(c.hint)+'</span>':'')+'</li>'}).join('');
  $('qaCheckAt').textContent=' '+new Date(d.checked_at_unix_ms).toLocaleTimeString('ja-JP')+' 時点'}catch(e){ul.innerHTML='<li class="qa-bad">チェックに失敗しました: '+esc(e.message)+'</li>'}};
window.qaCopyReport=async function(){try{const t=await (await fetch('/api/debug/report',{cache:'no-store'})).text();await navigator.clipboard.writeText(t);$('qaReportState').textContent=' コピーしました（'+Math.round(t.length/1024)+' KB）'}catch(e){$('qaReportState').textContent=' コピーできませんでした。ダウンロードを使ってください。'}};
// richer log viewer inside the existing Logs card
const logCard=$('logs')&&$('logs').closest('.card');let logTarget='llama',logTimer=null;
if(logCard){const bar=document.createElement('div');bar.className='qa-row';bar.style.margin='6px 0';
 bar.innerHTML='<label>行数 <select id="qaLogLines"><option>200</option><option selected>400</option><option>1000</option><option>2000</option></select></label><label>絞り込み <input type="search" id="qaLogFilter" placeholder="error, timeout…"></label><label><input type="checkbox" id="qaLogAuto"> 5秒ごとに更新</label><label><input type="checkbox" id="qaLogErr"> エラー・警告のみ</label><button onclick="qaLogs()">更新</button>';
 logCard.insertBefore(bar,$('logs'));
 logCard.querySelectorAll('button[onclick^="logs("]').forEach(b=>{const t=b.getAttribute('onclick').includes('admin')?'admin':'llama';b.removeAttribute('onclick');b.onclick=()=>{logTarget=t;qaLogs()}});
 ['qaLogFilter','qaLogErr','qaLogLines'].forEach(i=>$(i).addEventListener('input',()=>qaLogs()));
 $('qaLogAuto').onchange=()=>{clearInterval(logTimer);if($('qaLogAuto').checked)logTimer=setInterval(()=>{if(!panels.debug.hidden)qaLogs()},5000)};}
window.qaLogs=async function(){if(!logCard)return;try{const r=await fetch('/api/logs?target='+logTarget+'&lines='+$('qaLogLines').value,{cache:'no-store'});let lines=(await r.text()).split('\n');
 const f=$('qaLogFilter').value.trim().toLowerCase();if(f)lines=lines.filter(l=>l.toLowerCase().includes(f));
 if($('qaLogErr').checked)lines=lines.filter(l=>/\b(E|W)\s|error|fail|warn|timeout|cancel/i.test(l));
 const pre=$('logs'),atBottom=pre.scrollTop+pre.clientHeight>=pre.scrollHeight-20;
 pre.innerHTML=(logTarget==='admin'?'[管理サービス]\n':'[llama.cpp]\n')+lines.map(l=>{const e=esc(l);return /error|fail|\bE\s/i.test(l)?'<span class="qa-bad">'+e+'</span>':/warn|timeout|\bW\s|cancel/i.test(l)?'<span class="qa-warn">'+e+'</span>':/progress =|tokens per second/.test(l)?'<span class="qa-info">'+e+'</span>':e}).join('\n');
 if(atBottom)pre.scrollTop=pre.scrollHeight}catch(e){$('logs').textContent='ログを取得できません: '+e.message}};

document.addEventListener('visibilitychange',loop);
show((location.hash||'#home').slice(1));loop();
})();
</script>`

func shellCardMapJSON() string {
	var b strings.Builder
	b.WriteString("[")
	for i, m := range shellCardMap {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"Heading":` + jsString(m.Heading) + `,"Tab":` + jsString(m.Tab) + `,"Title":` + jsString(m.Title) + `}`)
	}
	b.WriteString("]")
	return b.String()
}

func jsString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "<", `<`, ">", `>`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func injectShellUI(html string) string {
	if strings.Contains(html, `id="qa-shell-style"`) {
		return html
	}
	html = strings.Replace(html, shellHeadAnchor, shellCSS+shellHeadAnchor, 1)
	html = strings.Replace(html, shellTitleAnchor, shellHTML, 1)
	script := strings.Replace(shellScript, "__CARD_MAP__", shellCardMapJSON(), 1)
	// Must run after all other scripts so that their functions (act, logs …) exist.
	if i := strings.LastIndex(html, "</body>"); i >= 0 {
		return html[:i] + script + html[i:]
	}
	return html + script
}
