// No browser dependencies: exercise the actual inline UI with a small DOM stub.
const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const source = fs.readFileSync(require('node:path').join(__dirname, '../admin/ui.go'), 'utf8');
const script = source.match(/<script>([\s\S]*?)<\/script>/)[1];
new vm.Script(script);
const presets = [...source.matchAll(/<option value="[^"]+" (data-url=[^>]+)>/g)]
  .map(m => Object.fromEntries([...m[1].matchAll(/data-(\w+)="([^"]+)"/g)].map(x => [x[1], x[2]])));
assert.equal(presets.length, 3);
function element() {
  return {value: '', textContent: '', children: [], dataset: {},
    replaceChildren() { this.children = []; this.value = ''; },
    appendChild(o) { this.children.push(o); },
    removeAttribute(k) { delete this[k]; }};
}
const nodes = Object.fromEntries(['models','dlpreset','dlurl','dlname','dlsha256','dlstate','dlprogress','downloadLocalModel','selectLocalModel','modelSelectionState'].map(k => [k, element()]));
let catalog = [{path:'/models/old.gguf', name:'old.gguf', size:100, selected:true}];
let state = {status:'idle', active:false}, calls = [];
const ctx = {document:{createElement:element}, el:id=>nodes[id], refresh:()=>{},
  j:async (url, req) => {
    if (req) { calls.push({url, body:JSON.parse(req.body)}); return {}; }
    if (url === '/api/models') return catalog;
    if (url === '/api/models/download') return state;
    throw Error('unrelated voice/provider endpoint must not be needed');
  }};
vm.createContext(ctx);
const names = ['selectModel','applyLocalModelPreset','downloadModel','refreshLocalModels','modelBytes','refreshLocalDownload'];
vm.runInContext(script.split('\n').filter(line => line.startsWith('let localDownloadSubmitting=') || names.some(n => line.startsWith('function '+n+'(') || line.startsWith('async function '+n+'('))).join('\n'), ctx);
(async () => {
  for (const dataset of presets) {
    nodes.dlpreset.selectedOptions = [{dataset}]; ctx.applyLocalModelPreset();
    assert.equal(nodes.dlname.value, dataset.filename);
    assert.match(dataset.url, /\/resolve\/[a-f0-9]{40}\//);
    assert.match(dataset.sha256, /^[a-f0-9]{64}$/);
    const count = calls.length; await ctx.downloadModel();
    assert.equal(calls.length, count+1);
    assert.equal(calls.at(-1).body.SHA256, dataset.sha256);
  }
  nodes.dlpreset.selectedOptions = [{dataset:{}}]; ctx.applyLocalModelPreset();
  assert.equal(nodes.dlsha256.value, '');
  state = {status:'downloading', active:true, name:'new.gguf', written_bytes:50, total_bytes:100, bytes_per_second:10};
  await ctx.refreshLocalDownload();
  assert.equal(nodes.dlprogress.value, 50); assert.equal(nodes.downloadLocalModel.disabled, true);
  assert.match(nodes.dlstate.textContent, /50.0%/);
  state.total_bytes = -1; await ctx.refreshLocalDownload();
  assert.equal(nodes.dlprogress.value, undefined); assert.ok(!nodes.dlstate.textContent.includes('%'));
  catalog.push({path:'/models/new.gguf', name:'<new>.gguf', size:100});
  state = {status:'complete', active:false, path:'/models/new.gguf', name:'new.gguf', finished_at:'now', written_bytes:100, total_bytes:100};
  const before = calls.length; await ctx.refreshLocalDownload();
  assert.equal(nodes.models.value, '/models/new.gguf'); assert.equal(calls.length, before); // no automatic switch
  assert.equal(nodes.models.children[1].textContent.startsWith('<new>'), true); // text, never HTML
  nodes.models.value = '/models/old.gguf'; await ctx.refreshLocalDownload(); await ctx.refreshLocalModels();
  assert.equal(nodes.models.value, '/models/old.gguf'); // polling preserves pending choice
  nodes.models.value = '/models/new.gguf'; await ctx.selectModel();
  assert.equal(calls.at(-1).url, '/api/models/select'); assert.equal(calls.at(-1).body.path, '/models/new.gguf');
  state = {status:'failed', active:false, error:'SHA-256 mismatch'}; await ctx.refreshLocalDownload();
  assert.match(nodes.dlstate.textContent, /SHA-256 mismatch/); assert.equal(nodes.downloadLocalModel.disabled, false);
  console.log('PASS: presets, progress, unknown total, completion/list refresh, selection persistence, explicit selection, failure/retry display');
})().catch(e => { console.error(e); process.exitCode = 1; });
