# Rodney Command Reference

## Browser Lifecycle

| Command | Syntax | Description |
|---------|--------|-------------|
| `start` | `rodney start` | Launch persistent headless Chrome |
| `stop` | `rodney stop` | Shut down Chrome and clean up |
| `status` | `rodney status` | Show PID, debug URL, pages, active tab |

## Navigation

| Command | Syntax | Description |
|---------|--------|-------------|
| `open` | `rodney open <url>` | Navigate to URL (auto-adds `http://`) |
| `back` | `rodney back` | Go back in history |
| `forward` | `rodney forward` | Go forward in history |
| `reload` | `rodney reload` | Reload current page |

## Page Information

| Command | Syntax | Description |
|---------|--------|-------------|
| `url` | `rodney url` | Print current URL |
| `title` | `rodney title` | Print current title |
| `html` | `rodney html [selector]` | Outer HTML of element (or full page) |
| `text` | `rodney text <selector>` | Text content of element |
| `attr` | `rodney attr <selector> <attr>` | Attribute value of element |
| `pdf` | `rodney pdf [file]` | Save page as PDF |

## Interaction

| Command | Syntax | Description |
|---------|--------|-------------|
| `click` | `rodney click <selector>` | Click element |
| `input` | `rodney input <selector> <text>` | Type into input (replaces existing) |
| `clear` | `rodney clear <selector>` | Clear input field |
| `select` | `rodney select <selector> <value>` | Select dropdown option by value |
| `submit` | `rodney submit <selector>` | Submit form |
| `hover` | `rodney hover <selector>` | Hover over element |
| `focus` | `rodney focus <selector>` | Focus element |
| `file` | `rodney file <selector> <path\|->` | Upload file to file input |
| `download` | `rodney download <selector> [file\|-]` | Download href/src target |

## JavaScript

| Command | Syntax | Description |
|---------|--------|-------------|
| `js` | `rodney js <expression>` | Evaluate JS (auto-wrapped in arrow function) |

Output: `null`/`undefined` as-is, strings unquoted, objects/arrays as pretty JSON.

## Wait / Timing

| Command | Syntax | Description |
|---------|--------|-------------|
| `wait` | `rodney wait <selector>` | Wait for element visible (up to ROD_TIMEOUT) |
| `waitload` | `rodney waitload` | Wait for page load event |
| `waitstable` | `rodney waitstable` | Wait for DOM to stop mutating |
| `waitidle` | `rodney waitidle` | Wait for network idle |
| `sleep` | `rodney sleep <seconds>` | Sleep N seconds (float) |

## Screenshots

| Command | Syntax | Description |
|---------|--------|-------------|
| `screenshot` | `rodney screenshot [-w N] [-h N] [file]` | Full-page screenshot (or viewport if -h set) |
| `screenshot-el` | `rodney screenshot-el <selector> [file]` | Screenshot specific element |

## Tab Management

| Command | Syntax | Description |
|---------|--------|-------------|
| `pages` | `rodney pages` | List all tabs (`*` = active) |
| `page` | `rodney page <index>` | Switch to tab by index |
| `newpage` | `rodney newpage [url]` | Open new tab |
| `closepage` | `rodney closepage [index]` | Close tab (default: active) |

## Element Queries

| Command | Syntax | Exit Code | Description |
|---------|--------|-----------|-------------|
| `exists` | `rodney exists <selector>` | 0=yes 1=no | Check element exists |
| `visible` | `rodney visible <selector>` | 0=yes 1=no | Check element visible |
| `count` | `rodney count <selector>` | — | Count matching elements |

## Accessibility

| Command | Syntax | Description |
|---------|--------|-------------|
| `ax-tree` | `rodney ax-tree [--depth N] [--json]` | Dump full accessibility tree |
| `ax-find` | `rodney ax-find [--role R] [--name N] [--json]` | Search by role/name |
| `ax-node` | `rodney ax-node <selector> [--json]` | A11y properties for element |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ROD_CHROME_BIN` | auto-detected | Path to Chrome/Chromium |
| `ROD_TIMEOUT` | `30` | Timeout in seconds for element queries |
| `HTTPS_PROXY` | — | Authenticated proxy (auto-detected) |
