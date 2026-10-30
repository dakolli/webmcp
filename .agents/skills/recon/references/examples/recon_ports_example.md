> Tool: `recon_ports` · Server: web-mcp · Surface: CLI · Tested: 2026-10-30

# recon_ports example

Goal: check port 80 on `scanme.sh`. The result holds one entry per unique `host`, `ip` and `port`.

## Call

```sh
browser recon run recon_ports --json '{"authorized":true,"targets":["scanme.sh"],"ports":"80","rate":5}'
```

## Result

```json
{
  "entries": [
    {"host": "scanme.sh", "ip": "2400:6180:0:d0::91:1001", "port": 80},
    {"host": "scanme.sh", "ip": "128.199.158.128", "port": 80}
  ],
  "count": 2,
  "truncated": 0,
  "dropped": 0
}
```

The live run returned two entries. Each entry is unique on `host`, `ip` and `port`. The host answered on IPv4 and IPv6. So port 80 holds one entry per address.

## Failure and fix

SYN mode needs libpcap and a CGO build:

```sh
browser recon run recon_ports --json '{"authorized":true,"targets":["scanme.sh"],"mode":"syn","ports":"80"}'
```

```
Error: recon_ports: invalid arguments: mode syn needs a libpcap-capable naabu build, but this binary is installed with CGO_ENABLED=0 — use mode connect (default) or mode udp, both of which work without libpcap
hint: fix the arguments per the tool InputSchema (required fields, enum values)
```

Fix: omit `mode`, or pass `"mode": "connect"` or `"mode": "udp"`.

The pin is naabu `v2.6.1`. v2.7.0 needs a newer Go than this host.

Verified: 2026-10-30 scanme.sh port 80, rate 5
