# Tool references

Read the MCP tool description first. Use these files when the description is unclear or a call failed.

| Tool | File | State |
| :--- | :--- | :--- |
| `browser_create` | [browser_create.md](browser_create.md) | tested |
| `browser_list` | [browser_list.md](browser_list.md) | tested |
| `browser_close` | [browser_close.md](browser_close.md) | draft, re-test pending |
| `fetch_page` | [fetch_page.md](fetch_page.md) | tested |
| `page_list` | [page_list.md](page_list.md) | draft, re-test pending |
| `session_list` | [session_list.md](session_list.md) | tested |
| `session_close` | [session_close.md](session_close.md) | tested |
| `page_fetch` | [page_fetch.md](page_fetch.md) | tested |
| `page_act` | [page_act.md](page_act.md) | tested |

## Rules

1. The schema in `pkg/mcp/tools/` is the source of truth.
2. Do not invent parameters.
3. Keep one reference per MCP tool.
4. Add the tool to this table when its reference lands.
