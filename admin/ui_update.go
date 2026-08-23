package main

import "strings"

const updateCardAnchor = `<h1>QnapAssistant</h1>`

const updateCardHTML = `<div class="card" id="updateCard"><h3>QnapAssistant Update</h3>
<style>.upd-row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.upd-version{font-size:18px;font-weight:700}.upd-notes{max-height:180px}.upd-busy{color:#e6bd5a}.upd-good{color:#65d98b}</style>
<div class="grid"><div><label>現在のバージョン</label><div id="updateCurrent" class="upd-version">...</div></div><div><label>最新バージョン</label><div id="updateLatest" class="upd-version">...</div></div></div>
<div id="updateMessage" class="hint">更新情報を確認中...</div>
<div class="upd-row"><button onclick="checkAppUpdate(true)">更新を確認</button><button id="updateInstall" onclick="installAppUpdate()" disabled>今すぐ更新</button><a id="updateReleaseLink" href="#" target="_blank" rel="noopener" style="display:none">GitHub Release</a></div>
<details id="updateNotesWrap" style="display:none;margin-top:8px"><summary>リリースノート</summary><pre id="updateNotes" class="upd-notes"></pre></details>
<div class="hint">stable版のみ対象です。QPKGと同時公開されたSHA-256を検証してから更新し、完了後は自動的に再接続します。</div>
</div>
`

const updateEnhancementScript = `<script>
window.qnapUpdateInfo=null;
window.qnapUpdatePolling=false;
function setUpdateMessage(text,cls){let e=el('updateMessage');if(!e)return;e.textContent=text;e.className='hint '+(cls||'');}
window.renderUpdateInfo=function(d){window.qnapUpdateInfo=d;el('updateCurrent').textContent='v'+(d.current_version||'?');el('updateLatest').textContent='v'+(d.latest_version||'?');let b=el('updateInstall');b.disabled=!d.available;if(d.available){b.textContent='v'+d.latest_version+' へ更新';setUpdateMessage('新しいバージョンが利用できます。','upd-busy')}else{b.textContent='今すぐ更新';setUpdateMessage(d.current_version==='unknown'?'現在のQPKGバージョンを確認できません。':'最新版です。','upd-good')}let a=el('updateReleaseLink');if(d.release_url){a.href=d.release_url;a.style.display='inline'}else{a.style.display='none'}let wrap=el('updateNotesWrap');if(d.notes){el('updateNotes').textContent=d.notes;wrap.style.display='block'}else{wrap.style.display='none'}};
window.checkAppUpdate=async function(force){try{setUpdateMessage('更新情報を確認中...','');let d=await j('/api/update/check'+(force?'?refresh=1':''));renderUpdateInfo(d);return d}catch(e){setUpdateMessage('更新確認に失敗: '+e.message,'bad');throw e}};
window.pollAppUpdate=async function(){if(window.qnapUpdatePolling)return;window.qnapUpdatePolling=true;let started=Date.now();let loop=async()=>{if(Date.now()-started>15*60*1000){window.qnapUpdatePolling=false;setUpdateMessage('更新状態の確認がタイムアウトしました。update.logを確認してください。','bad');return}try{let d=await j('/api/update/status');let label=d.message||d.status||'更新中';if(d.status==='complete'){setUpdateMessage(label+'。画面を再読み込みします。','upd-good');window.qnapUpdatePolling=false;setTimeout(()=>location.reload(),1200);return}if(d.status==='failed'){setUpdateMessage('更新失敗: '+label,'bad');window.qnapUpdatePolling=false;el('updateInstall').disabled=false;return}setUpdateMessage(label+'...','upd-busy')}catch(e){setUpdateMessage('QnapAssistantを再起動中...','upd-busy')}setTimeout(loop,1800)};loop()};
window.installAppUpdate=async function(){let d=window.qnapUpdateInfo||await checkAppUpdate(true);if(!d.available){alert('利用可能な更新はありません。');return}if(!confirm('QnapAssistant v'+d.latest_version+' へ更新します。\n更新中はWeb画面が一時的に切断されます。'))return;let b=el('updateInstall');b.disabled=true;setUpdateMessage('更新処理を開始しています...','upd-busy');try{await j('/api/update/apply',{method:'POST',headers:{'Content-Type':'application/json','X-Qnap-Update-Confirm':'install'},body:'{}'});pollAppUpdate()}catch(e){setUpdateMessage('更新開始に失敗: '+e.message,'bad');b.disabled=false}};
setTimeout(()=>checkAppUpdate(false).catch(()=>{}),700);
</script>`

func injectUpdateUI(html string) string {
	if strings.Contains(html, `id="updateCard"`) {
		return html
	}
	html = strings.Replace(html, updateCardAnchor, updateCardAnchor+"\n"+updateCardHTML, 1)
	return strings.Replace(html, "</body>", updateEnhancementScript+"</body>", 1)
}
