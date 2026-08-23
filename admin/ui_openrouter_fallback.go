package main

import "strings"

const openRouterFallbackAnchor = `<div class="or-actions"><button onclick="loadORModels()">モデル一覧を更新</button>`

const openRouterFallbackHTML = `<div id="orPolicyStatus" class="or-model-meta"></div>
<div class="grid" style="margin-top:10px">
  <div><label>無料モデル自動フォールバック</label><select id="OPENROUTER_AUTO_FREE_FALLBACK"><option value="0">OFF</option><option value="1">ON</option></select></div>
  <div><label>総試行回数（主モデルを含む）</label><select id="OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS"><option value="1">1</option><option value="2">2</option><option value="3">3（推奨）</option><option value="4">4</option><option value="5">5（最大）</option></select></div>
</div>
<div class="or-actions"><button onclick="saveORFallbackSettings()">フォールバック設定を保存</button></div>
<div class="hint">ON時は主モデル → 手動Fallback欄に指定した「現在利用可能な無料モデル」 → その他の利用可能な無料モデルの順で、OpenRouterの <code>models</code> フォールバックを使います。QnapAssistantがHTTP要求を5回再送する方式ではありません。最大回数は主モデルを含みます。</div>
`

const openRouterFallbackScript = `<script>
function orEsc(v){return String(v??'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');}
window.loadORFallbackSettings=async function(){try{let c=await j('/api/config');let e=el('OPENROUTER_AUTO_FREE_FALLBACK');if(e)e.value=c.OPENROUTER_AUTO_FREE_FALLBACK||'0';let a=el('OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS');if(a)a.value=c.OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS||'3';}catch(e){}}
window.saveORFallbackSettings=async function(){let enabled=el('OPENROUTER_AUTO_FREE_FALLBACK').value||'0';let attempts=el('OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS').value||'3';await putCfg({OPENROUTER_AUTO_FREE_FALLBACK:enabled,OPENROUTER_AUTO_FREE_FALLBACK_ATTEMPTS:attempts});el('orTest').textContent=(enabled==='1'?'無料モデル自動フォールバック ON':'無料モデル自動フォールバック OFF')+' · 総試行回数 '+attempts;};
window.updateORModelMeta=function(){let s=el('orModels');let m=window.orCatalog.find(x=>x.id===s.value);if(!m){el('orModelMeta').textContent=s.value||'';return}let bits=[];if(m.free)bits.push('FREE');if(m.policy_checked)bits.push(m.policy_allowed?'Policy利用可':'Privacy/Guardrail制限');else bits.push('Policy未確認');if(m.moderated)bits.push('provider moderationあり');if(m.context_length)bits.push('context '+Number(m.context_length).toLocaleString());let pricing=(typeof renderORPricing==='function')?renderORPricing(m):'';if(pricing)bits.push(pricing);el('orModelMeta').textContent=bits.join(' · ');el('orModelMeta').className='or-model-meta '+(m.policy_checked&&!m.policy_allowed?'bad':'');};
window.loadORModels=async function(){let s=el('orModels');let current=(el('OPENROUTER_MODEL').value||s.value||'openrouter/free').trim();s.innerHTML='<option>loading...</option>';try{let d=await j('/api/openrouter/models?free='+el('orFree').value);window.orCatalog=d.models||[];let opts=window.orCatalog.map(m=>{let blocked=!!(m.policy_checked&&!m.policy_allowed);let policy=m.policy_checked?(m.policy_allowed?'[利用可] ':'[制限] '):'[未確認] ';let moderated=m.moderated?'[MOD] ':'';let p=m.pricing||{};let price=m.free?'FREE':('in '+orMillionPrice(p.prompt)+'/M out '+orMillionPrice(p.completion)+'/M');return '<option value="'+orEsc(m.id)+'" '+(blocked?'disabled':'')+'>'+policy+(m.free?'[FREE] ':'')+moderated+orEsc(m.name)+' — '+orEsc(m.id)+' · ctx '+orEsc(m.context_length||'?')+' · '+orEsc(price)+'</option>'});if(current&&!window.orCatalog.some(m=>m.id===current))opts.unshift('<option value="'+orEsc(current)+'">Current / manual — '+orEsc(current)+'</option>');s.innerHTML=opts.join('');if(current)s.value=current;if(!s.value&&s.options.length){for(let i=0;i<s.options.length;i++){if(!s.options[i].disabled){s.selectedIndex=i;break}}}let ps=el('orPolicyStatus');if(ps){let blocked=window.orCatalog.filter(m=>m.policy_checked&&!m.policy_allowed).length;let allowed=window.orCatalog.filter(m=>!m.policy_checked||m.policy_allowed).length;if(d.policy_checked){ps.textContent='OpenRouter現在ポリシー判定: 利用可 '+allowed+' / 制限 '+blocked+' · Privacy/Guardrail/provider preferences反映済み · 429と入力内容依存Guardrailは事前判定不可';ps.className='or-model-meta '+(blocked?'off':'or-good')}else if(d.policy_error){ps.textContent='Policy判定は取得できません: '+d.policy_error;ps.className='or-model-meta off'}else{ps.textContent='APIキー保存後にPrivacy/Guardrail適合性も判定します。';ps.className='or-model-meta'}}updateORModelMeta()}catch(e){s.innerHTML='<option value="'+orEsc(current)+'">'+orEsc(current)+'</option>';el('orModelMeta').textContent='モデル一覧の取得に失敗: '+e.message;}};
window.applyORSelected=async function(){let model=el('orModels').value;if(!model){alert('モデルを選択してください');return}let m=window.orCatalog.find(x=>x.id===model);if(m&&m.policy_checked&&!m.policy_allowed){alert('このモデルは現在のOpenRouter Privacy/Guardrail/provider制限では利用できません。設定変更後にモデル一覧を更新してください。');return}el('OPENROUTER_MODEL').value=model;await putCfg({LLM_PROVIDER:'openrouter',OPENROUTER_MODEL:model});el('orTest').textContent='OpenRouterを使用: '+model;};
setTimeout(()=>{loadORFallbackSettings();loadORModels();},850);
</script>`

func injectOpenRouterFallbackUI(html string) string {
	if !strings.Contains(html, `id="OPENROUTER_AUTO_FREE_FALLBACK"`) {
		html = strings.Replace(html, openRouterFallbackAnchor, openRouterFallbackHTML+openRouterFallbackAnchor, 1)
	}
	if !strings.Contains(html, "function orEsc(v)") {
		html = strings.Replace(html, "</body>", openRouterFallbackScript+"</body>", 1)
	}
	return html
}
