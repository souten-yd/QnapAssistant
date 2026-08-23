package main

import "strings"

const openRouterBillingAnchor = `<div id="orConnection" class="or-model-meta"></div>`

const openRouterBillingHTML = `<div id="orBilling" class="or-model-meta" style="margin-top:8px;line-height:1.55"></div>`

const openRouterBillingScript = `<script>
function orNum(v){let n=Number(v);return Number.isFinite(n)?n:null;}
function orCredit(v){let n=orNum(v);if(n===null)return '-';return n.toFixed(n===0?2:(Math.abs(n)<0.01?6:4))+' credits';}
function orPrice(v,mult){let n=orNum(v);if(n===null)return null;let x=n*(mult||1);let digits=x===0?2:(Math.abs(x)<0.01?6:(Math.abs(x)<1?4:2));return '$'+x.toFixed(digits);}
function orPricingParts(p){if(!p)return[];let out=[];let tokenKeys=[['prompt','入力',1000000,'/M tokens'],['completion','出力',1000000,'/M tokens'],['internal_reasoning','推論',1000000,'/M tokens'],['input_cache_read','cache読込',1000000,'/M tokens'],['input_cache_write','cache書込',1000000,'/M tokens']];for(let x of tokenKeys){if(p[x[0]]!==undefined){let v=orPrice(p[x[0]],x[2]);if(v!==null)out.push(x[1]+' '+v+x[3]);}}let units=[['request','request',1,'/request'],['image','image',1,'/image'],['web_search','web search',1,'/search']];for(let x of units){if(p[x[0]]!==undefined){let v=orPrice(p[x[0]],x[2]);if(v!==null&&Number(p[x[0]])!==0)out.push(x[1]+' '+v+x[3]);}}return out;}
function renderORPricing(m){if(!m)return'';let tiers=m.pricing_tiers||[];if(tiers.length>1){let lines=[];for(let i=0;i<tiers.length;i++){let t=tiers[i]||{};let threshold=i===0?'標準':((Number(t.min_context)||0).toLocaleString()+' tokens以上');let parts=orPricingParts(t);if(parts.length)lines.push(threshold+': '+parts.join(' · '));}return lines.join(' / ');}return orPricingParts(m.pricing||{}).join(' · ');}
window.renderORBilling=function(d){let e=el('orBilling');if(!e)return;let lines=[];lines.push('<b>APIキー使用実績</b>');lines.push('今日 '+orCredit(d.usage_daily)+' · 今週 '+orCredit(d.usage_weekly)+' · 今月 '+orCredit(d.usage_monthly)+' · 累計 '+orCredit(d.usage));if(d.limit!==null&&d.limit!==undefined){lines.push('上限 '+orCredit(d.limit)+' · 残り '+orCredit(d.limit_remaining)+(d.limit_reset?' · reset '+d.limit_reset:''));}let byok=orNum(d.byok_usage);if(byok!==null&&byok!==0){lines.push('BYOK 累計 '+orCredit(d.byok_usage)+' · 今月 '+orCredit(d.byok_usage_monthly));}let flags=[];flags.push(d.is_free_tier?'free tier':'paid tier');if(d.is_management_key)flags.push('management key');if(d.expires_at)flags.push('expires '+d.expires_at);lines.push(flags.join(' · '));e.innerHTML=lines.map(x=>'<div>'+x+'</div>').join('');};
window.updateORModelMeta=function(){let s=el('orModels');let m=window.orCatalog.find(x=>x.id===s.value);if(!m){el('orModelMeta').textContent=s.value||'';return}let bits=[];if(m.free)bits.push('FREE');if(m.context_length)bits.push('context '+Number(m.context_length).toLocaleString());let pricing=renderORPricing(m);if(pricing)bits.push(pricing);el('orModelMeta').textContent=bits.join(' · ');};
window.checkOR=async function(){el('orConnection').textContent='接続確認中...';try{let d=await j('/api/openrouter/check');let parts=['接続OK',d.wall_ms+' ms'];if(d.label)parts.push(d.label);if(d.limit_remaining!==null&&d.limit_remaining!==undefined)parts.push('remaining '+orCredit(d.limit_remaining));el('orConnection').textContent=parts.join(' · ');el('orConnection').className='or-model-meta or-good';renderORBilling(d);return d}catch(e){el('orConnection').textContent='接続NG: '+e.message;el('orConnection').className='or-model-meta bad';let b=el('orBilling');if(b)b.textContent='';throw e}};
setTimeout(()=>{if(el('orKeyStatus')&&el('orKeyStatus').textContent.indexOf('configured')>=0)checkOR().catch(()=>{});},1100);
</script>`

func injectOpenRouterBillingUI(html string) string {
	if !strings.Contains(html, `id="orBilling"`) {
		html = strings.Replace(html, openRouterBillingAnchor, openRouterBillingAnchor+openRouterBillingHTML, 1)
	}
	if !strings.Contains(html, "function renderORPricing") {
		html = strings.Replace(html, "</body>", openRouterBillingScript+"</body>", 1)
	}
	return html
}
