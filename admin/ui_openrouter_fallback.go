package main

import "strings"

const openRouterFallbackAnchor = `<pre id="orTest"></pre>`

const openRouterFallbackHTML = `<div class="profile" id="orFallbackPanel" style="margin-top:12px">
<b>音声ストリーム 自動フォールバック</b>
<div class="grid" style="margin-top:8px"><div><label>429 / 404 時の無料モデル切替</label><select id="OPENROUTER_VOICE_AUTO_FALLBACK"><option value="1">ON</option><option value="0">OFF</option></select></div><div><label>最大試行回数（主モデルを含む）</label><select id="OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS"><option value="1">1</option><option value="2">2</option><option value="3">3</option><option value="4">4</option><option value="5">5</option></select></div></div>
<div class="or-actions"><button onclick="saveORFallback()">フォールバック設定を保存</button><button onclick="refreshORFallback()">統計を更新</button><button onclick="clearORBlacklist('')">ブラックリストを全解除</button></div>
<div class="hint">候補は現在のAPIキーで <code>/models/user</code> に残る無料モデルだけです。429は一時クールダウン、guardrail/privacy/data policy由来の404だけをブラックリスト化します。回答を一部ストリーム済みの場合は別モデルへ切り替えません。</div>
<div id="orFallbackStatus" class="or-model-meta"></div>
</div>`

const openRouterFallbackScript = `<script>
function orPolicyLabel(m){if(m.blacklisted)return '[BLACKLIST]';if(m.policy_status==='allowed')return '[Policy OK]';if(m.policy_status==='filtered')return '[Policy除外]';return '[Policy ?]';}
function orHealthLabel(m){let x='成功 '+(m.success_count||0)+' / 失敗 '+(m.failure_count||0);if(m.blacklisted)x+=' / blacklist';else if(m.cooldown_until)x+=' / cooldown '+m.cooldown_until;return x;}
window.updateORModelMeta=function(){let s=el('orModels');let m=window.orCatalog.find(x=>x.id===s.value);if(!m){el('orModelMeta').textContent=s.value||'';return}let bits=[orPolicyLabel(m)];if(m.free)bits.push('FREE');if(m.context_length)bits.push('context '+Number(m.context_length).toLocaleString());let pricing=(typeof renderORPricing==='function')?renderORPricing(m):'';if(pricing)bits.push(pricing);bits.push(orHealthLabel(m));if(m.blacklist_reason)bits.push('理由: '+m.blacklist_reason);el('orModelMeta').textContent=bits.join(' · ');};
window.loadORModels=async function(){let s=el('orModels');let current=(el('OPENROUTER_MODEL').value||s.value||'openrouter/free').trim();s.innerHTML='<option>loading...</option>';try{let d=await j('/api/openrouter/models?free='+el('orFree').value);window.orCatalog=d.models||[];let opts=window.orCatalog.map(m=>{let p=m.pricing||{};let price=m.free?'FREE':('in '+orMillionPrice(p.prompt)+'/M out '+orMillionPrice(p.completion)+'/M');let label=orPolicyLabel(m);return '<option value="'+m.id.replace(/"/g,'&quot;')+'">'+label+' '+(m.free?'[FREE] ':'')+m.name+' — '+m.id+' · ctx '+(m.context_length||'?')+' · '+price+' · S'+(m.success_count||0)+'/F'+(m.failure_count||0)+'</option>'});if(current&&!window.orCatalog.some(m=>m.id===current))opts.unshift('<option value="'+current.replace(/"/g,'&quot;')+'">Current / manual — '+current+'</option>');s.innerHTML=opts.join('');if(current)s.value=current;if(!s.value&&s.options.length)s.selectedIndex=0;updateORModelMeta()}catch(e){s.innerHTML='<option value="'+current+'">'+current+'</option>';el('orModelMeta').textContent='モデル一覧の取得に失敗: '+e.message;}};
window.saveORFallback=async function(){await putCfg({OPENROUTER_VOICE_AUTO_FALLBACK:el('OPENROUTER_VOICE_AUTO_FALLBACK').value,OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS:el('OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS').value});await refreshORFallback();};
window.refreshORFallback=async function(){try{let c=await j('/api/config');el('OPENROUTER_VOICE_AUTO_FALLBACK').value=c.OPENROUTER_VOICE_AUTO_FALLBACK||'1';el('OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS').value=c.OPENROUTER_VOICE_FALLBACK_MAX_ATTEMPTS||'3';let d=await j('/api/openrouter/fallback');let rows=(d.models||[]).filter(x=>x.health&&(x.health.success_count||x.health.failure_count||x.health.blacklisted));if(!rows.length){el('orFallbackStatus').textContent='まだフォールバック実績はありません。';return}el('orFallbackStatus').innerHTML=rows.map(x=>{let h=x.health;let b=h.blacklisted?' <b class="bad">BLACKLIST</b>':'';let clear=h.blacklisted?' <button onclick="clearORBlacklist('+JSON.stringify(x.model).replace(/"/g,'&quot;')+')">解除</button>':'';return '<div><code>'+x.model+'</code> · 成功 '+(h.success_count||0)+' · 失敗 '+(h.failure_count||0)+b+(h.last_failure_reason?' · '+h.last_failure_reason:'')+clear+'</div>'}).join('');}catch(e){el('orFallbackStatus').textContent='状態取得失敗: '+e.message;}};
window.clearORBlacklist=async function(model){if(!confirm(model?('ブラックリストから '+model+' を解除しますか？'):'ブラックリストを全解除しますか？'))return;await j('/api/openrouter/fallback',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'clear_blacklist',model:model||''})});await refreshORFallback();await loadORModels();};
setTimeout(()=>refreshORFallback().catch(()=>{}),900);
</script>`

func injectOpenRouterFallbackUI(html string) string {
	if !strings.Contains(html, `id="orFallbackPanel"`) {
		html = strings.Replace(html, openRouterFallbackAnchor, openRouterFallbackAnchor+openRouterFallbackHTML, 1)
	}
	if !strings.Contains(html, "function orPolicyLabel") {
		html = strings.Replace(html, "</body>", openRouterFallbackScript+"</body>", 1)
	}
	return html
}
