# Hanzo Commerce - Deployment Guide

This directory contains all deployment configurations for Hanzo Commerce.

## Quick Start

### Local Development

```bash
# Copy environment file
cp deploy/.env.example deploy/.env

# Edit .env with your configuration
vim deploy/.env

# Start services
docker compose -f deploy/compose.yml up -d

# View logs
docker compose -f deploy/compose.yml logs -f commerce
```

Commerce will be available at `http://localhost:8001`

### Staging Deployment

```bash
# Deploy to staging
./deploy/scripts/deploy.sh staging v2.0.0
```

### Production Deployment

```bash
# Standard deployment
./deploy/scripts/deploy.sh production v2.0.0

# Blue-green deployment
./deploy/scripts/deploy.sh production v2.0.0 --blue-green
```

## Architecture

```
                     +-----------------+
                     |     NGINX       |
                     |  Load Balancer  |
                     +--------+--------+
                              |
              +---------------+---------------+
              |                               |
     +--------v--------+             +--------v--------+
     |  Commerce Blue  |             | Commerce Green  |
     |    (active)     |             |   (standby)     |
     +--------+--------+             +--------+--------+
              |                               |
              +---------------+---------------+
                              |
              +---------------+---------------+
              |               |               |
     +--------v----+  +-------v-------+  +----v--------+
     |    Redis    |  |   ClickHouse  |  |   External  |
     |   (cache)   |  |  (analytics)  |  |  IAM (id)   |
     +-------------+  +---------------+  +-------------+
```

## Files

| File | Purpose |
|------|---------|
| `Dockerfile` | Multi-stage build for Commerce binary |
| `compose.yml` | Base Docker Compose configuration |
| `compose.production.yml` | Production overlay with monitoring |
| `compose.blue-green.yml` | Blue-green deployment configuration |
| `.env.example` | Environment variable template |

## Services

### Commerce (Main Application)
- Port: 8001
- Health check: `/health`
- Metrics: `/metrics`

### Redis (Cache)
- Port: 6379
- Used for: Session storage, caching

### ClickHouse (Analytics)
- HTTP Port: 8123
- Native Port: 9000
- Used for: Event tracking, order analytics

### NGINX (Load Balancer)
- HTTP Port: 80
- HTTPS Port: 443
- Used for: Reverse proxy, SSL termination, blue-green switching

## Environment Variables

See `.env.example` for all available configuration options.

### Required Variables

| Variable | Description |
|----------|-------------|
| `COMMERCE_SECRET` | Encryption key (generate with `openssl rand -hex 32`) |
| `IAM_URL` | IAM service URL (hanzo.id) |
| `IAM_API_KEY` | IAM API key for service auth |

### Payment Processing

| Variable | Description |
|----------|-------------|
| `STRIPE_SECRET_KEY` | Stripe API secret key |
| `STRIPE_PUBLISHABLE_KEY` | Stripe publishable key |
| `STRIPE_WEBHOOK_SECRET` | Stripe webhook signing secret |

## Blue-Green Deployment

Blue-green deployment allows zero-downtime updates by running two identical environments.

### How it Works

1. Deploy new version to inactive color (blue or green)
2. Wait for health checks to pass
3. Switch NGINX upstream to new deployment
4. Keep old deployment running as fallback

### Commands

```bash
# Check current status
./deploy/scripts/switch-active.sh status

# Switch to blue
./deploy/scripts/switch-active.sh blue

# Switch to green
./deploy/scripts/switch-active.sh green
```

### Rollback

If the new deployment has issues:

```bash
# Switch back to previous color
./deploy/scripts/switch-active.sh blue  # or green
```

## DigitalOcean Deployment

### App Platform

```bash
# Install doctl
brew install doctl

# Authenticate
doctl auth init

# Create app
doctl apps create --spec deploy/digitalocean/app-spec.yml

# Update app
doctl apps update <app-id> --spec deploy/digitalocean/app-spec.yml
```

### Required Secrets

Set these in the DO App Platform console:
- `COMMERCE_SECRET`
- `STRIPE_SECRET_KEY`
- `STRIPE_WEBHOOK_SECRET`
- `SENDGRID_API_KEY`
- `IAM_API_KEY`
- `CLICKHOUSE_URL`
- `COMMERCE_DATASTORE`

## Monitoring

### Prometheus + Grafana

Production deployments include Prometheus and Grafana for monitoring.

- Prometheus: `http://localhost:9090`
- Grafana: `http://localhost:3000` (admin/admin)

### Metrics Collected

- HTTP request rate and latency
- Error rates
- Database query performance
- Redis cache hit/miss ratio
- ClickHouse query performance

## Troubleshooting

### Container Won't Start

```bash
# Check logs
docker compose -f deploy/compose.yml logs commerce

# Check container status
docker inspect commerce-blue
```

### Health Check Failing

```bash
# Test health endpoint
curl http://localhost:8001/health

# Check container health
docker inspect --format='{{.State.Health}}' commerce-blue
```

### Database Connection Issues

```bash
# Test ClickHouse
docker exec commerce-clickhouse clickhouse-client --query "SELECT 1"

# Test Redis
docker exec commerce-redis redis-cli ping
```

### Reset Everything

```bash
# Stop all containers
docker compose -f deploy/compose.yml down -v

# Remove volumes
docker volume prune

# Rebuild
docker compose -f deploy/compose.yml build --no-cache
docker compose -f deploy/compose.yml up -d
```

## Security Considerations

1. **Never commit `.env` files** - Use `.env.example` as template
2. **Rotate secrets regularly** - Especially `COMMERCE_SECRET`
3. **Use TLS in production** - Configure SSL certificates
4. **Restrict network access** - Use firewall rules
5. **Enable rate limiting** - Configured in NGINX

## Support

- Documentation: https://docs.hanzo.ai/commerce
- Issues: https://github.com/hanzoai/commerce/issues
- Discord: https://discord.gg/hanzo
