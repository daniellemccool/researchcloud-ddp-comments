"""HTML for desk_stub.py: the two-pane desk page, the one-line message pages and the admin table.

Only templates and small render helpers live here; desk_stub.py decides what to show.
Values from a platform definition (labels, upload_hint) are trusted HTML; everything a
user, the pool or the log supplies is escaped.
"""
import html
import json
import re

CSS = """:root{--ink:#1d2329;--muted:#6b7480;--line:#e3e6ea;--bg:#f6f7f9;--accent:#1f6feb;--ok:#1a7f37;--warn:#b35900}
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
li[data-state=done] .mark::before{content:"\\2713";color:var(--ok)}
li[data-state=problem] .mark::before{content:"\\26A0";color:var(--warn)}
li[data-state=todo] .mark::before{content:"\\25CB";color:var(--muted)}
.sub{display:flex;gap:8px;align-items:center;margin-top:4px;font-size:12px;color:var(--muted)}
.sub select{font:inherit;font-size:12px;max-width:150px}
.msg{font-size:12px}.msg.warn{color:var(--warn)}.timer{font-variant-numeric:tabular-nums;font-weight:600;color:var(--accent)}
.timer.up{color:var(--ok)}
.more{padding:8px 0}.more button{font:inherit;padding:6px 10px;margin-right:6px;border:1px solid var(--accent);background:#fff;color:var(--accent);border-radius:6px;cursor:pointer}
.hint{font-size:12px;color:var(--muted);margin:6px 2px}
footer{padding:10px 14px;border-top:1px solid var(--line);background:#fff8e6;font-size:12px}
#starting{flex:1;display:flex;align-items:center;justify-content:center;color:var(--muted);font-size:15px}"""

PAGE = """<!doctype html>
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
<iframe id="screen" style="flex:1 1 auto;height:100vh;border:0;min-width:0;display:none" allow="clipboard-read; clipboard-write"></iframe>
<script>
const SECONDS=%%SECONDS%%,NOTE_CODES=%%NOTE_CODES%%;let timer=null;
async function waitScreen(){try{const r=await fetch('status');if(r.ok&&(await r.json()).ready){
 const f=document.getElementById('screen');f.src='/desk/vnc/';f.style.display='';
 document.getElementById('starting').style.display='none';return;}}catch(e){}setTimeout(waitScreen,2000);}
waitScreen();
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
</script></body></html>"""

MESSAGE = """<!doctype html><html lang="en"><head><meta charset="utf-8">%%REFRESH%%<title>desk</title></head>
<body style="font:16px system-ui;margin:3em">%%TEXT%%</body></html>"""

e = html.escape  # local shorthand


def message(text, refresh=None):
    """A one-line page; `text` is plain text and is escaped."""
    meta = f'<meta http-equiv="refresh" content="{int(refresh)}">' if refresh else ""
    return MESSAGE.replace("%%REFRESH%%", meta).replace("%%TEXT%%", e(text))


def problem_options(problems):
    return '<option value="">Problem…</option>' + "".join(
        f'<option value="{e(p.code)}">{e(p.label)}</option>' for p in problems)


def row(kind, item_id, state, opened, title, msg, options, timer):
    """One queue row. `msg` is already-escaped HTML (or "")."""
    cls = ' class="done"' if opened else ""
    k, i = e(kind), e(item_id)
    return (f'<li id="r{i}"{cls} data-state="{state}" data-kind="{k}" data-id="{i}"><div class="row">'
            f'<span class="mark"></span><a href="#" onclick="open_(this.closest(\'li\'));return false">{e(title)}</a>'
            + ('<span class="timer"></span>' if timer else "") +
            f'</div><div class="sub"><span>{i}</span><select onchange="problem(this)">{options}</select></div>'
            f'<div class="msg{" warn" if msg else ""}">{msg}</div></li>')


def progress(n_done, n):
    pct = round(100 * n_done / n)
    return (f'<div class="progress"><span id="prog">{n_done} of {n} done</span>'
            f'<div class="bar"><i id="barfill" style="width:{pct}%"></i></div></div>')


def desk(*, user, platform, noun, seconds, note_codes, progress_html, body, open_help, footer_hint):
    """The two-pane page. `body` and `progress_html` are already HTML; footer_hint None hides the footer."""
    footer = ("<footer>Before you leave: click <b>Z</b> in the browser's toolbar, "
              + footer_hint + ".</footer>\n") if footer_hint is not None else ""
    codes = json.dumps(list(note_codes)).replace("<", "\\u003c")
    values = {"TITLE": f"{e(platform.label)} desk — {e(user)}", "CSS": CSS, "USER": e(user), "NOUN": e(noun),
              "PROGRESS": progress_html, "OPEN_HELP": " open" if open_help else "", "BODY": body,
              "TIMER_HELP": ", or until the timer reaches 0:00" if seconds else "", "FOOTER": footer,
              "SECONDS": str(int(seconds)), "NOTE_CODES": codes}
    # One pass: text substituted in (a pool note, say) is never scanned for placeholders again.
    return re.sub(r"%%([A-Z_]+)%%", lambda m: values[m.group(1)], PAGE)


def admin(rows, free, total, noun, pool_error=None, fourcat=True):
    """rows: (user, firefox status, mitm status, claimed, done, open, last event) per live session.
    pool_error replaces the pool count; fourcat False drops the 4CAT link."""
    pool_line = (f"pool.csv unreadable: {e(pool_error)}" if pool_error is not None
                 else f"{free} of {total} {e(noun)}s unclaimed in the pool.")
    trs = "".join(
        f"<tr><td>{e(u)}</td><td>{e(ff)}</td><td>{e(mitm)}</td><td>{c}</td><td>{d}</td><td>{o}</td><td>{e(last)}</td>"
        f'<td><a href="/desk/admin/vnc/{e(u)}/" target="_blank">watch</a></td></tr>'
        for u, ff, mitm, c, d, o, last in rows)
    return ("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>desk admin</title></head>"
            "<body style=\"font:14px system-ui;margin:2em\"><h1>Desk sessions</h1>"
            f"<p>{pool_line}</p>"
            "<table cellpadding=\"6\"><tr><th>user</th><th>firefox</th><th>mitm</th><th>claimed</th>"
            "<th>done</th><th>open</th><th>last event</th><th>screen</th></tr>" + trs + "</table>"
            + ("<p><a href=\"/\">4CAT</a></p>" if fourcat else "") + "</body></html>")
