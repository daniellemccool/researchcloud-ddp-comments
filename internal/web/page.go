package web

import (
	"encoding/json"
	"html"
	"math"
	"strconv"
	"strings"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/platform"
)

// This file is the Go page layer: the two-pane
// desk page, the one-line message pages. The HTML, CSS and JavaScript are
// verbatim — same ids, classes, texts and fetch calls — only the templating
// moved to Go: the %%NAME%% slots are substituted with one
// strings.NewReplacer pass, so text substituted in (a pool note, say) is
// never scanned for placeholders again, exactly as the Python re.sub.
//
// Values from a platform definition (labels, upload_hint) are trusted HTML;
// everything a user, the pool or the log supplies is escaped with
// html.EscapeString.

// CSS is the page's stylesheet.
const CSS = `:root{--ink:#1d2329;--muted:#6b7480;--line:#e3e6ea;--bg:#f6f7f9;--accent:#1f6feb;--ok:#1a7f37;--warn:#b35900}
*{box-sizing:border-box}
body{margin:0;font:14px/1.4 system-ui,sans-serif;color:var(--ink);display:flex;height:100vh;overflow:hidden}
aside{width:340px;flex:0 0 340px;display:flex;flex-direction:column;border-right:1px solid var(--line);background:var(--bg)}
header{padding:12px 14px;border-bottom:1px solid var(--line);background:#fff}
header b{font-size:15px}.who{color:var(--muted);font-size:12px}
.progress{margin-top:6px;font-size:12px;color:var(--muted)}
.bar{height:6px;background:var(--line);border-radius:3px;margin-top:4px;overflow:hidden}.bar i{display:block;height:100%;background:var(--ok)}
details{padding:8px 14px;border-bottom:1px solid var(--line);background:#fff;font-size:13px}
details summary{cursor:pointer;color:var(--accent)}details ol{margin:6px 0 2px 18px;padding:0}
.list{flex:1;overflow:auto;padding:10px 10px 4px}
.next{display:block;width:100%;padding:10px;margin:0 0 10px;font:inherit;font-weight:600;color:#fff;background:var(--accent);border:0;border-radius:6px;cursor:pointer}
.next:disabled{background:#9aa4b0;cursor:default}
ul.rows{list-style:none;margin:0;padding:0}
ul.rows li{background:#fff;border:1px solid var(--line);border-radius:6px;padding:8px 10px;margin:0 0 6px}
ul.rows li.current{border-color:var(--accent);box-shadow:0 0 0 2px #1f6feb33}
.row{display:flex;gap:8px;align-items:baseline}
.row a{flex:1;color:var(--ink);text-decoration:none;font-weight:600}.row a:hover{color:var(--accent)}
.mark{width:1.2em;text-align:center}
li[data-state=done] .mark::before{content:"\2713";color:var(--ok)}
li[data-state=problem] .mark::before{content:"\26A0";color:var(--warn)}
li[data-state=todo] .mark::before{content:"\25CB";color:var(--muted)}
.sub{display:flex;gap:8px;align-items:center;margin-top:4px;font-size:12px;color:var(--muted)}
.sub select{font:inherit;font-size:12px;max-width:150px}
.msg{font-size:12px}.msg.warn{color:var(--warn)}.timer{font-variant-numeric:tabular-nums;font-weight:600;color:var(--accent)}
.timer.up{color:var(--ok)}
.more{padding:8px 0}.more button{font:inherit;padding:6px 10px;margin-right:6px;border:1px solid var(--accent);background:#fff;color:var(--accent);border-radius:6px;cursor:pointer}
.hint{font-size:12px;color:var(--muted);margin:6px 2px}
footer{padding:10px 14px;border-top:1px solid var(--line);background:#fff8e6;font-size:12px}
#starting{flex:1;display:flex;align-items:center;justify-content:center;color:var(--muted);font-size:15px}
#stopped{position:fixed;top:0;left:0;right:0;z-index:10;padding:14px;text-align:center;font-size:15px;font-weight:600;background:#fff8e6;color:var(--warn);border-bottom:1px solid var(--line)}
#stopped[hidden]{display:none}`

// PAGE is the two-pane desk page. %%NAME%% slots are
// substituted by deskPage.
const PAGE = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>%%TITLE%%</title>
<style>
%%CSS%%
</style></head>
<body>
<aside>
<header><b>Your %%NOUN%%s</b> <span class="who">%%USER%%</span>%%PROGRESS%%</header>
<details%%OPEN_HELP%%><summary>How to collect</summary><ol>
<li>Click <b>Open next %%NOUN%%</b> (or an item in the list). It opens in the browser on the right.</li>
<li>Open the comments and scroll until no more load%%TIMER_HELP%%. Don't open replies.</li>
<li>If the item won't show or comments won't load, pick what happened under <b>Problem…</b>.</li>
<li>When every item is done, get more.</li>
</ol></details>
<div class="list">%%BODY%%</div>
%%FOOTER%%</aside>
<div id="starting">Starting your browser…</div>
<div id="stopped" hidden>Your desk was stopped after %%IDLE_MINUTES%% minutes without activity. Reload to continue.</div>
<iframe id="screen" style="flex:1 1 auto;height:100vh;border:0;min-width:0;display:none" allow="clipboard-read; clipboard-write"></iframe>
<script>
const SECONDS=%%SECONDS%%,NOTE_CODES=%%NOTE_CODES%%;let timer=null,wasReady=false,sawInput=false,stopped=false;
async function waitScreen(){try{const r=await fetch('status');if(r.ok&&(await r.json()).ready){
 const f=document.getElementById('screen');f.src='/desk/vnc/';f.style.display='';
 wasReady=true;document.getElementById('starting').style.display='none';return;}}catch(e){}setTimeout(waitScreen,2000);}
waitScreen();
const INPUT_EVENTS=['pointerdown','pointermove','keydown','wheel','touchstart'];
function listen(d){INPUT_EVENTS.forEach(e=>d.addEventListener(e,()=>{sawInput=true;},{passive:true,capture:true}));}
listen(document);
document.getElementById('screen').addEventListener('load',function(){try{if(this.contentDocument)listen(this.contentDocument);}catch(e){}});
function showStopped(){stopped=true;document.getElementById('stopped').hidden=false;}
setInterval(async()=>{if(stopped)return;
 try{if(sawInput){sawInput=false;const r=await fetch('active',{method:'POST'});if(r.ok&&!(await r.json()).running)showStopped();return;}
  const r=await fetch('status');if(r.ok){if((await r.json()).ready)wasReady=true;else if(wasReady)showStopped();}}catch(e){}},60000);
function q(l){return 'id='+encodeURIComponent(l.dataset.id)+'&kind='+encodeURIComponent(l.dataset.kind);}
function setMsg(l,text,warn){const m=l.querySelector('.msg');m.textContent=text;m.className='msg'+(warn?' warn':'');}
function startTimer(l){if(timer)clearInterval(timer.h);document.querySelectorAll('.timer').forEach(t=>t.textContent='');
 const t=l.querySelector('.timer');const end=Date.now()+SECONDS*1000;t.className='timer';
 const tick=()=>{const s=Math.max(0,Math.round((end-Date.now())/1000));
  t.textContent=Math.floor(s/60)+':'+String(s%60).padStart(2,'0');
  if(s===0){clearInterval(timer.h);t.textContent='time’s up — next one';t.className='timer up';}};
 timer={h:setInterval(tick,1000)};tick();}
function current(l){document.querySelectorAll('li.current').forEach(x=>x.classList.remove('current'));
 l.classList.add('current');if(SECONDS>0)startTimer(l);}
async function open_(l){const r=await fetch('open?'+q(l),{method:'POST'});
 if(r.ok){if(l.dataset.state!=='problem')l.dataset.state='done';l.classList.add('done');setMsg(l,'');current(l);refresh();}
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
 if(rows.length&&done===rows.length&&!document.getElementById('getmore'))setTimeout(()=>location.reload(),1500);}
async function more(n){await fetch('claim?n='+n,{method:'POST'});location.reload();}
</script></body></html>`

// MESSAGE is the one-line message page.
const MESSAGE = `<!doctype html><html lang="en"><head><meta charset="utf-8">%%REFRESH%%<title>desk</title></head>
<body style="font:16px system-ui;margin:3em">%%TEXT%%</body></html>`

// message renders a one-line page; text is plain text and is escaped.
func message(text string, refresh int) string {
	meta := ""
	if refresh > 0 {
		meta = `<meta http-equiv="refresh" content="` + strconv.Itoa(refresh) + `">`
	}
	return strings.NewReplacer(
		"%%REFRESH%%", meta,
		"%%TEXT%%", html.EscapeString(text),
	).Replace(MESSAGE)
}

// problemOptions is the problem <select> of every row.
func problemOptions(problems []platform.Problem) string {
	var b strings.Builder
	b.WriteString(`<option value="">Problem…</option>`)
	for _, p := range problems {
		b.WriteString(`<option value="` + html.EscapeString(p.Code) + `">` + html.EscapeString(p.Label) + `</option>`)
	}
	return b.String()
}

// row renders one queue row. msg is already-escaped HTML
// (or "").
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
	b.WriteString(`</div><div class="sub"><span>` + i + `</span><select onchange="problem(this)">` + options + `</select></div>`)
	warn := ""
	if msg != "" {
		warn = " warn"
	}
	b.WriteString(`<div class="msg` + warn + `">` + msg + `</div></li>`)
	return b.String()
}

// progress renders the header progress bar. The
// percentage rounds half to even, as Python's round does.
func progress(nDone, n int) string {
	pct := pyRound(100 * float64(nDone) / float64(n))
	return `<div class="progress"><span id="prog">` + strconv.Itoa(nDone) + ` of ` + strconv.Itoa(n) + ` done</span>` +
		`<div class="bar"><i id="barfill" style="width:` + strconv.Itoa(pct) + `%"></i></div></div>`
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
	Admin         bool // admins get the /desk/admin/ link after their name (P10)
	PlatformLabel string
	Noun          string // the platform's default kind, escaped by deskPage
	Seconds       int    // the per-item timer, TimerMinutes*60
	NoteCodes     []string
	ProgressHTML  string // already HTML
	Body          string // already HTML
	OpenHelp      bool
	FooterHint    *string // nil hides the footer
	IdleMinutes   int     // N of the stopped banner, desk_idle_seconds/60 rounded down, at least 1
}

// deskPage renders the two-pane page. Body and
// ProgressHTML are already HTML; FooterHint nil hides the footer.
func deskPage(p pageParams) string {
	footer := ""
	if p.FooterHint != nil {
		footer = "<footer>Before you leave: click <b>Z</b> in the browser's toolbar, " + *p.FooterHint + ".</footer>\n"
	}
	noteCodes := "[]" // a nil NoteCodes renders as [], never null
	if p.NoteCodes != nil {
		codes, err := json.Marshal(p.NoteCodes)
		if err != nil {
			codes = []byte("[]")
		}
		noteCodes = strings.ReplaceAll(string(codes), "<", `\u003c`)
	}
	user := html.EscapeString(p.User)
	if p.Admin {
		user += ` <a href="/desk/admin/">admin</a>`
	}
	openHelp := ""
	if p.OpenHelp {
		openHelp = " open"
	}
	timerHelp := ""
	if p.Seconds > 0 {
		timerHelp = ", or until the timer reaches 0:00"
	}
	return strings.NewReplacer(
		"%%TITLE%%", html.EscapeString(p.PlatformLabel)+" desk — "+html.EscapeString(p.User),
		"%%CSS%%", CSS,
		"%%USER%%", user,
		"%%NOUN%%", html.EscapeString(p.Noun),
		"%%PROGRESS%%", p.ProgressHTML,
		"%%OPEN_HELP%%", openHelp,
		"%%BODY%%", p.Body,
		"%%TIMER_HELP%%", timerHelp,
		"%%FOOTER%%", footer,
		"%%SECONDS%%", strconv.Itoa(p.Seconds),
		"%%NOTE_CODES%%", noteCodes,
		"%%IDLE_MINUTES%%", strconv.Itoa(max(p.IdleMinutes, 1)),
	).Replace(PAGE)
}
