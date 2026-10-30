# caido_sitemap example

Run live on 2026-10-30. Read
[caido_sitemap.md](/home/dakolli/github.com/webmcp/.agents/skills/caido/references/tools/caido_sitemap.md) for every field and error.

## List the endpoint shapes of one host

```json
{"host":"docs.python.org"}
```

```json
{"ok":true,"data":{"host":"docs.python.org","project":"webmcp","scope_id":"","rows":[{"method":"GET","path":"/3/_static/basic.css","kind":"static","status":200,"mime":"text/css","requests":1,"params":[],"shape":"/{id}/_static/basic.css"}],"count":18,"scanned":28,"dropped":0,"truncated":0,"note":""}}
```

`count` 18 and `scanned` 28 mean the walk saw 28 nodes and kept 18 shapes. `note` is empty, so the depth
cap hid no node.

## Keep one prefix

```json
{"host":"docs.python.org","path":"/3/_static/"}
```

```json
{"ok":true,"data":{"host":"docs.python.org","rows":[{"method":"GET","path":"/3/_static/basic.css","kind":"static","status":200,"mime":"text/css","requests":1,"params":[],"shape":"/{id}/_static/basic.css"}],"count":17,"scanned":28,"dropped":0,"truncated":0,"note":""}}
```

`scanned` stays 28 because the tool walks the whole tree, then keeps the prefix.
