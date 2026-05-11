# av-bridge-ops

Deployment tooling for av-bridge across a multi-site fleet.

---

## Directory layout

```
av-bridge-ops/
├── ansible/
│   ├── deploy.yml                    # Main playbook
│   ├── inventory/
│   │   └── hosts.yml                 # All sites
│   ├── group_vars/
│   │   └── av_bridge_sites.yml       # Shared defaults
│   ├── host_vars/
│   │   └── site-london-01.yml        # Per-site devices & secrets
│   └── roles/av-bridge/
│       ├── tasks/main.yml            # Full provisioning logic
│       ├── handlers/main.yml
│       ├── defaults/main.yml
│       └── templates/
│           ├── config.yaml.j2        # av-bridge config
│           ├── env.j2                # Secrets env file
│           ├── av-bridge.service.j2  # systemd unit
│           ├── av-bridge-update.sh.j2  # Auto-update script
│           └── av-bridge-update.timer.j2
├── scripts/
│   └── av-bridge-update.sh           # Standalone update script
└── grafana/
    ├── av-bridge-fleet-dashboard.json # Import into Grafana
    ├── prometheus.yml                 # Prometheus scrape config
    └── av-bridge-alerts.yml          # Alerting rules
```

---

## Prerequisites

```bash
pip install ansible
ansible-galaxy collection install ansible.builtin
```

---

## First deployment

### 1. Add a site to inventory

Edit `ansible/inventory/hosts.yml` and add the host.
Create `ansible/host_vars/<hostname>.yml` with its devices and secrets.

### 2. Encrypt secrets with ansible-vault

```bash
# Encrypt a value inline
ansible-vault encrypt_string 'my-api-key' --name 'av_bridge_cloud_api_key'

# Or encrypt a whole vars file
ansible-vault encrypt ansible/host_vars/site-london-01.yml
```

Store your vault password in `~/.vault_pass` and add to `.gitignore`.

### 3. Deploy

```bash
# All sites (rolling 25% at a time)
ansible-playbook ansible/deploy.yml --vault-password-file ~/.vault_pass

# Single site
ansible-playbook ansible/deploy.yml -l site-london-01 --vault-password-file ~/.vault_pass

# Dry run
ansible-playbook ansible/deploy.yml --check --diff
```

---

## Updating the binary

### Manual (one site)
```bash
ssh ubuntu@<site-ip> sudo av-bridge-update
```

### Force via Ansible (all sites)
```bash
ansible-playbook ansible/deploy.yml -e av_bridge_version=v1.3.0
```

### Automatic
The `av-bridge-update.timer` systemd unit runs nightly at 02:30 (configurable).
It checks GitHub releases, downloads, verifies checksum, swaps binary, restarts,
health-checks, and rolls back automatically if the health check fails.

---

## Monitoring setup

### Prometheus

Add the scrape config from `grafana/prometheus.yml` to your Prometheus instance.

For **Grafana Cloud** (recommended for remote sites):

```yaml
# Each site: install Grafana Alloy and configure remote_write
# https://grafana.com/docs/alloy/latest/get-started/install/linux/
```

### Grafana dashboard

1. Open Grafana → Dashboards → Import
2. Upload `grafana/av-bridge-fleet-dashboard.json`
3. Select your Prometheus datasource
4. Done — you'll see fleet-wide status, per-device history, and availability %

### Alerting rules

```bash
# Copy to your Prometheus rules directory
cp grafana/av-bridge-alerts.yml /etc/prometheus/rules/
systemctl reload prometheus
```

Configure Alertmanager to route `severity: critical` to PagerDuty/OpsGenie
and `severity: warning` to Slack.

---

## Tailscale (recommended for remote access)

Set `av_bridge_tailscale_enabled: true` and provide an auth key in host_vars.
After deployment, each site appears in your Tailscale network as
`av-bridge-<site-id>` and you can reach its local API from anywhere
without opening firewall ports.

```bash
# From anywhere on your Tailnet
curl http://av-bridge-london-01/api/v1/devices
```

---

## Secrets management options

| Option | Complexity | Recommended for |
|--------|-----------|-----------------|
| ansible-vault | Low | Getting started, <20 sites |
| HashiCorp Vault | Medium | Production, many sites |
| AWS SSM Parameter Store | Medium | AWS-hosted portal |
| Azure Key Vault | Medium | Azure-hosted portal |

To pull from HashiCorp Vault, replace `!vault` blocks in host_vars with:
```yaml
av_bridge_cloud_api_key: "{{ lookup('hashi_vault', 'secret=av-bridge/data/site-london-01:cloud_api_key') }}"
```
