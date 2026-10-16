# FAQ

## Does webtool support headless mode?

webtool doesn't launch Chrome. It connects to an instance that is already running, so if you start Chrome in headless mode yourself, webtool can connect to it.

webtool intentionally avoids managing a headless Chrome for you, because connecting to your real Chrome session works better for agents:

- **No bot detection.** Automated Chrome instances (launched with `--remote-debugging-port` or through Playwright or Puppeteer) set `navigator.webdriver=true`, which triggers CAPTCHAs and blocks on many websites. Your normal Chrome session doesn't set this flag, and webtool keeps that advantage by connecting to it instead of launching its own instance.
- **Real logins.** Your Chrome already has your cookies, sessions, and saved passwords. There are no login flows to automate.
- **You can watch.** When an agent controls your browser, you see it work in real time.

For CI or servers, start Chrome yourself, for example `chrome --headless --remote-debugging-port=0 --user-data-dir=/tmp/chrome`, and point webtool at it with `WEBTOOL_CHROME_DATA_DIR=/tmp/chrome`. Bot detection is then your problem.

## How do I use webtool with a different Chrome profile?

Switch profiles in Chrome's profile picker. webtool connects to the Chrome process, not to one profile, so it works with whichever profile's windows are open.

## What version of Chrome do I need?

Chrome 144 or newer. That version added dynamic remote debugging, which lets webtool connect to your browser without restarting it with special flags. Check your version at `chrome://version`.

## Why does Chrome ask me to allow remote debugging?

Chrome asks each time a new program connects to it over the DevTools Protocol. webtool connects on the agent's first browser tool call in a session, so you see the dialog once per MCP client session, not when the client starts. If the connection drops, for example because Chrome restarted, the next tool call reconnects and Chrome asks again.

## Can two agent sessions share the browser?

Yes. Each MCP client session runs its own `webtool mcp` process with its own connection, so each asks for approval once. Both sessions see all open tabs, so keep them on separate tabs.

## How is this different from Playwright or Puppeteer?

Playwright and Puppeteer launch a separate browser instance. That instance has no cookies, no logins, and no extensions, and it sets `navigator.webdriver=true`, which gets it blocked on many websites.

webtool connects to the Chrome you already use, with your sessions, cookies, and extensions. It is a single Go binary with no Node.js runtime and no browser download.

The tradeoff: webtool is built for agent-driven browsing, not browser testing. For cross-browser test suites, use Playwright.
