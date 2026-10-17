# Egress

One OpenVPN tunnel (gluetun) plus `cmd/egress`, the only process allowed to
reconnect it, built on `github.com/cnc224/shared/egress`. Same design as the
books stack.

- `GET :9000/status` returns the proxy URL and current exit.
- `POST :9000/rotate` is served but **avxxx's scraper never calls it**: a
  challenged or blocked source is parked, not retried from a fresh exit.

## One tunnel per host

The project and network are both named `egress`. If another project's egress
stack (books) already runs on the target host, do **not** deploy a second one:
join the existing `egress` network instead and use its `vpn:8888`.

## Deploy

```bash
docker network create egress          # once per host
sudo install -d /opt/egress && cp deploy/egress/{docker-compose.yml,gluetun-auth.toml} /opt/egress/
cp deploy/egress/.env.example /opt/egress/.env   # fill PICK_VPN_NAME / PICK_VPN_PASSWORD
cd /opt/egress && GH_TOKEN=<read-only token for cnc224/shared> docker compose up -d --build
docker run --rm --network egress curlimages/curl -s http://egress:9000/status
```

Then point the scraper at it (`deploy/egress/scraper.override.yml`) and follow
docs/DEPLOYMENT.md 2.8 for the XSList / JAVLibrary dry-run.
