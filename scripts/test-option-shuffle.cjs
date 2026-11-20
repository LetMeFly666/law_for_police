// No dependencies required. Run from the repository root: node scripts/test-option-shuffle.cjs
const fs = require('node:fs');
const vm = require('node:vm');
const appPath = 'internal/web/app.js';
const htmlPath = 'internal/web/index.html';
const source = fs.readFileSync(appPath, 'utf8');
const html = fs.readFileSync(htmlPath, 'utf8');
const bankMatch = html.match(/const BANK = (\[.*?\]);<\/script>/s);
if (!bankMatch) throw new Error('Cannot locate BANK in index.html');
const bank = JSON.parse(bankMatch[1]);

function extract(startMarker, endMarker) {
  const start = source.indexOf(startMarker);
  const end = source.indexOf(endMarker, start);
  if (start < 0 || end < 0) throw new Error(`Cannot extract ${startMarker}`);
  return source.slice(start, end);
}

const context = {
  state: { settings: { shuffleOptions: true } },
  optionOrderCache: new Map(),
  esc: value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' }[c])),
  highlightText: value => String(value ?? ''),
  Set,
  Map,
};
vm.createContext(context);
vm.runInContext(
  extract('function questionOptions(q){', 'function renderAll()') +
  extract('function renderAnswer(q){', 'function renderSelfCheck(q)'),
  context,
);
vm.runInContext(
  extract('function submitObjective(){', '\n') +
  extract('function historyReleases(', 'async function checkForAppUpdates()') +
  'globalThis.testAPI = { optionPresentation, displayAnswer, submitObjective, historyReleases, updateReleaseHTML };',
  context,
);
const api = context.testAPI;

// The concrete example from the request: A=fairness, B=voluntary, C=anger.
const demo = {
  id: 'demo-multi', type: '多选', answer: 'AB',
  options: [{ key: 'A', text: '公平' }, { key: 'B', text: '自愿' }, { key: 'C', text: '愤怒' }],
  explanation: 'A：公平；B：自愿；C：愤怒。正确选项为AB。',
};
context.optionOrderCache.set(demo.id, ['A', 'C', 'B']);
const demoView = api.optionPresentation(demo);
assert(demoView.options.map(x => `${x.key}${x.text}`).join(' ') === 'A公平 B愤怒 C自愿', 'demo option order');
assert(demoView.answer === 'AC', 'demo original AB must become displayed AC');
assert(demoView.explanation === 'A：公平；C：自愿；B：愤怒。正确选项为AC。', 'demo explanation mapping');
assert(api.displayAnswer(demo, demoView) === 'A．公平\nC．自愿', 'demo displayed answer text');

// Validate every single- and multiple-choice question against the embedded bank.
function allPermutations(values) {
  if (values.length <= 1) return [values.slice()];
  const out = [];
  values.forEach((value, index) => {
    const rest = values.slice(0, index).concat(values.slice(index + 1));
    for (const tail of allPermutations(rest)) out.push([value, ...tail]);
  });
  return out;
}
const objective = bank.filter(q => ['单选', '多选'].includes(q.type));
let checked = 0;
for (const q of objective) {
  const originalKeys = q.options.map(option => option.key);
  if (originalKeys.length < 2) continue;
  // Test every possible permutation (up to 4! for this question bank).
  for (const order of allPermutations(originalKeys)) {
    context.optionOrderCache.set(q.id, order);
    const view = api.optionPresentation(q);
    assert(view.options.map(option => option.key).join('') === originalKeys.join(''), `${q.id}: visible labels should be reassigned in normal order`);
    assert(new Set(view.options.map(option => option.originalKey)).size === originalKeys.length, `${q.id}: source-key mapping must be one-to-one`);
    const expectedVisible = q.answer.split('').map(key => view.keyMap[key]).sort();
    const visibleCorrectByContent = view.options.filter(option => q.answer.includes(option.originalKey)).map(option => option.key).sort();
    assert(expectedVisible.join('') === visibleCorrectByContent.join(''), `${q.id}: converted answer must match original correct option contents`);
    const answerText = api.displayAnswer(q, view);
    for (const visibleKey of expectedVisible) {
      const option = view.options.find(item => item.key === visibleKey);
      assert(answerText.includes(`${visibleKey}．${option.text}`), `${q.id}: answer text for ${visibleKey} must use the matching displayed option`);
    }
    checked++;
  }
}

// Regression case 14: original ABD must track the new labels in both answer and prose.
const q14 = bank.find(q => q.id === 'multi-14');
context.optionOrderCache.set(q14.id, q14.options.map(option => option.key).reverse());
const q14View = api.optionPresentation(q14);
assert(q14View.answer === 'ACD', `multi-14 expected displayed answer ACD, got ${q14View.answer}`);
assert(q14View.explanation.includes('正确选项为 ACD'), 'multi-14 compact ABD answer reference must be remapped');
assert(q14View.explanation.includes('D：在地铁站内'), 'multi-14 A explanation must become D');
assert(q14View.explanation.includes('C：在网络论坛'), 'multi-14 B explanation must become C');
assert(q14View.explanation.includes('A：在医院候诊区'), 'multi-14 D explanation must become A');
assert(q14View.memory.startsWith('ACD：'), 'multi-14 memory answer code must be remapped too');

// Ensure actual submission logic marks the converted answer as correct and rejects a wrong set.
context.currentQ = () => q14;
context.applySelfTest = (_q, correct) => { context.recordedCorrect = correct; };
context.answerVisible = false;
context.lastResult = null;
context.selected = new Set(q14View.answer.split(''));
api.submitObjective();
assert(context.lastResult === true && context.recordedCorrect === true, 'submitting all remapped correct labels should pass');
context.answerVisible = false;
context.lastResult = null;
context.selected = new Set(['A', 'B', 'D']);
api.submitObjective();
assert(context.lastResult === false && context.recordedCorrect === false, 'submitting stale original labels should not accidentally pass');

// Rendered reference answer and explanation must carry the remapped labels too.
const answerElement = { classList: { remove() {} }, innerHTML: '' };
context.$ = () => answerElement;
context.lastResult = null;
vm.runInContext(extract('function renderAnswer(q){', 'function renderSelfCheck(q)'), context);
context.renderAnswer(q14);
for (const key of q14View.answer) {
  const option = q14View.options.find(item => item.key === key);
  assert(answerElement.innerHTML.includes(`${key}．${option.text}`), `rendered answer missing ${key} option content`);
}
assert(answerElement.innerHTML.includes('正确选项为 ACD'), 'rendered explanation should use remapped answer code');
const memoryElement = { innerHTML: '' };
context.$ = () => memoryElement;
context.answerVisible = true;
const memoryStart = source.indexOf('function renderMemory(q){');
const memoryEnd = source.indexOf('\n', memoryStart);
vm.runInContext(source.slice(memoryStart, memoryEnd), context);
context.renderMemory(q14);
assert(memoryElement.innerHTML.includes('ACD：') && memoryElement.innerHTML.includes('D堵塞'), 'rendered memory hook should use remapped option references');

// History contains only older versions, never versions already shown in this popup.
const history = api.historyReleases(
  [{ version: '1.0.0' }, { version: '1.0.1-beta' }, { version: '1.0.2' }],
  new Set(['1.0.1-beta', '1.0.2']),
);
assert(history.length === 1 && history[0].version === '1.0.0', 'history must exclude both versions displayed in the popup');
const updateHTML = api.updateReleaseHTML({ version: '1.0.1-beta', highlights: ['更新弹窗', '多版本测试'], items: ['测试更新弹窗'] });
assert(updateHTML.includes('update-focus') && updateHTML.includes('更新弹窗') && updateHTML.includes('多版本测试'), 'update focus words must render prominently');

function assert(condition, message) {
  if (!condition) throw new Error(`FAIL: ${message}`);
}
console.log(`PASS: ${checked} option permutations across ${objective.length} objective questions, answer judging, answer/explanation/memory remapping, and update-history de-duplication.`);
console.log('Example: A=公平, B=自愿, C=愤怒 -> A=公平, B=愤怒, C=自愿; original AB -> displayed AC.');
console.log('Question 14: original ABD -> displayed ACD under the deterministic reverse-order test.');
