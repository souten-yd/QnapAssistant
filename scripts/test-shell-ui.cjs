// Syntax check of the dashboard/tab shell script embedded in admin/ui_shell.go.
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const src = fs.readFileSync(path.join(__dirname, '../admin/ui_shell.go'), 'utf8');
const m = src.match(/const shellScript = `<script>([\s\S]*?)<\/script>`/);
if (!m) throw new Error('shellScript not found');
new vm.Script(m[1].replace('` + "__CARD_MAP__" + `', '[]'));
console.log('PASS: shell UI script parses');
