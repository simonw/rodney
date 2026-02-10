# Rodney Accessibility Testing Commands

*2026-02-10T02:47:48Z*

Rodney now includes three commands for accessibility testing, built on Chrome's Accessibility CDP domain. These let you inspect what assistive technologies see — without leaving the command line.

We'll demo against a sample bookstore page with navigation, a data table, forms, and disabled buttons.

## `ax-tree` — Dump the full accessibility tree

Shows what a screen reader would see: roles, names, and properties in a hierarchical view. Use `--depth` to limit how deep you go.

```bash
./rodney ax-tree --depth 3
```

```output
[RootWebArea] "Accessible Bookstore" (focusable, focused)
  [link] "Skip to main content" (focusable)
    [StaticText] "Skip to main content"
      [InlineTextBox]
  [banner]
    [navigation] "Primary"
      [list]
  [main]
    [heading] "Shopping Cart" (level=1)
      [StaticText] "Shopping Cart"
    [region] "Cart items"
      [table] "Your cart contains 2 items"
    [form] "Checkout"
      [group] "Promo Code"
    [region] "Order summary"
      [paragraph]
      [button] "Proceed to Checkout" (focusable)
      [button] "Continue Shopping" (disabled)
  [contentinfo]
    [paragraph]
      [StaticText] "© 2026 Accessible Bookstore"
```

The full tree reveals structural landmarks (`banner`, `main`, `contentinfo`), ARIA regions, and properties like `disabled` on the "Continue Shopping" button. Ignored/presentational nodes are filtered out automatically.

Pass `--json` for machine-readable output suitable for `jq` pipelines.

```bash
./rodney ax-tree --json | python3 -c "
import json, sys
nodes = json.load(sys.stdin)
roles = {}
for n in nodes:
    if not n.get(\"ignored\") and n.get(\"role\", {}).get(\"value\"):
        r = n[\"role\"][\"value\"]
        roles[r] = roles.get(r, 0) + 1
for role, count in sorted(roles.items(), key=lambda x: -x[1])[:10]:
    print(f\"{role}: {count}\")
"
```

```output
StaticText: 26
InlineTextBox: 26
cell: 8
button: 5
link: 4
columnheader: 4
listitem: 3
row: 3
ListMarker: 3
generic: 3
```

## `ax-find` — Search by accessible name or role

Quickly locate elements by what assistive technologies call them, not by CSS selectors. Useful for CI assertions like "does this page have a button labeled X?"

```bash
./rodney ax-find --role button
```

```output
[button] "Remove The Great Gatsby" backendNodeId=57 (focusable)
[button] "Remove 1984" backendNodeId=71 (focusable)
[button] "Apply" backendNodeId=82 (focusable)
[button] "Proceed to Checkout" backendNodeId=90 (focusable)
[button] "Continue Shopping" backendNodeId=93 (disabled)
```

Combine `--name` and `--role` for precise queries:

```bash
./rodney ax-find --role link --name "Cart"
```

```output
[link] "Cart" backendNodeId=25 (focusable)
```

Find all landmarks/regions on the page:

```bash
./rodney ax-find --role region
```

```output
[region] "Cart items" backendNodeId=30
[region] "Order summary" backendNodeId=85
```

## `ax-node` — Inspect a specific element's accessibility properties

Point at a DOM element with a CSS selector and see its computed role, name, and all ARIA properties. Great for checking that a specific widget is correctly labeled.

```bash
./rodney ax-node "#checkout-btn"
```

```output
role: button
name: Proceed to Checkout
invalid: false
focusable: true
```

Check the disabled button — its `disabled` property is visible:

```bash
./rodney ax-node "#continue-btn"
```

```output
role: button
name: Continue Shopping
disabled: true
invalid: false
```

Inspect a labeled input — the name is computed from the `<label>` element:

```bash
./rodney ax-node "#promo"
```

```output
role: textbox
name: Enter code
invalid: false
focusable: true
editable: plaintext
settable: true
multiline: false
readonly: false
required: false
labelledby: null
```

Use `--json` for the full CDP node as JSON — useful for scripting assertions:

```bash
./rodney ax-node "#checkout-btn" --json
```

```output
{
  "nodeId": "90",
  "ignored": false,
  "role": {
    "type": "role",
    "value": "button"
  },
  "chromeRole": {
    "type": "internalRole",
    "value": 9
  },
  "name": {
    "type": "computedString",
    "value": "Proceed to Checkout",
    "sources": [
      {
        "type": "relatedElement",
        "attribute": "aria-labelledby"
      },
      {
        "type": "attribute",
        "attribute": "aria-label"
      },
      {
        "type": "relatedElement",
        "nativeSource": "label"
      },
      {
        "type": "contents",
        "value": {
          "type": "computedString",
          "value": "Proceed to Checkout"
        }
      },
      {
        "type": "attribute",
        "attribute": "title",
        "superseded": true
      }
    ]
  },
  "properties": [
    {
      "name": "invalid",
      "value": {
        "type": "token",
        "value": "false"
      }
    },
    {
      "name": "focusable",
      "value": {
        "type": "booleanOrUndefined",
        "value": true
      }
    }
  ],
  "parentId": "85",
  "childIds": [
    "91"
  ],
  "backendDOMNodeId": 90
}
```

## Scripting example: CI accessibility check

All three commands compose with shell pipelines. Here's a quick check that every button on the page has an accessible name (not just an empty string):

```bash
./rodney ax-find --role button --json | python3 -c "
import json, sys
buttons = json.load(sys.stdin)
unnamed = [b for b in buttons if not b.get(\"name\", {}).get(\"value\")]
if unnamed:
    print(f\"FAIL: {len(unnamed)} button(s) missing accessible name\")
    sys.exit(1)
else:
    print(f\"PASS: all {len(buttons)} buttons have accessible names\")
"
```

```output
PASS: all 5 buttons have accessible names
```
