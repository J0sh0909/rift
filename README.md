# rift

A cross-hypervisor VM orchestration CLI built in Go.

## Features

- **Folders that span hypervisors and nodes** - `rift start -f DEV` acts on every VM in the DEV folder on every hypervisor of the node (VMware, VirtualBox, Hyper-V); run through the pipeline, it reaches every node
- **VM lifecycle management** - start, stop, suspend, reset, restart, and query power state
- **Parallel execution** - targets run concurrently (capped per hypervisor), transient hypervisor errors are retried, and results are printed sorted with a summary
- **Guest exec on every hypervisor** - `rift exec -f DEV ip a` prints the output of every guest; interpreter is inferred from the guest OS (Linux -> /bin/bash, Windows -> cmd.exe)
- **Guest IPs without credentials** - `rift ip -f DEV` lists the addresses reported by VMware Tools, Guest Additions or Hyper-V integration services
- **Bootstrap provisioning** - provision the runner automation user on guest VMs in one command
- **Snapshot management** - create (with duplicate detection), list, revert, and delete snapshots
- **Cross-hypervisor migration** - migrate VMs between VMware Workstation and VirtualBox with rift migrate
- **OVF/OVA archive pipeline** - export and import VMs via ovftool / VBoxManage with per-VM progress bars
- **Hardware configuration** - edit CPU, RAM, NIC, disk, CD/DVD, and display settings (VMware; refused while the VM runs)
- **Structured error codes** - every failure prints a [VMxxx] code; rift errors lists all codes
- **GitHub Actions pipeline** - run any rift command on all self-hosted nodes (or a subset) via workflow_dispatch, with a per-node results table in the run summary
- **AWS EC2 management** - create, start, stop, terminate, and SSH into EC2 instances
- **Cross-platform host support** - compiles and runs on both Windows and Linux hosts

## Supported Platforms

| Platform | Folders | Lifecycle | Snapshots | Exec | IP | Config | Export | Migrate |
|---|---|---|---|---|---|---|---|---|
| VMware Workstation | inventory folders | start, stop, suspend, reset | create, list, revert, delete | vmrun (VMware Tools) | VMware Tools | cpu, ram, nic, disk, cdrom, display, os, tpm | ovf/ova | to/from vbox |
| VirtualBox | groups (`/DEV`) | start, stop, suspend, reset | take, list, restore, delete | guestcontrol (Guest Additions) | Guest Additions | - | ovf/ova | to/from vmware |
| Hyper-V | VM groups or `rift-folder:` in notes | start, stop, save, reset | checkpoints | PowerShell Direct (Windows), SSH (Linux) | integration services | - | - | - |
| AWS EC2 (`rift aws`) | - | start, stop, create, terminate | - | ssh | `rift aws ip` | - | - | - |
| Proxmox VE | planned | - | - | - | - | - | - | - |

## Targeting VMs

Every command that acts on VMs takes VM names or `-f/--folder`:

```bash
rift start web-1 db-1          # by name, on whichever hypervisor has them
rift start -f DEV              # every VM in folder DEV, on every hypervisor of this node
rift stop -f DEV,TEST --hard   # several folders
rift --hv vbox start shared    # restrict to one hypervisor (needed if a name exists on two)
```

A VM's **folder** is:

- **VMware Workstation** - its folder in the Workstation library (inventory `.vmls`)
- **VirtualBox** - its group (`/LAB/DEV` -> `DEV`, the immediate parent, like VMware)
- **Hyper-V** - its VM group (`Get-VMGroup`), or a line `rift-folder: DEV` in the VM's notes (takes precedence)

VMs without a folder are in `Ungrouped`. Folder names are case-insensitive.

`rift list`, `rift overview` and `rift ip` show every hypervisor on the node. `--hv` limits any command to one backend.

### Guest commands

```bash
rift exec H26-U-S uname -a     # one VM: raw output, scriptable
rift exec -f DEV ip a          # a folder: output under a header per VM
rift exec -f WIN ipconfig      # Windows guests run through cmd.exe
rift exec -f DEV --timeout 2m 'apt-get update && apt-get -y upgrade'
rift ip -f DEV                 # IPs as reported by the hypervisor, no credentials
```

rift flags go before the VM name; everything after it is the guest command (`rift exec vm ls -la` works).
The words of the command are joined with spaces and run by the guest shell, so quote the whole command
when it contains `&&`, pipes or quoted strings.
Credentials default to `VM_DEFAULT_USER` / `VM_DEFAULT_PASS`. Exec transports:

- VMware: vmrun + VMware Tools
- VirtualBox: `VBoxManage guestcontrol` + Guest Additions (password passed via a temp file, not the command line)
- Hyper-V Windows guests: PowerShell Direct
- Hyper-V Linux guests: SSH to the IP reported by integration services (`hv_kvp_daemon`). Host keys are trusted on first use and pinned in `~/.rift/known_hosts` (`VM_SSH_KNOWN_HOSTS`)

When the guest OS can't be detected, rift falls back to the naming convention (`-w-` Windows, `-u-` Linux). On Hyper-V you can also add `rift-os: linux` (or `windows`) to the VM notes.

### Exit status

rift exits 1 when any targeted VM failed (including a guest command exiting non-zero) or a named VM/folder is missing. "Already running", "not running" and "not supported on this hypervisor" are reported as skipped and don't fail the run.

## Running across all nodes (GitHub Actions)

`.github/workflows/rift.yml` runs on self-hosted runners labelled `strong`, `weak` and `tank`:

- **push to main/dev** - vet, test and build `rift.exe` + `rift-linux` on `strong`, then install them on every node (`rift version` confirms)
- **pull request** - format/vet/test/build on a GitHub-hosted runner. Self-hosted runners never run PR code: the repository is public, so forks could otherwise run code on the lab machines
- **workflow_dispatch** - run a rift command on every node in parallel

Run a command from the Actions tab (*rift* -> *Run workflow*) or with the GitHub CLI:

```bash
gh workflow run rift.yml -f command="start -f DEV"
gh workflow run rift.yml -f command="exec -f DEV ip a"
gh workflow run rift.yml -f command="ip -f DEV" -f nodes=tank
gh workflow run rift.yml -f command="snapshot create -f DEV -n before-upgrade" -f nodes='["strong","tank"]'
```

Each node runs `rift dispatch`, which:

- reads the command from an environment variable and splits it without a shell, so the input can't inject shell code (quote arguments with spaces: `-n "pre upgrade"`)
- prints the node name and rift version, then the hypervisors found
- treats VMs/folders the node doesn't have as notices, so nodes without a DEV folder stay green
- writes a results table, and each guest's output, to the run summary - one section per node

Confirmation prompts can't be answered in the pipeline: pass `--yes` (e.g. `snapshot delete -f DEV --current --yes`).

Optional repository variables (*Settings -> Secrets and variables -> Actions -> Variables*):

| Variable | Default | Purpose |
|---|---|---|
| `RIFT_NODES` | `["strong","weak","tank"]` | runner labels of the nodes |
| `RIFT_HOME` | `C:\actions-runner` | where `rift.exe` is installed on nodes |
| `RIFT_ENV_PATH` | `C:/actions-runner/.env` | `.env` used by the pipeline |
| `RIFT_COMMAND_TIMEOUT` | `180` | minutes before a node's command job is cancelled |

### Running rift by hand on a node

The pipeline installs `rift.exe` into `RIFT_HOME` (`C:\actions-runner`) and calls it by full path. Typing `rift` in a terminal goes through PATH instead, so it can run an older copy installed somewhere else. Each deploy checks this and, where it's wrong, adds a warning and a section to the run summary. The runner service account can't change the machine PATH, so the fix is a one-time step in an elevated PowerShell on that node:

```powershell
powershell -ExecutionPolicy Bypass -File C:\actions-runner\rift-path.ps1               # check only
powershell -ExecutionPolicy Bypass -File C:\actions-runner\rift-path.ps1 -Fix          # put C:\actions-runner first on the machine PATH
powershell -ExecutionPolicy Bypass -File C:\actions-runner\rift-path.ps1 -Fix -RemoveStale  # and delete the other copies
```

Terminals that were already open keep their old PATH; open a new one.

## WSL/Windows Interoperability

Rift supports both native Windows execution and WSL/Linux execution through a dual-binary approach:

- **Windows Host**: Use rift.exe with .env file containing Windows paths
- **WSL/Linux Host**: Use rift-linux with .env.wsl file containing WSL paths
- **Limitation**: Some commands requiring file access to Windows user directories may not work when calling rift.exe from WSL
- **Workaround**: Use rift-linux binary within WSL with appropriate .env.wsl configuration

## Setup

```bash
git clone https://github.com/J0sh0909/rift
cd rift
```

### Environment Configuration

Create .env (Windows) or .env.wsl (WSL/Linux) files with your configuration. Set `ENV_PATH` to use a file elsewhere.

```bash
# VMware Workstation
VMRUN_PATH=path/to/vmrun
VM_DIRECTORY=path/to/vms
INVENTORY_PATH=path/to/inventory.vmls
NETMAP_PATH=path/to/netmap.conf
VDISK_PATH=path/to/vdiskmanager
VM_ENCRYPTION_PASS=            # for encrypted VMs (or --vp)

# VirtualBox (VBoxManage is often not on PATH on Windows)
VBOX_MANAGE_PATH=C:\Program Files\Oracle\VirtualBox\VBoxManage.exe

# Hyper-V
HYPERV_ENABLED=true

# Default hypervisor when a VM name exists on several (optional)
HYPERVISOR=workstation

# Guest credentials for exec/bootstrap
VM_DEFAULT_USER=runner
VM_DEFAULT_PASS=your_password
VM_SSH_PORT=22                 # Hyper-V Linux guests
VM_SSH_KNOWN_HOSTS=            # default ~/.rift/known_hosts

# Node
RIFT_NODE=omen                 # name shown in output/summaries (default: hostname)
RIFT_MAX_PARALLEL=             # concurrent operations per hypervisor (default 8, Hyper-V 4)

ISO_DIRECTORY=path/to/isos
ARCHIVE_PATH=path/to/archive
QEMU_IMG_PATH=path/to/qemu-img
LOG_PATH=path/to/rift.log
AWS_REGION=us-east-1
AWS_KEY_DIR=path/to/ssh/keys
```

Every hypervisor that is configured (or, for VirtualBox, found on PATH) is used.

### Building

```bash
# Windows
go build -o rift.exe .

# Linux/WSL
go build -o rift-linux .

# Tests (the Hyper-V script tests also need PowerShell 7: RIFT_TEST_PWSH=/path/to/pwsh)
go test ./...
```

## Architecture

Rift is built around a Hypervisor interface that abstracts backend operations, plus optional capability interfaces (`GuestExecutor`, `GuestIPReporter`, `GuestOSReporter`):

- internal/core/ - Hypervisor interface, node inventory (folder/name resolution across backends), settings, VMX helpers
- internal/vmware/ - VMware Workstation backend (vmrun)
- internal/vbox/ - VirtualBox backend (VBoxManage)
- internal/hyperv/ - Hyper-V backend (PowerShell)
- internal/guestssh/ - SSH exec transport with trust-on-first-use host keys
- internal/aws/ - AWS EC2 backend
- internal/proxmox/ - Proxmox VE (planned)
- cmd/ - CLI commands on a shared engine: resolve targets -> run in parallel -> report

## License

MIT License - Copyright 2026 J0sh0909
