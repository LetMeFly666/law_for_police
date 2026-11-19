const STORE_KEY = 'lawStudyBook.v2';
const LEGACY_KEY = 'lawStudyBook.v1';
const TOKEN_KEY = 'lawStudy.deviceToken.v1';
const CURSOR_KEY = 'lawStudy.syncCursor.v1';
const SHADOW_KEY = 'lawStudy.syncShadow.v1';
const RECOVERY_KEY = 'lawStudy.recoveryCode.v1';
const DRAW_TIP_KEY = 'lawStudy.drawTipSeen.v1';
const SECTIONS = ['判断题','填空题','单选题','多选题','简答题','论述题'];
const SECTION_PREFIX = {'判断题':'judge','填空题':'fill','单选题':'single','多选题':'multi','简答题':'short','论述题':'essay'};
const $ = id => document.getElementById(id);
const uid = () => (crypto.randomUUID ? crypto.randomUUID() : 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g,c=>{const r=Math.random()*16|0;return (c==='x'?r:(r&0x3|0x8)).toString(16);}));
const now = () => Date.now();
const nicknameParts = {a:['认真','沉稳','好学','敏锐','自律','踏实','清醒','专注','从容','勤勉','理性','坚韧'],b:['白鹭','松鼠','海獭','山雀','青竹','星河','小鹿','云杉','鲸鱼','石榴','远山','月桂']};
function randomNickname(){return nicknameParts.a[Math.floor(Math.random()*nicknameParts.a.length)]+nicknameParts.b[Math.floor(Math.random()*nicknameParts.b.length)]+String(Math.floor(Math.random()*90+10));}
function freshState(){const t=now();return {version:2,profile:{nickname:randomNickname(),email:'',updatedAt:t},settings:{threshold:3,updatedAt:t},notes:{},favorites:{},mastery:{},tests:[],testClearAt:0};}
function mapLegacyId(id){if(!id)return id;const m=String(id).match(/^(判断题|填空题|单选题|多选题|简答题|论述题)-(\d+)$/);return m?`${SECTION_PREFIX[m[1]]}-${m[2]}`:id;}
function loadState(){
  try{const saved=localStorage.getItem(STORE_KEY);if(saved){const s=JSON.parse(saved);return {...freshState(),...s,profile:{...freshState().profile,...(s.profile||{})},settings:{threshold:3,...(s.settings||{})},notes:s.notes||{},favorites:s.favorites||{},mastery:s.mastery||{},tests:Array.isArray(s.tests)?s.tests:[]};}}
  catch(e){console.warn('读取本地学习记录失败',e);}
  const s=freshState();
  try{const raw=localStorage.getItem(LEGACY_KEY);if(raw){const old=JSON.parse(raw);for(const [k,v] of Object.entries(old.notes||{}))s.notes[mapLegacyId(k)]={...v,updatedAt:v.updatedAt||now()};for(const [k,v] of Object.entries(old.records||{})){const id=mapLegacyId(k);s.mastery[id]={status:v.status||'learning',streak:v.status==='mastered'?3:0,attempts:0,correct:0,updatedAt:v.updatedAt||now()};}for(const id of old.stars||[])s.favorites[mapLegacyId(id)]={saved:true,updatedAt:now()};s.tests=(old.tests||[]).map(t=>({...t,qid:mapLegacyId(t.qid),id:t.id||uid()}));}}
  catch(e){console.warn('旧版数据迁移失败',e);}
  return s;
}
let state=loadState();
let section=state.settings.section||'判断题',index=Number(state.settings.index)||0,mode=state.settings.mode||'quiz',filter=state.settings.filter||'all',searchText=state.settings.searchText||'',selected=new Set(),answerVisible=false,lastResult=null,showAnnotations=!!state.settings.showAnnotations,noteEditQID='';
let cloud={token:localStorage.getItem(TOKEN_KEY)||'',cursor:Number(localStorage.getItem(CURSOR_KEY)||0),shadow:readShadow(),ready:false,checked:false,busy:false,pending:false,timer:0,settingsChanged:false};
let drawState={qid:'',strokes:[],active:null,tool:'pen',color:'#e53935',redoStack:[],lastSave:0,paperHeight:0};
let noteTimer=0, shareItems=[];
function readShadow(){try{return JSON.parse(localStorage.getItem(SHADOW_KEY)||'{}')}catch{return {}}}
function persistShadow(){localStorage.setItem(SHADOW_KEY,JSON.stringify(cloud.shadow));}
function saveState(schedule=true){state.settings={...state.settings,section,index,mode,filter,searchText,jumpOpen:!!$('jumpHolder')?.classList.contains('open'),showAnnotations,logoFullscreen:!!state.settings.logoFullscreen,updatedAt:now()};try{localStorage.setItem(STORE_KEY,JSON.stringify(state));}catch(e){toast('本地存储空间不足，部分记录可能无法保存');}renderStats();if(schedule)scheduleSync();}
function esc(v){return String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));}
function escapeRegExp(s){return s.replace(/[.*+?^${}()|[\]\\]/g,'\\$&');}
function highlightText(text, h){let out=esc(text);if(!h)return out;const entries=[];for(const x of (h.red||[]))if(x)entries.push({x,cls:'trap'});for(const x of (h.green||[]))if(x)entries.push({x,cls:'correct-word'});entries.sort((a,b)=>b.x.length-a.x.length);for(const e of entries){const safe=esc(e.x);out=out.replace(new RegExp(escapeRegExp(safe),'g'),m=>`<mark class="${e.cls}">${m}</mark>`);}return out;}
function toast(msg){const el=$('toast');el.textContent=msg;el.classList.add('show');clearTimeout(toast.timer);toast.timer=setTimeout(()=>el.classList.remove('show'),2600);}
function openModal(id){$(id).classList.add('show');}
function closeModal(id){$(id).classList.remove('show');}
function currentSectionItems(){return BANK.filter(q=>q.section===section);}
function isFavorite(id){return !!state.favorites[id]?.saved;}
function masteryOf(id){return state.mastery[id]||{status:'untested',streak:0,attempts:0,correct:0};}
function baseList(){let a=searchText.trim()?BANK.slice():BANK.filter(q=>q.section===section);if(searchText.trim()){const s=searchText.trim().toLowerCase();a=a.filter(q=>(q.question+' '+q.answer+' '+q.explanation+' '+(q.memory||'')+' '+(q.law||'')).toLowerCase().includes(s));}if(filter==='favorites')a=a.filter(q=>isFavorite(q.id));if(filter==='unmastered')a=a.filter(q=>masteryOf(q.id).status!=='mastered');if(filter==='mastered')a=a.filter(q=>masteryOf(q.id).status==='mastered');if(filter==='untested')a=a.filter(q=>masteryOf(q.id).attempts===0);return a;}
function list(){return baseList();}
function currentQ(){const a=list();if(!a.length)return null;index=Math.max(0,Math.min(index,a.length-1));return a[index];}
function displayAnswer(q){if(['判断'].includes(q.type))return q.answer==='√'?'正确（√）':'错误（×）';if(q.type==='单选'){const o=(q.options||[]).find(x=>x.key===q.answer);return o?`${q.answer}．${o.text}`:q.answer;}if(q.type==='多选')return q.answer.split('').map(k=>{const o=(q.options||[]).find(x=>x.key===k);return o?`${k}．${o.text}`:k;}).join('\n');return q.answer;}
function questionOptions(q){if(q.type==='判断')return [{key:'√',text:'正确'},{key:'×',text:'错误'}];return q.options||[];}
function renderAll(){$('studyMode').classList.toggle('active',mode==='study');$('quizMode').classList.toggle('active',mode==='quiz');$('modeDescription').textContent=mode==='study'?'背题模式：答案、解析和题友心得默认展开。':'做题模式：选择选项后提交，系统自动判分并记录。';$('listFilter').value=filter;$('searchInput').value=searchText;$('statusFilters').querySelectorAll('.chip').forEach(x=>x.classList.toggle('active',x.dataset.status===filter));$('jumpHolder').classList.toggle('open',!!state.settings.jumpOpen);$('jumpToggle').innerHTML=state.settings.jumpOpen?'收起题号导航 <span>⌃</span>':'展开题号导航 <span>⌄</span>';renderSections();renderJump();renderSearchResults();renderQuestion();renderStats();}
function renderSections(){const el=$('sectionTabs');el.innerHTML='';for(const s of SECTIONS){const n=BANK.filter(q=>q.section===s).length;const b=document.createElement('button');b.className='section-tab'+(s===section?' active':'');b.innerHTML=`<span>${s}</span><span class="section-count">${n}题</span>`;b.onclick=()=>{section=s;index=0;answerVisible=mode==='study';selected.clear();saveState();renderAll();};el.appendChild(b);}}
function renderJump(){const el=$('jumpGrid');el.innerHTML='';const a=list();$('visibleCount').textContent=`${a.length} / ${BANK.filter(q=>q.section===section).length}`;a.forEach((q,i)=>{const b=document.createElement('button');const m=masteryOf(q.id);b.className='jump'+(q===currentQ()?' current':'')+(m.status==='mastered'?' mastered':m.status==='weak'?' weak':m.status==='learning'?' learning':'');b.textContent=searchText.trim()?`${q.section.replace('题','').slice(0,2)}${q.no}`:q.no;b.title=`${q.section} · 第${q.no}题：${q.question}`;b.onclick=()=>{index=i;answerVisible=mode==='study';selected.clear();saveState();renderQuestion();renderJump();scrollToQuestion();};el.appendChild(b);});}
function renderSearchResults(){const box=$('searchResults');if(!box)return;const term=searchText.trim().toLowerCase();if(!term){box.classList.add('hidden');box.innerHTML='';return;}const matches=baseList().slice(0,40);box.classList.remove('hidden');box.innerHTML=matches.length?matches.map(q=>`<button class="search-result" data-search-q="${esc(q.id)}"><strong>${esc(q.section)} · 第${q.no}题</strong>${esc(q.question.slice(0,120))}</button>`).join(''):'<div class="empty">没有匹配的题目</div>';}
function renderStats(){const tested=new Set(state.tests.map(t=>t.qid)).size;const mastered=Object.values(state.mastery).filter(x=>x.status==='mastered').length;const notes=Object.values(state.notes).filter(x=>x&&((x.text&&x.text.trim())||(Array.isArray(x.strokes)&&x.strokes.some(stroke=>!stroke.deleted)))).length;$('statSeen').textContent=tested;$('statMastered').textContent=mastered;$('statNotes').textContent=notes;$('statTests').textContent=state.tests.length;$('favCount').textContent=Object.values(state.favorites).filter(x=>x.saved).length;const pct=Math.round(mastered/BANK.length*100);$('progressPct').textContent=pct+'%';$('progressFill').style.width=pct+'%';$('progressLabel').textContent=`已掌握 ${mastered} / ${BANK.length} 题`;}
let lastRenderedQuestionID='';
function renderQuestion(){
  const q=currentQ();if(q&&q.id!==lastRenderedQuestionID){sharePage=0;lastRenderedQuestionID=q.id;}selected.clear();lastResult=null;if(mode==='study')answerVisible=true;
  if(!q){$('qTags').innerHTML='';$('qTitle').textContent='当前筛选下没有题目';$('qHelp').textContent='更换筛选条件，或者切换题型。';$('textNotePreview')?.classList.add('hidden');$('textNotePreview')&&($('textNotePreview').innerHTML='');$('optionsArea').innerHTML='';$('questionActions').innerHTML='';$('sharePanel').classList.add('hidden');$('answerArea').classList.add('hidden');$('selfCheckArea').innerHTML='';$('questionMemory').innerHTML='';$('crumb').textContent=section+' · 暂无题目';$('qNo').textContent='';$('bottomTitle').textContent='暂无题目';$('bottomSub').textContent='请调整筛选条件';$('prevBtn').disabled=true;$('nextBtn').disabled=true;$('favoriteToggle').textContent='☆ 收藏';$('annotationPreview').classList.add('hidden');$('annotationPreview').innerHTML='';return;}
  const items=list();const m=masteryOf(q.id);const statusLabel=m.status==='mastered'?'已掌握':m.status==='learning'?'学习中':m.status==='weak'?'需复习':'未自测';
  $('crumb').textContent=`${q.section} · 第 ${q.no} / ${BANK.filter(x=>x.section===section).length} 题`;
  $('qNo').textContent=`全卷 ${BANK.findIndex(x=>x.id===q.id)+1} / ${BANK.length}`;
  $('qTags').innerHTML=`<span class="tag blue">${q.type}</span><span class="tag">${mode==='study'?'背题模式':'做题模式'}</span><span class="tag ${m.status==='mastered'?'green':m.status==='weak'?'red':m.status==='learning'?'amber':''}">${statusLabel}</span>`;
  $('qTitle').innerHTML=highlightText(q.question,(mode==='study'||answerVisible)?q.highlights:null);
  renderTextNotePreview(q);
  if(['填空','简答','论述'].includes(q.type))$('qHelp').textContent='';
  else if(mode==='study')$('qHelp').textContent=q.type==='判断'?'先判断题干对错，重点留意绝对化措辞和法律关键词。':'先在心里作答，再查看答案和解析。';
  else $('qHelp').textContent='选择你认为正确的选项后提交；系统会自动判定并记录连续答对次数。';
  const opts=mode==='study'&&q.type==='判断'?[]:questionOptions(q);let optsHtml='';if(opts.length){const interactive=mode==='quiz';optsHtml=`<div class="options">${opts.map(o=>`<button class="option" data-opt="${esc(o.key)}" ${interactive?'':'disabled'}><span class="option-key">${esc(o.key)}</span><span class="option-text">${esc(o.text)}</span></button>`).join('')}</div>`;}else optsHtml='';
  $('optionsArea').innerHTML=optsHtml;
  let action='';if(mode==='quiz'&&opts.length){action=`<button class="btn primary" id="submitAnswer" disabled>提交答案并核对</button><span class="small-muted">多选题需要选择全部正确项</span>`;}else if(!answerVisible){action=`<button class="btn primary" id="revealAnswer">查看参考答案与解析</button>`;}
  $('questionActions').innerHTML=action;
  $('answerArea').classList.toggle('hidden',!answerVisible);if(answerVisible)renderAnswer(q);
  renderSelfCheck(q);renderMemory(q);
  $('bottomTitle').textContent=`${q.section} · ${q.no}/${BANK.filter(x=>x.section===section).length}`;$('bottomSub').textContent=filter==='all'?'共 110 题':`当前筛选 ${items.length} 题`;
  $('prevBtn').disabled=index===0&&(!!searchText.trim()||filter!=='all'||section===SECTIONS[0]);$('nextBtn').disabled=items.length===0;$('nextBtn').textContent=(index>=items.length-1&&section==='论述题'&&filter==='all'&&!searchText.trim())?'已到最后一题':'下一题 →';
  $('favoriteToggle').textContent=isFavorite(q.id)?'★ 已收藏':'☆ 收藏';
  $('sharePanel').classList.toggle('hidden',!(mode==='study'||answerVisible));$('shareNickname').textContent=`发布者：${state.profile.nickname}`;if(mode==='study'||answerVisible)loadPublicShares();
  renderAnnotationPreview(q);
}
function renderTextNotePreview(q){
  const box=$('textNotePreview');if(!box)return;
  const text=String(state.notes[q.id]?.text||'').trim();
  if(mode!=='study'||!text){box.classList.add('hidden');box.innerHTML='';return;}
  box.innerHTML=`<div class="text-note-head"><strong>✎ 我的文字笔记</strong><button class="btn" id="editInlineNote" type="button">编辑</button></div><div class="text-note-content">${esc(text)}</div>`;
  box.classList.remove('hidden');
}
function renderAnswer(q){const box=$('answerArea');box.classList.remove('hidden');const h=q.highlights||{};let result='';if(lastResult!==null)result=`<div class="answer-main" style="color:${lastResult?'var(--green)':'var(--red)'}">${lastResult?'✓ 本次作答正确':'✕ 本次作答不正确'}</div>`;let html=`<div class="answer-head"><span>参考答案与解析</span><span class="tag green">标准答案</span></div><div class="answer-content">${result}<div class="answer-main">答案：${highlightText(displayAnswer(q),h)}</div><div class="section-label">解析</div><div>${highlightText(q.explanation,h)}</div>`;if(q.law)html+=`<div class="section-label">法律依据</div><div>${esc(q.law)}</div>`;if(q.caveat)html+=`<div class="section-label">特别提醒</div><div>${highlightText(q.caveat,h)}</div>`;if(h.red?.length||h.green?.length)html+=`<div class="section-label">本题关键词</div><div class="keypoints">${(h.red||[]).map(x=>`<span class="keypoint">易错：${esc(x)}</span>`).join('')}${(h.green||[]).map(x=>`<span class="keypoint good">正确依据：${esc(x)}</span>`).join('')}</div>`;html+='</div>';box.innerHTML=html;}
function renderSelfCheck(q){const el=$('selfCheckArea');if(!answerVisible){el.innerHTML='';return;}const m=masteryOf(q.id);const count=m.streak||0;const threshold=Number(state.settings.threshold||3);if(m.status==='mastered'){el.innerHTML=`<div class="self-check"><strong>✓ 已掌握</strong><p>已达到掌握标准，无需重复标记。</p></div>`;return;}if(mode==='quiz'&&['判断','单选','多选'].includes(q.type)&&lastResult!==null){el.innerHTML=`<div class="self-check"><strong>本次客观题自测已自动记录</strong><p>连续答对 ${count}/${threshold} 次${m.status==='mastered'?'，已达到掌握标准。':''}。答错会将连续正确次数重置。</p><div class="self-buttons"><button class="btn soft" id="selfMastered">标记掌握</button></div><div class="record-info">可以重新作答，也可以直接标记为已掌握。</div></div>`;return;}el.innerHTML=`<div class="self-check"><strong>自测：你真正掌握了吗？</strong><p>${['填空','简答','论述'].includes(q.type)?'对照参考答案，按关键词和逻辑判断，不要求逐字一致。':'背题模式下请诚实自评；做题模式下客观题已经自动记录。'} 连续答对 ${count}/${threshold} 次${m.status==='mastered'?'，已达到掌握标准。':''}</p><div class="self-buttons"><button class="btn know" id="selfKnow">✓ 答对一次</button><button class="btn notknow" id="selfNotKnow">↻ 还不熟悉</button><button class="btn soft" id="selfMastered">标记掌握</button></div><div class="record-info">连续答对达到设置次数后自动标记为已掌握；答错会重置连续次数。</div></div>`;}
function renderMemory(q){$('questionMemory').innerHTML=answerVisible&&q.memory?`<div class="memory-box"><div class="memory-title">助记 / 记忆钩子</div><div class="memory-text">${esc(q.memory)}</div></div>`:'';}
function applySelfTest(q,correct,source){const old=masteryOf(q.id);if(old.status==='mastered'){renderSelfCheck(q);return;}const t=now();const threshold=Number(state.settings.threshold||3);const streak=correct?(old.streak||0)+1:0;const attempts=(old.attempts||0)+1;const status=source==='manual-master'?'mastered':streak>=threshold?'mastered':correct?'learning':'weak';state.mastery[q.id]={status,streak,attempts,correct:(old.correct||0)+(correct?1:0),lastResult:correct,updatedAt:t};state.tests.push({id:uid(),qid:q.id,section:q.section,no:q.no,correct,streak,status,source,at:t});saveState();renderJump();renderQuestionAfterTest(q,correct);toast(correct?(status==='mastered'?'已达到掌握标准':'答对，连续正确次数已记录'):'已记录为需复习');}
function renderQuestionAfterTest(q,correct){answerVisible=true;lastResult=correct;renderAnswer(q);renderSelfCheck(q);renderMemory(q);const m=masteryOf(q.id);$('qTags').querySelectorAll('.tag')[2].textContent=m.status==='mastered'?'已掌握':m.status==='weak'?'需复习':'学习中';if(mode==='quiz'&&questionOptions(q).length){const keys=q.type==='多选'?q.answer.split(''):[q.answer];document.querySelectorAll('[data-opt]').forEach(b=>{const key=b.dataset.opt;b.disabled=true;if(keys.includes(key))b.classList.add('correct');else if(selected.has(key))b.classList.add('wrong');});$('questionActions').innerHTML='<button class="btn" id="retryAnswer">重新作答</button><span class="small-muted">本次结果已记录</span>';}}
function submitObjective(){const q=currentQ();if(!q||!selected.size)return;const expected=q.type==='多选'?q.answer.split(''): [q.answer];const got=[...selected];const ok=expected.length===got.length&&expected.every(k=>got.includes(k));answerVisible=true;lastResult=ok;applySelfTest(q,ok,'quiz');}
function markFavorite(){const q=currentQ();if(!q)return;const t=now();const saved=!isFavorite(q.id);state.favorites[q.id]={saved,updatedAt:t};saveState();$('favoriteToggle').textContent=saved?'★ 已收藏':'☆ 收藏';renderJump();toast(saved?'已加入收藏夹':'已从收藏夹移除');}
function saveNote(){const qid=noteEditQID;if(!qid)return;const old=state.notes[qid]||{strokes:[]};const t=now();state.notes[qid]={...old,text:$('noteText').value,textUpdatedAt:t,updatedAt:Math.max(t,old.updatedAt||0)};$('noteSaved').textContent='已保存到本机，正在同步云端…';saveState();const q=currentQ();if(q&&q.id===qid)renderTextNotePreview(q);}
function openNote(){const q=currentQ();if(!q)return;noteEditQID=q.id;const note=state.notes[q.id]||{};$('noteQuestion').textContent=`${q.section}第${q.no}题：${q.question}`;$('noteText').value=note.text||'';$('noteSaved').textContent='停止输入后自动保存';openModal('noteModal');setTimeout(()=>$('noteText').focus(),100);}
function scrollToQuestion(){const el=$('questionCard');const top=el.getBoundingClientRect().top+window.scrollY-12;window.scrollTo({top,behavior:'smooth'});}
function navigate(delta){const a=list();if(!a.length)return;let next=index+delta;if(next<0||next>=a.length){if(searchText.trim()||filter!=='all'){toast(delta>0?'当前筛选列表已完成':'已经是当前列表的第一题');return;}const si=SECTIONS.indexOf(section)+(delta>0?1:-1);if(si<0){toast('已经是第一道题');return;}if(si>=SECTIONS.length){toast(section==='论述题'?'本题库全部完成':'已经是最后一类题目');return;}section=SECTIONS[si];const nextList=list();index=delta>0?0:Math.max(0,nextList.length-1);answerVisible=mode==='study';selected.clear();saveState();renderAll();return;}const y=window.scrollY;index=next;answerVisible=mode==='study';selected.clear();saveState();renderQuestion();renderJump();renderSearchResults();requestAnimationFrame(()=>window.scrollTo({top:y,behavior:'instant'}));}
function setQuestionById(id){const q=BANK.find(x=>x.id===id);if(!q)return;section=q.section;filter='all';$('listFilter').value='all';$('statusFilters').querySelectorAll('.chip').forEach(x=>x.classList.toggle('active',x.dataset.status==='all'));index=list().findIndex(x=>x.id===id);answerVisible=mode==='study';saveState();renderAll();closeModal('favoritesModal');closeModal('mySharesModal');scrollToQuestion();}
function setFilter(f){filter=f;index=0;saveState();$('listFilter').value=f;$('statusFilters').querySelectorAll('.chip').forEach(x=>x.classList.toggle('active',x.dataset.status===f));index=0;answerVisible=false;renderJump();renderQuestion();}
function renderFavorites(){const items=BANK.filter(q=>isFavorite(q.id));const el=$('favoritesList');if(!items.length){el.innerHTML='<div class="empty">收藏夹还是空的。点题目上方的“收藏”，这里就能集中复习。</div>';return;}el.innerHTML=items.map(q=>`<div class="list-row"><div style="font-size:12px;font-weight:850">${esc(q.section)} · 第${q.no}题</div><div style="font-size:13px;margin:4px 0 8px">${esc(q.question)}</div><button class="btn soft" data-open-q="${q.id}">打开这道题</button> <button class="btn" data-unfav="${q.id}">取消收藏</button></div>`).join('');}
function renderHistory(){const list=[...state.tests].sort((a,b)=>b.at-a.at);$('historySummary').textContent=`共 ${list.length} 次自测，涉及 ${new Set(list.map(x=>x.qid)).size} 道题。`;const el=$('historyList');if(!list.length){el.innerHTML='<div class="empty">还没有自测记录。完成自评或做题后，会自动记录在这里。</div>';return;}el.innerHTML=list.slice(0,250).map(t=>{const q=BANK.find(x=>x.id===t.qid);return `<div class="history-row"><div style="display:flex;justify-content:space-between;gap:8px"><strong style="font-size:12px">${esc(q?`${q.section}第${q.no}题`:'题目')}</strong><span class="tag ${t.correct?'green':'red'}">${t.correct?'答对':'需复习'}</span></div><div style="font-size:12px;margin-top:4px">${esc(q?.question||'')}</div><div class="small-muted">${new Date(t.at).toLocaleString()} · ${t.source==='quiz'?'做题模式':'背题自评'} · 连续正确 ${t.streak||0} 次</div></div>`;}).join('');}
function renderShareList(items,container,own=false){const empty=own?null:$('shareEmpty');if(!items.length){container.innerHTML=own?'<div class="empty">你还没有发布过心得。</div>':'';if(empty&&!own)empty.classList.remove('hidden');return;}if(empty)empty.classList.add('hidden');container.innerHTML=items.map(x=>`<div class="share-item" data-share-id="${esc(x.id)}"><div class="share-meta"><strong>${esc(x.nickname||'匿名同学')}${own?' · 我分享的':''}</strong><span>${new Date(x.created_at||Date.now()).toLocaleString()}</span></div><div class="share-content">${esc(x.content||'')}</div><div class="share-actions"><button class="btn" data-like="${esc(x.id)}">${x.liked_by_me?'取消点赞':'点赞'} · ${Number(x.likes_count)||0}</button>${own?`<button class="btn" data-edit-share="${esc(x.id)}">编辑</button><button class="btn" data-delete-share="${esc(x.id)}">删除</button><button class="btn soft" data-open-q="${esc(x.question_id)}">查看对应题目</button>`:''}<button class="btn" data-like-users="${esc(x.id)}">查看点赞用户</button></div><div class="share-like-users hidden" id="likeUsers-${esc(x.id)}"></div></div>`).join('');}
async function api(path,options={}){const headers={...(options.headers||{})};if(cloud.token)headers.Authorization='Bearer '+cloud.token;if(options.body!==undefined)headers['Content-Type']='application/json';const res=await fetch(path,{...options,headers,body:options.body===undefined?undefined:JSON.stringify(options.body)});let data={};try{data=await res.json();}catch{}if(!res.ok){const err=new Error(data.detail||`请求失败（${res.status}）`);err.status=res.status;throw err;}return data;}
function setCloudStatus(status,text){$('cloudDot').className='status-dot '+(status==='online'?'online':status==='offline'?'offline':'');$('cloudStatus').textContent=text;$('syncModalDot').className='status-dot '+(status==='online'?'online':status==='offline'?'offline':'');$('syncModalStatus').textContent=text;}
function applyCurrentRecoveryCode(code,notify=false){
  if(typeof code!=='string'||!code)return false;
  const old=localStorage.getItem(RECOVERY_KEY)||'';
  if(old===code)return false;
  localStorage.setItem(RECOVERY_KEY,code);
  if($('deviceCode'))$('deviceCode').textContent=code;
  if($('syncCodeStatus'))$('syncCodeStatus').textContent='已同步账号当前连接码';
  if(notify&&old)toast('其他设备已轮换连接码，本机已自动更新');
  return true;
}
async function refreshRecoveryCode(notify=false){
  if(!cloud.token||!navigator.onLine)return;
  try{const me=await api('/api/me',{cache:'no-store'});applyCurrentRecoveryCode(me.recovery_code,notify);}
  catch(e){if(e.status===401){cloud.checked=false;}}
}
async function ensureAnonymous(){
  if(cloud.token&&!cloud.checked){
    try{const me=await api('/api/me',{cache:'no-store'});cloud.checked=true;applyCurrentRecoveryCode(me.recovery_code,false);return true;}
    catch(e){if(e.status!==401)throw e;cloud.token='';cloud.checked=false;localStorage.removeItem(TOKEN_KEY);}}
  if(cloud.token&&cloud.checked)return true;
  const previousRecovery=localStorage.getItem(RECOVERY_KEY)||'';
  const data=await api('/api/anonymous',{method:'POST',body:{}});
  cloud.token=data.token;cloud.checked=true;localStorage.setItem(TOKEN_KEY,cloud.token);
  // Store the newly issued code immediately. If joining the previous account
  // fails for a transient reason, this temporary account remains recoverable.
  if(data.recovery_code)localStorage.setItem(RECOVERY_KEY,data.recovery_code);
  if(data.nickname&&!state.profile.nickname){state.profile.nickname=data.nickname;state.profile.updatedAt=now();}
  if(previousRecovery){
    try{
      await api('/api/sync/join',{method:'POST',body:{code:previousRecovery}});
      cloud.cursor=0;cloud.shadow={};localStorage.setItem(CURSOR_KEY,'0');persistShadow();
      localStorage.setItem(RECOVERY_KEY,previousRecovery);
      state.profile={...state.profile,updatedAt:0};state.settings={...state.settings,updatedAt:0};
      toast('已使用本机保存的永久连接码恢复账号');
    }catch(e){
      if(e.status!==401)throw e;
      // The account is silent by default; users can reveal their recovery code in 多设备同步.
    }
  }
  localStorage.setItem(STORE_KEY,JSON.stringify(state));
  return true;
}
function flattenState(){
  const out=[];
  out.push({key:'test-clear',value:{at:state.testClearAt||0},updated_at:state.testClearAt||1});
  out.push({key:'profile',value:state.profile,updated_at:state.profile.updatedAt||1});
  out.push({key:'settings',value:state.settings,updated_at:state.settings.updatedAt||1});
  for(const [id,v] of Object.entries(state.notes)){
    const legacyTimestamps=v.textUpdatedAt===undefined&&v.strokesUpdatedAt===undefined;
    out.push({key:`note-text:${id}`,value:v.text||'',updated_at:v.textUpdatedAt||(legacyTimestamps?v.updatedAt:1)||1});
    out.push({key:`note-strokes:${id}`,value:{strokes:Array.isArray(v.strokes)?v.strokes:[],paperVersion:v.paperVersion||1,paperWidth:v.paperWidth||DRAW_WIDTH,paperHeight:v.paperHeight||0},updated_at:v.strokesUpdatedAt||(legacyTimestamps?v.updatedAt:1)||1});
  }
  for(const [id,v] of Object.entries(state.favorites))out.push({key:`favorite:${id}`,value:v,updated_at:v.updatedAt||1});
  for(const [id,v] of Object.entries(state.mastery))out.push({key:`mastery:${id}`,value:v,updated_at:v.updatedAt||1});
  for(const t of state.tests)out.push({key:`test:${t.id}`,value:t,updated_at:t.at||1});
  return out;
}
function applyRemote(change){
  const {key,value,updated_at}=change;
  cloud.shadow[key]={updated_at,value};
  if(key==='test-clear'){
    state.testClearAt=Math.max(state.testClearAt||0,value?.at||updated_at);
    state.tests=state.tests.filter(t=>(t.at||0)>state.testClearAt);return;
  }
  const mergeNote=(id,field,content,extra={})=>{
    const old=state.notes[id]||{text:'',strokes:[]};
    const stampKey=field==='text'?'textUpdatedAt':'strokesUpdatedAt';
    const hasSplitTimestamps=old.textUpdatedAt!==undefined||old.strokesUpdatedAt!==undefined;
    const oldStamp=old[stampKey]||(hasSplitTimestamps?0:(old.updatedAt||0));
    if(field==='strokes'){
      const merged=mergeStrokeSets(old.strokes,content);
      const metadata=oldStamp<=updated_at?extra:{};
      state.notes[id]={...old,...metadata,strokes:merged,[stampKey]:Math.max(oldStamp,updated_at),updatedAt:Math.max(old.updatedAt||0,updated_at)};
      return true;
    }
    if(oldStamp<=updated_at){
      state.notes[id]={...old,...extra,[field]:content,[stampKey]:updated_at,updatedAt:Math.max(old.updatedAt||0,updated_at)};
      return true;
    }
    return false;
  };
  if(key==='profile'){
    if(!state.profile||(state.profile.updatedAt||0)<=updated_at)state.profile=value;
  }else if(key==='settings'){
    if(!state.settings||(state.settings.updatedAt||0)<=updated_at){state.settings=value||{};section=state.settings.section||section;index=Number(state.settings.index)||0;mode=state.settings.mode||mode;filter=state.settings.filter||filter;searchText=state.settings.searchText||'';showAnnotations=!!state.settings.showAnnotations;answerVisible=mode==='study';selected.clear();cloud.settingsChanged=true;}
  }else if(key.startsWith('note-text:')){
    mergeNote(key.slice(10),'text',String(value??''));
  }else if(key.startsWith('note-strokes:')){
    const payload=Array.isArray(value)?{strokes:value}:value||{};
    mergeNote(key.slice(13),'strokes',Array.isArray(payload.strokes)?payload.strokes:[],{paperVersion:payload.paperVersion||1,paperWidth:payload.paperWidth||DRAW_WIDTH,paperHeight:Number(payload.paperHeight)||0});
  }else if(key.startsWith('note:')){
    // Migrate composite notes from earlier releases without replacing newer
    // text or strokes received from the independent fields above.
    const id=key.slice(5),legacy=value||{};
    if(Object.prototype.hasOwnProperty.call(legacy,'text'))mergeNote(id,'text',String(legacy.text||''));
    if(Array.isArray(legacy.strokes))mergeNote(id,'strokes',legacy.strokes,{paperVersion:legacy.paperVersion||1,paperWidth:legacy.paperWidth||DRAW_WIDTH,paperHeight:legacy.paperHeight||0});
  }else if(key.startsWith('favorite:')){
    const id=key.slice(9),old=state.favorites[id];if(!old||(old.updatedAt||0)<=updated_at)state.favorites[id]=value;
  }else if(key.startsWith('mastery:')){
    const id=key.slice(8),old=state.mastery[id];if(!old||(old.updatedAt||0)<=updated_at)state.mastery[id]=value;
  }else if(key.startsWith('test:')){
    const t=value;if((t.at||0)>(state.testClearAt||0)&&!state.tests.some(x=>x.id===t.id))state.tests.push(t);
  }
}
function stable(v){return JSON.stringify(v);}
function mergeStrokeSets(a=[],b=[]){
  const map=new Map();
  for(const stroke of [...(Array.isArray(a)?a:[]),...(Array.isArray(b)?b:[])]){
    if(!stroke||typeof stroke!=='object')continue;
    const key=stroke.id?String(stroke.id):'legacy:'+stable(stroke);
    const old=map.get(key);
    if(!old)map.set(key,stroke);
    else{const merged={...old,...stroke};if(old.deleted||stroke.deleted){merged.deleted=true;merged.deletedAt=Math.max(old.deletedAt||0,stroke.deletedAt||0);}map.set(key,merged);}
  }
  return [...map.values()].sort((x,y)=>(x.at||0)-(y.at||0));
}
async function syncNow(){if(!navigator.onLine)return;if(cloud.busy){cloud.pending=true;return;}cloud.busy=true;cloud.pending=false;try{await ensureAnonymous();let loops=0;do{const flat=flattenState();const changes=flat.filter(x=>{const old=cloud.shadow[x.key];return !old||old.updated_at!==x.updated_at||stable(old.value)!==stable(x.value);});const res=await api('/api/sync/exchange',{method:'POST',body:{cursor:cloud.cursor,changes}});for(const c of res.changes||[])applyRemote(c);for(const c of res.conflicts||[])applyRemote(c);if(Number.isFinite(res.cursor))cloud.cursor=res.cursor;localStorage.setItem(CURSOR_KEY,String(cloud.cursor));persistShadow();loops++;if(!res.has_more)break;}while(loops<10);localStorage.setItem(STORE_KEY,JSON.stringify(state));if(cloud.settingsChanged&&!drawState.active){cloud.settingsChanged=false;renderAll();}else{renderStats();renderJump();}setCloudStatus('online','已连接学习空间 · 自动同步已开启');if(showAnnotations&&currentQ()&&!drawState.active)renderAnnotationPreview(currentQ());if(currentQ())renderTextNotePreview(currentQ());}catch(e){console.warn('sync failed',e);setCloudStatus('offline','云端暂不可用 · 本地学习记录仍正常保存');}finally{cloud.busy=false;if(cloud.pending){cloud.pending=false;scheduleSync();}}}
function scheduleSync(){if(!cloud.token&& !navigator.onLine)return;clearTimeout(cloud.timer);cloud.timer=setTimeout(syncNow,60);}
async function initializeCloud(){try{const h=await fetch('/api/health',{cache:'no-store'});if(!h.ok)throw new Error('API unavailable');await ensureAnonymous();await syncNow();}catch(e){console.warn('cloud init failed',e);setCloudStatus('offline','云端暂不可用 · 本地记录会暂存，恢复网络后自动上传');}}
function showFirstRecoveryCode(code){if(!$('recoveryModal')||!code)return;$('firstRecoveryCode').textContent=code;openModal('recoveryModal');}
async function createPairCode(){try{await ensureAnonymous();const me=await api('/api/me',{cache:'no-store'});let code=me.recovery_code||'';if(!code){const d=await api('/api/recovery/rotate',{method:'POST',body:{}});code=d.recovery_code;}applyCurrentRecoveryCode(code,false);$('deviceCode').textContent=code;$('syncCodeStatus').textContent='已读取账号当前连接码；其他已登录设备也会自动更新';$('syncCreatePanel').classList.remove('hidden');$('syncJoinPanel').classList.add('hidden');}catch(e){toast('读取永久连接码失败：'+e.message);}}
async function rotateRecoveryCode(){if(!confirm('轮换后旧永久连接码立即失效。其他已登录设备会自动获得新码。确定继续？'))return;try{const d=await api('/api/recovery/rotate',{method:'POST',body:{}});applyCurrentRecoveryCode(d.recovery_code,false);$('deviceCode').textContent=d.recovery_code;$('syncCodeStatus').textContent='已轮换；其他在线设备将在短时间内自动更新';toast('新连接码已生成，其他已登录设备会自动更新');}catch(e){toast('轮换失败：'+e.message);}}
async function joinDevice(){const code=$('deviceCodeInput').value.trim().toUpperCase();if(!code){toast('请粘贴永久设备连接码');return;}try{await ensureAnonymous();await api('/api/sync/join',{method:'POST',body:{code}});localStorage.setItem(RECOVERY_KEY,code);cloud.cursor=0;cloud.shadow={};localStorage.setItem(CURSOR_KEY,'0');persistShadow();state.profile={...state.profile,updatedAt:0};state.settings={...state.settings,updatedAt:0};localStorage.setItem(STORE_KEY,JSON.stringify(state));await syncNow();closeModal('syncModal');toast('账号已恢复，服务器中的学习数据正在加载');}catch(e){toast('恢复失败：'+e.message);}}
async function requestRecoveryEmail(){const email=$('recoveryEmailInput').value.trim();if(!email){toast('请填写此前验证绑定的邮箱');return;}try{await api('/api/recovery/email/request',{method:'POST',body:{email}});toast('如该邮箱已绑定账号，验证码已发送');}catch(e){toast('发送失败：'+e.message);}}
async function verifyRecoveryEmail(){const email=$('recoveryEmailInput').value.trim(),code=$('recoveryEmailCode').value.trim();if(!email||!code){toast('请填写邮箱和验证码');return;}try{const d=await api('/api/recovery/email/verify',{method:'POST',body:{email,code}});localStorage.setItem(RECOVERY_KEY,d.recovery_code);cloud.cursor=0;cloud.shadow={};localStorage.setItem(CURSOR_KEY,'0');persistShadow();state.profile={...state.profile,updatedAt:0};state.settings={...state.settings,updatedAt:0};await syncNow();$('deviceCodeInput').value='';closeModal('syncModal');showFirstRecoveryCode(d.recovery_code);toast('邮箱验证成功，账号已恢复');}catch(e){toast('找回失败：'+e.message);}}
let sharePage=0,sharePageSize=10,myShareCache=[],myShareShowAll=false;
function updateSharePager(total){$('sharePrev').disabled=sharePage<=0;$('shareNext').disabled=(sharePage+1)*sharePageSize>=total;$('sharePageInfo').textContent=total?`第 ${sharePage+1} 页 · 共 ${total} 条`:'暂无心得';}
async function loadPublicShares(){const q=currentQ();if(!q)return;const listEl=$('publicShares');listEl.innerHTML='<div class="empty">正在加载题友心得…</div>';try{const data=await api(`/api/experiences?question_id=${encodeURIComponent(q.id)}&limit=${sharePageSize}&offset=${sharePage*sharePageSize}&sort=likes`);shareItems=data.items||[];renderShareList(shareItems,listEl,false);updateSharePager(data.total||0);}catch(e){listEl.innerHTML='<div class="empty">题友心得暂时无法加载。个人笔记仍会保存在你的账号中。</div>';updateSharePager(0);}}
async function publishShare(){const q=currentQ(),content=$('shareContent').value.trim();if(!q||!content){toast('请填写心得内容');return;}try{await ensureAnonymous();await api('/api/experiences',{method:'POST',body:{question_id:q.id,nickname:state.profile.nickname,content}});$('shareContent').value='';sharePage=0;await loadPublicShares();toast('心得已发布');}catch(e){toast('发布失败：'+e.message);}}
async function loadMyShares(){const limit=myShareShowAll?100:10,first=await api(`/api/experiences/mine?limit=${limit}&offset=0`);myShareCache=first.items||[];if(myShareShowAll){for(let offset=limit;offset<(first.total||0);offset+=100){const page=await api(`/api/experiences/mine?limit=100&offset=${offset}`);myShareCache.push(...(page.items||[]));}}renderShareList(myShareCache,$('mySharesList'),true);$('showAllMyShares').classList.toggle('hidden',myShareShowAll||(first.total||0)<=10);}
async function showMyShares(){openModal('mySharesModal');$('mySharesList').innerHTML='<div class="empty">正在加载…</div>';myShareShowAll=false;try{await ensureAnonymous();await loadMyShares();}catch(e){$('mySharesList').innerHTML='<div class="empty">我的心得暂时无法加载。</div>';}}
async function showAllMyShares(){myShareShowAll=true;try{await loadMyShares();}catch(e){toast('加载失败：'+e.message);}}
async function handleShareAction(e){const like=e.target.closest('[data-like]'),edit=e.target.closest('[data-edit-share]'),del=e.target.closest('[data-delete-share]'),users=e.target.closest('[data-like-users]');try{if(like){await api(`/api/experiences/${encodeURIComponent(like.dataset.like)}/like`,{method:'POST',body:{}});if(document.getElementById('mySharesModal').classList.contains('show'))await loadMyShares();else await loadPublicShares();}else if(edit){const id=edit.dataset.editShare,item=myShareCache.find(x=>x.id===id)||shareItems.find(x=>x.id===id);if(!item)return;const content=prompt('修改心得内容',item.content);if(content===null)return;await api(`/api/experiences/${encodeURIComponent(id)}`,{method:'PATCH',body:{content}});await loadMyShares();toast('心得已更新');}else if(del){const id=del.dataset.deleteShare;if(!confirm('确定删除这条心得？'))return;await api(`/api/experiences/${encodeURIComponent(id)}`,{method:'DELETE'});await loadMyShares();toast('心得已删除');}else if(users){const id=users.dataset.likeUsers,box=$('likeUsers-'+id);if(!box)return;if(!box.classList.contains('hidden')){box.classList.add('hidden');return;}const data=await api(`/api/experiences/${encodeURIComponent(id)}/likes`);box.textContent=data.items?.length?'点赞用户：'+data.items.map(x=>x.nickname||'匿名同学').join('、'):'还没有人点赞';box.classList.remove('hidden');}}catch(err){toast('操作失败：'+err.message);}}
async function submitFeedback(){const content=$('feedbackContent').value.trim();if(!content){toast('请填写反馈内容');return;}try{await ensureAnonymous();await api('/api/feedback',{method:'POST',body:{nickname:state.profile.nickname,email:state.profile.email||'',category:$('feedbackCategory').value,content}});$('feedbackContent').value='';$('feedbackStatus').textContent='已提交';toast('建议已提交，感谢反馈');}catch(e){toast('提交失败：'+e.message);}}
function renderFavoritesAndOpen(){renderFavorites();openModal('favoritesModal');}
function profileOpen(){ $('nicknameInput').value=state.profile.nickname||'';$('emailInput').value=state.profile.email||'';$('thresholdInput').value=String(state.settings.threshold||3);$('logoFullscreenToggle').checked=!!state.settings.logoFullscreen;$('emailCodeField').classList.add('hidden');$('verifyEmailRow').classList.add('hidden');$('emailCodeInput').value='';$('requestEmailCode').textContent=state.profile.email?'更换绑定邮箱':'发送绑定验证码';$('emailBindStatus').textContent=state.profile.email?'当前已验证绑定：'+state.profile.email+'。输入新邮箱后可更换。':'尚未绑定经过验证的邮箱';openModal('profileModal');}
function saveProfile(){const nickname=$('nicknameInput').value.trim()||randomNickname();const threshold=Number($('thresholdInput').value)||3;state.profile={...state.profile,nickname,updatedAt:now()};state.settings={...state.settings,threshold,logoFullscreen:$('logoFullscreenToggle').checked,updatedAt:now()};saveState();$('shareNickname').textContent=`发布者：${nickname}`;closeModal('profileModal');renderQuestion();toast('设置已保存');}
async function requestBindEmail(){const email=$('emailInput').value.trim();if(!email){toast('请先填写邮箱地址');return;}try{await ensureAnonymous();await api('/api/email/request',{method:'POST',body:{email}});$('emailCodeField').classList.remove('hidden');$('verifyEmailRow').classList.remove('hidden');$('emailBindStatus').textContent='验证码已发送至 '+email+'，请查收邮件';$('emailCodeInput').value='';$('emailCodeInput').focus();toast('验证码已发送');}catch(e){$('emailCodeField').classList.add('hidden');$('verifyEmailRow').classList.add('hidden');toast('发送失败：'+e.message);}}
async function verifyBindEmail(){const email=$('emailInput').value.trim(),code=$('emailCodeInput').value.trim();if(!email||!code){toast('请填写邮箱和验证码');return;}try{const d=await api('/api/email/verify',{method:'POST',body:{email,code}});state.profile={...state.profile,email:d.email,updatedAt:now()};saveState();$('emailCodeField').classList.add('hidden');$('verifyEmailRow').classList.add('hidden');$('emailCodeInput').value='';$('requestEmailCode').textContent='更换绑定邮箱';$('emailBindStatus').textContent='当前已验证绑定：'+d.email+'。输入新邮箱后可更换。';toast('邮箱已验证绑定');}catch(e){toast('绑定失败：'+e.message);}}
function openSync(){openModal('syncModal');$('syncCreatePanel').classList.add('hidden');$('syncJoinPanel').classList.add('hidden');$('deviceCodeInput').value='';if(cloud.token){setCloudStatus('online','账号已连接，学习数据实时保存到服务端');refreshRecoveryCode(false);}else setCloudStatus('','正在建立账号…');}
const DRAW_WIDTH=700;
function paperLines(value,maxChars=29){
  const lines=[];for(const raw of String(value??'').split('\n')){const chars=Array.from(raw);if(!chars.length){lines.push('');continue;}for(let i=0;i<chars.length;i+=maxChars)lines.push(chars.slice(i,i+maxChars).join(''));}return lines;
}
function paperBlock(value,cls){return `<div class="${cls}">${paperLines(value).map(line=>`<div class="paper-line">${line?esc(line):'&nbsp;'}</div>`).join('')}</div>`;}
function paperContentHTML(q){
  let html=paperBlock(`${q.section} · 第 ${q.no} 题\n${q.question}`,'draw-paper-question');
  for(const o of (q.options||[]))html+=paperBlock(`${o.key}．${o.text}`,'draw-paper-option');
  if(['填空','简答','论述'].includes(q.type))html+='<div class="draw-paper-foot">在这里圈出题干关键词、标注法条要点或写下自己的记忆提示。</div>';
  return html;
}
function paperHTML(prefix,q){
  return `<div class="paper-stage" id="${prefix}Stage"><div class="paper-scaler" id="${prefix}Scaler"><div class="draw-paper" id="${prefix}Paper"><div class="draw-paper-content" id="${prefix}Text">${paperContentHTML(q)}</div><canvas id="${prefix}Canvas"></canvas></div></div></div>`;
}
function renderAnnotationPreview(q){
  const box=$('annotationPreview');if(!box)return;
  if(q&&drawState.qid!==q.id){
    const saved=state.notes[q.id]||{};
    drawState={qid:q.id,strokes:JSON.parse(JSON.stringify(saved.strokes||[])),active:null,tool:'pen',color:'#e53935',redoStack:[],lastSave:0,paperHeight:Number(saved.paperHeight)||0,enabled:showAnnotations};
  }
  const note=state.notes[q.id]||{},strokes=Array.isArray(note.strokes)?note.strokes:[];
  const visible=drawState.strokes.filter(x=>!x.deleted).length;
  box.classList.toggle('hidden',!showAnnotations);
  $('toggleAnnotations').classList.toggle('active',showAnnotations);
  if(!showAnnotations){box.innerHTML='';return;}
  const colors=[['#e53935','红色'],['#2563eb','蓝色'],['#16835d','绿色'],['#8b5cf6','紫色'],['#202638','黑色']];
  box.innerHTML=`<div class="annotation-preview-head"><strong>本题圈画笔记</strong><span>${visible?`${visible} 笔`:'暂无圈画'}</span></div>
    <div class="draw-toolbar">
      <div class="draw-tool-group" aria-label="绘图工具">
        <button class="btn draw-tool ${drawState.tool==='pen'?'active':''}" data-draw-tool="pen" title="画笔">✎ 画笔</button>
        <button class="btn draw-tool ${drawState.tool==='highlighter'?'active':''}" data-draw-tool="highlighter" title="荧光笔">▰ 荧光笔</button>
        <button class="btn draw-tool ${drawState.tool==='eraser'?'active':''}" data-draw-tool="eraser" title="橡皮">▱ 橡皮</button>
      </div>
      <div class="draw-color-group" aria-label="画笔颜色">${colors.map(([color,label])=>`<button class="draw-color ${drawState.color===color?'active':''}" data-draw-color="${color}" style="--draw-color:${color}" title="${label}" aria-label="${label}"></button>`).join('')}</div>
      <div class="draw-action-group" aria-label="圈画操作">
        <button class="btn" id="undoStroke" title="撤销一笔" ${visible?'':'disabled'}>↶ 撤销</button>
        <button class="btn" id="redoStroke" title="恢复一笔" ${drawState.redoStack.length?'':'disabled'}>↷ 恢复</button>
        <button class="btn" id="clearStrokes" title="清空本题圈画" ${visible?'':'disabled'}>⌫ 清空</button>
      </div>
      <button class="btn draw-done" id="finishDrawing">✓ 完成</button>
    </div>
    ${paperHTML('preview',q)}
    <div class="small-muted" id="drawSaveStatus" style="margin-top:8px">在题目纸面上直接圈画，自动保存并同步</div>`;
  requestAnimationFrame(()=>{resizePaperCanvas('preview',drawState.strokes,note.paperHeight||0);bindPreviewCanvas();});
}
function resizePaperCanvas(prefix,strokes,storedHeight=0){const stage=$(prefix+'Stage'),scaler=$(prefix+'Scaler'),paper=$(prefix+'Paper'),textEl=$(prefix+'Text'),canvas=$(prefix+'Canvas');if(!stage||!scaler||!paper||!textEl||!canvas)return;const height=Math.max(650,Math.ceil(textEl.scrollHeight),Number(storedHeight)||0);if(prefix==='preview'){drawState.paperHeight=height;}paper.style.height=height+'px';const dpr=Math.max(1,Math.min(3,window.devicePixelRatio||1));canvas.width=Math.round(DRAW_WIDTH*dpr);canvas.height=Math.round(height*dpr);canvas.style.width=DRAW_WIDTH+'px';canvas.style.height=height+'px';canvas.style.touchAction='none';canvas.style.pointerEvents=drawState.enabled?'auto':'none';const ctx=canvas.getContext('2d');ctx.setTransform(dpr,0,0,dpr,0,0);ctx.clearRect(0,0,DRAW_WIDTH,height);for(const stroke of(strokes||[]))drawOneStroke(ctx,stroke,DRAW_WIDTH,height);if(drawState.active)drawOneStroke(ctx,drawState.active,DRAW_WIDTH,height);const parent=stage.parentElement,available=Math.max(220,parent.clientWidth-24),scale=Math.min(1,available/DRAW_WIDTH);stage.style.width=DRAW_WIDTH*scale+'px';stage.style.height=height*scale+'px';scaler.style.transform=`scale(${scale})`;}
function drawOneStroke(ctx,stroke,w,h){const pts=stroke.points||[];if(stroke.deleted||!pts.length)return;ctx.save();ctx.lineCap='round';ctx.lineJoin='round';ctx.globalCompositeOperation=stroke.tool==='eraser'?'destination-out':'source-over';ctx.strokeStyle=stroke.color||'#e53935';ctx.fillStyle=stroke.color||'#e53935';ctx.globalAlpha=stroke.tool==='highlighter'?.28:1;ctx.lineWidth=stroke.width||3;if(pts.length===1){ctx.beginPath();ctx.arc(pts[0].x*w,pts[0].y*h,(stroke.width||3)/2,0,Math.PI*2);ctx.fill();}else{ctx.beginPath();ctx.moveTo(pts[0].x*w,pts[0].y*h);for(let i=1;i<pts.length;i++)ctx.lineTo(pts[i].x*w,pts[i].y*h);ctx.stroke();}ctx.restore();}
function pointerPoint(e){const canvas=e.currentTarget,rect=canvas.getBoundingClientRect();return{x:Math.max(0,Math.min(1,(e.clientX-rect.left)/Math.max(1,rect.width))),y:Math.max(0,Math.min(1,(e.clientY-rect.top)/Math.max(1,rect.height)))};}
function drawLiveSegment(a,b,stroke){const c=$('previewCanvas');if(!c)return;const ctx=c.getContext('2d'),dpr=Math.max(1,Math.min(3,window.devicePixelRatio||1));ctx.save();ctx.setTransform(dpr,0,0,dpr,0,0);ctx.lineCap='round';ctx.lineJoin='round';ctx.globalCompositeOperation=stroke.tool==='eraser'?'destination-out':'source-over';ctx.globalAlpha=stroke.tool==='highlighter'?.28:1;ctx.strokeStyle=stroke.color;ctx.fillStyle=stroke.color;ctx.lineWidth=stroke.width;if(a&&b){ctx.beginPath();ctx.moveTo(a.x*DRAW_WIDTH,a.y*drawState.paperHeight);ctx.lineTo(b.x*DRAW_WIDTH,b.y*drawState.paperHeight);ctx.stroke();}else{ctx.beginPath();ctx.arc(b.x*DRAW_WIDTH,b.y*drawState.paperHeight,stroke.width/2,0,Math.PI*2);ctx.fill();}ctx.restore();}
function saveDrawStrokes(){if(!drawState.qid)return;const old=state.notes[drawState.qid]||{text:''},t=now();state.notes[drawState.qid]={...old,strokes:drawState.strokes,strokesUpdatedAt:t,paperVersion:2,paperWidth:DRAW_WIDTH,paperHeight:drawState.paperHeight||old.paperHeight||0,updatedAt:Math.max(t,old.updatedAt||0)};try{localStorage.setItem(STORE_KEY,JSON.stringify(state));}catch{toast('本地存储空间不足，圈画可能无法暂存');}renderStats();scheduleSync();const status=$('drawSaveStatus');if(status)status.textContent='已保存到本机，正在同步云端…';const head=$('annotationPreview')?.querySelector('.annotation-preview-head span');if(head)head.textContent=`${drawState.strokes.filter(x=>!x.deleted).length} 笔`;const undo=$('undoStroke'),redo=$('redoStroke'),clear=$('clearStrokes');if(undo)undo.disabled=!drawState.strokes.some(x=>!x.deleted);if(redo)redo.disabled=!drawState.redoStack.length;if(clear)clear.disabled=!drawState.strokes.some(x=>!x.deleted);}
function undoStroke(){const last=[...drawState.strokes].reverse().find(x=>!x.deleted);if(!last)return;drawState.redoStack.push(JSON.parse(JSON.stringify(last)));last.deleted=true;last.deletedAt=now();resizePaperCanvas('preview',drawState.strokes,drawState.paperHeight);saveDrawStrokes();}
function redoStroke(){const stroke=drawState.redoStack.pop();if(!stroke)return;const restored={...JSON.parse(JSON.stringify(stroke)),id:uid(),at:now()};delete restored.deleted;delete restored.deletedAt;drawState.strokes.push(restored);resizePaperCanvas('preview',drawState.strokes,drawState.paperHeight);saveDrawStrokes();}
function clearStrokes(){if(!drawState.strokes.some(x=>!x.deleted))return;if(!confirm('确定清空本题全部圈画？'))return;const t=now();for(const x of drawState.strokes)if(!x.deleted){x.deleted=true;x.deletedAt=t;}drawState.redoStack=[];resizePaperCanvas('preview',drawState.strokes,drawState.paperHeight);saveDrawStrokes();}
function beginStroke(e){if(!drawState.enabled||drawState.active)return;if(e.cancelable)e.preventDefault();const p=pointerPoint(e),tool=drawState.tool;drawState.redoStack=[];drawState.active={tool,color:tool==='highlighter'?'#f5c542':tool==='eraser'?'#000':drawState.color,width:tool==='highlighter'?15:tool==='eraser'?24:3,points:[p],pointerId:e.pointerId};try{e.currentTarget.setPointerCapture(e.pointerId);}catch{}drawLiveSegment(null,p,drawState.active);}
function moveStroke(e){if(!drawState.active)return;if(e.cancelable)e.preventDefault();const p=pointerPoint(e),pts=drawState.active.points,last=pts[pts.length-1];if(Math.hypot(p.x-last.x,p.y-last.y)<.0005)return;pts.push(p);drawLiveSegment(last,p,drawState.active);}
function finishStroke(e){if(!drawState.active)return;if(e&&drawState.active.pointerId!=null&&e.pointerId!=null&&e.pointerId!==drawState.active.pointerId)return;delete drawState.active.pointerId;drawState.active.id=uid();drawState.active.at=now();drawState.strokes.push(drawState.active);drawState.active=null;saveDrawStrokes();}
function bindPreviewCanvas(){const c=$('previewCanvas');if(!c||c.dataset.bound)return;c.dataset.bound='1';c.style.touchAction='none';c.addEventListener('pointerdown',beginStroke,{passive:false});c.addEventListener('pointermove',moveStroke,{passive:false});c.addEventListener('pointerup',finishStroke);c.addEventListener('pointercancel',finishStroke);c.addEventListener('lostpointercapture',finishStroke);}
function openDrawMode(tool='pen'){showAnnotations=true;drawState.tool=tool;const q=currentQ();if(!q)return;if(drawState.qid!==q.id){const old=state.notes[q.id]||{};drawState={qid:q.id,strokes:JSON.parse(JSON.stringify(old.strokes||[])),active:null,tool,color:'#e53935',redoStack:[],lastSave:0,paperHeight:Number(old.paperHeight)||0,enabled:true};}else drawState.enabled=true;renderAnnotationPreview(q);$('annotationPreview').scrollIntoView({block:'nearest',behavior:'smooth'});}
function closeDrawMode(){if(drawState.active)finishStroke();drawState.enabled=false;const c=$('previewCanvas');if(c)c.style.pointerEvents='none';}
function bindDraw(){drawState.enabled=false;$('toggleAnnotations').onclick=()=>{showAnnotations=!showAnnotations;if(showAnnotations)openDrawMode();else{closeDrawMode();renderAnnotationPreview(currentQ());}saveState();};window.addEventListener('resize',()=>{const q=currentQ();if(showAnnotations&&q&&!drawState.active)renderAnnotationPreview(q);});$('annotationPreview').addEventListener('click',e=>{const tool=e.target.closest('[data-draw-tool]'),color=e.target.closest('[data-draw-color]');if(tool){if(drawState.active)finishStroke();drawState.tool=tool.dataset.drawTool;renderAnnotationPreview(currentQ());return;}if(color){drawState.color=color.dataset.drawColor;renderAnnotationPreview(currentQ());return;}if(e.target.closest('#undoStroke'))undoStroke();else if(e.target.closest('#redoStroke'))redoStroke();else if(e.target.closest('#clearStrokes'))clearStrokes();else if(e.target.closest('#finishDrawing')){closeDrawMode();toast('圈画已保存');}});}
function showAndBindRecovery(){
  $('copyFirstRecoveryCode').onclick=async()=>{try{await navigator.clipboard.writeText($('firstRecoveryCode').textContent);toast('已复制新的永久连接码');}catch{toast('请手动选中连接码复制');}};
  $('closeRecoveryModal').onclick=()=>closeModal('recoveryModal');
  $('syncCreateChoice').onclick=createPairCode;$('newDeviceCode').onclick=rotateRecoveryCode;
  $('copyDeviceCode').onclick=async()=>{try{await navigator.clipboard.writeText($('deviceCode').textContent);toast('已复制永久连接码');}catch{toast('请手动选中连接码并复制');}};
  $('syncJoinChoice').onclick=()=>{$('syncJoinPanel').classList.remove('hidden');$('syncCreatePanel').classList.add('hidden');};
  $('joinDevice').onclick=joinDevice;$('requestRecoveryEmail').onclick=requestRecoveryEmail;$('verifyRecoveryEmail').onclick=verifyRecoveryEmail;
  $('requestEmailCode').onclick=requestBindEmail;$('verifyEmail').onclick=verifyBindEmail;
}
const STUDY_PENDING_KEY='lawStudy.pendingStudyMs.v1',STUDY_BATCH_KEY='lawStudy.pendingStudyBatch.v1',STUDY_TOTAL_KEY='lawStudy.totalStudyMs.v1';
let studyAccumulated=Math.max(0,Number(localStorage.getItem(STUDY_TOTAL_KEY)||0)||0),studyLastTick=0,studyTimer=null,studyReported=Math.max(0,Number(localStorage.getItem(STUDY_PENDING_KEY)||0)||0),studyBatch=null,studyBusy=false;
try{studyBatch=JSON.parse(localStorage.getItem(STUDY_BATCH_KEY)||'null');}catch{studyBatch=null;}
function formatStudyTime(ms){const minutes=Math.floor(ms/60000),hours=Math.floor(minutes/60),rest=minutes%60;return hours?`${hours} 小时 ${rest} 分钟`:`${minutes} 分钟`;}
function renderStudyTime(){if($('studyTime'))$('studyTime').textContent=formatStudyTime(studyAccumulated);}
function persistStudyQueue(){localStorage.setItem(STUDY_PENDING_KEY,String(studyReported));if(studyBatch)localStorage.setItem(STUDY_BATCH_KEY,JSON.stringify(studyBatch));else localStorage.removeItem(STUDY_BATCH_KEY);}
function studyActive(){return !document.hidden&&document.hasFocus();}
async function reportStudyTime(){if(studyBusy||!navigator.onLine||(!studyBatch&&studyReported<=0))return;studyBusy=true;try{await ensureAnonymous();while(studyBatch||studyReported>0){if(!studyBatch){const delta=Math.min(60000,studyReported);studyBatch={batch_id:uid(),delta_ms:delta};studyReported-=delta;persistStudyQueue();}const d=await api('/api/study-time',{method:'POST',body:studyBatch});studyBatch=null;persistStudyQueue();studyAccumulated=Math.max(studyAccumulated,Number(d.total_ms)||0);localStorage.setItem(STUDY_TOTAL_KEY,String(studyAccumulated));renderStudyTime();}}catch(e){console.warn('study time sync failed; queued locally',e);}finally{studyBusy=false;}}
function studyTick(){const t=Date.now();if(studyLastTick&&studyActive()){const d=Math.min(5000,t-studyLastTick);studyAccumulated+=d;studyReported+=d;persistStudyQueue();renderStudyTime();if(studyReported>=30000){localStorage.setItem(STUDY_TOTAL_KEY,String(studyAccumulated));reportStudyTime();}}studyLastTick=t;}
async function loadStudyTime(){try{await ensureAnonymous();const path=studyBatch?'/api/study-time?batch_id='+encodeURIComponent(studyBatch.batch_id):'/api/study-time';const d=await api(path);if(studyBatch&&d.batch_applied){studyBatch=null;persistStudyQueue();}studyAccumulated=(Number(d.total_ms)||0)+studyReported+(studyBatch?.delta_ms||0);localStorage.setItem(STUDY_TOTAL_KEY,String(studyAccumulated));renderStudyTime();if(studyReported||studyBatch)reportStudyTime();}catch{const cached=Math.max(0,Number(localStorage.getItem(STUDY_TOTAL_KEY)||0)||0);studyAccumulated=Math.max(studyAccumulated,cached+studyReported+(studyBatch?.delta_ms||0));renderStudyTime();}}
function bind(){
 $('studyMode').onclick=()=>{mode='study';$('studyMode').classList.add('active');$('quizMode').classList.remove('active');$('modeDescription').textContent='背题模式：答案、解析和题友心得默认展开。';answerVisible=true;saveState();renderQuestion();};
 $('quizMode').onclick=()=>{mode='quiz';$('quizMode').classList.add('active');$('studyMode').classList.remove('active');$('modeDescription').textContent='做题模式：选择选项后提交，系统自动判分并记录。';answerVisible=false;$('sharePanel').classList.add('hidden');saveState();renderQuestion();};
 $('prevBtn').onclick=()=>navigate(-1);$('nextBtn').onclick=()=>navigate(1);
 $('randomBtn').onclick=()=>{const a=list();if(!a.length)return;const y=window.scrollY;index=Math.floor(Math.random()*a.length);answerVisible=mode==='study';saveState();renderQuestion();renderJump();requestAnimationFrame(()=>window.scrollTo({top:y,behavior:'instant'}));};
 $('questionActions').addEventListener('click',e=>{if(e.target.id==='revealAnswer'){answerVisible=true;lastResult=null;renderQuestion();}if(e.target.id==='submitAnswer')submitObjective();if(e.target.id==='retryAnswer'){answerVisible=false;lastResult=null;selected.clear();renderQuestion();}});
 $('optionsArea').addEventListener('click',e=>{const b=e.target.closest('[data-opt]');if(!b||mode!=='quiz'||answerVisible)return;const q=currentQ();const key=b.dataset.opt;if(q.type==='多选'){selected.has(key)?selected.delete(key):selected.add(key);}else{selected.clear();selected.add(key);}document.querySelectorAll('[data-opt]').forEach(x=>x.classList.toggle('selected',selected.has(x.dataset.opt)));const submit=$('submitAnswer');if(submit)submit.disabled=!selected.size;});
 $('selfCheckArea').addEventListener('click',e=>{const q=currentQ();if(!q)return;if(e.target.closest('#selfKnow'))applySelfTest(q,true,'self');if(e.target.closest('#selfNotKnow'))applySelfTest(q,false,'self');if(e.target.closest('#selfMastered'))applySelfTest(q,true,'manual-master');});
 $('favoriteToggle').onclick=markFavorite;$('noteOpen').onclick=openNote;$('textNotePreview').addEventListener('click',e=>{if(e.target.closest('#editInlineNote'))openNote();});bindDraw();showAndBindRecovery();$('noteText').addEventListener('input',()=>{clearTimeout(noteTimer);$('noteSaved').textContent='正在保存…';noteTimer=setTimeout(saveNote,250);});
 $('shareOpen').onclick=()=>{const panel=$('sharePanel');panel.classList.toggle('hidden');if(!panel.classList.contains('hidden')){sharePage=0;loadPublicShares();}};$('refreshShares').onclick=()=>{sharePage=0;loadPublicShares();};$('publishShare').onclick=publishShare;$('sharePrev').onclick=()=>{sharePage=Math.max(0,sharePage-1);loadPublicShares();};$('shareNext').onclick=()=>{sharePage++;loadPublicShares();};$('publicShares').addEventListener('click',handleShareAction);$('mySharesList').addEventListener('click',handleShareAction);$('showAllMyShares').onclick=showAllMyShares;
 $('sectionTabs').addEventListener('click',()=>{});$('searchInput').addEventListener('input',e=>{searchText=e.target.value;index=0;renderJump();renderSearchResults();renderQuestion();saveState();});$('searchResults').addEventListener('click',e=>{const b=e.target.closest('[data-search-q]');if(b){searchText='';$('searchInput').value='';$('searchResults').classList.add('hidden');setQuestionById(b.dataset.searchQ);}});$('listFilter').onchange=e=>setFilter(e.target.value);$('statusFilters').onclick=e=>{const b=e.target.closest('[data-status]');if(b)setFilter(b.dataset.status);};$('jumpToggle').onclick=()=>{$('jumpHolder').classList.toggle('open');$('jumpToggle').innerHTML=$('jumpHolder').classList.contains('open')?'收起题号导航 <span>⌃</span>':'展开题号导航 <span>⌄</span>';saveState();};
 $('syncOpen').onclick=openSync;
 $('profileOpen').onclick=profileOpen;$('saveProfile').onclick=saveProfile;$('brandMark').ondblclick=()=>{if(!state.settings.logoFullscreen)return;const ua=navigator.userAgent;const ios=/iPhone|iPad|iPod/i.test(ua)||(navigator.platform==='MacIntel'&&navigator.maxTouchPoints>1);const standalone=!!navigator.standalone||window.matchMedia('(display-mode: standalone)').matches;if(ios&&!standalone){toast('iPhone 浏览器不支持网页强制全屏。请用 Safari 分享 → 添加到主屏幕，再从桌面图标打开。');return;}if(ios&&standalone){toast('已通过主屏幕独立模式隐藏浏览器工具栏；系统状态栏仍会显示。');return;}if(document.fullscreenElement){document.exitFullscreen?.();return;}if(document.documentElement.requestFullscreen)document.documentElement.requestFullscreen().catch(()=>toast('当前浏览器不允许网页进入全屏'));else toast('当前浏览器不支持网页全屏');};$('favoritesOpen').onclick=renderFavoritesAndOpen;$('historyOpen').onclick=()=>{renderHistory();openModal('historyModal');};$('clearHistory').onclick=()=>{if(confirm('确定清空自测历史？掌握状态、笔记和收藏不会被删除。')){state.tests=[];state.testClearAt=now();saveState();renderHistory();toast('自测历史已清空');}};
 $('mySharesOpen').onclick=showMyShares;$('feedbackOpen').onclick=()=>openModal('feedbackModal');$('sendFeedback').onclick=submitFeedback;
 document.addEventListener('click',e=>{const close=e.target.closest('[data-close]');if(close){if(close.dataset.close==='noteModal'){clearTimeout(noteTimer);saveNote();noteEditQID='';}closeModal(close.dataset.close);}const open=e.target.closest('[data-open-q]');if(open)setQuestionById(open.dataset.openQ);const unfav=e.target.closest('[data-unfav]');if(unfav){const id=unfav.dataset.unfav;state.favorites[id]={saved:false,updatedAt:now()};saveState();renderFavorites();renderJump();toast('已从收藏夹移除');}});
 document.querySelectorAll('.modal-backdrop').forEach(m=>m.addEventListener('click',e=>{if(e.target===m){closeModal(m.id);}}));
 document.addEventListener('keydown',e=>{if(e.target.matches('textarea,input,select'))return;if(document.querySelector('.modal-backdrop.show'))return;if(e.key==='ArrowRight')navigate(1);if(e.key==='ArrowLeft')navigate(-1);if(e.key==='Escape'){if(document.body.classList.contains('fullscreen-fallback'))document.body.classList.remove('fullscreen-fallback');document.querySelectorAll('.modal-backdrop.show').forEach(m=>closeModal(m.id));}});
}
const touchDevice=/iPad|iPhone|iPod|Android/i.test(navigator.userAgent)||(navigator.platform==='MacIntel'&&navigator.maxTouchPoints>1);if(touchDevice)document.body.classList.add('touch');
bind();renderStudyTime();if(state.settings.jumpOpen){$('jumpHolder').classList.add('open');$('jumpToggle').innerHTML='收起题号导航 <span>⌃</span>';}$('searchInput').value=searchText;renderAll();initializeCloud().then(loadStudyTime);document.addEventListener('visibilitychange',()=>{studyTick();if(document.hidden&&(studyReported||studyBatch))reportStudyTime();});window.addEventListener('focus',studyTick);window.addEventListener('blur',()=>{studyTick();if(studyReported||studyBatch)reportStudyTime();});studyLastTick=Date.now();studyTimer=setInterval(studyTick,1000);window.addEventListener('pagehide',()=>{if(studyReported||studyBatch)reportStudyTime();});window.addEventListener('online',()=>{setCloudStatus('','网络已恢复，正在同步…');syncNow();reportStudyTime();});window.addEventListener('visibilitychange',()=>{if(!document.hidden){syncNow();refreshRecoveryCode(true);}});window.addEventListener('focus',()=>refreshRecoveryCode(true));setInterval(()=>{if(!document.hidden){syncNow();refreshRecoveryCode(true);}},15000);if('serviceWorker' in navigator&&(location.protocol==='https:'||location.hostname==='localhost'))window.addEventListener('load',()=>navigator.serviceWorker.register('/service-worker.js').catch(e=>console.warn('service worker registration failed',e)));
