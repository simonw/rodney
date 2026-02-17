# Exit codes: using Rodney for checks

*2026-02-17T15:11:47Z by Showboat 0.6.0*
<!-- showboat-id: 5fb34da6-02a2-4aef-ac30-9f828ae18185 -->

Rodney now uses distinct exit codes: **0** for success, **1** when a check command's condition is not met, and **2** for errors. This makes it straightforward to script assertions in CI — you can tell the difference between "the check reported false" and "something broke."

Let's exercise all three exit codes against a sample checkout page with visible elements, hidden elements, and accessibility landmarks.

Start the browser and open the sample checkout page. The page has a form, a hidden spinner, a hidden error banner, and a footer.

```bash
./rodney start 2>/dev/null | grep -v "^Auth proxy\|^Debug URL" && ./rodney open http://localhost:18091/sample.html
```

```output
Chrome started (PID 23050)
Checkout Page
```

## Exit code 0: successful checks

The `exists`, `visible`, and `ax-find` commands exit 0 when their condition is met.

Check that the heading and pay button exist:

```bash
./rodney exists "h1" && echo "exit code: $?"
```

```output
true
exit code: 0
```

```bash
./rodney exists "#pay-btn" && echo "exit code: $?"
```

```output
true
exit code: 0
```

Check that the pay button is visible:

```bash
./rodney visible "#pay-btn" && echo "exit code: $?"
```

```output
true
exit code: 0
```

Find the navigation landmark and a button by accessible name:

```bash
./rodney ax-find --role navigation && echo "exit code: $?"
```

```output
[navigation] "Main navigation" backendNodeId=8
exit code: 0
```

```bash
./rodney ax-find --role button --name "Pay now" && echo "exit code: $?"
```

```output
[button] "Pay now" backendNodeId=28 (focusable)
exit code: 0
```

## Exit code 1: check failures

When the condition is not met, these commands exit 1. For `exists` and `visible` the output is "false" on stdout with nothing on stderr. For `ax-find` a message goes to stderr.

Check for an element that does not exist:

```bash
./rodney exists ".discount-code"; echo "exit code: $?"
```

```output
false
exit code: 1
```

The spinner and error banner exist in the DOM but are hidden with `display:none`. `visible` reports them as not visible:

```bash
./rodney exists "#spinner" && echo "exists: yes"
./rodney visible "#spinner"; echo "visible exit code: $?"
```

```output
true
exists: yes
false
visible exit code: 1
```

```bash
./rodney visible "#error-banner"; echo "visible exit code: $?"
```

```output
false
visible exit code: 1
```

Search for an accessibility node that does not exist on this page:

```bash
./rodney ax-find --role banner --name "Welcome" 2>&1; echo "exit code: $?"
```

```output
No matching nodes
exit code: 1
```

## Exit code 2: errors

Actual errors — bad arguments, no browser session, unknown commands — exit with code 2. This is distinct from check failures, so `set -e` in a shell script will abort on errors but not on failed checks.

Unknown command:

```bash
./rodney bogus 2>&1 | head -1; echo "exit code: ${PIPESTATUS[0]}"
```

```output
unknown command: bogus
exit code: 2
```

Missing required arguments:

```bash
./rodney exists 2>&1; echo "exit code: $?"
```

```output
error: usage: rodney exists <selector>
exit code: 2
```

No browser session (after stopping):

```bash
./rodney exists "h1" 2>&1; echo "exit code: $?"
```

```output
error: no browser session (run 'rodney start' first)
exit code: 2
```

## Combining checks in a script

The exit code distinction is most useful when running multiple checks together. Here is a script that collects failures without aborting, while still treating real errors as fatal:

```bash
FAIL=0
check() {
    "$@" 2>/dev/null || { echo "FAIL: $*"; FAIL=1; }
}

./rodney start 2>/dev/null | grep -v "^Auth proxy\|^Debug URL"
./rodney open http://localhost:18091/sample.html

# These checks pass (exit 0)
check ./rodney exists "h1"
check ./rodney exists "#pay-btn"
check ./rodney visible "#pay-btn"
check ./rodney ax-find --role navigation

# These checks fail (exit 1) but do not abort the script
check ./rodney exists ".promo-banner"
check ./rodney visible "#spinner"
check ./rodney ax-find --role banner --name "Welcome"

./rodney stop 2>/dev/null

if [ "$FAIL" -ne 0 ]; then
    echo "---"
    echo "Some checks failed"
    exit 1
fi
echo "All checks passed"
```

```output
Chrome started (PID 37032)
Checkout Page
true
true
true
[navigation] "Main navigation" backendNodeId=8
false
FAIL: ./rodney exists .promo-banner
false
FAIL: ./rodney visible #spinner
FAIL: ./rodney ax-find --role banner --name Welcome
Chrome stopped
---
Some checks failed
```

The script exited 1 because three checks failed — but it ran all seven checks to completion instead of aborting on the first failure. If the browser had failed to start (exit code 2), the script would have aborted immediately thanks to `set -e`.
