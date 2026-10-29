"""HTML for desk_stub.py: the two-pane desk page, the one-line message pages and the admin table.

Only templates and small render helpers live here; desk_stub.py decides what to show.
Values from a platform definition (labels, upload_hint) are trusted HTML; everything a
user, the pool or the log supplies is escaped.
"""
import html
import json
import re
import time

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


ADMIN_CSS = ("body{font:14px/1.4 system-ui,sans-serif;margin:2em;max-width:70em;color:#1d2329}"
             "h2{margin:1.6em 0 .4em;border-bottom:1px solid #e3e6ea}table{border-collapse:collapse}"
             "td,th{padding:4px 8px;border-bottom:1px solid #e3e6ea;text-align:left;vertical-align:top}"
             ".notice{background:#fff8e6;border:1px solid #e8d9a8;padding:8px 12px;border-radius:6px}"
             ".warn{color:#b35900}form{display:inline}td form button{margin-right:4px}")


def admin_shell(title, inner, notice=None, back=True):
    """An admin page around already-built HTML `inner`; `notice` is plain text, shown in a box."""
    box = f'<p class="notice" role="status">{e(notice)}</p>' if notice else ""
    link = '<p><a href="/desk/admin/">back to the overview</a></p>' if back else ""
    return (f'<!doctype html><html lang="en"><head><meta charset="utf-8"><title>{e(title)}</title>'
            f'<style>{ADMIN_CSS}</style></head><body><h1>{e(title)}</h1>{box}{inner}{link}</body></html>')


def _post(action, label, confirm=None):
    ask = f' onclick="return confirm(\'{confirm}\')"' if confirm else ""
    return f'<form method="post" action="{e(action)}"><button{ask}>{e(label)}</button></form>'


def _session_row(r, fourcat):
    u = e(r["user"])
    state = "ready" if r["ready"] else e(r["step"])
    q = f"user={e(r['user'])}"
    buttons = (_post(f"/desk/admin/sessions?op=stop&{q}", "stop") +
               _post(f"/desk/admin/sessions?op=retire&{q}", "retire", f"Retire {u}?") +
               _post(f"/desk/admin/sessions?op=repair&{q}", "repair") +
               (_post(f"/desk/admin/4cat/rotate?{q}", "rotate 4CAT") if fourcat else ""))
    return (f"<tr><td>{u}</td><td>{r['index']}</td><td>{'running' if r['running'] else 'stopped'}</td>"
            f"<td>{e(r['seen'])}</td><td>{state}</td><td>{e(r['error'] or '')}</td>"
            f"<td>{e(r['firefox'])} / {e(r['mitm'])}</td><td>{r['claimed']}/{r['done']}/{r['open']}</td>"
            f'<td><a href="/desk/admin/vnc/{u}/" target="_blank">watch</a></td><td>{buttons}</td></tr>')


def admin(*, sessions, pool_line, expected, expected_warning, fourcat, notice=None):
    """The overview. sessions: dicts with user, index, running, seen, step, ready, error, firefox,
    mitm, claimed, done, open. expected: users expected but not yet visited, or None when the CO
    list could not be read (expected_warning says why). pool_line is already-escaped text."""
    trs = "".join(_session_row(r, fourcat) for r in sessions)
    if expected_warning:
        exp = f'<p class="warn">{e(expected_warning)}</p>'
    elif expected:
        exp = ("<p>expected, not yet visited:</p><ul>" + "".join(f"<li>{e(u)}</li>" for u in expected) + "</ul>")
    else:
        exp = "<p>expected, not yet visited: nobody.</p>"
    four = ('<p><a href="/">4CAT</a></p>' + _post("/desk/admin/4cat/password", "Reissue my 4CAT password") +
            ' <a href="/desk/admin/4cat/password">Show my 4CAT password</a>'
            if fourcat else "<p>4CAT is not enabled on this desk.</p>")
    inner = (
        "<h2>Queue</h2>"
        f"<p>{pool_line}</p>"
        '<form method="post" action="/desk/admin/pool" enctype="multipart/form-data">'
        '<label>Add to the pool (CSV with an id column; kind, bucket, note optional): '
        '<input type="file" name="file" accept=".csv,text/csv" required></label> <button>Upload</button></form>'
        "<h2>Sessions</h2>"
        + _post("/desk/admin/sessions?op=reconcile", "reconcile") + " "
        + _post("/desk/admin/sessions?op=prune", "prune", "Retire everyone who is no longer a member?") +
        '<table><tr><th>user</th><th>index</th><th>running</th><th>last seen</th><th>step</th>'
        '<th>last error</th><th>firefox / mitm</th><th>claimed/done/open</th><th>screen</th><th>actions</th></tr>'
        + trs + "</table>" + exp +
        "<h2>4CAT</h2>" + four +
        "<h2>Export</h2>"
        '<ul><li><a href="/desk/admin/export/captures.zip">captures.zip</a> (captures and the desk log)</li>'
        '<li><a href="/desk/admin/export/desk-log.ndjson">desk-log.ndjson</a></li></ul>'
        '<h2>Problems</h2><p><a href="/desk/admin/problems">the last 200 problems, newest first</a></p>')
    return admin_shell("Desk admin", inner, notice, back=False)


def admin_password(user, password):
    """The requesting admin's own 4CAT password, once. Both arguments are plain text."""
    return admin_shell("Your 4CAT password",
                       f"<p>Username <b>{e(user)}</b>, password:</p><p><code>{e(password)}</code></p>"
                       "<p>It is shown on this page only. Reload to see it again, or reissue a new one.</p>")


def admin_problems(events):
    """events: dicts with t (epoch), user, kind, id, code, note, newest first."""
    trs ="".join(
        f"<tr><td>{e(time.strftime('%Y-%m-%d %H:%M', time.localtime(ev['t'])))}</td><td>{e(ev['user'])}</td>"
        f"<td>{e(ev['kind'])}</td><td>{e(ev['id'])}</td><td>{e(ev['code'])}</td><td>{e(ev['note'])}</td></tr>"
        for ev in events)
    body = ("<table><tr><th>time</th><th>user</th><th>kind</th><th>id</th><th>code</th><th>note</th></tr>"
            + trs + "</table>") if events else "<p>No problems recorded.</p>"
    return admin_shell("Problems", body)
