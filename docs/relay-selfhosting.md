# Niuniu Relay — Self-Hosting Guide

## Overview

The **niuniu-relay** service acts as a rendezvous point between Niuniu desktop (personal edition, running on your LAN) and mobile clients that are outside your local network. The desktop registers itself with the relay; mobile clients connect to the relay and the relay forwards traffic to the desktop.

**When to self-host**: If you run the Niuniu personal edition behind a home router or office NAT, your mobile cannot reach the desktop directly. Deploying your own relay on a public VPS gives every device a stable HTTPS endpoint to connect through — no dynamic DNS or port-forwarding required.

---

## Quickstart (Docker Compose)

### Prerequisites

- A VPS / cloud instance with a public IP address.
- Docker + Docker Compose installed.
- A domain name (or subdomain) pointing at your host's IP.

### Steps

1. **Clone the repository on your server.**

   ```sh
   git clone https://github.com/niuniu-dev/niuniu.git
   cd niuniu
   ```

2. **Configure environment variables.**

   ```sh
   cp relay/deploy/.env.example relay/deploy/.env
   $EDITOR relay/deploy/.env
   ```

   Set `RELAY_DOMAIN` to your public domain and generate a strong `POSTGRES_PASSWORD`:

   ```sh
   openssl rand -base64 32   # use this as POSTGRES_PASSWORD
   ```

3. **Configure the relay.**

   ```sh
   mkdir -p /srv/relay-data
   cp relay/deploy/relay.yaml.example /srv/relay-data/relay.yaml
   $EDITOR /srv/relay-data/relay.yaml
   ```

   Replace the placeholder secret values (see [Configuration reference](#configuration-reference) below).
   The `db.dsn` should already point at the Postgres container in the compose file; if using an external database, update it accordingly.

4. **Start the stack.**

   ```sh
   cd relay/deploy && docker compose up -d
   ```

5. **Point DNS at your host.**

   Create an A record (and AAAA if IPv6) for `RELAY_DOMAIN` → your server's public IP. Caddy will automatically obtain a Let's Encrypt TLS certificate once DNS propagates (usually within a few minutes).

---

## Configuration Reference

The relay is configured via a single YAML file. See `relay/config.example.yaml` for an annotated example.

| Field | Default | Description |
|-------|---------|-------------|
| `listen` | `0.0.0.0:8090` | TCP address the relay HTTP server binds to. In Docker this must be `0.0.0.0:8090`; Caddy proxies to this port internally. |
| `db.dsn` | — | **Required.** PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/db?sslmode=disable`. Recommended: PostgreSQL 14 or newer. |
| `secrets.jwt` | — | **Required.** Secret used to sign JWT tokens. Must be at least 32 random bytes. Generate with `openssl rand -base64 32`. |
| `secrets.hmac_key` | — | **Required.** HMAC key for request signing. Same size recommendation. Generate with `openssl rand -base64 32`. |
| `access_token_ttl_seconds` | `900` | How long access tokens remain valid before a client must refresh (15 minutes by default). |

---

## Database

The relay requires **PostgreSQL 14 or newer**. The `pg-data` Docker volume persists your database across container restarts.

### Connection string format

```yaml
db:
  dsn: "postgres://niuniu:password@postgres:5432/niuniu_relay?sslmode=disable"
```

For an external managed database (e.g. RDS, Supabase, Neon):

```yaml
db:
  dsn: "postgres://user:password@db.example.com:5432/niuniu_relay?sslmode=require"
```

The relay applies database migrations automatically on startup — no manual migration step is required.

---

## TLS

Caddy handles TLS automatically via Let's Encrypt. No manual certificate management is required. Caddy renews certificates before they expire. The only requirement is that:

- Port 80 and 443 are reachable from the public internet.
- DNS for `RELAY_DOMAIN` resolves to this host before you start the stack.

---

## Upgrade

```sh
cd relay/deploy
docker compose pull
docker compose up -d
```

The relay applies database migrations on startup — no manual migration step is needed.

## Migrations

Schema is managed by sequentially-numbered migrations. Every upgrade to a
newer relay version may include new migrations; they are applied
automatically on startup.

To inspect current state:

```
niuniu-relay migrate status --config /path/to/relay.yaml
```

To roll back the most recent migration (if a new version has issues):

```
niuniu-relay migrate down --config /path/to/relay.yaml
```

To roll back to a specific version:

```
niuniu-relay migrate down --to 3 --config /path/to/relay.yaml
```

Back up your database BEFORE rolling back.

---

## Backup

Use `pg_dump` to back up the relay database:

```sh
# From the host, using docker compose exec
docker compose -f relay/deploy/docker-compose.yml exec postgres \
  pg_dump -U niuniu niuniu_relay | gzip > /backup/relay-$(date +%Y%m%d).sql.gz
```

Or if using an external managed database, use your provider's native backup and snapshot tools. Back up before every upgrade.

---

## Observability

The relay exposes a Prometheus metrics endpoint at `/metrics`. By default this endpoint is **not** proxied through Caddy (i.e., it is not accessible from the public internet). Scrape it from an internal Prometheus instance that can reach port `8090` on the relay container directly.

To add scraping from within the same Docker Compose network, add a Prometheus service and configure:

```yaml
scrape_configs:
  - job_name: niuniu-relay
    static_configs:
      - targets: ["niuniu-relay:8090"]
```

---

## Pointing Mobile Clients at Your Relay

In the Niuniu mobile app, open **Settings → Relay** and set the relay URL to:

```
https://<RELAY_DOMAIN>
```

Share this URL with anyone who needs to connect to your Niuniu personal instance. They will need a valid access token issued by your relay.

---

## Running Without Docker (systemd)

Use this method if you prefer to run the relay directly on the host without containers.

1. **Build the binary.**

   ```sh
   # From the repo root
   make build-relay
   # Output: bin/niuniu-relay-<timestamp>
   ```

2. **Install and configure.**

   ```sh
   sudo cp bin/niuniu-relay-* /usr/local/bin/niuniu-relay
   sudo useradd -r -s /usr/sbin/nologin niuniu-relay
   sudo mkdir -p /etc/niuniu-relay
   sudo cp relay/config.example.yaml /etc/niuniu-relay/relay.yaml
   sudo $EDITOR /etc/niuniu-relay/relay.yaml
   ```

   Update `db.dsn` to point at your PostgreSQL instance and replace the placeholder secrets.

3. **Install and enable the systemd unit.**

   ```sh
   sudo cp relay/deploy/niuniu-relay.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now niuniu-relay
   ```

4. **Check status.**

   ```sh
   sudo systemctl status niuniu-relay
   sudo journalctl -u niuniu-relay -f
   ```

5. **TLS without Caddy.**

   Place a reverse proxy (nginx, Caddy standalone, Traefik, etc.) in front of port `8090`. Point its SSL termination at the port and make sure `flush_interval` / streaming is enabled for long-lived SSE/WebSocket connections.

---

## Security Checklist

- **Replace secrets before first start.** Both `secrets.jwt` and `secrets.hmac_key` must be unique, random, and at least 32 bytes each.

  ```sh
  openssl rand -base64 32   # run twice — once for jwt, once for hmac_key
  ```

- **Rotate secrets every 90 days.** Rotation invalidates all existing tokens and forces all clients to re-authenticate.

- **Firewall**: expose only ports **80** and **443** inbound. Port `8090` must **not** be reachable from the public internet — it should only be accessible between Caddy and the relay container on the internal Docker network.

- **DDoS protection**: for public deployments consider placing the host behind Cloudflare or a similar CDN/WAF.

- **Keep the image updated**: run `docker compose pull` at least monthly to pick up security patches.

---

## Cluster / Multi-Node Deployment

By default the relay runs as a single process with an in-memory tunnel registry. For high-availability or multi-region deployments you can run multiple relay nodes behind a public load balancer, with Redis as a shared ownership store.

### When to use

- You need more than one relay instance (redundancy, rolling updates, or regional distribution).
- A single relay VM is not sufficient for the number of concurrent tunnels you expect.

### Prerequisites

- **Redis ≥ 6.0** accessible from every relay node (within the same VPC / private network).
- Each relay node needs a stable **internal URL** reachable by peer nodes (not the public domain). For example: `http://relay-pod-1.internal:8091`.
- A **public load balancer** that distributes desktop and mobile connections across nodes.

### How it works

1. When a desktop connects to any relay node, that node writes two Redis keys with a 60-second TTL:
   - `niuniu:tunnel:desktop:<desktop_id>` → `<node_id>` (which node owns this tunnel)
   - `niuniu:node:<node_id>` → `<internal_url>` (how to reach that node internally)
2. While the tunnel is alive the owning node refreshes both keys every 15 seconds.
3. When a mobile request arrives at any node, the node checks Redis:
   - **Self-owned tunnel** → served locally with zero extra hops.
   - **Peer-owned tunnel** → the request is reverse-proxied to the owning node's internal URL. The internal hop typically adds 1–5 ms on the same LAN / VPC.
4. When a desktop disconnects, the owning node deletes `niuniu:tunnel:desktop:<desktop_id>`.

**Node failure model**: if a node crashes without cleanup, its keys expire after 60 seconds. The desktop reconnects through the public load balancer, lands on any surviving node, and registers a new mapping. Clients experience a brief disconnect matching the desktop's own reconnect logic.

### Configuration

Add the following to your `relay.yaml` on **each** relay node:

```yaml
cluster:
  # Redis address accessible from all relay nodes (host:port).
  redis_url: "redis-primary.internal:6379"
  # Internal URL of THIS node — must be reachable by peer relay nodes.
  self_internal_url: "http://relay-pod-1.internal:8091"
  # Optional: stable node ID. Defaults to hostname + 8-char random suffix.
  node_id: "relay-pod-1"
```

Each node gets its own `self_internal_url`. The `redis_url` is the same on every node.

> **Important**: `self_internal_url` must be the node's private/internal address, not the public relay domain. Exposing the internal port (`8091`) to the internet is not required and not recommended.

### Single-node deployments

Leave `cluster.redis_url` empty (or omit the `cluster` section entirely). The relay uses a plain in-memory `LocalRouter` — no Redis dependency, identical behavior to previous releases.

---

## Payments (Optional)

If you want mobile users to be able to subscribe to paid plans directly from the app, configure Stripe integration. Without this, `POST /api/billing/change-plan` for paid plans returns HTTP 503 and no checkout happens.

### Prerequisites

- A [Stripe](https://stripe.com) account.
- At least one Stripe **Product** with a **recurring Price** for each paid plan (e.g. one Price for "pro", one for "enterprise").

### Configuration

Add the following to your `relay.yaml`:

```yaml
stripe:
  # Stripe secret key. Use sk_test_... for testing, sk_live_... for production.
  api_key: "sk_live_..."

  # URL Stripe redirects to after the user completes checkout.
  success_url: "https://app.example.com/billing/success"

  # URL Stripe redirects to when the user clicks "Back" or cancels checkout.
  cancel_url: "https://app.example.com/billing"

  # Map each relay plan_id to a Stripe Price ID.
  # Create these in the Stripe dashboard: Products → Prices.
  price_ids:
    pro: "price_1ABC123..."
    enterprise: "price_1XYZ789..."
```

### Stripe Webhook

Configure Stripe to send webhook events to your relay:

1. In the Stripe dashboard, go to **Developers → Webhooks → Add endpoint**.
2. Set the endpoint URL to `https://<RELAY_DOMAIN>/api/webhooks/billing`.
3. Select these events:
   - `checkout.session.completed`
   - `invoice.payment_succeeded`
   - `customer.subscription.created`
   - `customer.subscription.deleted`
4. Copy the **Signing secret** (starts with `whsec_...`).
5. Add the signing secret to your relay config under `webhooks.shared_secret` — see [Configuration Reference](#configuration-reference).

> **Important**: The relay's webhook handler uses a simple HMAC-SHA256 check (`X-Webhook-Signature: sha256=<hex>`), not the Stripe-SDK `Stripe-Signature` header format. Configure Stripe to send the signature in the relay's expected format, or leave `webhooks.shared_secret` empty to skip signature verification (not recommended for production).

### Full Checkout Flow

1. Mobile app calls `POST /api/billing/change-plan` with `{"target_plan_id": "pro"}`.
2. Relay creates a Stripe Checkout Session with `client_reference_id` set to the account ID.
3. Relay responds with `{"checkout_url": "https://checkout.stripe.com/pay/...", "target_plan_id": "pro"}`.
4. Mobile app opens `checkout_url` in the system browser.
5. User completes payment on the Stripe-hosted page.
6. Stripe redirects user to `success_url`.
7. Stripe fires `checkout.session.completed` webhook to `/api/webhooks/billing`.
8. Relay webhook handler looks up the account by `client_reference_id`, upgrades the plan, and records a `checkout_completed` billing event.
9. Mobile app polls `GET /api/billing/my-plan` to confirm the plan upgrade.

### Plan ID ↔ Stripe Price Mapping

| relay `plan_id` | Stripe dashboard | relay.yaml `price_ids` key |
|-----------------|------------------|---------------------------|
| `pro`           | Create a Product "Pro" with a monthly Price | `pro: "price_1..."` |
| `enterprise`    | Create a Product "Enterprise" with a monthly Price | `enterprise: "price_1..."` |

Free plans (`price_cents_usd: 0`) are applied immediately without Stripe; no price mapping needed.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| TLS certificate not issued | DNS not yet propagated, or ports 80/443 blocked | Wait for DNS propagation; check firewall rules. Check `docker compose logs caddy`. |
| DB connection refused | Postgres not started or wrong DSN | Check `docker compose ps` and `docker compose logs postgres`. Verify `db.dsn` in relay.yaml. |
| DB migration errors on startup | First migration failed against existing schema | Check `docker compose logs niuniu-relay`. If fresh install, verify Postgres is healthy before relay starts. |
| High CPU / slow responses | Many concurrent active tunnels | Check `/metrics` for `active_tunnels` count. Scale up the VPS or add more relay instances with Redis cluster config. |
| Mobile cannot connect | Wrong relay URL, or relay not reachable | Verify `https://<RELAY_DOMAIN>` is reachable from the mobile device. Check `docker compose logs niuniu-relay`. |
| Desktop shows offline after node restart | Redis keys expired or not cleaned up | Desktop will reconnect and re-register; keys expire within 60 s. Reduce `cluster.node_id` churn by pinning a stable ID. |
| `redis ping` error on startup | Redis unreachable or wrong address | Check `cluster.redis_url` and network connectivity from the relay container to Redis. |
