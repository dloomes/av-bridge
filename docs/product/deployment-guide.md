---
title: AV Bridge — Deployment Guide
description: How to deploy the AV Bridge Collector across your estate — on-premises, cloud-hosted, containerised, or orchestrated. Practical recipes, network requirements, sizing, and operations.
audience: Deployment engineers, network teams, technical evaluators
version: 1.0
---

# AV Bridge — Deployment Guide

Involve Cloud's AV Bridge is designed to deploy where it makes sense for your business, not where it makes sense for ours. This guide walks through every supported deployment shape — on-premises, cloud-hosted, containerised, or orchestrated — with the practical recipes, network requirements, sizing, and operations detail you need to plan.

Two things stay constant across every option:

- Involve Cloud runs the **cloud SaaS** — the portal, the ingest pipeline, the alert engine, and the Public API. Hosted in the United Kingdom and European Union on AWS. Nothing on your side needs to run it.
- One lightweight **Collector** service runs on your side. It connects outbound to the cloud on HTTPS 443 and speaks native vendor protocols to devices on the local network.

Everything else is your choice.

## Contents

1. Two decisions
2. Topology patterns
3. Packaging options
4. Quick-start recipes
5. Network requirements
6. Configuration and secrets
7. Sizing
8. Operations
9. Choosing your combination

---

## 1. Two decisions

Your deployment shape comes from two independent choices — where the Collector sits, and how it is packaged. Both choices are orthogonal: you can mix them freely across a single estate.

### Decision A — Where does the Collector sit?

| Where | Best for |
|---|---|
| **On-premises at each site** | The default. Devices reached over LAN, failure isolation per site, zero cross-site dependencies. |
| **On-premises central or hybrid** | A single Collector — at HQ or in a data centre — reaches every site over the corporate WAN, SD-WAN, or MPLS. Or a hybrid of central plus per-site. |
| **Cloud-hosted in your tenant** | Your wider AV or video platform is already in Google Cloud, AWS, or Azure. Colocate the Collector alongside it and reach sites over your existing Cloud VPN / Interconnect / ExpressRoute. |

### Decision B — How is the Collector packaged?

| Package | Best for |
|---|---|
| **Native binary + systemd (Linux)** | A traditional Linux estate. One install command, native service, systemd manages restarts. |
| **Native binary + Windows service** | Windows Server estates. Registered as a standard Windows service. |
| **Docker container** | Portable, host-OS agnostic, single image across every environment. Runs anywhere Docker runs. |
| **Kubernetes Deployment** | You already run Kubernetes — GKE, EKS, AKS, or on-prem. Standard operational tooling: rolling updates, ConfigMap, Secret Manager. |
| **Managed container platform** | You want the container without operating a container platform. Google Compute Engine with Container-Optimized OS, AWS ECS on Fargate, Azure Container Instances. |

A worked example: you might run **native systemd** at your HQ, **Docker Compose** on a bench VM at a regional office, and a **GKE Deployment** for a cloud-hosted Collector colocated with your other cloud workloads — all three federate to the same Involve Cloud tenant and appear as three Collectors in the portal.

---

## 2. Topology patterns

Four topologies cover essentially every real-world deployment. All four are shown visually in the accompanying *AV Bridge — Multi-Site Network Flow* diagrams.

### 2.1 Per-site Collector — the default

One Collector per site, on the local AV VLAN. Devices are reached over LAN. Each site firewall permits outbound HTTPS only.

**Pick this for:** most deployments. Best failure isolation, lowest latency to devices, simplest network story.

### 2.2 Shared central Collector

One Collector, sited at HQ or in a data centre, reaches every site's AV VLAN over your corporate WAN.

**Pick this for:** small-to-medium estates on a reliable corporate WAN where consolidating operations to a single host is preferable to per-site deployment.

### 2.3 Hybrid

One Collector at each major site, plus a central Collector serving smaller regional offices.

**Pick this for:** large estates with mixed site scale. The most common pattern in practice.

### 2.4 Cloud-hosted Collector

The Collector runs in your own cloud tenant (Google Cloud, AWS, or Azure), reaching your sites over your existing Cloud VPN, Interconnect, or ExpressRoute.

**Pick this for:** deployments where your existing IT or AV workloads already run in that cloud tenant, and you want the Collector alongside them rather than on-premises.

---

## 3. Packaging options

The Collector is a single static Go binary — no runtime dependencies, no interpreter, no shared libraries. These are the ways it ships.

### 3.1 Native binary + systemd (Linux)

Direct install onto a Linux host, run as a systemd service. Standard corporate Linux workflow.

- **You get:** a static binary, a systemd unit, and a YAML config
- **Managed by:** systemd
- **Updates:** one-line reinstall, or your preferred configuration management

### 3.2 Native binary + Windows service

Same binary, cross-compiled for Windows. Registered as a Windows service.

- **You get:** a static executable, a service registration script, and a YAML config
- **Managed by:** Windows Service Control Manager
- **Updates:** scripted replace-and-restart, or MSI upgrade

### 3.3 Docker container

Multi-stage build compiled down to a `scratch` runtime image around 15 MB. Just the binary, TLS certificate authority bundle, and timezone data.

- **Config:** YAML mounted at `/etc/av-bridge/config.yaml`
- **State:** small volume mounted at `/var/lib/av-bridge/`
- **Ports:** TCP 8080 optional for local API; no ports required for cloud operation

### 3.4 Kubernetes Deployment

The container runs cleanly as a standard `Deployment` in any Kubernetes cluster. Config via `ConfigMap`; secrets via `Secret` or a Secret Store CSI driver bound to your cloud KMS.

- **Replicas:** one per logical Collector — each Collector holds a device-scoped HMAC key and small operational state
- **Rolling updates:** standard Kubernetes update strategy applies
- **Autoscaling:** scale horizontally by adding more Collector Deployments, not more replicas of one

### 3.5 Managed container platforms

For teams that want the container image but not the container platform:

- **Google Compute Engine with Container-Optimized OS** — Google's minimal, container-managed OS runs one container per VM.
- **AWS ECS on Fargate** — a task definition runs the container; no EC2 to provision.
- **Azure Container Instances** — one-container deployments with no cluster.

---

## 4. Quick-start recipes

Practical steps to stand up each option. Placeholder values in `[…]` are replaced with real values from the portal's *Add Collector* screen at enrolment time.

### 4.1 On-premises Linux (systemd)

The default install path. From the portal's *Collectors → Add Collector* screen, copy the one-line install command onto your target host:

```bash
curl -fsSL https://[cloud-host]/install/collector \
  | sudo bash -s -- \
      --collector-id [collector-id] \
      --enrolment-token [one-time-token]
```

The script creates a non-root `av-bridge` service user, downloads the binary to `/usr/local/bin/av-bridge`, writes `/etc/av-bridge/config.yaml`, registers `av-bridge.service` with systemd, and confirms the first heartbeat to the cloud.

**Follow the logs:** `journalctl -u av-bridge -f`

**Typical time to stand up:** under 15 minutes end to end.

### 4.2 On-premises Windows Server

The Windows install package is delivered from the same *Add Collector* screen. Download the MSI, run it with the enrolment token, and the installer registers the service and confirms the first heartbeat.

**Follow the logs:** Windows Event Viewer → Applications and Services Logs → Involve → AV Bridge, or the rotating log file at `C:\ProgramData\Involve\AV Bridge\logs\`.

**Typical time to stand up:** under 15 minutes end to end.

### 4.3 Docker Compose on any host

For teams that prefer containers over native services on the same host:

```yaml
services:
  av-bridge:
    image: involvecloud/av-bridge:1.0
    restart: unless-stopped
    volumes:
      - ./config.yaml:/etc/av-bridge/config.yaml:ro
      - av-bridge-state:/var/lib/av-bridge
    environment:
      - TZ=Europe/London

volumes:
  av-bridge-state:
```

Bring it up with `docker compose up -d`. The image is delivered from Involve's container registry — image path and tag are provided on enrolment.

### 4.4 Google Cloud — Compute Engine with Docker

The smallest-footprint Google Cloud option. One VM per Collector.

```bash
gcloud compute instances create-with-container avbridge-01 \
  --project=[project] \
  --zone=europe-west2-a \
  --machine-type=e2-small \
  --container-image=involvecloud/av-bridge:1.0 \
  --network=[vpc] --subnet=[subnet]
```

- **Region:** UK — `europe-west2` (London), or any region approved by your data residency policy.
- **Egress:** attach a Cloud NAT gateway if the VM has no public IP.
- **Reaching devices:** the VM must have routed reach into each site's AV VLAN via Cloud VPN, Cloud Interconnect, or Partner Interconnect.

### 4.5 Google Cloud — GKE

For teams that already run GKE. A short reference manifest:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: av-bridge
spec:
  replicas: 1
  strategy:
    type: Recreate
  template:
    spec:
      containers:
        - name: av-bridge
          image: involvecloud/av-bridge:1.0
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
          configMap: { name: av-bridge-config }
        - name: state
          persistentVolumeClaim: { claimName: av-bridge-state }
```

Scale horizontally by adding more Collector Deployments — one per Collector identity. Bind secrets from GCP Secret Manager via the Secret Store CSI driver rather than baking them into the ConfigMap.

### 4.6 Google Cloud — Compute Engine with Container-Optimized OS

If you want the Docker image without operating GKE:

```bash
gcloud compute instances create-with-container avbridge-01 \
  --image-family=cos-stable \
  --image-project=cos-cloud \
  --machine-type=e2-small \
  --container-image=involvecloud/av-bridge:1.0 \
  --container-restart-policy=always
```

Middle ground between plain Compute Engine and full GKE.

### 4.7 AWS — EC2 with Docker

A `t3.small` VM in a private subnet with a NAT gateway for outbound is a typical shape:

```bash
docker run -d \
  --restart unless-stopped \
  --name av-bridge \
  -v /etc/av-bridge:/etc/av-bridge:ro \
  -v av-bridge-state:/var/lib/av-bridge \
  involvecloud/av-bridge:1.0
```

### 4.8 AWS — ECS on Fargate

An ECS task definition of 0.5 vCPU / 1 GB runs one Collector as a Fargate task. State persists on Amazon EFS so tasks survive replacement. Reference secrets from AWS Secrets Manager via the task definition's `secrets` block.

Set `desiredCount: 1` — one task per Collector identity. Route the task into a subnet with reach to the AV VLANs via Transit Gateway or Site-to-Site VPN.

### 4.9 AWS — EKS

The Kubernetes manifest from §4.5 works unchanged on EKS. Use IAM Roles for Service Accounts plus the Secrets Store CSI driver for secret injection.

### 4.10 Azure — VM with Docker

A `Standard_B2s` VM running Ubuntu LTS with Docker installed is the typical shape. Run the container with the same `docker run` command as §4.7.

### 4.11 Azure — Container Instances

For teams that want a single-container deployment without AKS:

```bash
az container create \
  --resource-group [rg] \
  --name av-bridge \
  --image involvecloud/av-bridge:1.0 \
  --cpu 1 --memory 2 \
  --restart-policy Always
```

Mount the config file from Azure Files, or use a small custom image that bundles it.

### 4.12 Azure — AKS

The Kubernetes manifest from §4.5 works unchanged on AKS. Use the Azure Key Vault Provider for Secrets Store CSI for secret injection.

---

## 5. Network requirements

Consistent across every deployment option.

### 5.1 Egress from the Collector to Involve Cloud

| From | To | Port | Purpose |
|---|---|---|---|
| Collector host | `*.involvecloud.com` | TCP 443 | Long-polled command channel · telemetry push · configuration sync |

That is the only external firewall rule required. TLS 1.2 or higher end-to-end; every request is additionally signed with an HMAC-SHA256 key unique to the Collector for message-level authenticity independent of the transport.

If your allow-list needs a specific hostname rather than a wildcard, we will pin your Collectors to a specific regional hostname (for example `uk1.involvecloud.com`) on request.

### 5.2 Collector to devices

| From | To | Protocols | Purpose |
|---|---|---|---|
| Collector host | Site AV VLANs | Vendor-native: TCP, UDP, Telnet, SSH, HTTP, vendor SDKs | Live polling and command dispatch |

For on-premises Collectors this is native LAN traffic. For cloud-hosted Collectors this requires a routed path from the cloud tenant into each site's AV VLAN — Cloud VPN in Google Cloud, Site-to-Site VPN or Direct Connect in AWS, or ExpressRoute in Azure.

### 5.3 Inbound to the Collector

**None required from the internet.** No public-facing ports at any customer location.

An optional local-network port is available for on-site tooling:

| From | To | Port | Purpose |
|---|---|---|---|
| Site operations tooling (optional) | Collector host | TCP 8080 | Local API for on-site diagnostics and health checks |

This is optional; the cloud does not require it.

---

## 6. Configuration and secrets

### 6.1 Configuration file

The Collector reads a YAML configuration file — at `/etc/av-bridge/config.yaml` on Linux and Docker, or `%ProgramData%\Involve\AV Bridge\config.yaml` on Windows. The file is generated by the enrolment flow and contains the cloud endpoint URL, the Collector's HMAC key, its unique identity, and the local state path.

Device configuration itself is authored in the portal and synced automatically. Your local YAML normally contains no device entries.

### 6.2 Secret handling

The HMAC key is the load-bearing secret. Handle it appropriately for your platform:

| Platform | Recommended store |
|---|---|
| On-premises Linux | Filesystem permissions (0600, service user only) |
| On-premises Windows | Windows DPAPI-protected config file |
| Docker on any host | Docker Secrets or a bind-mounted config with strict permissions |
| Kubernetes | Kubernetes Secrets, or Secrets Store CSI referencing your cloud KMS |
| Google Cloud | Secret Manager, exposed via CSI driver |
| AWS | Secrets Manager, referenced from the task definition's `secrets` block or CSI |
| Azure | Key Vault, exposed via the Azure Key Vault Provider for Secrets Store CSI |

Rotate on demand from the portal — the previous key is invalidated instantly.

---

## 7. Sizing

Per Collector, on typical host specifications:

| Host | Comfortable | Stretch (relaxed poll rates) |
|---|---|---|
| 2 vCPU · 4 GB RAM | 250 – 500 devices | Up to 1,000 devices |
| 4 vCPU · 8 GB RAM | 500 – 1,000 devices | Up to 2,000 devices |
| Heavy-adapter mix (many persistent SSH or video-conferencing codecs) | Around 60 % of the above | Around 75 % of the above |

**Add a second Collector when:**

- A single host is serving more than 500 devices.
- A remote site is more than 50 ms away with dozens of devices.
- You want blast-radius isolation between business units or environments.

Sizing is orthogonal to the runtime option. A container running on GKE at 2 vCPU / 4 GB behaves identically to a bare VM at 2 vCPU / 4 GB.

---

## 8. Operations

### 8.1 Updates

| Runtime | Update mechanism |
|---|---|
| Native binary + systemd | Re-run the install script; systemd restarts the service |
| Native binary + Windows service | MSI upgrade, or scripted replace-and-restart |
| Docker Compose | `docker compose pull && docker compose up -d` |
| Kubernetes | Update the image tag in the Deployment — a rolling update follows |
| Managed container (COS / ECS / ACI) | Redeploy the container with the new image tag |

Collector releases are semver-tagged. Backwards compatibility with the cloud is maintained across at least the two most recent minor versions.

### 8.2 Health and monitoring

- **Local health endpoint** — `GET /healthz` on port 8080, unauthenticated. Returns `200 OK` when the Collector is running and the cloud connection is healthy.
- **Operational metrics** — available on the same port in Prometheus format.
- **Cloud-side view** — the portal's *Collectors* page shows every Collector's last heartbeat, version, and health.

### 8.3 Backup and restore

The Collector holds a small amount of local state — last-known device statuses and telemetry awaiting its next push. This state is fully recoverable: on a fresh start the Collector rebuilds it from the cloud within one poll cycle.

**You do not need to back up the Collector.** Redeployment is safe. The one artefact worth preserving is the configuration file, because it contains the HMAC key.

### 8.4 Logging

- **Native services** — systemd journal on Linux, Windows Event Log on Windows.
- **Docker and Kubernetes** — stdout and stderr, pipe to your existing logging platform (Google Cloud Logging, Amazon CloudWatch Logs, Azure Monitor, or a self-hosted equivalent).

---

## 9. Choosing your combination

A short decision tree.

**Do you already run workloads in a specific cloud tenant?**

- **Yes** — deploy the Collector in the same tenant. Use the platform's managed container option (Container-Optimized OS on Google Cloud, Fargate on AWS, Container Instances on Azure) for the lowest operational cost, or your existing container platform (GKE / EKS / AKS) if you already run one.
- **No** — on-premises.

**How many sites, and what is between them?**

- **One site, or many well-connected sites over a reliable WAN** — shared central Collector.
- **Many sites with independent AV VLANs and no reliable inter-site reach** — per-site Collector.
- **Mixed** — hybrid.

**Do you run Kubernetes today?**

- **Yes** — deploy the container as a `Deployment` in your cluster.
- **No, but you run Docker** — Docker Compose, or a managed container platform.
- **No, you prefer a native service** — systemd on Linux or Windows service.

If your organisation has a policy about container images versus native binaries, follow the policy. Both are equally supported and produce the same operational outcome.

---

## Related documents

- *AV Bridge — Product Overview* — feature breakdown and business context
- *AV Bridge — Datasheet* — full technical specifications
- *AV Bridge — Security & Trust* — security architecture including the Collector-to-Cloud communication model
- *AV Bridge — Data Residency & Retention* — where data lives and for how long
- *AV Bridge — Multi-Site Network Flow* diagrams — visual reference for the topology patterns in §2

---

*Involve Cloud · AV Bridge Platform · Deployment Guide v1.0*
