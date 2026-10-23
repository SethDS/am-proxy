# am-proxy systemd: service won't start

Binary install path only (`deploy/am-proxy.service`). Unit expects:

| Item | Path / value |
|------|----------------|
| Binary | `/usr/local/bin/am-proxy` |
| Config | `--config.file=/etc/am-proxy/am-proxy.yml` |
| User/group | `am-proxy` / `am-proxy` |
| Listen | config `listen` (default `:9094`) |

## 1. Status and logs

```bash
sudo systemctl status am-proxy --no-pager
sudo journalctl -u am-proxy -n 50 --no-pager
sudo journalctl -u am-proxy -b --no-pager   # this boot
```

Note exit code, `status=…`, and the first panic / error line.

## 2. Common failures

| Symptom (journal / status) | Check / fix |
|----------------------------|-------------|
| `No such file or directory` / exec missing | `ls -l /usr/local/bin/am-proxy` — install binary (`install -o root -g root -m 0755`) |
| Permission denied on binary | Binary must be executable by others (`0755`); owned root:root |
| Config open / read error | See §4 — file missing or not readable by `am-proxy` |
| YAML / config panic on load | Fix `/etc/am-proxy/am-proxy.yml`; validate with foreground run (§3) |
| `address already in use` / listen fail | `ss -tlnp \| grep 9094` (or your `listen` port) — stop the other process or change `listen` |
| `Failed to create am-proxy/…`: No such user | `getent passwd am-proxy` — create: `useradd --system --home-dir /var/lib/am-proxy --create-home --shell /sbin/nologin am-proxy` |
| Starts then dies; AVC / denied in audit | Brief: `sudo ausearch -m AVC -ts recent \| grep am-proxy` — restore contexts or adjust SELinux policy |

After fixes: `sudo systemctl daemon-reload && sudo systemctl restart am-proxy`.

## 3. Foreground run (see the real error)

Stop the unit first so the port is free:

```bash
sudo systemctl stop am-proxy
sudo -u am-proxy /usr/local/bin/am-proxy --config.file=/etc/am-proxy/am-proxy.yml
```

Config load failures panic with the parse/IO error. Ctrl-C when done, then `sudo systemctl start am-proxy`.

## 4. Config readable by service user

```bash
sudo ls -la /etc/am-proxy/ /etc/am-proxy/am-proxy.yml
sudo -u am-proxy test -r /etc/am-proxy/am-proxy.yml && echo OK || echo NOT_READABLE
```

Expected install: dir `0755` root:`am-proxy`; file `0640` root:`am-proxy`. Fix ownership/mode if `NOT_READABLE`.

## 5. Health check after start

```bash
sudo systemctl is-active am-proxy
curl -sf http://127.0.0.1:9094/-/healthy && echo healthy
```

If `listen` is not `:9094`, use that host:port instead.

## 6. Not a start failure

Process **active** and `/-/healthy` OK, but Alertmanager unreachable → **runtime** (backends, network, auth). Use `journalctl -u am-proxy` and `GET /metrics` (`am_proxy_*` forward/retry series) — not this start checklist.

Containers / WAL: out of scope here; use the README binary+systemd section for this install path.
