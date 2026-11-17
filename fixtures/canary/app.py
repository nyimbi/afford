"""Canary application for afford.

Every defect below is planted on purpose. If afford stops finding one, the tool is
broken, not the app. Ground truth lives in GROUND_TRUTH.md.
"""

from __future__ import annotations

import uuid

from fastapi import FastAPI, Form, Request
from fastapi.responses import HTMLResponse, JSONResponse, RedirectResponse, Response

app = FastAPI(title="afford canary")

# Lifecycle hook calls and the run ids seen on X-Afford-Run, for the canary to read.
HOOKS: list[dict[str, str]] = []
RUN_HEADERS: set[str] = set()


@app.middleware("http")
async def record_run_header(request: Request, call_next):
	run = request.headers.get("x-afford-run")
	if run:
		RUN_HEADERS.add(run)
	return await call_next(request)

# Live sessions. Logging out removes the session server-side, so every browser
# context sharing that storage state is logged out at once, as in a real app.
SESSIONS: set[str] = set()
# Toggles the baseline round trip flips between runs.
MUTATE = {"plant": False, "fix_console": False}

NAV = """
<nav>
  <a href="/">home</a>
  <a href="/dashboard">dashboard</a>
  <a href="/dead">dead</a>
  <a href="/crash">crash</a>
  <a href="/console">console</a>
  <a href="/overflow">overflow</a>
  <a href="/unlabeled">unlabeled</a>
  <a href="/novalidate">novalidate</a>
  <a href="/broken-api">broken api</a>
  <a href="/missing">missing</a>
  <a href="/soft-404">soft 404</a>
  <a href="/old-route">old route</a>
  <a href="/dropdown">dropdown</a>
  <a href="/upload">upload</a>
  <a href="/form-ok">valid form</a>
</nav>
"""

STYLE = """
<style>
body{font:15px/1.5 system-ui,sans-serif;margin:0;padding:24px;max-width:900px}
nav{display:flex;gap:14px;flex-wrap:wrap;margin-bottom:28px}
nav a{color:#24507a}
main{border-top:1px solid #ddd;padding-top:20px}
label{display:block;margin:10px 0 4px}
input,textarea{width:100%;max-width:380px;padding:6px;border:1px solid #bbb;border-radius:4px}
button{padding:6px 12px;margin-top:12px}
.error{color:#a11}
</style>
"""


def page(title: str, body: str, script: str = "") -> str:
	return (
		f"<!doctype html><html lang='en'><head><meta charset='utf-8'>"
		f"<title>{title}</title>{STYLE}</head><body>{NAV}<main>{body}</main>{script}</body></html>"
	)


def authed(request: Request) -> bool:
	return request.cookies.get("sid") in SESSIONS


def login_form() -> str:
	return """
	<form method="post" action="/login">
	  <label for="username">Username</label>
	  <input id="username" name="username" autocomplete="username" required>
	  <label for="password">Password</label>
	  <input id="password" name="password" type="password" autocomplete="current-password" required>
	  <button type="submit">Sign in</button>
	</form>
	"""


@app.get("/", response_class=HTMLResponse)
def index() -> str:
	body = """
	<h1>afford canary</h1>
	<p>This app carries known defects so the crawler can be checked against ground truth.</p>
	<p><button id="refresh">Refresh</button> — this button has no handler.</p>
	<p><a href="#">A link that goes nowhere</a></p>
	<p><a href="/dead" target="_blank">Open the dead page in a new tab</a></p>
	<p><a href="/overflow" target="_blank">Open overflow in a new tab</a></p>
	<p><a href="/late">late render</a> · <a href="/never-ready">never ready</a> · <a href="/flaky">flaky handler</a>
	 · <a href="/reveal">reveal</a> · <a href="/shadow">shadow</a> · <a href="/frames">frames</a> · <a href="/lazy">lazy</a>
	 · <a href="/garbage">garbage</a> · <a href="/code-sample">code sample</a> · <a href="/trap">trap</a>
	 · <a href="/admin">admin</a> · <a href="/wide">wide</a> · <a href="/throttled">throttled</a></p>
	<ul>
	  <li>dashboard needs a session</li>
	  <li>crash throws on load</li>
	  <li>console logs an error</li>
	  <li>overflow is 1400px wide</li>
	</ul>
	"""
	return page("Canary home", body)


@app.get("/login", response_class=HTMLResponse)
def login_page() -> str:
	return page("Sign in", "<h1>Sign in</h1>" + login_form())


@app.post("/login")
def login(username: str = Form(...), password: str = Form(...)) -> Response:
	if (username, password) in {("admin", "secret"), ("viewer", "view")}:
		response = RedirectResponse("/dashboard", status_code=303)
		sid = uuid.uuid4().hex
		SESSIONS.add(sid)
		response.set_cookie("sid", sid, httponly=True, samesite="lax")
		response.set_cookie("role", username, samesite="lax")
		return response
	return HTMLResponse(
		page("Sign in", "<h1>Sign in</h1><p class='error'>Invalid credentials</p>" + login_form()),
		status_code=401,
	)


@app.get("/dashboard", response_class=HTMLResponse)
def dashboard(request: Request) -> Response:
	if not authed(request):
		return RedirectResponse("/login", status_code=303)
	body = """
	<h1>Dashboard</h1>
	<p>Create an order. The endpoint is broken on purpose: every valid submission is a 500.</p>
	<p>
	  <button type="button" id="delete-order">Delete order</button>
	  <button type="button" id="publish">Publish report</button>
	  <button type="button" id="signout"
	          onclick="fetch('/session', {method: 'DELETE'}).then(() => { location.href = '/login'; })">Sign out</button>
	</p>
	<form method="post" action="/orders">
	  <label for="email">Customer email</label>
	  <input id="email" name="customer_email" type="email" required>
	  <label for="amount">Amount</label>
	  <input id="amount" name="amount" type="number" min="1" max="9999">
	  <label for="notes">Notes</label>
	  <textarea id="notes" name="notes"></textarea>
	  <button type="submit">Create order</button>
	</form>
	"""
	if request.cookies.get("role") == "admin":
		body += "<p><button type='button' onclick=\"document.title='admin'\">Admin-only action</button></p>"
	return HTMLResponse(page("Dashboard", body))


@app.delete("/session")
def end_session(request: Request) -> Response:
	SESSIONS.discard(request.cookies.get("sid", ""))
	return JSONResponse({"ok": True})


@app.post("/orders")
def orders(customer_email: str = Form(...), amount: int = Form(0), notes: str = Form("")) -> Response:
	# Planted defect: the write path is broken and returns 500 for every valid input.
	return JSONResponse({"detail": "order service unavailable"}, status_code=500)


@app.get("/novalidate", response_class=HTMLResponse)
def novalidate(request: Request) -> Response:
	if not authed(request):
		return RedirectResponse("/login", status_code=303)
	body = """
	<h1>Queue a job</h1>
	<p>The submit button sets formnovalidate, so the required field is decorative.</p>
	<form method="post" action="/queue">
	  <label for="job">Job name</label>
	  <input id="job" name="job" type="text" required>
	  <button type="submit" formnovalidate>Continue</button>
	</form>
	"""
	return HTMLResponse(page("Queue a job", body))


@app.post("/queue")
def queue(job: str = Form("")) -> Response:
	return HTMLResponse(page("Queued", f"<h1>Queued</h1><p>job={job or 'empty'}</p>"))


@app.get("/dead", response_class=HTMLResponse)
def dead() -> str:
	body = """
	<h1>Dead controls</h1>
	<p><button id="nothing">Recalculate</button> — no handler, no effect.</p>
	<p><button id="opens" onclick="document.getElementById('panel').hidden = false">Show panel</button></p>
	<div id="panel" hidden><p>Panel content.</p></div>
	"""
	return page("Dead controls", body)


@app.get("/crash", response_class=HTMLResponse)
def crash() -> str:
	body = "<h1>Crash</h1><p>This page throws after paint.</p>"
	script = "<script>setTimeout(function(){ throw new Error('canary boom'); }, 60);</script>"
	return page("Crash", body, script)


@app.get("/console", response_class=HTMLResponse)
def console() -> str:
	body = "<h1>Console error</h1><p>Nothing visibly wrong here.</p>"
	if MUTATE["fix_console"] and not MUTATE["plant"]:
		script = ""
	elif MUTATE["plant"]:
		script = "<script>console.error('canary: second widget failed');</script>"
	else:
		script = "<script>console.error('canary: widget failed to load');</script>"
	return page("Console error", body, script)


@app.get("/overflow", response_class=HTMLResponse)
def overflow() -> str:
	body = """
	<h1>Overflow</h1>
	<div style="width:1400px;background:#eef;padding:8px">
	  This block is 1400px wide, wider than the viewport.
	</div>
	"""
	return page("Overflow", body)


@app.get("/unlabeled", response_class=HTMLResponse)
def unlabeled() -> str:
	body = """
	<h1>Unlabeled control</h1>
	<form method="get" action="/unlabeled">
	  <input type="text" name="nickname">
	  <button type="submit">Save</button>
	</form>
	"""
	return page("Unlabeled control", body)


@app.get("/broken-api", response_class=HTMLResponse)
def broken_api(request: Request) -> Response:
	if not authed(request):
		return RedirectResponse("/login", status_code=303)
	body = """
	<h1>Broken API</h1>
	<p><button id="load" onclick="fetch('/api/boom').then(r => r.text())">Load data</button></p>
	<div id="out"></div>
	"""
	return page("Broken API", body)


@app.get("/api/boom")
def api_boom() -> Response:
	return JSONResponse({"detail": "upstream exploded"}, status_code=500)


@app.get("/soft-404", response_class=HTMLResponse)
def soft_404() -> str:
	# Planted defect: a 200 response whose body says the page is missing. This is
	# what an SPA router does when it renders its "not found" view.
	body = "<h1>Page not found</h1><p>The item you asked for does not exist.</p>"
	return page("Missing thing", body)


@app.get("/old-route")
def old_route() -> Response:
	# Planted defect: a linked route redirects somewhere else, so the URL a user
	# shared does not show the page it names.
	return RedirectResponse("/dashboard", status_code=302)


@app.get("/dropdown", response_class=HTMLResponse)
def dropdown() -> str:
	# Three planted defects: a UUID shown as an option label, UUID option values,
	# and an enabled select with nothing to choose.
	body = """
	<h1>Dropdowns</h1>
	<form method="get" action="/dropdown">
	  <label for="owner">Owner</label>
	  <select id="owner" name="owner">
	    <option value="1">Alice</option>
	    <option value="3f2504e0-4f89-11d3-9a0c-0305e82c3301">3f2504e0-4f89-11d3-9a0c-0305e82c3301</option>
	  </select>
	  <label for="team">Team</label>
	  <select id="team" name="team">
	    <option value="7c9e6679-7425-40de-944b-e07fc1f90ae7">Support</option>
	    <option value="9b2f1c4a-8e3d-4b6a-9f12-5d7c8e9a0b1c">Billing</option>
	  </select>
	  <label for="region">Region</label>
	  <select id="region" name="region">
	    <option value="">Choose a region</option>
	  </select>
	  <button type="submit">Apply</button>
	</form>
	"""
	return page("Dropdowns", body)


@app.get("/upload", response_class=HTMLResponse)
def upload() -> str:
	# Planted defect: the change handler throws, which proves afford attached a
	# real file. It also means the upload path is not silent.
	body = """
	<h1>Upload</h1>
	<p>Choose a file. The handler throws on purpose.</p>
	<label for="attachment">Attachment</label>
	<input id="attachment" name="attachment" type="file"
	       accept="image/*,.pdf"
	       onchange="throw new Error('canary upload handler')">
	"""
	return page("Upload", body)


@app.get("/form-ok", response_class=HTMLResponse)
def form_ok() -> str:
	body = """
	<h1>Valid form</h1>
	<form method="post" action="/echo">
	  <label for="name">Name</label>
	  <input id="name" name="name" type="text" required>
	  <label for="mail">Email</label>
	  <input id="mail" name="email" type="email">
	  <label for="attachment">Attachment</label>
	  <input id="attachment" name="attachment" type="file">
	  <button type="submit">Submit</button>
	</form>
	"""
	return page("Valid form", body)


@app.post("/echo")
def echo(name: str = Form(...), email: str = Form("")) -> Response:
	return HTMLResponse(page("Received", f"<h1>Received</h1><p>{name} {email}</p>"))


@app.get("/_canary/mutate")
def mutate(plant: int = 0, fix_console: int = 0) -> Response:
	MUTATE["plant"] = bool(plant)
	MUTATE["fix_console"] = bool(fix_console)
	return JSONResponse(MUTATE)


@app.get("/late", response_class=HTMLResponse)
def late() -> str:
	# The shell paints at once; the real content arrives 1.6s later. A heuristic
	# that watches the node count settles on the shell and never sees the button.
	body = """
	<h1>Late render</h1>
	<div id="slot"><p>loading…</p></div>
	"""
	script = """<script>
	setTimeout(function () {
	  document.getElementById('slot').innerHTML =
	    '<button id="late-dead">Late dead</button><span id="late-ready">ready</span>';
	}, 1600);
	</script>"""
	return page("Late render", body, script)


@app.get("/never-ready", response_class=HTMLResponse)
def never_ready() -> str:
	return page("Never ready", "<h1>Never ready</h1><p>The readiness marker is never rendered.</p>")


FLAKY = {"loads": 0}


@app.get("/flaky", response_class=HTMLResponse)
def flaky() -> Response:
	# Every second load of this route ships its handlers missing. A probe that
	# lands on a broken load sees a dead control; the confirming probe is the very
	# next load and sees a working one. Two buttons guarantee one probe lands on
	# a broken load whatever the parity of the loads before it.
	FLAKY["loads"] += 1
	handler = "" if FLAKY["loads"] % 2 == 0 else " onclick=\"document.getElementById('out').textContent='saved'\""
	body = f"""
	<h1>Flaky handler</h1>
	<button id="flaky-a"{handler}>Flaky save A</button>
	<button id="flaky-b"{handler}>Flaky save B</button>
	<p id="out"></p>
	"""
	return HTMLResponse(page("Flaky handler", body))


@app.get("/reveal", response_class=HTMLResponse)
def reveal() -> str:
	# The dead item exists only after the menu opens, so a scan of the loaded
	# page cannot see it.
	body = """
	<h1>Reveal</h1>
	<button id="menu" onclick="document.getElementById('m').hidden = false">Open menu</button>
	<div id="m" hidden><button id="menu-dead">Menu dead item</button></div>
	"""
	return page("Reveal", body)


@app.get("/shadow", response_class=HTMLResponse)
def shadow() -> str:
	body = """
	<h1>Shadow DOM</h1>
	<x-card></x-card>
	"""
	script = """<script>
	customElements.define('x-card', class extends HTMLElement {
	  connectedCallback() {
	    const root = this.attachShadow({mode: 'open'});
	    root.innerHTML = '<button id="sh">Shadow dead</button>';
	  }
	});
	</script>"""
	return page("Shadow DOM", body, script)


@app.get("/frames", response_class=HTMLResponse)
def frames() -> str:
	# A same-origin frame holds a dead button afford must reach. The foreign
	# frame is another origin (localhost, not 127.0.0.1) and must stay untouched.
	body = """
	<h1>Frames</h1>
	<iframe id="inner" src="/frame-inner" width="400" height="120"></iframe>
	<iframe id="foreign" src="http://localhost:8099/frame-foreign" width="400" height="120"></iframe>
	"""
	return page("Frames", body)


@app.get("/frame-inner", response_class=HTMLResponse)
def frame_inner() -> str:
	return "<!doctype html><html><body><button id='fd'>Frame dead</button></body></html>"


@app.get("/frame-foreign", response_class=HTMLResponse)
def frame_foreign() -> str:
	return "<!doctype html><html><body><button id='ff'>Foreign dead</button></body></html>"


@app.get("/lazy", response_class=HTMLResponse)
def lazy() -> str:
	# The button is appended only when the viewer scrolls to the bottom.
	body = """
	<h1>Lazy content</h1>
	<div style="height:3200px;background:linear-gradient(#fff,#ddd)">tall</div>
	<div id="tail"></div>
	"""
	script = """<script>
	let done = false;
	window.addEventListener('scroll', function () {
	  if (!done && window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 40) {
	    done = true;
	    document.getElementById('tail').innerHTML = '<button id="lz">Lazy dead</button>';
	  }
	});
	</script>"""
	return page("Lazy content", body, script)


@app.get("/garbage", response_class=HTMLResponse)
def garbage() -> str:
	# Five planted page-level defects: leaked undefined, a broken image with no
	# alt, an unhandled rejection, a clickable div the keyboard cannot reach, and
	# a button whose focus ring was styled away.
	body = """
	<h1>Garbage</h1>
	<p>Welcome back, undefined. Your balance is NaN.</p>
	<img src="/missing-image.png" width="40" height="40">
	<div id="fake" onclick="document.title='x'" style="cursor:pointer;padding:6px;border:1px solid #888;width:120px">Fake button</div>
	<style>#ring { outline: none; border: 1px solid #888; background: #fff; }
	#ring:focus, #ring:focus-visible { outline: none; }</style>
	<button id="ring" type="button" onclick="document.title='y'">No ring</button>
	"""
	script = "<script>Promise.reject(new Error('canary rejection'));</script>"
	return page("Garbage", body, script)


@app.get("/code-sample", response_class=HTMLResponse)
def code_sample() -> str:
	# Control: the same tokens inside code and pre are documentation, not leakage.
	body = """
	<h1>Code sample</h1>
	<p>Calling <code>fetch()</code> may give <code>undefined</code> or <code>NaN</code>.</p>
	<pre>const x = undefined; // NaN</pre>
	"""
	return page("Code sample", body)


@app.get("/trap", response_class=HTMLResponse)
def trap() -> str:
	body = """
	<h1>Trap</h1>
	<a href="/">before</a>
	<input id="t" aria-label="Trap field" type="text"
	       onkeydown="if (event.key === 'Tab') { event.preventDefault(); this.focus(); }">
	<a href="/dead">after</a>
	"""
	return page("Trap", body)


@app.get("/_canary/hook")
def hook(event: str, run: str = "") -> Response:
	HOOKS.append({"event": event, "run": run})
	return JSONResponse({"ok": True})


@app.get("/_canary/hooks")
def hooks() -> Response:
	return JSONResponse({"hooks": HOOKS, "run_headers": sorted(RUN_HEADERS)})


@app.get("/admin", response_class=HTMLResponse)
def admin(request: Request) -> Response:
	# Planted: the menu hides this from viewers, the server does not enforce it.
	if not authed(request):
		return RedirectResponse("/login", status_code=303)
	return page("Admin", "<h1>Admin</h1><p>User management.</p>")


@app.get("/wide", response_class=HTMLResponse)
def wide() -> str:
	# Fits at 1280, overflows at phone width.
	return page("Wide", "<h1>Wide</h1><div style='width:700px;background:#eee'>fixed-width block</div>")


@app.get("/orphan", response_class=HTMLResponse)
def orphan() -> str:
	return page("Orphan", "<h1>Orphan</h1><p>Nothing links here.</p>")


@app.get("/sitemap.xml")
def sitemap() -> Response:
	body = ("<?xml version='1.0'?><urlset xmlns='http://www.sitemaps.org/schemas/sitemap/0.9'>"
		"<url><loc>http://127.0.0.1:8099/</loc></url><url><loc>http://127.0.0.1:8099/orphan</loc></url></urlset>")
	return Response(body, media_type="application/xml")


THROTTLE = {"hits": 0}


@app.get("/throttled", response_class=HTMLResponse)
def throttled() -> Response:
	# 429 with Retry-After on the first load of each run; fine on the retry.
	THROTTLE["hits"] += 1
	if THROTTLE["hits"] % 2 == 1:
		return HTMLResponse("slow down", status_code=429, headers={"Retry-After": "1"})
	return HTMLResponse(page("Throttled", "<h1>Throttled</h1><p>Served after backoff.</p>"))
