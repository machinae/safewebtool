//go:build integration

// Package integration contains browser-level integration tests that run against
// a real Chrome instance and a local HTTP server.
//
// Tab strategy:
//   - Single-tab tests call `tab := openTab(t, ctx, "/path")`, which navigates
//     a *shared* tab to the fixture and returns it. One Chrome tab handles
//     the whole suite, keeping tab count bounded and matching the pre-refactor
//     pattern where tests shared the active tab via in-place navigation.
//   - Multi-tab tests (that genuinely need several simultaneous tabs) call
//     `newTab(t, ctx, url)` to create additional tabs with per-test cleanup.
//   - Tests run serially. Do not add t.Parallel() without reworking the
//     shared-tab model.
//
// Adding a new test:
//  1. Define an HTML constant (e.g. const myPageHTML = `...`) in a new or existing test file.
//  2. Register it in the pages map below (e.g. "/my-page": myPageHTML).
//  3. Call `tab := openTab(t, ctx, "/my-page")` and use tab.Snapshot, tab.Click, etc.
//
// Run with: go test -tags integration ./test/integration/ -v -count=1
package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/machinae/safewebtool/browser"
)

var (
	b         *browser.Browser
	server    *httptest.Server
	sharedTab *browser.Tab
)

const integrationTestTimeout = 30 * time.Second

// pages maps route paths to HTML content. Add new fixtures here.
var pages = map[string]string{
	"/simple":     simpleHTML,
	"/controlled": controlledHTML,
	"/spa":        spaHTML,
	"/extract":    extractHTML,
	"/sections":   sectionsHTML,
	"/dynamic":    dynamicHTML,
	"/obscured":   obscuredHTML,
	"/rerender":   rerenderHTML,
	// Same fixture on a second route: the navigation-invalidates-ids test
	// needs a new document whose content is identical to the old one.
	"/rerender-2": rerenderHTML,
	// Soft-nav fixture: a pushState route that swaps in a skeleton then loads
	// real content async (see click_nav_test.go).
	"/nav-spa": navSpaHTML,
	// Same-origin iframe fixtures: a parent that embeds /iframe-child (see
	// iframe_test.go).
	"/iframe-parent": iframeParentHTML,
	"/iframe-child":  iframeChildHTML,
	// LinkedIn-shaped fixture: non-scrolling root, fixed header, nested scroll
	// container (see scroll_test.go).
	"/nested-scroll": nestedScrollHTML,
	// Keyboard fixture: two inputs for focus traversal and a keydown log
	// (see type_test.go).
	"/keys": keysHTML,
	// Canvas-app-shaped fixture: an off-screen input that grabs focus on
	// load, like Google Docs' hidden capture iframe (see type_test.go).
	"/capture": captureHTML,
	// Below-the-fold click target (see click_offscreen_test.go).
	"/longpage": longPageHTML,
}

func TestMain(m *testing.M) {
	// Start local HTTP server serving embedded HTML fixtures.
	mux := http.NewServeMux()
	for path, content := range pages {
		body := content // capture for closure
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		})
	}
	// /slow sleeps before responding. Used by the concurrency tests to hold
	// one tab's worker while another tab is exercised.
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, slowHTML)
	})
	server = httptest.NewServer(mux)

	// Connect to Chrome once for all tests.
	b = browser.New()
	if err := b.Connect(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to Chrome: %v\n", err)
		server.Close()
		os.Exit(1)
	}

	// Open one shared tab that single-tab tests navigate in place.
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	tab, _, err := b.OpenTab(setupCtx, "about:blank")
	setupCancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open shared tab: %v\n", err)
		b.Close()
		server.Close()
		os.Exit(1)
	}
	sharedTab = tab

	code := m.Run()

	teardownCtx, teardownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = b.CloseTab(teardownCtx, sharedTab.ID())
	teardownCancel()

	b.Close()
	server.Close()
	os.Exit(code)
}

// pageURL returns the full URL for a fixture path (e.g. "/simple").
func pageURL(path string) string {
	return server.URL + path
}

// openTab navigates the shared tab to the given fixture path and returns it.
// Single-tab tests should use this — one Chrome tab handles the whole suite.
//
// The tab is never activated. It is created in the background and stays
// there, so the suite runs against an occluded renderer: focus emulation
// (initPage) is what keeps frames flowing, and without it synthetic input
// queues past our settle waits and clicks read as silent no-ops. The pointer
// tests are the regression check for that.
func openTab(t *testing.T, ctx context.Context, path string) *browser.Tab {
	t.Helper()
	if _, err := sharedTab.Open(ctx, pageURL(path)); err != nil {
		t.Fatalf("navigate shared tab to %s: %v", path, err)
	}
	return sharedTab
}

// openTwoTabsParallel opens two tabs concurrently and returns them once both
// have been created. Both OpenTab calls fire from separate goroutines so the
// coordinator has to service them in parallel. Cleanup is registered for
// both tabs on test completion.
func openTwoTabsParallel(t *testing.T, ctx context.Context, urlA, urlB string) (*browser.Tab, *browser.Tab) {
	t.Helper()
	type result struct {
		tab *browser.Tab
		err error
	}
	chA := make(chan result, 1)
	chB := make(chan result, 1)
	go func() {
		tab, _, err := b.OpenTab(ctx, urlA)
		chA <- result{tab, err}
	}()
	go func() {
		tab, _, err := b.OpenTab(ctx, urlB)
		chB <- result{tab, err}
	}()
	resA := <-chA
	resB := <-chB

	closeIfLive := func(tab *browser.Tab) {
		if tab == nil {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = b.CloseTab(closeCtx, tab.ID())
	}
	if resA.err != nil {
		closeIfLive(resB.tab)
		t.Fatalf("OpenTab %s: %v", urlA, resA.err)
	}
	if resB.err != nil {
		closeIfLive(resA.tab)
		t.Fatalf("OpenTab %s: %v", urlB, resB.err)
	}
	t.Cleanup(func() {
		closeIfLive(resA.tab)
		closeIfLive(resB.tab)
	})
	return resA.tab, resB.tab
}

// newTab creates an additional Chrome tab at the given URL. Intended only
// for tests that genuinely need multiple simultaneous tabs. The tab is
// closed when the test finishes so it does not leak into later tests.
func newTab(t *testing.T, ctx context.Context, rawURL string) *browser.Tab {
	t.Helper()
	tab, _, err := b.OpenTab(ctx, rawURL)
	if err != nil {
		t.Fatalf("OpenTab %s: %v", rawURL, err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.CloseTab(closeCtx, tab.ID()); err != nil {
			t.Logf("CloseTab %s: %v", tab.ID(), err)
		}
	})
	return tab
}

// findElement returns the backendNodeId string for the first element matching
// the given substring in a snapshot string. Snapshot lines look like: [12345] role "name"
func findElement(t *testing.T, snapshot, match string) string {
	t.Helper()
	for _, line := range strings.Split(snapshot, "\n") {
		if strings.Contains(line, match) {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "[") {
				continue
			}
			end := strings.Index(line, "]")
			if end < 0 {
				continue
			}
			return line[1:end]
		}
	}
	t.Fatalf("no element matching %q found in snapshot:\n%s", match, snapshot)
	return ""
}

func findTabByURL(t *testing.T, tabs []browser.TabInfo, url string) browser.TabInfo {
	t.Helper()
	want := strings.SplitN(url, "#", 2)[0]
	for _, tab := range tabs {
		got := strings.SplitN(tab.URL, "#", 2)[0]
		if got == want {
			return tab
		}
	}
	t.Fatalf("no tab with URL %q found in tabs: %+v", url, tabs)
	return browser.TabInfo{}
}

const simpleHTML = `<!DOCTYPE html>
<html>
<head><title>Simple Test</title></head>
<body>
	<h1>Hello</h1>
	<button id="btn" onclick="document.getElementById('output').textContent = 'clicked'">Click me</button>
	<div id="output"></div>
</body>
</html>`

const controlledHTML = `<!DOCTYPE html>
<html>
<head>
	<title>Controlled Form</title>
	<meta charset="utf-8">
</head>
<body>
	<div id="app"></div>
	<script>
	(() => {
		const state = {
			draft: "",
			priority: "Normal",
			notes: "",
			ownerEmail: "",
			deliveryMode: "Standard",
			notifyTeam: false,
			fileName: "",
			items: [],
			status: "Idle"
		};

		function summaryText() {
			return "Task: " + (state.draft || "Nothing yet") +
				" | Priority: " + state.priority +
				" | Notes: " + (state.notes || "None") +
				" | Owner: " + (state.ownerEmail || "Unassigned") +
				" | Delivery: " + state.deliveryMode +
				" | Notify: " + (state.notifyTeam ? "Yes" : "No");
		}

		function render() {
			const itemsHTML = state.items.map(item => "<li>" + escapeHTML(item) + "</li>").join("");
			document.getElementById("app").innerHTML =
				"<main>" +
					"<h1>Task board</h1>" +
					"<label for=\"priority-select\">Priority</label>" +
					"<select id=\"priority-select\">" +
						"<option" + (state.priority === "Low" ? " selected" : "") + ">Low</option>" +
						"<option" + (state.priority === "Normal" ? " selected" : "") + ">Normal</option>" +
						"<option" + (state.priority === "Urgent" ? " selected" : "") + ">Urgent</option>" +
					"</select>" +
					"<label for=\"task-input\">Task name</label>" +
					"<input id=\"task-input\" type=\"text\" value=\"" + escapeHTML(state.draft) + "\" autocomplete=\"off\">" +
					"<label for=\"notes-input\">Notes</label>" +
					"<textarea id=\"notes-input\">" + escapeHTML(state.notes) + "</textarea>" +
					"<label for=\"owner-email\">Owner email</label>" +
					"<input id=\"owner-email\" type=\"email\" value=\"" + escapeHTML(state.ownerEmail) + "\" autocomplete=\"off\">" +
					"<fieldset>" +
						"<legend>Delivery mode</legend>" +
						"<label><input type=\"radio\" name=\"delivery-mode\" value=\"Standard\"" + (state.deliveryMode === "Standard" ? " checked" : "") + ">Standard delivery</label>" +
						"<label><input type=\"radio\" name=\"delivery-mode\" value=\"Expedite\"" + (state.deliveryMode === "Expedite" ? " checked" : "") + ">Expedite delivery</label>" +
					"</fieldset>" +
					"<label><input id=\"notify-team\" type=\"checkbox\"" + (state.notifyTeam ? " checked" : "") + ">Notify team</label>" +
					"<label for=\"attachment\">Attachment</label>" +
					"<input id=\"attachment\" type=\"file\">" +
					"<div id=\"file-status\">" + escapeHTML(state.fileName || "No file selected") + "</div>" +
					"<button id=\"save-btn\" " + (state.draft.trim() ? "" : "disabled") + ">Add task</button>" +
					"<div role=\"status\" aria-live=\"polite\">" + escapeHTML(state.status) + "</div>" +
					"<p>Preview: " + escapeHTML(summaryText()) + "</p>" +
					"<ul>" + itemsHTML + "</ul>" +
				"</main>";
		}

		function submit() {
			const value = state.draft.trim();
			if (!value) return;
			const item = "[" + state.priority + "] " + value +
				" / " + state.deliveryMode +
				" / notify=" + (state.notifyTeam ? "yes" : "no") +
				" / owner=" + (state.ownerEmail || "none") +
				" / notes=" + (state.notes || "none");
			state.items.push(item);
			state.status = "Added " + item;
			state.draft = "";
			state.notes = "";
			state.ownerEmail = "";
			state.deliveryMode = "Standard";
			state.notifyTeam = false;
			render();
			document.getElementById("task-input").focus();
		}

		function escapeHTML(value) {
			return value
				.replace(/&/g, "&amp;")
				.replace(/</g, "&lt;")
				.replace(/>/g, "&gt;")
				.replace(/"/g, "&quot;");
		}

		document.addEventListener("input", (event) => {
			if (event.target.id === "task-input") {
				state.draft = event.target.value;
				state.status = state.draft ? "Draft ready" : "Idle";
			} else if (event.target.id === "notes-input") {
				state.notes = event.target.value;
				state.status = state.notes ? "Notes updated" : "Notes cleared";
			} else if (event.target.id === "owner-email") {
				state.ownerEmail = event.target.value;
				state.status = state.ownerEmail ? "Owner updated" : "Owner cleared";
			} else {
				return;
			}
			render();
			if (event.target.id === "task-input") {
				document.getElementById("task-input").focus();
			}
			if (event.target.id === "notes-input") {
				document.getElementById("notes-input").focus();
			}
			if (event.target.id === "owner-email") {
				document.getElementById("owner-email").focus();
			}
		});

		document.addEventListener("change", (event) => {
			if (event.target.id === "priority-select") {
				state.priority = event.target.value;
				state.status = "Priority set to " + state.priority;
			} else if (event.target.name === "delivery-mode") {
				state.deliveryMode = event.target.value;
				state.status = "Delivery set to " + state.deliveryMode;
			} else if (event.target.id === "notify-team") {
				state.notifyTeam = event.target.checked;
				state.status = state.notifyTeam ? "Notifications enabled" : "Notifications disabled";
			} else if (event.target.id === "attachment") {
				state.fileName = event.target.files.length > 0 ? event.target.files[0].name : "";
				state.status = state.fileName ? "File attached: " + state.fileName : "File removed";
			} else {
				return;
			}
			render();
			if (event.target.id === "priority-select") {
				document.getElementById("priority-select").focus();
			}
		});

		document.addEventListener("click", (event) => {
			if (event.target.id === "save-btn") {
				submit();
			}
		});

		document.addEventListener("keydown", (event) => {
			if (event.target.id === "task-input" && event.key === "Enter") {
				event.preventDefault();
				submit();
			}
		});

		render();
	})();
	</script>
</body>
</html>`

const dynamicHTML = `<!DOCTYPE html>
<html>
<head>
	<title>Dynamic Test</title>
	<meta charset="utf-8">
	<style>
		.card-actions { display: none; }
		.card.hovered .card-actions { display: block; }
		#app { min-height: 200vh; }
	</style>
</head>
<body>
	<div id="app">
		<h1>Dashboard</h1>

		<!-- Hover reveal: actions visible only on hover (for Hover test) -->
		<div class="card" id="project-card">
			<h2>Project Alpha</h2>
			<p>Status: active</p>
			<div class="card-actions">
				<button id="archive-btn">Archive</button>
				<button id="delete-btn">Delete</button>
			</div>
		</div>

		<!-- Re-render section: clicking refresh destroys and recreates nodes (for Stale node test) -->
		<div id="feed">
			<h2>Activity Feed</h2>
			<ul id="feed-list">
				<li id="feed-item-1">Deploy v1.0 completed</li>
				<li id="feed-item-2">Test suite passed</li>
				<li id="feed-item-3">PR #42 merged</li>
			</ul>
			<button id="refresh-feed">Refresh Feed</button>
		</div>

		<!-- Shadow DOM: web component with encapsulated internals (for Shadow DOM test) -->
		<shadow-card></shadow-card>

		<!-- Counter: changes on interaction, resets on reload (for Reload test) -->
		<div id="counter-section">
			<span id="counter-value">0</span>
			<button id="increment-btn">Increment</button>
		</div>
	</div>

	<script>
	(() => {
		// Shadow DOM web component.
		class ShadowCard extends HTMLElement {
			connectedCallback() {
				const shadow = this.attachShadow({ mode: "open" });
				shadow.innerHTML =
					'<div>' +
						'<h2>Shadow Heading</h2>' +
						'<p id="shadow-status">Idle</p>' +
						'<button id="shadow-btn">Shadow Action</button>' +
					'</div>';
				shadow.getElementById("shadow-btn").addEventListener("click", () => {
					shadow.getElementById("shadow-status").textContent = "Shadow clicked";
				});
			}
		}
		customElements.define("shadow-card", ShadowCard);

		// Refresh feed: destroys all existing list items and creates new ones.
		document.getElementById("refresh-feed").addEventListener("click", () => {
			const list = document.getElementById("feed-list");
			list.innerHTML =
				'<li id="feed-item-4">Hotfix v1.0.1 deployed</li>' +
				'<li id="feed-item-5">Monitoring alert cleared</li>';
		});

		// Hover reveal: mouseenter adds .hovered permanently. Persistent so a
		// later layout shift can't un-hover the card and hide the buttons
		// again — that made the test flaky.
		document.getElementById("project-card").addEventListener("mouseenter", (e) => {
			e.currentTarget.classList.add("hovered");
		});

		// Archive button (revealed on hover).
		document.addEventListener("click", (e) => {
			if (e.target.id === "archive-btn") {
				const card = document.getElementById("project-card");
				card.querySelector("p").textContent = "Status: archived";
			}
		});

		// Counter.
		let count = 0;
		document.getElementById("increment-btn").addEventListener("click", () => {
			count++;
			document.getElementById("counter-value").textContent = String(count);
		});
	})();
	</script>
</body>
</html>`

const extractHTML = `<!DOCTYPE html>
<html>
<head><title>Extract Test</title><meta charset="utf-8"></head>
<body>
	<header><nav><a href="/home">Home</a></nav></header>
	<main>
		<h1>Main Heading</h1>
		<p>This is the main content paragraph.</p>
		<a href="https://example.com">Example Link</a>
		<ul>
			<li>Item one</li>
			<li>Item two</li>
		</ul>
	</main>
	<footer><p>Footer content here</p></footer>
</body>
</html>`

// sectionsHTML is a multi-section article with nested headings. History's
// h2 is wrapped in a div (the markup that broke the old AX-subtree walk),
// proving the document-order slice survives wrapping. Its content and the
// deeper Etymology h3 are siblings of the heading, not children. Each
// section carries a link: ModeDefault prunes a structural container with no
// interactive descendant, so a heading-only page would snapshot empty (a
// separate snapshot quirk); the links match how real articles render.
const sectionsHTML = `<!DOCTYPE html>
<html>
<head><title>Coffee</title><meta charset="utf-8"></head>
<body>
	<main>
		<h1>Coffee</h1>
		<p>Coffee is a brewed drink prepared from roasted beans. <a href="/wiki/Bean">More</a></p>
		<div class="mw-heading"><h2>History</h2></div>
		<p>The story of coffee dates to the 15th century. <a href="/wiki/History_of_coffee">History of coffee</a></p>
		<h3>Etymology</h3>
		<p>The word entered English in 1582. <a href="/wiki/Etymology">Word origin</a></p>
		<div class="mw-heading"><h2>Cultivation</h2></div>
		<p>Coffee is grown in over seventy countries. <a href="/wiki/Cultivation">Growing coffee</a></p>
	</main>
</body>
</html>`

const spaHTML = `<!DOCTYPE html>
<html>
<head>
	<title>Single Page App</title>
	<meta charset="utf-8">
</head>
<body>
	<div id="app"></div>
	<script>
	(() => {
		const state = {
			route: "home"
		};

		function setRoute(route, push) {
			state.route = route;
			if (push) {
				history.pushState({route}, "", "#" + route);
			}
			render();
		}

		function routeBody() {
			if (state.route === "settings") {
				return "<section>" +
					"<h1>Settings</h1>" +
					"<p>Notifications are enabled.</p>" +
					"<button id=\"save-settings\">Save preferences</button>" +
				"</section>";
			}

			return "<section>" +
				"<h1>Home</h1>" +
				"<p>Dashboard overview.</p>" +
			"</section>";
		}

		function render() {
			document.getElementById("app").innerHTML =
				"<main>" +
					"<nav aria-label=\"Primary\">" +
						"<button id=\"nav-home\">Home</button>" +
						"<button id=\"nav-settings\">Settings</button>" +
					"</nav>" +
					"<div role=\"status\" aria-live=\"polite\">Route: " + state.route + "</div>" +
					routeBody() +
				"</main>";
		}

		document.addEventListener("click", (event) => {
			if (event.target.id === "nav-home") {
				setRoute("home", true);
			}
			if (event.target.id === "nav-settings") {
				setRoute("settings", true);
			}
		});

		window.addEventListener("popstate", (event) => {
			const route = event.state && event.state.route ? event.state.route : "home";
			setRoute(route, false);
		});

		if (location.hash === "#settings") {
			state.route = "settings";
		}
		history.replaceState({route: state.route}, "", "#" + state.route);
		render();
	})();
	</script>
</body>
</html>`

const slowHTML = `<!DOCTYPE html>
<html>
<head><title>Slow Page</title></head>
<body>
	<h1>Slow Page</h1>
	<p>This page is served by a handler that sleeps before responding.</p>
</body>
</html>`
