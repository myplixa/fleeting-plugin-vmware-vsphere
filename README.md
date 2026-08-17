# vmware-fleeting-plugin

> [!note]
> This is a fork of [santhanuv/fleeting-plugin-vmware-vsphere](https://gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere), detached from upstream. It adds `num_cpus`/`memory_mb`/`disk_size_gb` clone-time resource overrides (see [Resource Sizing](#resource-sizing)), prefixes vCenter-auth fields with `vsphere_` (see [Provider Configuration](#provider-configuration)), sets the guest OS hostname per clone, and includes the datacenter-scoping and guest-IP-selection fixes from upstream MR !32.

This is a [fleeting plugin](https://gitlab.com/gitlab-org/fleeting/fleeting) for VMware vSphere environments. The vSphere plugin allows GitLab Runner to provision virtual machines from templates, enabling
CI/CD jobs to be executed on dynamically created instances in your vSphere
infrastructure.

## Testing

### Testing Against a Real vCenter

`make real-vcenter-test` builds the plugin, provisions a real VM from your template through the actual plugin RPC path (same one GitLab Runner uses), and verifies SSH access, that the Docker daemon is reachable, that Docker can actually run a container, and that the guest hostname was set correctly — then deletes the VM and the temporary binary regardless of pass/fail.

To use it:

1. `cp test/real-vcenter/config.example.json test/real-vcenter/config.json`
2. Fill in your real vCenter connection details, template, and sizing (`config.json` is gitignored — never commit it)
3. `make real-vcenter-test`

Without `test/real-vcenter/config.json` present, the test skips cleanly (this is also what runs, and is skipped, under plain `make test`/CI). If your VMs have no direct internet access, set `docker_test_image` in the config to an image reachable from your network — it defaults to the public `hello-world` image otherwise.

## Installation

Every push to `main` builds binaries for all supported platforms and publishes them as assets on the corresponding [GitHub Release](../../releases), alongside a `release.sha256` checksums file.

To install manually, per the [GitLab Fleeting documentation](https://docs.gitlab.com/runner/fleet_scaling/fleeting/#install-binary-manually):

1. Download the binary matching your control host's OS/architecture from the latest release.
2. Rename it to `fleeting-plugin-vmware` and place it on `$PATH` (e.g. `/usr/local/bin`).
3. Reference it by name in `config.toml`:

```toml
plugin = "fleeting-plugin-vmware"
```

## Full Control Runner Example

A complete `config.toml` for a GitLab Runner "control" host (the one running `gitlab-runner` itself) using `docker-autoscaler` with this plugin, covering every parameter — both the ones this plugin defines and the surrounding GitLab Runner/Fleeting settings needed to actually use it:

```toml
concurrent = 20
check_interval = 3

[[runners]]
  name = "ci-vm-small"
  url = "https://gitlab.example.com/"
  token = "glrt-XXXXXXXXXXXXXXXXXXXX"
  executor = "docker-autoscaler"
  limit = 10
  tags = ["ci-vm-small"]
  run_untagged = false

  [runners.docker]
    image = "example.registry/ci-default:latest"
    privileged = false
    volumes = ["/var/run/docker.sock:/var/run/docker.sock", "/cache"]

  [runners.autoscaler]
    plugin = "fleeting-plugin-vmware"
    capacity_per_instance = 2
    max_use_count = 10
    max_instances = 5

    [runners.autoscaler.connector_config]
      username = "fleeting"
      use_external_addr = false

    [[runners.autoscaler.policy]]
      idle_count = 1
      idle_time = "5m0s"
      preemptive_mode = false

    [runners.autoscaler.plugin_config]
      vsphere_url = "https://vcenter.example.com/sdk"
      vsphere_username = "svc-fleeting@example.com"
      vsphere_password = "..."
      vsphere_allow_insecure_connection = false
      name = "ci-vm-small"
      vsphere_template = "vm-template"
      vsphere_datacenter = "DC1"
      vsphere_folder = "/DC1/vm/ci"
      vsphere_host = "/DC1/host/cluster1/esx-01.example.com"
      vsphere_resource_pool = "/DC1/host/cluster1/esx-01.example.com/Resources"
      vsphere_datastore = "esx-01-datastore"
      vsphere_linked_clone = false
      num_cpus = 2
      memory_mb = 2048
      disk_size_gb = 20
```

### Parameter Reference

| Section | Parameter | Description |
|---|---|---|
| top-level | `concurrent` | Total job slots across *all* `[[runners]]` sections on this control host combined |
| top-level | `check_interval` | How often (seconds) the runner manager polls GitLab for new jobs |
| `[[runners]]` | `name` | Display name for this runner registration in the GitLab UI |
| `[[runners]]` | `url` / `token` | GitLab instance URL and the runner authentication token, created via *Settings → CI/CD → Runners → New runner* |
| `[[runners]]` | `executor` | Must be `"docker-autoscaler"` to use Fleeting-managed instances with Docker |
| `[[runners]]` | `limit` | Max jobs *this runner section* will pull from GitLab at once. Should be ≥ `capacity_per_instance × max_instances`, or it becomes the real ceiling regardless of how much the autoscaler could otherwise provide |
| `[[runners]]` | `tags` / `run_untagged` | Which jobs get routed to this runner. With multiple tiers (e.g. small/large), set `run_untagged = false` on each so a job without a matching tag doesn't land on an arbitrary tier |
| `[runners.docker]` | `image` | Default job container image if `.gitlab-ci.yml` doesn't specify one |
| `[runners.docker]` | `volumes` | Bind mounts into every job container. `/var/run/docker.sock:/var/run/docker.sock` is only needed if jobs themselves run `docker build`/`docker run` (Docker-in-Docker via the host socket) |
| `[runners.autoscaler]` | `plugin` | The Fleeting plugin binary/image name — `fleeting-plugin-vmware` for a manually-installed binary |
| `[runners.autoscaler]` | `capacity_per_instance` | Concurrent jobs allowed *on one VM* — they share that VM's CPU/RAM/Docker daemon, no isolation between them |
| `[runners.autoscaler]` | `max_use_count` | Total jobs a VM may serve over its lifetime before being replaced. A ceiling, not a guarantee — see [How Parameters Interact](#how-parameters-interact) |
| `[runners.autoscaler]` | `max_instances` | Max VMs this runner section may have running at once |
| `[runners.autoscaler.connector_config]` | `username` | User the plugin connects as. Must match what the template's cloud-init actually creates (`"fleeting"` by default — see [Connector Configuration](#connector-configuration)) |
| `[runners.autoscaler.connector_config]` | `use_external_addr` | Whether to connect over the VM's external/public address instead of its internal one. `false` for on-prem vSphere with no public IPs |
| `[[runners.autoscaler.policy]]` | `idle_count` | How many *idle* (no job running) VMs to keep on standby for instant job pickup. `0` = pure scale-to-zero |
| `[[runners.autoscaler.policy]]` | `idle_time` | How long idle VMs beyond active demand are kept before being torn down |
| `[[runners.autoscaler.policy]]` | `preemptive_mode` | Whether to provision instances ahead of confirmed demand |
| `[runners.autoscaler.plugin_config]` | *(all fields)* | This plugin's own settings — see [Provider Configuration](#provider-configuration) below for the complete list |

### How Parameters Interact

**Concurrency ceiling** — `limit` must cover what the autoscaler can actually deliver, or it's the real bottleneck:

```
capacity_per_instance = 2
max_instances         = 5
→ up to 10 concurrent jobs are possible

limit = 10   # must be at least 10, or GitLab Runner won't request that many
```

**`max_use_count` only matters if `idle_count > 0`** — with no idle capacity kept warm, a VM is scaled down as soon as it has zero running jobs, regardless of unused `max_use_count` budget:

```
idle_count = 0, idle_time = "0s"
→ a VM that ran 2 of its allowed max_use_count = 10 jobs, then went idle,
  is torn down almost immediately — the other 8 "uses" are never spent.

idle_count = 1, idle_time = "5m0s"
→ 1 VM is kept warm for 5 minutes after going idle, so back-to-back jobs
  arriving within that window reuse it instead of waiting for a fresh clone,
  actually working toward max_use_count.
```

**One template, multiple hardware tiers** — `num_cpus`/`memory_mb`/`disk_size_gb` let separate `[[runners]]` sections (different `name`/`tags`) share the same `vsphere_template`, instead of maintaining one template per size (see [Resource Sizing](#resource-sizing)).

**VM naming ties `vsphere_host` and `name` together** — two tiers pointed at different `vsphere_host` values but the same `name` would produce distinguishable clone names (e.g. `esx-01-ci-vm-small-a1b2c3d4` vs `esx-02-ci-vm-small-f9e8d7c6`), while also being how the plugin tells its own instances apart from everything else in the destination `vsphere_folder` (see [VM Naming](#vm-naming)).

## Configuration

The plugin requires configuration for both the vSphere environment and VM connection details.

### Provider Configuration

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `vsphere_url` | string | Yes | URL of the vCenter server |
| `vsphere_template` | string | Yes | Path to the VM template (full clone) or VM with snapshots (linked clone) |
| `vsphere_allow_insecure_connection` | bool | Yes | Whether to skip SSL certificate verification |
| `name` | string | Yes | Identifier for the instance group, used as part of the prefix for VM names (see [VM Naming](#vm-naming)) |
| `vsphere_username` | string | No | Username to access the vCenter server — distinct from `connector_config.username`, which is the guest OS SSH login (see [Connector Configuration](#connector-configuration)) |
| `vsphere_password` | string | No | Password to access the vCenter server |
| `vsphere_folder` | string | No | Destination folder where VMs will be created |
| `vsphere_datacenter` | string | No | Datacenter where VMs will be created |
| `vsphere_host` | string | No | Target ESXi host for the cloned VMs |
| `vsphere_datastore` | string | No | Datastore where the cloned VMs will be located |
| `vsphere_resource_pool` | string | No | Resource pool to which cloned VMs will be added |
| `vsphere_linked_clone` | bool | No | Use linked clone instead of full clone (default: `false`) |
| `vsphere_snapshot` | string | No | Snapshot name for linked clones. If omitted, the current snapshot is used |
| `num_cpus` | int | No | Overrides the cloned VM's vCPU count. If unset (or `0`), inherits the template's value |
| `memory_mb` | int | No | Overrides the cloned VM's memory size, in MB. If unset (or `0`), inherits the template's value |
| `disk_size_gb` | int | No | Grows the cloned VM's primary (first) disk to this size, in GB. If unset (or `0`), inherits the template's disk size. vSphere does not support shrinking a disk, so a value smaller than the template's current disk size is rejected at startup |
| `cloud_init_extra_file` | string | No | Path to a file with additional cloud-init directives to merge into every clone's user data, on top of the plugin's own. See [Extending cloud-init](#extending-cloud-init) |
| `cloud_init_vars` | map[string]string | No | Arbitrary key/value pairs made available to `cloud_init_extra_file` as `{{ .Vars.<key> }}` — the place for values the extra file needs but that shouldn't be hardcoded into it (tokens, endpoints). See [Extending cloud-init](#extending-cloud-init) |

If optional parameters are not specified, the plugin will attempt to use default values from the vSphere environment.

### Resource Sizing

`num_cpus`, `memory_mb` and `disk_size_gb` let multiple `[[runners]]` sections share a single template while requesting different hardware per tag/tier, instead of maintaining one template per size:

```toml
[[runners]]
  name = "ci-vm-small"
  # ...
  [runners.autoscaler.plugin_config]
    vsphere_template = "vm-template"
    num_cpus = 2
    memory_mb = 2048
    disk_size_gb = 10

[[runners]]
  name = "ci-vm-large"
  # ...
  [runners.autoscaler.plugin_config]
    vsphere_template = "vm-template"
    num_cpus = 4
    memory_mb = 8192
    disk_size_gb = 50
```

### VM Naming

Cloned VMs are named `<host>-<name>-<id>`, where `<host>` is the short form of the `vsphere_host` parameter (e.g. `vsphere_host` set to `dc1/host/cluster1/esx-01.example.com` becomes `esx-01`; omitted if `vsphere_host` isn't set), `<name>` is the `name` parameter, and `<id>` is a random 8-character identifier:

```
esx-01-ci-vm-small-a1b2c3d4
```

This same prefix is also how the plugin recognizes which VMs in the destination folder belong to a given instance group, so `name` (and `vsphere_host`, if set) must stay consistent between runs.

### Connector Configuration

The plugin uses the following defaults for VM connections:

| Parameter | Default Value |
|-----------|---------------|
| Username | `"fleeting"` |
| Protocol | `"ssh"` for Linux VMs |
| OS Detection | Auto-detected from VM, defaults to Linux if unknown |

Note: When using
[Docker Autoscaler](https://docs.gitlab.com/runner/executors/docker_autoscaler/),
to enable Runner Manager’s access to the Docker socket on the VM, the user must be part of the `docker` group. The plugin does not add the connector user to that group itself (only to `sudo`/`wheel`, since the user doesn't exist yet at template-build time) — the template needs to either add a matching group membership itself, or reconfigure the Docker socket's group to one the connector user is already in. On Debian-based images with Docker installed via `apt`, note that Docker typically runs under systemd socket activation, in which case the socket's group comes from the `docker.socket` unit's `SocketGroup=` setting, not from `daemon.json`'s `group` key — that key is silently ignored when socket activation is in use, and a socket unit override is required instead.

## Clone Strategies

### Full Clone (default)

By default, the plugin creates full clones from a vSphere template. This copies the entire disk, which is reliable but can be slow for large VMs.

### Linked Clone

Linked clones use a snapshot-based delta disk instead of copying the entire disk, resulting in significantly faster clone operations and reduced storage usage.

To use linked clones:

1. Create a VM in vSphere and configure it as desired
2. Take a snapshot (e.g., `Base_1`)
3. Configure the plugin:

```toml
[runners.autoscaler.plugin_config]
  vsphere_template = "my-source-vm"
  vsphere_linked_clone = true
  vsphere_snapshot = "Base_1"  # optional: omit to use the current snapshot
```

> [!note]
> For linked clones, the `vsphere_template` parameter must point to a VM with at least one snapshot.

## VM Provisioning

### Linux VMs

- Provisioning credentials is supported via cloud-init
- The template VM must be configured with cloud-init
- User data with username and SSH public key is injected into cloned VMs
- The guest OS hostname is also set to the clone's name (see [VM Naming](#vm-naming)) via the same cloud-init user data (`hostname`/`preserve_hostname: false`/`manage_etc_hosts: true`), so every clone gets a distinct hostname instead of inheriting the template's — this matters if you point a monitoring/logging agent at these VMs and rely on hostname to tell them apart
- Package management on boot is disabled (`package_update: false`/`package_upgrade: false`/`package_reboot_if_required: false`) — the template is expected to already have everything it needs (see the Packer build), and updating/rebooting on every clone would slow down provisioning and risk drift between clones of the same template
- `final_message: "Cloud-init finished successfully at $TIMESTAMP"` is set so a completed boot is visible in the guest's own cloud-init logs (`/var/log/cloud-init-output.log`) — useful when troubleshooting a clone that came up unreachable

### Windows VMs

- Provisioning credentials is not supported for Windows VMs
- Use static credentials with username and password

## Extending cloud-init

The plugin always generates its own minimal cloud-config (hostname, SSH user/key, package management disabled — see [VM Provisioning](#vm-provisioning)). That part isn't user-editable, since the plugin depends on it for the connector to be able to reach the VM at all.

To add anything beyond that — installing a monitoring agent, writing extra files, running arbitrary `runcmd` steps — set `cloud_init_extra_file` to a separate file instead of touching the plugin's own generated config:

```toml
[runners.autoscaler.plugin_config]
  # ...
  cloud_init_extra_file = "/etc/gitlab-runner/cloud-init-extra.yaml"

  [runners.autoscaler.plugin_config.cloud_init_vars]
    example_token = "xxxxxxxx"
```

If `cloud_init_extra_file` is unset, behavior is unchanged from before this option existed — a single `#cloud-config` document, exactly as described in [VM Provisioning](#vm-provisioning).

### How the two configs combine

If `cloud_init_extra_file` is set, the plugin does **not** merge its fields into the extra file's YAML by hand. Instead, at boot the guest receives two separate `#cloud-config` documents as parts of a single [MIME multi-part `guestinfo.userdata`](https://cloudinit.readthedocs.io/en/latest/explanation/format.html) message — the plugin's own document first, the rendered extra file second. cloud-init itself merges them: list-valued keys like `write_files`, `runcmd`, and `packages` are concatenated across parts, so the extra file only needs to declare what it wants to add, never repeat what the plugin already sets.

This means the extra file cannot accidentally disable the plugin's own SSH/hostname setup — the two are independent documents, not one YAML tree the extra file could clobber a key in.

### Template variables

`cloud_init_extra_file` is read once, at plugin startup — not per clone — and parsed as a [Go `text/template`](https://pkg.go.dev/text/template). A broken template fails startup immediately rather than every subsequent clone. The following fields are available:

| Field | Description |
|---|---|
| `{{ .Hostname }}` | The clone's unique name (same value set as the guest hostname, see [VM Naming](#vm-naming)) |
| `{{ .RunnerName }}` | The `name` parameter of the `[[runners]]` section that owns this clone — useful for telling instance groups/tiers apart in labels |
| `{{ .Vars.<key> }}` | Any key from `cloud_init_vars` — the place to put values that shouldn't be hardcoded into the shared extra file, e.g. an internal registry token |

### Example: installing a monitoring agent

Anonymized example — the extra file installs a metrics/log-shipping agent from a private repository and configures a group/service override, without touching the plugin's own cloud-config:

```yaml
# /etc/gitlab-runner/cloud-init-extra.yaml
write_files:
  - path: /etc/agent/config.yaml
    permissions: '0644'
    owner: root:root
    content: |
      instance_label: {{ .RunnerName }}-{{ .Hostname }}
      remote_write_url: https://monitoring.example.com/api/v1/write

  - path: /etc/systemd/system/agent.service.d/override.conf
    permissions: '0644'
    owner: root:root
    content: |
      [Service]
      User=root

runcmd:
  - curl -fsSL -o /tmp/agent.deb "https://{{ .Vars.download_token }}@repo.example.com/agent-1.0.0.amd64.deb"
  - dpkg -i /tmp/agent.deb
  - rm -f /tmp/agent.deb
  - systemctl daemon-reload
  - usermod -aG docker agent
  - systemctl enable --now agent
```

With:

```toml
[runners.autoscaler.plugin_config.cloud_init_vars]
  download_token = "svc-repo-token:xxxxxxxx"
```
