package web

import (
	"encoding/json"
	"html"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/desklog"
	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

// This file is the Go page layer: the two-pane desk page and the message
// pages, in the datadonation.eu look (style.go, assets/desk.css). The
// JavaScript keeps the prototype's ids, classes and fetch calls; it never
// reloads the page, because a reload rebuilds the streamed browser's frame
// and loses the open item's timer: after the last item and after getting
// more it fetches the queue's parts (GET /desk/queue, queueParts) and
// redraws the count and the sidebar in place. The %%NAME%% slots are
// substituted with one strings.NewReplacer pass, so text substituted in (a
// pool note, say) is never scanned for placeholders again, exactly as the
// Python re.sub.
//
// Values from a platform definition (labels, upload_hint) are trusted HTML;
// everything a user, the pool or the log supplies is escaped with
// html.EscapeString.

// PAGE is the two-pane desk page: the header with the count and the two
// folds (Earlier days, How to collect), then the sidebar and the streamed
// browser; the sidebar comes first in the markup and the stylesheet puts it
// on the browser's right. The sidebar is a route: its head names it (Your queue, the page's
// name on every platform) and holds the next action, the queue runs down a line with a node per item, and the
// upload note closes it. A stopped or signed-out desk fills the browser's
// pane with its message. %%NAME%% slots are substituted by deskPage.
//
// The script finds its parts by id (prog, barfill, next, getmore, worked,
// upload, stopped, denied, screen, starting), by ul.rows li with data-state,
// data-kind and data-id, and by the classes msg, timer and current; the
// stylesheet keys every state on those same hooks. To redraw the queue it
// also finds the classes count, count__bar, side__head and side__list, and
// a row by its id (r and the item's id). The guide (guideJSON) finds its
// hint and ring by id (guide, guide-text, guide-next, guide-skip,
// guide-ring) and its targets by getmore's button, next, the class
// screen-slot, the open row's pick and upload; every call into it from the
// rest of the script is caught, so a fault in the guide ends the guide and
// never stops an open or a redraw.
const PAGE = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>%%TITLE%%</title>
<style>
%%CSS%%
</style></head>
<body class="desk">
%%HEADER%%
<div class="panes">
<aside class="side">
<div class="side__head"><h1 class="side__title">Your queue</h1>%%NEXT%%</div>
<div class="side__list">%%BODY%%</div>
%%FOOTER%%</aside>
<div class="screen-slot">
<div id="starting" class="starting">Starting your browser…</div>
<iframe id="screen" title="Your browser" style="display:none" allow="clipboard-read; clipboard-write"></iframe>
<div id="stopped" hidden class="halt"><div class="stop"><span class="bang" aria-hidden="true">!</span><div><p class="lede">Your desk was stopped after %%IDLE_MINUTES%% minutes without activity.</p>%%RELOAD_CONTINUE%%</div></div></div>
<div id="denied" hidden class="halt"><div class="stop"><span class="bang" aria-hidden="true">!</span><div><p class="lede">You are signed out, or your access to the desk was removed.</p><p>If reloading keeps failing, contact %%CONTACT%%.</p>%%RELOAD_PAGE%%</div></div></div>
</div>
</div>
<div id="guide" class="guide" hidden><p id="guide-text" class="guide__text"></p><div class="guide__row"><button id="guide-next" class="cmd cmd--sm cmd--main"></button><button id="guide-skip" class="guide__skip">Skip the guide</button></div></div>
<i id="guide-ring" class="guide__ring" hidden></i>
<script>
const SECONDS=%%SECONDS%%,NOTE_CODES=%%NOTE_CODES%%;let timer=null,wasReady=false,sawInput=false,stopped=false;
async function waitScreen(){try{const r=await fetch('status');if(r.ok&&(await r.json()).ready){
 const f=document.getElementById('screen');f.src='/desk/vnc/?resize=remote';f.style.display='';
 wasReady=true;document.getElementById('starting').style.display='none';return;}}catch(e){}setTimeout(waitScreen,2000);}
waitScreen();
const INPUT_EVENTS=['pointerdown','pointermove','keydown','wheel','touchstart'];
function listen(d){INPUT_EVENTS.forEach(e=>d.addEventListener(e,()=>{sawInput=true;},{passive:true,capture:true}));}
listen(document);
document.getElementById('screen').addEventListener('load',function(){try{if(this.contentDocument)listen(this.contentDocument);}catch(e){}});
function showStopped(){stopped=true;document.getElementById('stopped').hidden=false;}
function showDenied(){stopped=true;document.getElementById('denied').hidden=false;}
function refused(r){return r.status===401||r.status===403||r.type==='opaqueredirect';}
function showFigures(j){const w=document.getElementById('worked');if(w&&typeof j.worked==='string')w.textContent=j.worked;
 const f=document.getElementById('upload');if(f&&typeof j.footer==='string'){f.innerHTML=j.footer;f.classList.toggle('warn',j.footer_warn===true);}}
setInterval(async()=>{if(stopped)return;const hadInput=sawInput;sawInput=false;
 try{const r=hadInput?await fetch('active',{method:'POST',redirect:'manual'}):await fetch('status',{redirect:'manual'});
  if(refused(r)){showDenied();return;}
  if(!r.ok){if(hadInput)sawInput=true;return;}
  const j=await r.json();showFigures(j);
  if(hadInput){if(!j.running)showStopped();}
  else if(j.ready)wasReady=true;else if(wasReady&&j.running===false)showStopped();
 }catch(e){if(hadInput)sawInput=true;}},60000);
function q(l){return 'id='+encodeURIComponent(l.dataset.id)+'&kind='+encodeURIComponent(l.dataset.kind);}
function setMsg(l,text,warn){const m=l.querySelector('.msg');m.textContent=text;m.className='msg'+(warn?' warn':'');}
function tick(){const t=document.querySelector('li.current .timer');if(!t)return;
 const s=Math.max(0,Math.round((timer.end-Date.now())/1000));
 if(s===0){clearInterval(timer.h);t.textContent='Time’s up. Open the next one.';t.className='timer up';return;}
 t.className='timer';t.textContent=Math.floor(s/60)+':'+String(s%60).padStart(2,'0');}
function startTimer(){if(timer)clearInterval(timer.h);document.querySelectorAll('.timer').forEach(t=>t.textContent='');
 timer={end:Date.now()+SECONDS*1000};timer.h=setInterval(tick,1000);tick();}
function current(l){document.querySelectorAll('li.current').forEach(x=>x.classList.remove('current'));
 l.classList.add('current');if(SECONDS>0)startTimer();}
async function open_(l){const r=await fetch('open?'+q(l),{method:'POST'});
 if(r.ok){if(l.dataset.state!=='problem')l.dataset.state='done';l.classList.add('done');setMsg(l,'');current(l);refresh();guideOn('opened');}
 else if(r.status===503){setMsg(l,'the browser is still starting; try again in a moment',true);}
 else{setMsg(l,'could not open; tell the operator',true);}}
function openNext(){const n=document.querySelector('ul.rows li[data-state=todo]');if(n)open_(n);}
async function problem(sel){const l=sel.closest('li'),k=sel.value;if(!k)return;let note='';
 if(NOTE_CODES.includes(k)){note=prompt('What went wrong? (a few words)')||'';if(!note.trim()){sel.value='';return;}}
 const r=await fetch('problem?'+q(l)+'&code='+encodeURIComponent(k)+'&note='+encodeURIComponent(note.trim()),{method:'POST'});
 if(r.ok){l.dataset.state='problem';setMsg(l,sel.options[sel.selectedIndex].text+(note?': '+note.trim():''),true);refresh();}
 else{setMsg(l,'could not save; tell the operator',true);}
 sel.value='';}
function refresh(){const rows=[...document.querySelectorAll('ul.rows li')];const done=rows.filter(r=>r.dataset.state!=='todo').length;
 const p=document.getElementById('prog');if(p)p.textContent=done+' of '+rows.length+' done';
 const b=document.getElementById('barfill');if(b)b.style.width=(rows.length?100*done/rows.length:0)+'%';
 const nx=document.getElementById('next');if(nx)nx.disabled=!rows.some(r=>r.dataset.state==='todo');
 if(rows.length&&done===rows.length&&!document.getElementById('getmore'))redraw();}
function draw(j){const cur=document.querySelector('li.current'),id=cur?cur.id:'';
 const c=document.querySelector('.count');c.querySelectorAll('#prog,.count__bar').forEach(x=>x.remove());c.insertAdjacentHTML('afterbegin',j.progress);
 const nx=document.getElementById('next');if(nx)nx.remove();document.querySelector('.side__head').insertAdjacentHTML('beforeend',j.next);
 document.querySelector('.side__list').innerHTML=j.body;
 const l=id&&document.getElementById(id);if(l){l.classList.add('current');if(timer)tick();}guideOn('drawn');}
async function redraw(){if(stopped)return;
 try{const r=await fetch('queue',{redirect:'manual'});
  if(refused(r)){showDenied();return;}
  if(r.ok){draw(await r.json());return;}
 }catch(e){}setTimeout(redraw,2000);}
async function more(n){try{await fetch('claim?n='+n,{method:'POST'});}catch(e){}document.querySelectorAll('details.fold[open]').forEach(d=>{d.open=false;});redraw();}
const G=%%GUIDE%%,GUIDE_STEPS=['more','next','screen','problem','upload'];let guide=null;
function guideTarget(s){
 if(s==='more')return document.querySelector('#getmore button');
 if(s==='next'){const n=document.getElementById('next');return n&&!n.disabled?n:null;}
 if(s==='screen')return document.querySelector('.screen-slot');
 if(s==='problem')return document.querySelector('li.current .pick');
 if(s==='upload')return document.getElementById('upload')?document.querySelector('.screen-slot'):null;
 return null;}
function guideEnd(){if(guide)clearInterval(guide.h);guide=null;document.getElementById('guide').hidden=true;document.getElementById('guide-ring').hidden=true;}
function guidePlace(){if(!guide)return;
 try{if(stopped){guideEnd();return;}
  const t=guideTarget(guide.step),box=document.getElementById('guide'),ring=document.getElementById('guide-ring');
  if(!t){box.hidden=ring.hidden=true;return;}
  let r=t.getBoundingClientRect();
  if(guide.step==='upload')r={left:r.right-101,top:r.top+48,width:32,height:32};
  else if(guide.step==='screen')r={left:r.left+8,top:r.top+8,width:r.width-16,height:r.height-16};
  box.hidden=ring.hidden=false;
  ring.style.left=(r.left-4)+'px';ring.style.top=(r.top-4)+'px';ring.style.width=(r.width+8)+'px';ring.style.height=(r.height+8)+'px';
  const w=box.offsetWidth,h=box.offsetHeight;let x=r.left-w-20,y=r.top+r.height/2-h/2;
  if(guide.step==='screen'){x=r.left+24;y=r.top+r.height-h-24;}
  if(guide.step==='upload'){x=r.left+r.width-w;y=r.top+r.height+16;}
  box.style.left=Math.max(8,Math.min(x,innerWidth-w-8))+'px';box.style.top=Math.max(8,Math.min(y,innerHeight-h-8))+'px';
 }catch(e){guideEnd();}}
function guideGo(i){for(;i<GUIDE_STEPS.length&&!guideTarget(GUIDE_STEPS[i]);i++);
 if(i>=GUIDE_STEPS.length){guideEnd();return;}
 guide.step=GUIDE_STEPS[i];let later=false;for(let j=i+1;j<GUIDE_STEPS.length;j++)if(guideTarget(GUIDE_STEPS[j]))later=true;
 document.getElementById('guide-text').innerHTML=G.steps[guide.step];
 const nx=document.getElementById('guide-next');nx.textContent=later?'Next':'Done';nx.hidden=!guide.replay&&i<2;
 const t=guideTarget(guide.step);if(i!==2&&i!==4&&t.scrollIntoView)t.scrollIntoView({block:'nearest'});
 guidePlace();}
function guideStart(replay){try{document.querySelectorAll('details.fold[open]').forEach(d=>{d.open=false;});
  if(guide)clearInterval(guide.h);guide=null;
  if(!replay&&!guideTarget('more')&&!guideTarget('next'))return;
  guide={replay:replay,step:'',h:setInterval(guidePlace,500)};guideGo(0);}catch(e){guideEnd();}}
function guideOn(e){if(!guide)return;
 try{const i=GUIDE_STEPS.indexOf(guide.step);
  if(e==='opened'&&i<2)guideGo(2);else if(!guideTarget(guide.step))guideGo(i);else guidePlace();}catch(x){guideEnd();}}
document.getElementById('guide-next').onclick=()=>{if(guide)try{guideGo(GUIDE_STEPS.indexOf(guide.step)+1);}catch(e){guideEnd();}};
document.getElementById('guide-skip').onclick=guideEnd;
if(G.auto)guideStart(false);
</script></body></html>`

// message renders a one-message page: title (plain text) names the tab,
// band (plain text; the project's name, bandName) is on the band, and text
// (plain text, escaped) is the message under it, in the message text style,
// under the header with subtitle (already HTML). With refresh > 0 the page
// reloads itself after that many seconds, and says so.
func message(title, band, subtitle, text string, refresh int) string {
	meta, note := "", ""
	if refresh > 0 {
		meta = `<meta http-equiv="refresh" content="` + strconv.Itoa(refresh) + `">`
		note = `<p class="muted">This page reloads by itself.</p>`
	}
	return pageTop(title, meta, subtitle, nil, bandText(band)) +
		`<p class="lede">` + html.EscapeString(text) + `</p>` + note + pageBottom
}

// problemOptions is the problem <select> of every row.
func problemOptions(problems []platform.Problem) string {
	var b strings.Builder
	b.WriteString(`<option value="">Report a problem…</option>`)
	for _, p := range problems {
		b.WriteString(`<option value="` + html.EscapeString(p.Code) + `">` + html.EscapeString(p.Label) + `</option>`)
	}
	return b.String()
}

// nextAction is the sidebar's next step: the arrow disc heading the queue's
// line, and its label. noun is already escaped; off disables it (nothing
// left to open).
func nextAction(noun string, off bool) string {
	disabled := ""
	if off {
		disabled = " disabled"
	}
	return `<button id="next" class="next" onclick="openNext()"` + disabled + `>` + arrow("next__disc") +
		`<span class="next__label">Open next ` + noun + `</span></button>`
}

// row renders one queue row: a node on the queue's line, the title that
// opens the item, the timer's place, the item's id and its problem control,
// which names the item it is about. msg is already-escaped HTML (or "").
func row(kind, itemID, state string, opened bool, title, msg, options string, timer bool) string {
	cls := ""
	if opened {
		cls = ` class="done"`
	}
	k, i := html.EscapeString(kind), html.EscapeString(itemID)
	var b strings.Builder
	b.WriteString(`<li id="r` + i + `"` + cls + ` data-state="` + state + `" data-kind="` + k + `" data-id="` + i + `"><div class="row">`)
	b.WriteString(`<span class="mark"></span><a href="#" onclick="open_(this.closest('li'));return false">` + html.EscapeString(title) + `</a>`)
	if timer {
		b.WriteString(`<span class="timer"></span>`)
	}
	b.WriteString(`</div><div class="sub"><span>` + i + `</span><select class="pick" aria-label="Report a problem with ` + html.EscapeString(title) +
		`" onchange="problem(this)">` + options + `</select></div>`)
	warn := ""
	if msg != "" {
		warn = " warn"
	}
	b.WriteString(`<div class="msg` + warn + `">` + msg + `</div></li>`)
	return b.String()
}

// progress renders the header's count: done of shown, and the blue line
// that runs that far towards its ring node. The percentage rounds half to
// even, as Python's round does.
func progress(nDone, n int) string {
	pct := pyRound(100 * float64(nDone) / float64(n))
	return `<span id="prog" class="count__done">` + strconv.Itoa(nDone) + ` of ` + strconv.Itoa(n) + ` done</span>` +
		`<div class="count__bar"><i id="barfill" class="count__fill" style="width:` + strconv.Itoa(pct) + `%"></i></div>`
}

// pyRound rounds half to even like Python's round for the positive values
// progress needs.
func pyRound(x float64) int {
	f := math.Floor(x)
	d := x - f
	switch {
	case d > 0.5:
		return int(f) + 1
	case d < 0.5:
		return int(f)
	case int(f)%2 == 0:
		return int(f)
	default:
		return int(f) + 1
	}
}

// pageParams are the values deskPage substitutes into PAGE.
type pageParams struct {
	User          string
	Admin         bool // admins get the Admin link in the header (P10)
	PlatformLabel string
	Noun          string // the platform's default kind, escaped by deskPage
	Seconds       int    // the per-item timer, TimerMinutes*60
	NoteCodes     []string
	ProgressHTML  string         // already HTML
	Next          string         // already HTML: the next action (nextAction), in the sidebar's head; "" without items
	Body          string         // already HTML
	Guide         bool           // the guide starts by itself: the collector has never opened an item
	Footer        *string        // the footer's inner HTML (uploadFooter); nil hides the footer
	FooterWarn    bool           // the footer warns: something captured is not in 4CAT yet
	IdleMinutes   int            // N of the stopped banner, desk_idle_seconds/60 rounded down, at least 1
	Contact       string         // desk_contact, escaped by deskPage
	Worked        desklog.Worked // the user's time worked, shown in the header
	Now           time.Time      // the clock's reading: which of Worked's days is today
}

// deskPage renders the two-pane page. Body, Next and ProgressHTML are
// already HTML, and so is Footer; a nil Footer hides the footer.
func deskPage(p pageParams) string {
	footer := ""
	if p.Footer != nil {
		cls := "upload"
		if p.FooterWarn {
			cls = "upload warn" // the class the script toggles; the note's own alert says it
		}
		footer = `<footer id="upload" class="` + cls + `">` + *p.Footer + "</footer>\n"
	}
	noteCodes := "[]" // a nil NoteCodes renders as [], never null
	if p.NoteCodes != nil {
		codes, err := json.Marshal(p.NoteCodes)
		if err != nil {
			codes = []byte("[]")
		}
		noteCodes = strings.ReplaceAll(string(codes), "<", `\u003c`)
	}
	noun := html.EscapeString(p.Noun)
	links := []navLink{{Label: "Your queue", Href: "/desk/", Current: true}}
	if p.Admin {
		links = append(links, navLink{Label: "Admin", Href: "/desk/admin/"})
	}
	count := `<div class="count">` + p.ProgressHTML + workedHTML(p.Worked, p.Now) + `</div>`
	folds := earlierDaysHTML(p.Worked, p.Now) + howToHTML(noun, p.Seconds > 0)
	subtitle := platformLine(p.PlatformLabel) + " · " + html.EscapeString(p.User)
	return strings.NewReplacer(
		"%%TITLE%%", html.EscapeString(p.PlatformLabel)+" desk — "+html.EscapeString(p.User),
		"%%CSS%%", pageCSS,
		"%%HEADER%%", headerHTML(subtitle, count, links, folds, true),
		"%%NEXT%%", p.Next,
		"%%BODY%%", p.Body,
		"%%FOOTER%%", footer,
		"%%SECONDS%%", strconv.Itoa(p.Seconds),
		"%%NOTE_CODES%%", noteCodes,
		"%%GUIDE%%", guideJSON(noun, p.Guide),
		"%%IDLE_MINUTES%%", strconv.Itoa(max(p.IdleMinutes, 1)),
		"%%CONTACT%%", html.EscapeString(p.Contact),
		"%%RELOAD_CONTINUE%%", actionLink("/desk/", "Reload to continue"),
		"%%RELOAD_PAGE%%", actionLink("/desk/", "Reload this page"),
	).Replace(PAGE)
}

// howToHTML is the header's How to collect fold: the four steps down one
// line, numbered in discs, in a panel under the header, and the button that
// starts the guide again. noun is already escaped. The fold never opens by
// itself: a collector who is new gets the guide, and an open panel would
// cover the queue. The folds share a name, so opening one closes the other.
func howToHTML(noun string, timer bool) string {
	timerHelp := ""
	if timer {
		timerHelp = ", or until the timer reaches 0:00"
	}
	return `<li><details class="fold" name="fold"><summary class="site-nav__link">How to collect</summary>` +
		`<div class="panel"><ol class="howto">` +
		`<li>Click <b>Open next ` + noun + `</b>, or a ` + noun + ` in the list. It opens in the browser on the left.</li>` +
		`<li>Open the comments and scroll until no more load` + timerHelp + `. Don't open replies.</li>` +
		`<li>If the ` + noun + ` won't show or the comments won't load, pick what happened under <b>Report a problem</b>.</li>` +
		`<li>When every ` + noun + ` is done, get more.</li>` +
		`</ol><button class="guide__again" onclick="guideStart(true)">Show me around</button></div></details></li>`
}

// guideIcon is the speech bubble the guide shows for a platform's comments
// button, which the page cannot point at inside the streamed browser.
const guideIcon = `<svg class="guide__icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 5h16v11H10l-5 4v-4H4z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/></svg>`

// guideJSON is the script's G: whether the guide starts by itself, and the
// hint of each step as HTML. noun is already escaped. encoding/json escapes
// <, > and &, so the value is safe inside the script element.
//
// The guide walks along with a collector's first item. Each hint sits beside
// what to use next and is ringed: getting items, opening the first, the
// browser's pane (the comments button is drawn, since the page cannot see
// into the streamed browser), reporting a problem, and the Zeeschuimer
// button in the browser's toolbar, which is at a fixed place from the pane's
// top right corner. The first two hints of a walk move on when the collector
// does the thing; started again from How to collect, every hint has Next.
func guideJSON(noun string, auto bool) string {
	g := struct {
		Auto  bool              `json:"auto"`
		Steps map[string]string `json:"steps"`
	}{auto, map[string]string{
		"more":    "Start here: get your first " + noun + "s.",
		"next":    "Open the next " + noun + " here. It opens in the browser on the left.",
		"screen":  "Open the comments in the browser: look for the speech-bubble button " + guideIcon + " beside the " + noun + ". Then scroll the comments until no more load. Don't open replies.",
		"problem": "If the " + noun + " won't show or the comments won't load, pick what happened here.",
		"upload":  "Before you leave, send what you captured to 4CAT: click <b>Z</b> here, in the browser's toolbar. The note at the bottom of your queue has the steps.",
	}}
	b, err := json.Marshal(g)
	if err != nil {
		return `{"auto":false,"steps":{}}`
	}
	return string(b)
}
