"""Canary application for afford.

Every defect below is planted on purpose. If afford stops finding one, the tool is
broken, not the app. Ground truth lives in GROUND_TRUTH.md.
"""

from __future__ import annotations

from fastapi import FastAPI, Form, Request
from fastapi.responses import HTMLResponse, JSONResponse, RedirectResponse, Response

app = FastAPI(title="afford canary")

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
	return request.cookies.get("sid") == "ok"


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
	if username == "admin" and password == "secret":
		response = RedirectResponse("/dashboard", status_code=303)
		response.set_cookie("sid", "ok", httponly=True, samesite="lax")
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
	return HTMLResponse(page("Dashboard", body))


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


@app.get("/form-ok", response_class=HTMLResponse)
def form_ok() -> str:
	body = """
	<h1>Valid form</h1>
	<form method="post" action="/echo">
	  <label for="name">Name</label>
	  <input id="name" name="name" type="text" required>
	  <label for="mail">Email</label>
	  <input id="mail" name="email" type="email">
	  <button type="submit">Submit</button>
	</form>
	"""
	return page("Valid form", body)


@app.post("/echo")
def echo(name: str = Form(...), email: str = Form("")) -> Response:
	return HTMLResponse(page("Received", f"<h1>Received</h1><p>{name} {email}</p>"))
