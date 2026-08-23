package main

import "strings"

const openRouterVoiceHealthAnchor = `<div class="or-actions"><button onclick="saveORFallbackSettings()">フォールバック設定を保存</button></div>`

const openRouterVoiceHealthHTML = `<div class="or-actions"><button onclick="refreshORVoiceHealth()">成功/失敗実績を更新</button><button onclick="clearORVoiceBlacklist('')">ブラックリストを全解除</button></div>
<div id="orVoiceHealth" class="or-model-meta" style="line-height:1.55"></div>
<div class="hint">音声ストリームでは429/404を回答開始前だけ再試行します。429は一時クールダウン、明示的なPrivacy/Guardrail/Data Policy/ZDR 404だけをブラックリスト化します。候補は成功率を優先し、同程度なら分散のためランダム化します。</div>`

const openRouterVoiceHealthScript = `<script>
function orVoiceCooldownActive(m){if(!m||!m.voice_cooldown_until)return false;let t=Date.parse(m.voice_cooldown_until);return Number.isFinite(t)&&t>Date.now();}
function orVoiceBadge(m){if(m.voice_blacklisted)return '[BLACKLIST] ';if(orVoiceCooldownActive(m))return '[COOLDOWN] ';return '';}
function orVoiceStats(m){return 'S'+(m.voice_success_count||0)+'/F'+(m.voice_failure_count||0);}
window.updateORModelMeta=function(){let s=el('orModels');let m=window.orCatalog.find(x=>x.id===s.value);if(!m){el('orModelMeta').textContent=s.value||'';return}let bits=[];if(m.free)bits.push('FREE');if(m.policy_checked)bits.push(m.policy_allowed?'Policy利用可':'Privacy/Guardrail制限');else bits.push('Policy未確認');if(m.moderated)bits.push('provider moderationあり');if(m.voice_blacklisted)bits.push('VOICE BLACKLIST');else if(orVoiceCooldownActive(m))bits.push('一時cooldown');bits.push('音声 '+orVoiceStats(m));if(m.context_length)bits.push('context '+Number(m.context_length).toLocaleString());let pricing=(typeof renderORPricing==='function')?renderORPricing(m):'';if(pricing)bits.push(pricing);if(m.voice_blacklist_reason)bits.push('理由: '+m.voice_blacklist_reason);el('orModelMeta').textContent=bits.join(' · ');el('orModelMeta').className='or-model-meta '+((m.policy_checked&&!m.policy_allowed)||m.voice_blacklisted?'bad':'');};
window.loadORModels=async function(){let s=el('orModels');let current=(el('OPENROUTER_MODEL').value||s.value||'openrouter/free').trim();s.innerHTML='<option>loading...</option>';try{let d=await j('/api/openrouter/models?free='+el('orFree').value);window.orCatalog=d.models||[];let opts=window.orCatalog.map(m=>{let blocked=!!(m.policy_checked&&!m.policy_allowed);let policy=m.policy_checked?(m.policy_allowed?'[利用可] ':'[制限] '):'[未確認] ';let moderated=m.moderated?'[MOD] ':'';let p=m.pricing||{};let price=m.free?'FREE':('in '+orMillionPrice(p.prompt)+'/M out '+orMillionPrice(p.completion)+'/M');return '<option value="'+orEsc(m.id)+'" '+(blocked?'disabled':'')+'>'+orVoiceBadge(m)+policy+(m.free?'[FREE] ':'')+moderated+orEsc(m.name)+' — '+orEsc(m.id)+' · '+orVoiceStats(m)+' · ctx '+orEsc(m.context_length||'?')+' · '+orEsc(price)+'</option>'});if(current&&!window.orCatalog.some(m=>m.id===current))opts.unshift('<option value="'+orEsc(current)+'">Current / manual — '+orEsc(current)+'</option>');s.innerHTML=opts.join('');if(current)s.value=current;if(!s.value&&s.options.length){for(let i=0;i<s.options.length;i++){if(!s.options[i].disabled){s.selectedIndex=i;break}}}let ps=el('orPolicyStatus');if(ps){let restricted=window.orCatalog.filter(m=>m.policy_checked&&!m.policy_allowed).length;let blacklisted=window.orCatalog.filter(m=>m.voice_blacklisted).length;let allowed=window.orCatalog.filter(m=>!m.policy_checked||m.policy_allowed).length;if(d.policy_checked){ps.textContent='OpenRouter現在ポリシー判定: 利用可 '+allowed+' / 制限 '+restricted+' · 音声Blacklist '+blacklisted+' · Privacy/Guardrail/provider preferences反映済み · 429と入力内容依存Guardrailは事前判定不可';ps.className='or-model-meta '+(restricted||blacklisted?'off':'or-good')}else if(d.policy_error){ps.textContent='Policy判定は取得できません: '+d.policy_error;ps.className='or-model-meta off'}else{ps.textContent='APIキー保存後にPrivacy/Guardrail適合性も判定します。';ps.className='or-model-meta'}}updateORModelMeta()}catch(e){s.innerHTML='<option value="'+orEsc(current)+'">'+orEsc(current)+'</option>';el('orModelMeta').textContent='モデル一覧の取得に失敗: '+e.message;}};
window.refreshORVoiceHealth=async function(){let box=el('orVoiceHealth');if(!box)return;try{let d=await j('/api/openrouter/voice-health');let rows=(d.models||[]).filter(x=>x.health&&(x.health.success_count||x.health.failure_count||x.health.blacklisted||x.health.cooldown_until));if(!rows.length){box.textContent='音声フォールバック実績はまだありません。';return}box.innerHTML=rows.map(x=>{let h=x.health||{};let flags=[];if(h.blacklisted)flags.push('<b class="bad">BLACKLIST</b>');else if(h.cooldown_until)flags.push('cooldown '+orEsc(h.cooldown_until));let clear=h.blacklisted?' <button onclick="clearORVoiceBlacklist('+JSON.stringify(x.model).replace(/"/g,'&quot;')+')">解除</button>':'';return '<div><code>'+orEsc(x.model)+'</code> · 成功 '+(h.success_count||0)+' · 失敗 '+(h.failure_count||0)+(flags.length?' · '+flags.join(' · '):'')+(h.last_failure_reason?' · '+orEsc(h.last_failure_reason):'')+clear+'</div>'}).join('');}catch(e){box.textContent='音声実績の取得に失敗: '+e.message;}};
window.clearORVoiceBlacklist=async function(model){if(!confirm(model?('音声ブラックリストから '+model+' を解除しますか？'):'音声ブラックリストを全解除しますか？'))return;await j('/api/openrouter/voice-health',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'clear_blacklist',model:model||''})});await refreshORVoiceHealth();await loadORModels();};
setTimeout(()=>refreshORVoiceHealth().catch(()=>{}),1050);
</script>`

func injectOpenRouterVoiceHealthUI(html string) string {
	if !strings.Contains(html, `id="orVoiceHealth"`) {
		html = strings.Replace(html, openRouterVoiceHealthAnchor, openRouterVoiceHealthAnchor+openRouterVoiceHealthHTML, 1)
	}
	if !strings.Contains(html, "function orVoiceBadge") {
		html = strings.Replace(html, "</body>", openRouterVoiceHealthScript+"</body>", 1)
	}
	return html
}
