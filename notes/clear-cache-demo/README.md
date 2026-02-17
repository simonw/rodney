# Demonstrating rodney reload --hard and clear-cache

The `reload --hard` flag and `clear-cache` command give rodney users control over Chrome's browser cache. This demo uses a small Python HTTP server (`server.py`) that returns every response with `Cache-Control: public, max-age=3600` — the page includes a random number and a timestamp so we can see when Chrome actually fetches fresh content versus serving from cache.

Start the caching server in the background and launch a headless browser with a clean profile.

```bash
nohup python3 notes/clear-cache-demo/server.py > /dev/null 2>&1 & disown
sleep 1 && curl -s http://127.0.0.1:9876/ > /dev/null && echo "Server started on port 9876"
```

```output
Server started on port 9876
```

```bash
rm -rf ~/.rodney/chrome-data  # start with empty disk cache
go run . start 2>&1 | grep -v -E "Debug URL|PID|proxy" && go run . status 2>&1 | head -1
```

```output
Chrome started
Browser running
```

## Initial page load

Navigate to the caching server. The page shows a timestamp and random value.

```bash
go run . open http://127.0.0.1:9876/
```

```output
Cache Demo
```

```bash
echo "Value: $(go run . text '#value'), Time: $(go run . text '#time')"
```

```output
Value: 3904, Time: 19:20:02
```

## Navigate away and back (cache hit)

The server sent `Cache-Control: public, max-age=3600`, so Chrome's disk cache considers this response fresh for an hour. Navigating to a different page and then back to the same URL serves the cached copy — the random value and timestamp are unchanged.

```bash
sleep 2
go run . open http://example.com
go run . open http://127.0.0.1:9876/
```

```output
Example Domain
Cache Demo
```

```bash
echo "Value: $(go run . text '#value'), Time: $(go run . text '#time')"
```

```output
Value: 3904, Time: 19:20:02
```

Same values — Chrome served the page from its disk cache without contacting the server.

> **Note:** `rodney reload` (which calls `location.reload()` under the hood) does
> *not* serve from cache — headless Chrome sends conditional requests on reload
> even when max-age hasn't expired. Only navigation to a URL respects the disk
> cache fully.

## Hard reload (bypasses cache)

The `--hard` flag uses the CDP `Page.reload` API with `ignoreCache: true`, which is the equivalent of pressing Shift+Refresh. This unconditionally bypasses the disk cache and forces a network fetch.

```bash
sleep 2 && go run . reload --hard
```

```output
Reloaded
```

```bash
echo "Value: $(go run . text '#value'), Time: $(go run . text '#time')"
```

```output
Value: 2330, Time: 19:20:33
```

Fresh value — the hard reload bypassed the cache and fetched from the server.

Navigate away and back to confirm the new response is now cached:

```bash
sleep 2
go run . open http://example.com
go run . open http://127.0.0.1:9876/
echo "Value: $(go run . text '#value'), Time: $(go run . text '#time')"
```

```output
Example Domain
Cache Demo
Value: 2330, Time: 19:20:33
```

Same values as the hard reload — the fresh response replaced the old cache entry.

## Clearing the browser cache

The `clear-cache` command calls `Network.clearBrowserCache` via CDP, wiping all cached resources. After clearing, even a normal navigation will fetch fresh content because there is nothing left in the cache to serve.

```bash
go run . clear-cache
```

```output
Browser cache cleared
```

```bash
sleep 2
go run . open http://example.com
go run . open http://127.0.0.1:9876/
echo "Value: $(go run . text '#value'), Time: $(go run . text '#time')"
```

```output
Example Domain
Cache Demo
Value: 1091, Time: 19:21:14
```

Fresh value — the cache was empty so Chrome had to fetch from the server.

## Cleanup

```bash
go run . stop && kill $(lsof -ti :9876) 2>/dev/null; echo "Done"
```

```output
Chrome stopped
Done
```
