# Rodney: Chrome automation from the command line

[![PyPI](https://img.shields.io/pypi/v/rodney.svg)](https://pypi.org/project/rodney/)
[![Changelog](https://img.shields.io/github/v/release/simonw/rodney?include_prereleases&label=changelog)](https://github.com/simonw/rodney/releases)
[![Tests](https://github.com/simonw/rodney/actions/workflows/test.yml/badge.svg)](https://github.com/simonw/rodney/actions/workflows/test.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](https://github.com/simonw/rodney/blob/main/LICENSE)

A Go CLI tool that drives a persistent headless Chrome instance using the [rod](https://github.com/go-rod/rod) browser automation library. Each command connects to the same long-running Chrome process, making it easy to script multi-step browser interactions from shell scripts or interactive use.

## Architecture

```
rodney start     →  launches Chrome (headless, persists after CLI exits)
                     saves WebSocket debug URL to ~/.rodney/state.json

rodney open URL  →  connects to running Chrome via WebSocket
                     navigates the active tab, disconnects

rodney js EXPR   →  connects, evaluates JS, prints result, disconnects

rodney stop      →  connects and shuts down Chrome, cleans up state
```

Each CLI invocation is a short-lived process. Chrome runs independently and tabs persist between commands.

## Building

If you prefer not to build from source, you can run Rodney directly from PyPI using `uvx`:

```bash
uvx rodney
```

Then build from source if you want a local development binary:

```bash
go build -o rodney .
```

Requires:
- Go 1.21+
- Google Chrome or Chromium installed (or set `ROD_CHROME_BIN=/path/to/chrome`)

## Usage

### Start/stop the browser

```bash
rodney start          # Launch headless Chrome
rodney status         # Show browser info and active page
rodney stop           # Shut down Chrome
```

### Navigate

```bash
rodney open https://example.com    # Navigate to URL
rodney open example.com            # http:// prefix added automatically
rodney back                        # Go back
rodney forward                     # Go forward
rodney reload                      # Reload page
```

### Extract information

```bash
rodney url                    # Print current URL
rodney title                  # Print page title
rodney text "h1"              # Print text content of element
rodney html "div.content"     # Print outer HTML of element
rodney html                   # Print full page HTML
rodney attr "a#link" href     # Print attribute value
rodney pdf output.pdf         # Save page as PDF
```

### Run JavaScript

```bash
rodney js document.title                        # Evaluate expression
rodney js "1 + 2"                               # Math
rodney js 'document.querySelector("h1").textContent'  # DOM queries
rodney js '[1,2,3].map(x => x * 2)'            # Returns pretty-printed JSON
rodney js 'document.querySelectorAll("a").length'     # Count elements
```

The expression is automatically wrapped in `() => { return (expr); }`.

### Interact with elements

```bash
rodney click "button#submit"       # Click element
rodney input "#search" "query"     # Type into input field
rodney clear "#search"             # Clear input field
rodney select "#dropdown" "value"  # Select dropdown by value
rodney submit "form#login"         # Submit a form
rodney hover ".menu-item"          # Hover over element
rodney focus "#email"              # Focus element
```

### Wait for conditions

```bash
rodney wait ".loaded"       # Wait for element to appear and be visible
rodney waitload             # Wait for page load event
rodney waitstable           # Wait for DOM to stop changing
rodney waitidle             # Wait for network to be idle
rodney sleep 2.5            # Sleep for N seconds
```

### Screenshots

```bash
rodney screenshot                      # Save as screenshot.png
rodney screenshot page.png             # Save to specific file
rodney screenshot-el ".chart" chart.png  # Screenshot specific element
```

### Manage tabs

```bash
rodney pages                    # List all tabs (* marks active)
rodney newpage https://...      # Open URL in new tab
rodney page 1                   # Switch to tab by index
rodney closepage 1              # Close tab by index
rodney closepage                # Close active tab
```

### Query elements

```bash
rodney exists ".loading"    # Exit 0 if exists, exit 1 if not
rodney count "li.item"      # Print number of matching elements
rodney visible "#modal"     # Exit 0 if visible, exit 1 if not
```

### Accessibility testing

```bash
rodney ax-tree                           # Dump full accessibility tree
rodney ax-tree --depth 3                 # Limit tree depth
rodney ax-tree --json                    # Output as JSON

rodney ax-find --role button             # Find all buttons
rodney ax-find --name "Submit"           # Find by accessible name
rodney ax-find --role link --name "Home" # Combine filters
rodney ax-find --role button --json      # Output as JSON

rodney ax-node "#submit-btn"             # Inspect element's a11y properties
rodney ax-node "h1" --json               # Output as JSON
```

These commands use Chrome's [Accessibility CDP domain](https://chromedevtools.github.io/devtools-protocol/tot/Accessibility/) to expose what assistive technologies see. `ax-tree` uses `getFullAXTree`, `ax-find` uses `queryAXTree`, and `ax-node` uses `getPartialAXTree`.

```bash
# CI check: verify all buttons have accessible names
rodney ax-find --role button --json | python3 -c "
import json, sys
buttons = json.load(sys.stdin)
unnamed = [b for b in buttons if not b.get('name', {}).get('value')]
if unnamed:
    print(f'FAIL: {len(unnamed)} button(s) missing accessible name')
    sys.exit(1)
print(f'PASS: all {len(buttons)} buttons have accessible names')
"
```

### Shell scripting examples

```bash
# Wait for page to load and extract data
rodney start
rodney open https://example.com
rodney waitstable
title=$(rodney title)
echo "Page: $title"

# Conditional logic based on element presence
if rodney exists ".error-message"; then
    rodney text ".error-message"
fi

# Loop through pages
for url in page1 page2 page3; do
    rodney open "https://example.com/$url"
    rodney waitstable
    rodney screenshot "${url}.png"
done

rodney stop
```

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `ROD_CHROME_BIN` | `/usr/bin/google-chrome` | Path to Chrome/Chromium binary |
| `ROD_TIMEOUT` | `30` | Default timeout in seconds for element queries |
| `HTTPS_PROXY` / `HTTP_PROXY` | (none) | Authenticated proxy auto-detected on start |

State is stored in `~/.rodney/state.json`. Chrome user data is stored in `~/.rodney/chrome-data/`.

## Proxy support

In environments with authenticated HTTP proxies (e.g., `HTTPS_PROXY=http://user:pass@host:port`), `rodney start` automatically:

1. Detects the proxy credentials from environment variables
2. Launches a local forwarding proxy that injects `Proxy-Authorization` headers into CONNECT requests
3. Configures Chrome to use the local proxy

This is necessary because Chrome cannot natively authenticate to proxies during HTTPS tunnel (CONNECT) establishment. The local proxy runs as a background process and is automatically cleaned up by `rodney stop`.

See [claude-code-chrome-proxy.md](claude-code-chrome-proxy.md) for detailed technical notes.

## How it works

The tool uses the [rod](https://github.com/go-rod/rod) Go library which communicates with Chrome via the DevTools Protocol (CDP) over WebSocket. Key implementation details:

- **`start`** uses rod's `launcher` package to start Chrome with `Leakless(false)` so Chrome survives after the CLI exits
- **Proxy auth** handled via a local forwarding proxy that bridges Chrome to authenticated upstream proxies
- **State persistence** via a JSON file containing the WebSocket debug URL and Chrome PID
- **Each command** creates a new rod `Browser` connection to the same Chrome instance, executes the operation, and disconnects
- **Element queries** use rod's built-in auto-wait with a configurable timeout (default 30s)
- **JS evaluation** wraps user expressions in arrow functions as required by rod's `Eval`
- **Accessibility commands** call CDP's Accessibility domain directly via rod's `proto` package (`getFullAXTree`, `queryAXTree`, `getPartialAXTree`)

## Dependencies

- [github.com/go-rod/rod](https://github.com/go-rod/rod) v0.116.2 - Chrome DevTools Protocol automation

## Commands reference

| Command | Arguments | Description |
|---|---|---|
| `start` | | Launch headless Chrome |
| `stop` | | Shut down Chrome |
| `status` | | Show browser status |
| `open` | `<url>` | Navigate to URL |
| `back` | | Go back in history |
| `forward` | | Go forward in history |
| `reload` | | Reload current page |
| `url` | | Print current URL |
| `title` | | Print page title |
| `html` | `[selector]` | Print HTML (page or element) |
| `text` | `<selector>` | Print element text content |
| `attr` | `<selector> <name>` | Print attribute value |
| `pdf` | `[file]` | Save page as PDF |
| `js` | `<expression>` | Evaluate JavaScript |
| `click` | `<selector>` | Click element |
| `input` | `<selector> <text>` | Type into input |
| `clear` | `<selector>` | Clear input |
| `select` | `<selector> <value>` | Select dropdown value |
| `submit` | `<selector>` | Submit form |
| `hover` | `<selector>` | Hover over element |
| `focus` | `<selector>` | Focus element |
| `wait` | `<selector>` | Wait for element to appear |
| `waitload` | | Wait for page load |
| `waitstable` | | Wait for DOM stability |
| `waitidle` | | Wait for network idle |
| `sleep` | `<seconds>` | Sleep N seconds |
| `screenshot` | `[file]` | Page screenshot |
| `screenshot-el` | `<selector> [file]` | Element screenshot |
| `pages` | | List tabs |
| `page` | `<index>` | Switch tab |
| `newpage` | `[url]` | Open new tab |
| `closepage` | `[index]` | Close tab |
| `exists` | `<selector>` | Check element exists (exit code) |
| `count` | `<selector>` | Count matching elements |
| `visible` | `<selector>` | Check element visible (exit code) |
| `ax-tree` | `[--depth N] [--json]` | Dump accessibility tree |
| `ax-find` | `[--name N] [--role R] [--json]` | Find accessible nodes |
| `ax-node` | `<selector> [--json]` | Show element accessibility info |
| `file` | `<selector> <path\|->` | Upload file to file input |
| `download` | `<selector> [file\|-]` | Download href/src target |

## Agent Skill

This repository includes a **rodney-browser** [agent skill](https://agentskills.io) that teaches AI coding agents how to automate browser interactions using rodney. The skill follows the open [Agent Skills standard](https://agentskills.io) and works with any compatible tool (Claude Code, Codex, etc.). When installed, your agent can open websites, fill forms, click buttons, take screenshots, run JavaScript, test web flows, and perform accessibility audits — all driven through rodney commands.

### Global install (use across all projects)

To make the skill available in every project, copy it from `skills/rodney-browser/` in this repo to your agent's global skills directory.

**Claude Code:**

```bash
mkdir -p ~/.claude/skills
cp -r skills/rodney-browser ~/.claude/skills/rodney-browser
```

Or as a one-liner from anywhere:

```bash
git clone https://github.com/simonw/rodney /tmp/rodney-skill && \
  mkdir -p ~/.claude/skills && \
  cp -r /tmp/rodney-skill/skills/rodney-browser ~/.claude/skills/rodney-browser && \
  rm -rf /tmp/rodney-skill
```

**OpenAI Codex:**

```bash
mkdir -p ~/.agents/skills
cp -r skills/rodney-browser ~/.agents/skills/rodney-browser
```

Or as a one-liner from anywhere:

```bash
git clone https://github.com/simonw/rodney /tmp/rodney-skill && \
  mkdir -p ~/.agents/skills && \
  cp -r /tmp/rodney-skill/skills/rodney-browser ~/.agents/skills/rodney-browser && \
  rm -rf /tmp/rodney-skill
```

**Other agents:** Copy the `skills/rodney-browser/` directory to your agent's global skills path. Refer to your tool's documentation for the correct location.

After installation, the skill is active in all sessions. Make sure the `rodney` binary is on your `PATH`.

### What the skill enables

Once installed, your AI coding agent will automatically use rodney when you ask it to:

- **Browse websites** — "Open example.com and take a screenshot"
- **Fill forms and interact** — "Log into the portal and search for X"
- **Test web applications** — "Test the checkout flow end-to-end"
- **Extract data** — "Scrape the product prices from this page"
- **Accessibility audits** — "Check if all buttons have accessible names"
- **Download files** — "Download the CSV export from the dashboard"

The skill teaches the agent the screenshot-observe loop pattern, form filling, JS-based element discovery, click failure recovery, and multi-tab workflows.
