---
name: rodney-browser
description: >
  CLI-driven Chrome browser automation for UI browsing, testing, scraping, and interaction
  using the rodney tool. Use this skill when you need to: (1) open websites and navigate pages,
  (2) fill forms, click buttons, or interact with web UI elements, (3) take screenshots or PDFs
  of web pages, (4) extract text, HTML, or attribute values from pages, (5) run JavaScript in a
  live browser, (6) test web application flows end-to-end, (7) perform accessibility audits
  (ax-tree, ax-find, ax-node), (8) download files from authenticated sessions, (9) automate
  multi-step browser workflows in shell. Triggers on requests like "open this URL", "take a
  screenshot", "click the button", "fill the form", "test the login flow", "scrape the page",
  "check accessibility", "browse to", "navigate to", or any browser automation task.
---

# Rodney Browser Automation

Rodney is a CLI tool that drives a persistent headless Chrome instance. Each command is a
short-lived process that connects to the same running Chrome via WebSocket, executes one
action, and exits. This makes it ideal for scripting multi-step browser workflows from the
shell.

For the full command reference, see [references/commands.md](references/commands.md).

## Setup

1. Ensure `rodney` is on your `PATH`. Alternatively, run
   `scripts/ensure_rodney.sh --build-dir /path/to/rodney/source` to build from source.
2. Start Chrome: `rodney start`
3. Run commands against the live browser.
4. When done: `rodney stop`

## Core Workflow

Every browser automation task follows this pattern:

1. **Ensure Chrome is running** — `rodney start` (idempotent; check with `rodney status`)
2. **Navigate** — `rodney open <url>`, then `rodney waitstable` or `rodney waitidle`
3. **Interact** — `rodney click`, `rodney input`, `rodney select`, `rodney js`, etc.
4. **Observe** — `rodney screenshot`, `rodney text`, `rodney html`, `rodney title`
5. **Clean up** — `rodney stop` when session is complete

## Key Patterns

### Screenshot-Observe Loop

Take a screenshot, read it with the Read tool to see the page, then decide the next action.
This is the primary feedback loop for navigating unfamiliar UIs:

```bash
rodney open https://example.com
rodney waitstable
rodney screenshot /tmp/step1.png    # then Read /tmp/step1.png to see the page
# analyze screenshot, decide next action
rodney click "button.submit"
rodney sleep 2
rodney screenshot /tmp/step2.png    # then Read /tmp/step2.png to verify result
```

### Form Filling

```bash
rodney input "input[name='email']" "user@example.com"
rodney input "input[name='password']" "secret"
rodney click "button[type='submit']"
rodney waitstable
```

`rodney input` replaces existing content (select-all then type). No need to `clear` first.

### Element Discovery via JS

When CSS selectors are insufficient, use `rodney js` to query the DOM:

```bash
# Find elements by text content
rodney js "Array.from(document.querySelectorAll('button')).map(b => ({text: b.textContent.trim(), id: b.id, cls: b.className}))"

# Click element by text
rodney js "Array.from(document.querySelectorAll('button')).find(b => b.textContent.trim() === 'Submit')?.click()"

# Check if an element is disabled
rodney js "document.querySelector('#myBtn').disabled"
```

### Handling Click Failures

If `rodney click` fails with `pointer-events is none` or `context deadline exceeded`, fall
back to JavaScript:

```bash
rodney js "document.querySelector('#myButton').click()"
```

### Waiting for Dynamic Content

```bash
rodney wait ".results-table"       # wait for element to appear and be visible
rodney waitstable                  # wait for DOM mutations to stop
rodney waitidle                    # wait for network to go idle
rodney sleep 3                     # hard sleep as last resort
```

Prefer `wait`/`waitstable`/`waitidle` over `sleep` when possible.

### Multi-Tab Workflows

```bash
rodney newpage https://other-site.com   # opens new tab and makes it active
rodney pages                             # list all tabs
rodney page 0                            # switch back to first tab
rodney closepage 1                       # close second tab
```

### Conditional Logic with Exit Codes

```bash
# exists and visible return exit code 0 (true) or 1 (false)
if rodney exists ".error-banner"; then
  error=$(rodney text ".error-banner")
  echo "Error: $error"
fi
```

### Downloading Files

```bash
rodney download "a.export-link" report.csv     # download href target
rodney download "img.chart" chart.png           # download image src
rodney download "a#dl" -                        # pipe to stdout
```

Download uses `fetch()` in page context, preserving cookies/session auth.

### Accessibility Auditing

```bash
rodney ax-tree --depth 3             # overview of page structure
rodney ax-find --role button         # find all buttons
rodney ax-find --role link --name "Home"  # find specific link
rodney ax-node "#submit" --json      # inspect element a11y properties
```

## Important Notes

- **Selectors**: All commands accept standard CSS selectors. Quote selectors containing
  spaces or special characters.
- **Auto-wait**: Element commands auto-wait up to `ROD_TIMEOUT` (default 30s) for the
  element to appear before failing.
- **JS wrapping**: `rodney js <expr>` wraps the expression as `() => { return (expr); }`.
  Semicolons are optional. Objects/arrays are pretty-printed as JSON.
- **Screenshots**: Without `-h`, captures full scrollable page. With `-h N`, captures
  viewport only. Default width is 1280.
- **State**: Chrome state persists in `~/.rodney/`. Cookies, sessions, and localStorage
  carry across commands until `rodney stop`.
- **Proxy**: Authenticated HTTP proxies are auto-detected from `HTTPS_PROXY`/`HTTP_PROXY`.
