# rift

A cross-hypervisor VM orchestration CLI built in Go.

---

## Features

- **VM lifecycle management** -start, stop, suspend, reset, and query power state
- **Parallel folder operations** -target all VMs in a VMware folder with `-f <folder>`, running eligible operations concurrently
- **Guest exec with OS auto-detection** -run commands inside VMs; interpreter is inferred from the VMX `guestOS` key (Linux -> `/bin/bash`, Windows -> `cmd.exe`)
- **Bootstrap provisioning** -provision the `runner` automation user on guest VMs in one command; downloads and runs the [bootstrap-utilities](https://github.com/J0sh0909/bootstrap-utilities) script automatically over VMware guest tools
- **Snapshot management** -create (with duplicate detection), list, revert, and delete snapshots; running VMs are suspended automatically before capture
- **OVF/OVA archive pipeline** -export and import VMs via `ovftool` with per-VM mpb progress bars; versioned directory layout under `ARCHIVE_PATH`
- **Hardware configuration** -edit CPU, RAM, NIC, disk, CD/DVD, and display settings with host-resource validation
- **Structured error codes** -every failure prints a `[VMxxx]` code; `rift errors` lists all codes and descriptions
- **GitHub Actions pipeline** -trigger any rift command against a self-hosted runner via `workflow_dispatch`
- **Cross-platform host support** -compiles and runs on both Windows and Linux hosts; host resource detection, VMware process management, and ovftool lookup all adapt automatically to the host OS

---

## Supported Hypervisors

| Hypervisor | `HYPERVISOR` value | Status |
|---|---|---|
| VMware Workstation | `workstation` | Implemented |
| Proxmox VE | `proxmox` | Planned |

---

## Prerequisites

- Go 1.23+
- VMware Workstation with `vmrun`, `vmware-vdiskmanager`, and `ovftool` available (`.exe` on Windows, no extension on Linux)
- Guest credentials for `exec` -use any existing account or provision a dedicated one with the [bootstrap-utilities](https://github.com/J0sh0909/bootstrap-utilities) script (recommended)

---

## Guest Credentials

The `exec` command requires guest OS credentials to run commands inside VMs. You can pass them explicitly on every call with `--user` and `--pass`, or set defaults in `.env` with `VM_DEFAULT_USER` and `VM_DEFAULT_PASS` so you never have to type them again.

**Provisioning a dedicated automation user (recommended)**

Use `rift bootstrap` to provision the `runner` automation account directly from the host -no manual guest login required. The command authenticates into the guest using VMware guest tools (with an existing admin/root account), downloads the [bootstrap-utilities](https://github.com/J0sh0909/bootstrap-utilities) script, and runs it inside the guest. It creates a `runner` user with the password you specify and grants it escalated privileges (`sudo` on Linux, local administrator on Windows).

```
# Provision the automation user (shorthand: rift bootstrap MyVM --runner-pass ...)
rift bootstrap create MyVM --runner-pass <runner-pass>
rift bootstrap create --folder MyFolder --runner-pass <runner-pass>
rift bootstrap create MyVM --user Administrator --pass AdminPass --runner-pass <runner-pass>

# Verify the automation user is correctly configured
rift bootstrap verify MyVM
rift bootstrap verify --folder MyFolder

# Reset the automation user password
rift bootstrap reset MyVM --runner-pass <new-password>
rift bootstrap reset --folder MyFolder --runner-pass <new-password>

# Remove the automation user
rift bootstrap revoke MyVM
rift bootstrap revoke --folder MyFolder
```

Once provisioned, add the following to your `.env` and all `exec` commands work without flags:

```
VM_DEFAULT_USER=runner
VM_DEFAULT_PASS=<password chosen during bootstrap>
```

**exec usage patterns**

```
# Explicit flags -works with any existing account, no .env required
rift exec MyVM "hostname" --user USER --pass PASSWORD

# .env defaults -after bootstrap, no flags needed
rift exec MyVM "hostname"

# Folder-wide with .env defaults
rift exec --folder MyFolder "hostname"
```

---

## Setup

```
git clone https://github.com/J0sh0909/remote-vm-manipulation
cd remote-vm-manipulation
```

Create a `.env` file in the repo root (or at the path pointed to by `ENV_PATH`):

**Windows**
```
VMRUN_PATH=C:\Program Files (x86)\VMware\VMware Workstation\vmrun.exe
VM_DIRECTORY=C:\Users\USER\Documents\Virtual Machines
INVENTORY_PATH=C:\Users\USER\AppData\Roaming\VMware\inventory.vmls
NETMAP_PATH=C:\ProgramData\VMware\netmap.conf
ISO_DIRECTORY=C:\Users\USER\Documents\ISO
VDISK_PATH=C:\Program Files (x86)\VMware\VMware Workstation\vmware-vdiskmanager.exe
ARCHIVE_PATH=C:\Users\USER\Documents\vm-storage
VM_DEFAULT_USER=runner
VM_DEFAULT_PASS=PASSWORD
HYPERVISOR=workstation
```

**Linux**
```
VMRUN_PATH=/usr/bin/vmrun
VM_DIRECTORY=/home/USER/vmware
INVENTORY_PATH=/home/USER/.vmware/inventory.vmls
NETMAP_PATH=/etc/vmware/netmap.conf
ISO_DIRECTORY=/home/USER/iso
VDISK_PATH=/usr/bin/vmware-vdiskmanager
ARCHIVE_PATH=/home/USER/vm-storage
VM_DEFAULT_USER=runner
VM_DEFAULT_PASS=PASSWORD
HYPERVISOR=workstation
```

Build:

```
# Windows
go build -o rift.exe .

# Linux
go build -o rift .

# or install to $GOPATH/bin on either platform:
go install .
```

---

## Usage (VMware Workstation)

> The commands below are specific to the VMware Workstation backend. When additional backends are implemented, the same command surface works identically -the `Hypervisor` interface abstracts all backend differences, so only the `.env` configuration changes.

```
# List all VMs grouped by folder
rift list

# Power operations
rift start MyVM
rift stop MyVM --hard
rift suspend --folder MyFolder
rift reset MyVM1 MyVM2

# VM info
rift info MyVM --specs --net --disk

# Bootstrap the runner automation user on a guest
rift bootstrap create MyVM --runner-pass PASSWORD
rift bootstrap create --folder MyFolder --runner-pass PASSWORD
rift bootstrap verify MyVM
rift bootstrap reset MyVM --runner-pass NEWPASSWORD
rift bootstrap revoke MyVM

# Run a command inside a guest
rift exec MyVM "whoami" --user USER --pass PASSWORD
rift exec --folder MyFolder "hostname" --user USER --pass PASSWORD

# Snapshots
rift snapshot create MyVM --name pre-upgrade
rift snapshot create --folder MyFolder --name pre-upgrade
rift snapshot list MyVM
rift snapshot revert MyVM pre-upgrade
rift snapshot revert --folder MyFolder --origin -y
rift snapshot delete MyVM pre-upgrade
rift snapshot delete --folder MyFolder --current -y

# OVF/OVA archives
rift archive export MyVM --format ova
rift archive export --folder MyFolder --format ovf --name backup
rift archive list
rift archive import MyVM-20260101-120000
rift archive import MyVM --latest --format ova
rift archive delete MyVM --oldest -y

# Hardware config
rift config cpu MyVM --sockets 1 --cores 4
rift config ram MyVM --size 8
rift config nic MyVM --add --type bridged
rift config nic MyVM --regen-mac --index 0
rift config disk add MyVM --size 50 --controller scsi0
rift config disk expand MyVM --controller scsi0 --slot 1 --size 100
rift config display MyVM --accel3d on --gfxmem 512
rift config os MyVM --set ubuntu-64

# Networking
rift networks

# ISO management
rift isos
rift config cdrom mount MyVM --iso ubuntu.iso --controller sata0
rift config cdrom unmount MyVM --controller sata0

# Error code reference
rift errors
```

### Running via GitHub Actions

When using the pipeline, enter only the command and its arguments in the workflow input -not the `rift` prefix. The workflow prepends the binary path automatically.

| Context | What you type |
|---|---|
| Local | `rift start MyVM` |
| Workflow input | `start MyVM` |

```
# Local
rift exec --folder MyFolder "hostname" --user USER --pass PASSWORD

# Workflow input field (Actions -> rift -> Run workflow)
exec --folder MyFolder "hostname" --user USER --pass PASSWORD
```

---

## Architecture

rift is built around a `Hypervisor` interface (`internal/hypervisor.go`) that abstracts all backend operations. Each hypervisor backend (e.g., `internal/workstation.go`) implements this interface independently, so commands in `cmd/root.go` are fully backend-agnostic.

VM inventory and power state are resolved through `internal.ResolveTargets` / `internal.ResolveAllVMs`, which query the backend and filter by folder or explicit name list. Parallel execution (folder mode) is handled at the command layer using goroutines with a shared mutex for result collection.

Adding a new hypervisor backend requires implementing the `Hypervisor` interface and registering it in `internal/config.go`.

---

## Pipeline

rift ships a `workflow_dispatch` GitHub Actions workflow (`.github/workflows/rift.yml`) that runs on a self-hosted Windows runner co-located with the VMware Workstation host.

The workflow has no checkout or build steps -it executes a pre-built `C:\actions-runner\rift.exe` directly, making dispatch-to-execution near-instant. The `.env` file lives at `C:\actions-runner\.env` on the runner machine and is never committed to the repository.

To use: go to **Actions -> rift -> Run workflow**, enter a rift command (e.g. `start MyVM`), and click **Run**.

---

## Roadmap

- Proxmox VE backend
- VM creation from YAML manifest
- Cross-hypervisor VM migration
- TUI dashboard

---

## License

MIT License - Copyright 2026 J0sh0909
