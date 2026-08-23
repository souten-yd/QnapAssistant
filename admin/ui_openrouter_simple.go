package main

import (
	"io"
	"net/http"
	"strings"
)

const openRouterCardStart = `<div class="card"><h3>OpenRouter</h3>`
const localModelsCardStart = `<div class="card"><h3>Local GGUF models</h3>`

const simpleOpenRouterCard = `<div class="card"><h3>OpenRouter</h3>
<style>.or-main{border:1px solid #3b4454;border-radius:12px;padding:14px;background:#141820}.or-main label{display:block;margin-top:6px}.or-actions{display:flex;gap:6px;flex-wrap:wrap;margin-top:8px}.or-good{color:#65d98b}.or-muted{color:#8f9bad}.or-model-meta{font-size:12px;color:#aeb7c6;margin-top:6px;min-height:18px}details.or-advanced{margin-top:14px;border-top:1px solid #303745;padding-top:10px}details.or-advanced summary{cursor:pointer;color:#c9d2e2;font-weight:600;margin-bottom:10px}</style>
<div class="hint">通常は <b>APIキーを保存 → モデルを選択 → 接続確認</b> だけで使えます。その他はOpenRouterの規定値を利用します。</div>
<div class="or-main">
<div class="grid"><div><label>API key</label><input id="orKey" class="secret" type="password" autocomplete="new-password" placeholder="sk-or-... (保存後は表示しません)"></div><div><label>Key status</label><div id="orKeyStatus" class="hint">...</div><div id="orConnection" class="or-model-meta"></div></div></div>
<div class="or-actions"><button onclick="saveORKey()">APIキーを保存</button><button onclick="checkOR()">API接続確認（課金なし）</button><button onclick="clearORKey()">APIキーを削除</button></div>
<hr style="border:0;border-top:1px solid #303745;margin:14px 0">
<div class="grid"><div><label>モデル</label><select id="orModels" onchange="updateORModelMeta()"><option>モデル一覧を取得中...</option></select><div id="orModelMeta" class="or-model-meta"></div></div><div><label>表示</label><select id="orFree" onchange="loadORModels()"><option value="0">すべてのテキストモデル</option><option value="1">無料モデルのみ</option></select><div class="hint">OpenRouterの最新モデルカタログから取得します。</div></div></div>
<div class="or-actions"><button onclick="loadORModels()">モデル一覧を更新</button><button onclick="applyORSelected()">選択モデルを使用</button><button onclick="testOR()">無料生成テスト</button></div>
<pre id="orTest"></pre>
<div class="hint">「API接続確認」は <code>GET /api/v1/key</code> のみで生成しません。「無料生成テスト」は常に <code>openrouter/free</code> を使用し、最大8トークンです。</div>
</div>

<details class="or-advanced"><summary>詳細設定（通常は変更不要）</summary>
<div class="hint">空欄はOpenRouterまたはQnapAssistantの規定値を使用します。provider設定を空欄にするとOpenRouterの標準ルーティング・provider failoverが使われます。</div>
<div class="grid"><div><label>Primary model ID (manual override)</label><input id="OPENROUTER_MODEL" placeholder="openrouter/free"></div><div><label>Fallback models (CSV, ordered)</label><input id="OPENROUTER_FALLBACK_MODELS" placeholder="空欄 = model fallbackなし"></div><div><label>Preset</label><input id="OPENROUTER_PRESET" placeholder="空欄 = 使用しない"></div><div><label>Base URL</label><input id="OPENROUTER_BASE_URL" placeholder="空欄 = https://openrouter.ai/api/v1"></div></div>
<div class="grid"><div><label>Temperature</label><input id="OPENROUTER_TEMPERATURE" placeholder="空欄 = request/model default"></div><div><label>Top P</label><input id="OPENROUTER_TOP_P" placeholder="空欄 = model default"></div><div><label>Reasoning effort</label><select id="OPENROUTER_REASONING_EFFORT"><option value="">Model default</option><option value="none">none</option><option value="minimal">minimal</option><option value="low">low</option><option value="medium">medium</option><option value="high">high</option><option value="xhigh">xhigh</option><option value="max">max</option></select></div><div><label>Provider sort</label><select id="OPENROUTER_PROVIDER_SORT"><option value="">OpenRouter default</option><option value="price">price</option><option value="throughput">throughput</option><option value="latency">latency</option></select></div></div>
<div class="grid"><div><label>Provider fallback</label><select id="OPENROUTER_ALLOW_FALLBACKS"><option value="">OpenRouter default (enabled)</option><option value="1">Force enabled</option><option value="0">Disable</option></select></div><div><label>Require parameter support</label><select id="OPENROUTER_REQUIRE_PARAMETERS"><option value="">OpenRouter default</option><option value="1">Yes</option><option value="0">No</option></select></div><div><label>Data collection</label><select id="OPENROUTER_DATA_COLLECTION"><option value="">OpenRouter default</option><option value="deny">deny</option><option value="allow">allow</option></select></div><div><label>Zero data retention only</label><select id="OPENROUTER_ZDR"><option value="">OpenRouter default</option><option value="1">Yes</option><option value="0">No</option></select></div></div>
<div class="grid"><div><label>Provider only (CSV)</label><input id="OPENROUTER_PROVIDER_ONLY"></div><div><label>Provider ignore (CSV)</label><input id="OPENROUTER_PROVIDER_IGNORE"></div><div><label>Max prompt price / 1M tokens</label><input id="OPENROUTER_MAX_PRICE_PROMPT"></div><div><label>Max completion price / 1M tokens</label><input id="OPENROUTER_MAX_PRICE_COMPLETION"></div><div><label>HTTP-Referer (optional)</label><input id="OPENROUTER_HTTP_REFERER"></div><div><label>X-Title</label><input id="OPENROUTER_X_TITLE"></div></div>
<div class="or-actions"><button onclick="saveORSettings()">詳細設定を保存</button><button onclick="resetORAdvanced()">OpenRouter規定値へ戻す</button></div>
</details></div>

`

const openRouterEnhancementScript = `<script>
window.orCatalog=[];
function orMillionPrice(v){let n=Number(v);if(!Number.isFinite(n))return '?';let x=n*1000000;return '$'+(x>=1?x.toFixed(2):x.toFixed(4));}
window.updateORModelMeta=function(){let s=el('orModels');let m=window.orCatalog.find(x=>x.id===s.value);if(!m){el('orModelMeta').textContent=s.value||'';return}let p=m.pricing||{};let bits=[];if(m.free)bits.push('FREE');if(m.context_length)bits.push('context '+Number(m.context_length).toLocaleString());if(p.prompt!==undefined||p.completion!==undefined)bits.push('input '+orMillionPrice(p.prompt)+'/M · output '+orMillionPrice(p.completion)+'/M');el('orModelMeta').textContent=bits.join(' · ');};
window.loadORModels=async function(){let s=el('orModels');let current=(el('OPENROUTER_MODEL').value||s.value||'openrouter/free').trim();s.innerHTML='<option>loading...</option>';try{let d=await j('/api/openrouter/models?free='+el('orFree').value);window.orCatalog=d.models||[];let opts=window.orCatalog.map(m=>{let p=m.pricing||{};let price=m.free?'FREE':('in '+orMillionPrice(p.prompt)+'/M out '+orMillionPrice(p.completion)+'/M');return '<option value="'+m.id.replace(/"/g,'&quot;')+'">'+(m.free?'[FREE] ':'')+m.name+' — '+m.id+' · ctx '+(m.context_length||'?')+' · '+price+'</option>'});if(current&&!window.orCatalog.some(m=>m.id===current))opts.unshift('<option value="'+current.replace(/"/g,'&quot;')+'">Current / manual — '+current+'</option>');s.innerHTML=opts.join('');if(current)s.value=current;if(!s.value&&s.options.length)s.selectedIndex=0;updateORModelMeta()}catch(e){s.innerHTML='<option value="'+current+'">'+current+'</option>';el('orModelMeta').textContent='モデル一覧の取得に失敗: '+e.message;}};
window.applyORSelected=async function(){let model=el('orModels').value;if(!model){alert('モデルを選択してください');return}el('OPENROUTER_MODEL').value=model;await putCfg({LLM_PROVIDER:'openrouter',OPENROUTER_MODEL:model});el('orTest').textContent='OpenRouterを使用: '+model;};
window.checkOR=async function(){el('orConnection').textContent='接続確認中...';try{let d=await j('/api/openrouter/check');let parts=['接続OK',d.wall_ms+' ms'];if(d.label)parts.push(d.label);if(d.is_free_tier)parts.push('free tier');if(d.limit_remaining!==null&&d.limit_remaining!==undefined)parts.push('remaining '+d.limit_remaining);el('orConnection').textContent=parts.join(' · ');el('orConnection').className='or-model-meta or-good';return d}catch(e){el('orConnection').textContent='接続NG: '+e.message;el('orConnection').className='or-model-meta bad';throw e}};
window.saveORKey=async function(){if(!el('orKey').value){alert('API key is empty');return}await j('/api/openrouter/key',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({api_key:el('orKey').value})});el('orKey').value='';await refresh();try{await checkOR()}catch(e){}await loadORModels();};
window.testOR=async function(){el('orTest').textContent='無料モデルで生成確認中...';try{let d=await j('/api/openrouter/test',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({max_tokens:8})});el('orTest').textContent='生成OK · model '+(d.model||'?')+' · provider '+(d.provider||'?')+' · '+d.wall_ms+' ms\n'+(d.content||'')}catch(e){el('orTest').textContent='生成NG: '+e.message}};
window.resetORAdvanced=async function(){let keys=['OPENROUTER_BASE_URL','OPENROUTER_FALLBACK_MODELS','OPENROUTER_PRESET','OPENROUTER_TEMPERATURE','OPENROUTER_TOP_P','OPENROUTER_REASONING_EFFORT','OPENROUTER_PROVIDER_SORT','OPENROUTER_ALLOW_FALLBACKS','OPENROUTER_REQUIRE_PARAMETERS','OPENROUTER_DATA_COLLECTION','OPENROUTER_ZDR','OPENROUTER_PROVIDER_ONLY','OPENROUTER_PROVIDER_IGNORE','OPENROUTER_MAX_PRICE_PROMPT','OPENROUTER_MAX_PRICE_COMPLETION','OPENROUTER_HTTP_REFERER','OPENROUTER_X_TITLE'];let c={};for(let k of keys)c[k]='';await putCfg(c);el('orTest').textContent='詳細設定を規定値へ戻しました。選択モデルは維持しています。';};
setTimeout(loadORModels,500);
</script>`

func renderedIndexHTML() string {
	start := strings.Index(indexHTML, openRouterCardStart)
	if start < 0 {
		return indexHTML
	}
	relEnd := strings.Index(indexHTML[start:], localModelsCardStart)
	if relEnd < 0 {
		return indexHTML
	}
	end := start + relEnd
	html := indexHTML[:start] + simpleOpenRouterCard + indexHTML[end:]
	return strings.Replace(html, "</body>", openRouterEnhancementScript+"</body>", 1)
}

func (m *manager) handleSimpleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, renderedIndexHTML())
}
