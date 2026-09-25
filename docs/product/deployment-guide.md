---
title: M.A.R.C.U.S. — Deployment Guide
product: M.A.R.C.U.S.
vendor: Involve Visual Collaboration Ltd
website: https://involve.vc
version: 1.0
status: General Availability
audience: Deployment engineers, network teams, technical evaluators
description: How to deploy the M.A.R.C.U.S. Collector across your estate — on-premises, cloud-hosted or containerised — with install steps, network requirements, sizing and day-2 operations.
---

<!-- ============================================================
  M.A.R.C.U.S.
  Managed · Assets · Resources · Control · Updates · Status
  Involve Visual Collaboration Ltd · involve.vc
============================================================ -->

<!-- Structure: each numbered "##" section is one page on the Mintlify
     site (see docs/product/mintlify/build.py). Keep one topic per "##",
     and cross-reference with §N so links resolve on both outputs. -->

# M.A.R.C.U.S. — Deployment Guide

**Managed · Assets · Resources · Control · Updates · Status**

## 1. Overview

M.A.R.C.U.S. deploys where it makes sense for your organisation. This guide covers every supported way to run it: on-premises, cloud-hosted, containerised and orchestrated. It walks you through planning, installing and operating the Collector.

Two things are the same in every deployment:

- **M.A.R.C.U.S. Cloud** is the managed service: the operator portal, telemetry ingest, the alert engine and the Public API. Involve operates it exclusively in the AWS London region (`eu-west-2`). You don't run any cloud components yourself.
- **The M.A.R.C.U.S. Collector** is one lightweight service on your network. It connects outbound to M.A.R.C.U.S. Cloud on HTTPS 443, and talks to devices on your local network using each device's native control protocol.

> **UK-hosted exclusively.** All customer data is stored, processed and backed up inside the United Kingdom: configuration, telemetry, audit records and backups. Where you run the Collector doesn't change this.

### Two decisions

Your deployment comes down to two independent choices: where the Collector sits and how it's packaged. You can mix the answers freely across a single estate.

**Where does the Collector sit?**

| Where | Best for |
|---|---|
| **On-premises at each site** | The default. Each Collector reaches devices over the LAN, a failure affects only one site, and no site depends on another. |
| **On-premises, central or hybrid** | A single Collector at head office or in a data centre reaches every site over your corporate WAN, SD-WAN or MPLS. You can also combine a central Collector with per-site ones. |
| **Cloud-hosted in your own tenant** | Your wider IT or AV platform already runs in AWS, Azure or Google Cloud. Run the Collector alongside it. |

**How is the Collector packaged?**

| Package | Best for |
|---|---|
| **Linux service (systemd)** | Traditional Linux estates. One install command, and systemd handles restarts. |
| **Windows service** | Windows Server estates. Installs as a standard Windows service. |
| **Container image** | Teams that standardise on containers: Docker, Kubernetes or a managed container platform. |

For example, you might run the Linux service at head office, a Docker container on a VM at a regional office, and a Kubernetes deployment in your cloud tenant. All three report to the same M.A.R.C.U.S. tenant and appear as three Collectors in the portal.

### How the guide is organised

1. **Plan:** choose a topology (§2), check network requirements (§3) and size the host (§4).
2. **Install:** follow the recipe for your platform (§5 – §9), then secure the configuration (§10).
3. **Operate:** keep the Collector updated (§11), monitor it (§12), and know what to back up (§13).

## 2. Choose a topology

Four topologies cover almost every real-world deployment. The *M.A.R.C.U.S. — Network Flow* and *Multi-Site Network Flow* diagrams illustrate them.

### Per-site Collector (the default)

Each site has one Collector on its local AV VLAN, which reaches devices over the LAN. Each site's firewall allows outbound HTTPS only.

> **Best for:** most deployments. It gives the best failure isolation, the lowest latency to devices and the simplest network design.

### Shared central Collector

One Collector, at head office or in a data centre, reaches every site's AV VLAN over your corporate WAN.

> **Best for:** small and medium estates on a reliable corporate WAN, where running one host is preferable to deploying at every site.

### Hybrid

Each major site runs its own Collector, and a central Collector serves the smaller regional offices.

> **Best for:** large estates with sites of mixed sizes. This is the most common pattern in practice.

### Cloud-hosted Collector

The Collector runs in your own cloud tenant (AWS, Azure or Google Cloud). It reaches your sites over your existing private connectivity: site-to-site VPN, AWS Direct Connect, Azure ExpressRoute or Google Cloud Interconnect.

> **Best for:** organisations whose IT or AV workloads already run in that cloud tenant and who want the Collector alongside them.

### Which one is right for you?

**Do you already run workloads in a particular cloud tenant?**

- **Yes:** deploy the Collector in that tenant. Use your existing Kubernetes platform (§8) or the cloud's managed container option (§9).
- **No:** deploy on-premises.

**How many sites do you have, and what connects them?**

- **One site, or several sites on a reliable WAN:** use a shared central Collector.
- **Many sites with separate AV VLANs and no reliable link between them:** use a Collector at each site.
- **A mix of both:** use the hybrid pattern.

**How does your organisation prefer to run services?**

- **Native services:** Linux (§5) or Windows Server (§6).
- **Docker, without Kubernetes:** Docker (§7) or a managed container platform (§9).
- **Kubernetes:** a `Deployment` in your cluster (§8).

If your organisation has a policy on container images versus native binaries, follow it. Both are fully supported and behave the same in operation.

## 3. Network requirements

These requirements are the same for every deployment option.

### Egress to M.A.R.C.U.S. Cloud

| From | To | Port | Purpose |
|---|---|---|---|
| Collector host | `*.involvecloud.com` | TCP 443 | Long-polled command channel, telemetry push and configuration sync |

This is the only external firewall rule required. Every connection uses TLS 1.2 or higher. Each request is also signed with an HMAC-SHA256 key unique to the Collector, so messages are authenticated independently of the transport.

If your allow-list needs specific hostnames rather than a wildcard, Involve will give you the exact hostnames for your tenant on request.

### Collector to devices

| From | To | Protocols | Purpose |
|---|---|---|---|
| Collector host | Site AV VLANs | Each device's native control protocol. Examples: HTTP/HTTPS, WebSocket, Telnet (including Biamp Tesira TTP on TCP 23), VISCA-over-IP (UDP 52381), serial-over-IP and ICMP | Live polling and command dispatch |

For an on-premises Collector, this is ordinary LAN traffic. A cloud-hosted Collector needs a routed path from your cloud tenant into each site's AV VLAN.

### Inbound to the Collector

**Nothing inbound is required from the internet.** No customer location needs public-facing ports.

An optional local-network port is available:

| From | To | Port | Purpose |
|---|---|---|---|
| Operator workstations and site tooling (optional) | Collector host | TCP 8080 | Local API: health check, Prometheus metrics and the touch-panel proxy |

The cloud does not use this port. Allow it only from your internal network.

## 4. Size the host

These figures are per Collector, on typical host specifications:

| Host | Comfortable | Stretch (relaxed poll rates) |
|---|---|---|
| 2 vCPU · 4 GB RAM · 10 GB disk | 250 – 500 devices | Up to 1,000 devices |
| 4 vCPU · 8 GB RAM · 10 GB disk | 500 – 1,000 devices | Up to 2,000 devices |
| Codec-heavy estates (many video-conferencing endpoints) | Around 60% of the above | Around 75% of the above |

Capacity depends on poll rates, WAN latency to each site and the mix of adapters. Involve confirms sizing for your estate during onboarding.

### When to add another Collector

- A single host serves more than 500 devices.
- A remote site is more than 50 ms away and has dozens of devices.
- You want to isolate business units or environments from each other.

Packaging doesn't affect sizing: a container with 2 vCPU and 4 GB behaves the same as a VM with 2 vCPU and 4 GB.

## 5. Install on Linux

The Collector installs directly onto a Linux host (`amd64` or `arm64`) and runs as a systemd service. This is the default install path.

### Before you start

- A Linux host with systemd that meets the sizing in §4 and the network requirements in §3.
- Root access (`sudo`) and `curl`.
- An enrolment token: in the portal, go to **Collectors → Add Collector** and name the Collector. The portal issues a **single-use token** and a ready-to-paste install command.

### Run the installer

Paste the command from the portal onto your host:

```bash
curl -fsSL https://[portal-host]/public/collectors/install.sh \
  | sudo AV_ENROLL_TOKEN=[one-time-token] bash
```

The installer:

1. Checks the host for root access, `curl` and systemd.
2. Redeems the token with M.A.R.C.U.S. Cloud and receives the Collector's identity and HMAC key.
3. Creates a dedicated non-root `av-bridge` system user.
4. Installs the Collector binary and writes its configuration.
5. Registers and starts `av-bridge.service`, then waits for the local health check to pass.

The Collector then downloads its device list from the cloud automatically. The whole process typically takes **under 15 minutes**.

### What gets installed

| Item | Location |
|---|---|
| Binary | `/usr/local/bin/av-bridge` |
| Configuration | `/etc/av-bridge/config.yaml` and `/etc/av-bridge/env` |
| State | `/var/lib/av-bridge/` |
| Logs | `/var/log/av-bridge/` and the systemd journal |
| Service | `av-bridge.service`, running as user `av-bridge` |

**Follow the logs:** `journalctl -u av-bridge -f`

## 6. Install on Windows Server

The Collector installs as a standard Windows service.

### Before you start

- A Windows Server host that meets the sizing in §4 and the network requirements in §3.
- An elevated (Administrator) PowerShell session.
- An enrolment token from **Collectors → Add Collector** in the portal.

### Run the installer

Paste the command from the portal into an elevated PowerShell session:

```powershell
$env:AV_ENROLL_TOKEN='[one-time-token]'
iwr https://[portal-host]/public/collectors/install.ps1 -UseBasicParsing | iex
```

The installer:

1. Checks for elevation and enables TLS 1.2.
2. Redeems the token with M.A.R.C.U.S. Cloud and receives the Collector's identity and HMAC key.
3. Installs the Collector and writes its configuration.
4. Registers and starts the `av-bridge` Windows service, then waits for the local health check to pass.

The whole process typically takes **under 15 minutes**.

> **Tokens are single-use.** If you run the installer again with a token that has already been redeemed, it stops without making changes. To re-enrol a host, issue a new token from the portal.

### What gets installed

| Item | Location |
|---|---|
| Binary | `C:\Program Files\av-bridge\av-bridge.exe` |
| Configuration | `C:\ProgramData\av-bridge\config.yaml` and `C:\ProgramData\av-bridge\env` |
| Logs | `C:\ProgramData\av-bridge\logs\` |
| Service | `av-bridge` (Windows Service Control Manager) |

**Restart the service:** `Restart-Service av-bridge`

## 7. Run with Docker

The Collector is available as a minimal container image built from `scratch`. It contains only the Collector binary, the TLS certificate-authority bundle and timezone data.

### Before you start

- A Docker host that meets the sizing in §4 and the network requirements in §3.
- The **image path and version tag**, and the **enrolled Collector configuration**. Involve provides both during onboarding for container deployments.

### Image reference

| Item | Value |
|---|---|
| Configuration | Mounted read-only at `/etc/av-bridge/config.yaml` |
| State | A small volume at `/var/lib/av-bridge/` |
| Ports | TCP 8080 is optional (local API only). No ports are needed for cloud operation. |

### Docker Compose

```yaml
services:
  marcus-collector:
    image: [registry]/av-bridge:[version]
    restart: unless-stopped
    volumes:
      - ./config.yaml:/etc/av-bridge/config.yaml:ro
      - collector-state:/var/lib/av-bridge
    environment:
      - TZ=Europe/London

volumes:
  collector-state:
```

Start it with `docker compose up -d`.

### docker run

```bash
docker run -d --restart unless-stopped --name marcus-collector \
  -v /etc/av-bridge/config.yaml:/etc/av-bridge/config.yaml:ro \
  -v collector-state:/var/lib/av-bridge \
  [registry]/av-bridge:[version]
```

The same commands work on any Docker host, including a VM in AWS, Azure or Google Cloud.

> **One container per Collector.** Each Collector has its own identity and HMAC key. To scale out, add more Collectors, each enrolled separately. Never run two copies of the same one.

## 8. Run on Kubernetes

The container image runs as a standard `Deployment` on EKS, AKS, GKE or an on-premises cluster. Before you start, you need the image and enrolled configuration described in §7.

### Reference manifest

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: marcus-collector
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: marcus-collector
  template:
    metadata:
      labels:
        app: marcus-collector
    spec:
      containers:
        - name: collector
          image: "[registry]/av-bridge:[version]"
          resources:
            requests: { cpu: "500m", memory: "512Mi" }
            limits:   { cpu: "2000m", memory: "2Gi" }
          volumeMounts:
            - name: config
              mountPath: /etc/av-bridge
              readOnly: true
            - name: state
              mountPath: /var/lib/av-bridge
      volumes:
        - name: config
          secret: { secretName: marcus-collector-config }
        - name: state
          persistentVolumeClaim: { claimName: marcus-collector-state }
```

Keep `replicas: 1` and the `Recreate` strategy. To scale out, add another Deployment for each additional Collector.

### Secrets on your platform

The configuration contains the Collector's HMAC key, so mount it from a `Secret` (as above) or from a Secrets Store CSI driver backed by your cloud key vault. Never put it in a plain `ConfigMap`.

- **AWS EKS:** use IAM Roles for Service Accounts with the Secrets Store CSI driver and AWS Secrets Manager.
- **Azure AKS:** use the Azure Key Vault provider for Secrets Store CSI.
- **Google GKE:** use Secret Manager through the Secrets Store CSI driver.

## 9. Run on a managed container platform

To run the image without operating a container platform, use your cloud's managed option. You need the image and enrolled configuration described in §7.

- **AWS ECS on Fargate:** a task of 0.5 vCPU and 1 GB runs one Collector. Keep state on Amazon EFS so it survives task replacement. Reference the configuration from AWS Secrets Manager in the task definition's `secrets` block, and set `desiredCount: 1`.
- **Azure Container Instances:** run a single container group with `--restart-policy Always`, and mount the configuration from Azure Files.
- **Google Compute Engine:** run the image on Container-Optimized OS, one container per VM, with `--container-restart-policy=always`.

For any cloud-hosted Collector, choose a UK region (AWS `eu-west-2`, Azure UK South or Google `europe-west2`) unless your own policy requires otherwise. The Collector also needs routed reach into each site's AV VLAN (§3).

## 10. Secure the configuration

### What the configuration contains

The configuration holds the cloud endpoint, the Collector's identity, its HMAC key and local settings such as the state path. The Linux and Windows installers generate it automatically (§5, §6).

You manage device configuration in the portal, and it syncs to the Collector automatically. The local configuration normally contains no device entries.

### Protect the HMAC key

The HMAC key is the Collector's most important secret. Protect it the way that suits your platform:

| Platform | Recommended protection |
|---|---|
| On-premises Linux | Restrict the files to root and the `av-bridge` service user. The installer sets mode `0640`. |
| On-premises Windows | Use NTFS permissions to restrict `C:\ProgramData\av-bridge\` to Administrators and SYSTEM. |
| Docker on any host | Use a bind-mounted configuration file with strict permissions, or Docker Secrets. |
| Kubernetes | Use a Kubernetes `Secret`, or Secrets Store CSI backed by your cloud key vault. |
| AWS | Use AWS Secrets Manager, referenced from the task definition or through CSI. |
| Azure | Use Azure Key Vault, through the Key Vault provider for Secrets Store CSI. |
| Google Cloud | Use Secret Manager, through the Secrets Store CSI driver. |

Inside M.A.R.C.U.S. Cloud, every Collector key is encrypted at rest.

### Rotate the key

To rotate a key, re-enrol the Collector with a new token from the portal. This issues a fresh key and retires the old identity.

## 11. Update the Collector

| Runtime | How to update |
|---|---|
| Linux service | Replace the binary, then run `systemctl restart av-bridge` |
| Windows service | Replace the binary, then run `Restart-Service av-bridge` |
| Docker Compose | `docker compose pull && docker compose up -d` |
| Kubernetes | Update the image tag in the Deployment |
| Managed container platforms | Redeploy with the new image tag |

Collector releases use semantic version tags. The current cloud release always supports the two most recent Collector minor versions. The portal's **Collectors** page shows each Collector's version, so you can see which ones need updating. The *M.A.R.C.U.S. — Release & Upgrade Policy* has the full compatibility commitments.

## 12. Monitor health and logs

### Health checks

- **Local health check:** `GET /healthz` on port 8080 returns `200 OK` when the Collector is running.
- **Metrics:** `GET /metrics` on the same port, in Prometheus text format.
- **In the portal:** the **Collectors** page shows each Collector's status, last heartbeat, version and host operating system. If a Collector stops reporting, the portal marks it offline, and every device behind it shows **Collector offline** instead of a stale status.

### Logs

| Runtime | Where to find logs |
|---|---|
| Linux | The systemd journal (`journalctl -u av-bridge`) and `/var/log/av-bridge/` |
| Windows | `C:\ProgramData\av-bridge\logs\` |
| Docker and Kubernetes | stdout and stderr. Forward these to your logging platform, such as Amazon CloudWatch Logs, Azure Monitor, Google Cloud Logging or a self-hosted equivalent. |

## 13. Back up and restore

The Collector keeps only a small amount of local state: the last-known status of each device, and telemetry waiting for its next push. When it restarts from scratch, it downloads its device list from the cloud and rebuilds current device status within one poll cycle.

> **You don't need to back up the Collector.** Redeploying is safe. The only file worth keeping is the configuration, because it holds the Collector's identity and HMAC key. If it's lost, re-enrol the Collector with a new token (§10).

---

## Related documents

- *M.A.R.C.U.S. — Product Overview:* features and business context
- *M.A.R.C.U.S. — Datasheet:* full technical specifications
- *M.A.R.C.U.S. — Security & Trust:* security architecture, including how the Collector communicates with the cloud
- *M.A.R.C.U.S. — Release & Upgrade Policy:* versioning and compatibility commitments
- *M.A.R.C.U.S. — Network Flow* and *Multi-Site Network Flow* diagrams: visual references for the topologies in §2

---

*Involve Visual Collaboration Ltd · M.A.R.C.U.S. Deployment Guide v1.0*
