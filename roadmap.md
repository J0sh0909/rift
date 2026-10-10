RIFT — Complete Project Roadmap

v1.0.0 — VMware Workstation CLI (DONE)

VM lifecycle: start, stop, suspend, reset, restart
Parallel folder operations with goroutines and retry logic
Guest exec with OS auto-detection (80+ guest OS interpreters)
Windows exec via runProgramInGuest with cmd.exe
Linux exec via runScriptInGuest with output capture
Snapshot management with duplicate detection
OVF/OVA archive pipeline with mpb progress bars
Hardware config: CPU, RAM, NIC, disk, display, CD/DVD, TPM
VM encryption support with auto-detection and -vp flag
Structured error codes (VM1xx through VM9xx)
.env based configuration
rift errors reference command
rift info, rift list, rift networks, rift isos
Bootstrap companion tool integration (create, verify, reset, revoke)
Cross-platform: Windows and Linux host support
GitHub Actions pipeline with self-hosted runner
Build script with Windows/Linux cross-compilation


v1.1.0 — VirtualBox CLI Backend (DONE)

VBox lifecycle: list, start, stop, info, delete
VBox snapshots: create, list, revert, delete
VBox import/export OVF
VBox on the Hypervisor interface (same rift list works)
VBox detection for overview
rift vbox subcommands as shortcuts


v1.2.0 — Cross-Hypervisor Migration (DONE)

rift migrate <vm> --from vmware --to vbox and vice versa
Direct disk conversion via qemu-img (VMDK↔VDI, no OVF intermediate)
Config translation: CPU, RAM, guest OS, NIC type mapping
Folder migration with --folder flag and parallel mpb progress bars
Archive naming with hypervisor identifier (-vmw-, -vbox-)


v1.3.0 — AWS EC2 Backend (DONE)

rift aws create with auto key pair, security group, elastic IP
rift aws list, rift aws start, rift aws stop, rift aws terminate
rift aws ssh with auto user detection (ubuntu, ec2-user, admin)
rift aws ip for quick IP lookup
rift aws state for resource tracking via rift-state.json
rift aws destroy --yes for full cleanup (instances, EIPs, key pairs, security groups)
--count flag for mass creation with parallel goroutines
--key flag for key pair reuse


v1.4.0 — Unified Overview and Multi-Node (DONE)

rift overview showing VMware, VirtualBox, Hyper-V, and AWS in one view
Auto-detection of installed hypervisors from .env
--hv flag to override default hypervisor
Multi-section .env with all hypervisor configs
Auto-select when 1 hypervisor detected, list all on list/overview, require --hv on mutating commands when 2+ detected
Hyper-V detection stub
VBox and Hyper-V detection for overview
Multi-runner pipeline: build on Strong, distribute to Weak and Tank
Manual dispatch runs on all three machines simultaneously
Three-node infrastructure: Omen (VMware), Pavilion (VBox), AI (Hyper-V)


v1.4.1 — Node-Wide Folders and Pipeline Fan-Out (DONE)

Folder targeting spans every hypervisor on a node: VMware folders, VirtualBox groups, Hyper-V VM groups / "rift-folder:" notes
Name targeting across hypervisors, ambiguity detection, --hv to pick, HYPERVISOR as tie-breaker
Shared execution engine: per-hypervisor concurrency caps, retries for transient failures only, exit 1 on failures
Guest exec on VirtualBox (guestcontrol) and Hyper-V (PowerShell Direct / SSH with TOFU host keys)
rift ip: guest IPs from VMware Tools, Guest Additions, Hyper-V integration services
rift dispatch: injection-safe pipeline entry point, missing folders are notices, results in the run summary
Workflow: node matrix, node selection input, PR verification on hosted runner, versioned binaries, timeouts
Fixes: power-off gate for VMX edits, Linux bootstrap sudo/quoting, vmrun password redaction, VBOX_MANAGE_PATH


v1.5.0 — Hyper-V CLI Backend (mostly done in v1.4.1; VM creation remaining)

Full Hypervisor interface implementation wrapping PowerShell cmdlets
Get-VM → list with state, CPU, RAM
Start-VM / Stop-VM → lifecycle
Checkpoint-VM → snapshot create
Restore-VMCheckpoint / Remove-VMCheckpoint → snapshot revert/delete
Export-VM / Import-VM → archive operations
New-VM → VM creation
Guest exec via PowerShell Direct or WinRM
Test on AI (Tank) with real Hyper-V VMs


v1.6.0 — Migration Expansion

VMware ↔ Hyper-V migration (VMDK ↔ VHDX via qemu-img)
VirtualBox ↔ Hyper-V migration (VDI ↔ VHDX via qemu-img)
All six migration paths: vmware↔vbox, vmware↔hyperv, vbox↔hyperv
Driver cleanup during migration (remove VMware Tools, VBox Guest Additions, inject correct drivers)
initramfs regeneration for Linux guests after migration
NIC renaming and fstab UUID updates
Boot mode preservation (BIOS vs UEFI)


v2.0.0 — JSON Output and Machine-Readable Interface

--json flag on every command
All commands return structured JSON when --json is set
Error responses in JSON format with error codes
Enables AI/automation consumers to parse rift output
rift --json list returns {"hypervisor": "workstation", "vms": [{"name": "...", "state": "running"}]}
rift --json overview returns aggregated JSON from all detected hypervisors


v2.1.0 — VM Creation from YAML

rift create --file topology.yml
YAML manifest format:

yaml  vms:
    - name: k3s-worker-1
      os: ubuntu-64
      cpu: 4
      ram: 8192
      disks:
        - size: 60GB
      network:
        - type: bridged
      cloud-init: userdata.yml

Creates VMs on the default hypervisor or specified with --hv
VMware: generates .vmx + creates disks with vmware-vdiskmanager
VirtualBox: VBoxManage createvm + modifyvm + storagectl + storageattach
Hyper-V: New-VM + Set-VM + New-VHD
Cloud-init support for unattended Linux install
Parallel creation when manifest has multiple VMs
rift plan --file topology.yml shows what would be created without executing
Post-creation bootstrap: auto-run bootstrap-utilities to create runner user


v2.2.0 — REST API (rift serve)

rift serve --port 8080 starts HTTP server on each node
Endpoints mirror CLI commands:

GET /api/list → list VMs
POST /api/start → start VM
POST /api/stop → stop VM
POST /api/exec → execute command in guest
GET /api/overview → local hypervisor overview
POST /api/create → create VM from JSON payload
POST /api/migrate → start migration
GET /api/snapshots → list snapshots


API token authentication from .env
JSON request/response format
Runs on Tailscale IP for secure mesh access
Each node exposes its local hypervisor
Enables rift overview --fabric to query all nodes over HTTP


v2.3.0 — Fabric Mode (Multi-Node Orchestration)

rift overview --fabric queries all known nodes over Tailscale
Node registry in .env or nodes.yml:

yaml  nodes:
    - name: omen
      address: 100.82.208.41:8080
      hypervisor: workstation
    - name: pavilion
      address: 100.89.48.127:8080
      hypervisor: vbox
    - name: ai
      address: 100.84.84.55:8080
      hypervisor: hyperv

rift fabric list shows all VMs across all nodes
rift fabric start <vm> finds which node owns the VM and starts it remotely
rift fabric migrate <vm> --from omen --to pavilion migrates between physical machines

Converts disk, SCPs to target node over Tailscale, creates VM on target


Cross-node network visualization: shows which VMs on different nodes share the same bridge/subnet


v2.4.0 — TUI Dashboard (Bubble Tea)

rift tui launches terminal UI
Dashboard view: all VMs across all nodes in a navigable list
Keyboard shortcuts: Enter to start/stop, S for snapshot, E for exec
Live status updates polling each node's API
Fabric view: visualize nodes and their VMs
Progress bars for migration and export within the TUI
Lip Gloss styling: colors for state (green running, red stopped), borders, layout
Runs over SSH for remote access: ssh user@omen rift tui
Restricted SSH mode: ForceCommand rift tui for locked-down access


v3.0.0 — Proxmox Backend

Full Hypervisor interface via Proxmox REST API (/api2/json/)
Authentication with API tokens
VM lifecycle: start, stop, suspend, reset
Snapshot management
VM creation with cloud-init templates and storage pools
LXC container support alongside VMs
Cluster awareness: multi-node Proxmox clusters
Live migration between Proxmox nodes
Storage pool management: local, ZFS, Ceph
Network management: bridges, VLANs, bonds
Install Proxmox on dedicated hardware (old laptop or Tank alternative)


v3.1.0 — Type 2 to Type 1 Migration

rift migrate <vm> --from vmware --to proxmox
Read VMX config, convert VMDK to QCOW2 via qemu-img
SCP disk to Proxmox over SSH/Tailscale
qm importdisk on Proxmox side via API
Create VM with matching specs via Proxmox API
VirtIO driver injection for Linux guests
Windows guests: inject VirtIO drivers before migration
rift migrate <vm> --from proxmox --to vmware for reverse path
All Type 2 ↔ Type 1 paths: vmware↔proxmox, vbox↔proxmox, hyperv↔proxmox


v3.2.0 — Cloud Migration

rift migrate <vm> --to aws

Convert disk to RAW format
Upload to S3
Call aws ec2 import-image
Poll until AMI is ready
Launch instance from imported AMI
Assign security group, elastic IP, key pair


rift migrate <instance> --from aws --to vmware

Create AMI from instance
Export to S3 as VMDK
Download and register in VMware


Cloud-init and driver normalization for cloud targets
Pre-migration checks: compatible disk format, boot mode, drivers


v3.3.0 — API Backends (Performance)

Replace CLI shelling (vmrun, VBoxManage) with direct API calls
VMware Workstation: vmrest REST API
VirtualBox: COM/XPCOM API via Go bindings
Hyper-V: WMI direct queries instead of PowerShell subprocess
Reduced overhead, faster operations, more granular control
API backends expose features CLI backends can't: custom security groups, fine-grained network rules, programmatic firewall management


v4.0.0 — Ansible and Terraform Integration

Ansible inventory plugin: rift dynamic inventory source

Returns all VMs across all hypervisors as Ansible hosts
Groups by hypervisor, folder, OS type


Ansible module: rift_vm for lifecycle management in playbooks
Terraform provider: terraform-provider-rift

rift_vm resource for VM creation
rift_snapshot resource
rift_network data source
State management via Terraform state file


Post-creation Ansible playbooks: auto-configure VMs after rift create


v4.1.0 — Network Lab Automation

Topology-aware deployments from YAML

yaml  topology:
    routers:
      - name: R1
        image: vyos
        interfaces:
          - network: transit
          - network: lan-a
    switches:
      - name: SW1
        interfaces:
          - network: lan-a
          - network: lan-b
    clients:
      - name: PC1
        network: lan-a
      - name: PC2
        network: lan-b

Virtual network creation across hypervisors
Cross-hypervisor networking: bridge VMware VM to VBox VM on same subnet
Predefined templates: multi-router topology, K8s cluster, AD domain, DMZ
Network validation: ping tests, route checks, port scans after deployment


v5.0.0 — Additional Cloud Providers

Azure: rift azure create, rift azure list, managed disks, NSG rules
GCP: rift gcp create, rift gcp list, firewall rules, persistent disks
Oracle Cloud: KVM-based, similar to Proxmox integration
Cloud migration paths: AWS↔Azure, AWS↔GCP, any cloud↔any hypervisor
Multi-cloud overview in rift overview
Cost estimation: rift plan --file topology.yml --estimate shows projected cloud costs


v5.1.0 — Additional Hypervisors

KVM/libvirt: virsh CLI or libvirt Go bindings
XCP-ng: XenAPI
oVirt/RHV: REST API
Each follows the same pattern: implement Hypervisor interface, register in factory


v6.0.0 — AI-Driven Orchestration

rift ai command group
Local LLM (Ollama on Tank) interprets natural language:

"Start my K3S cluster" → rift start --folder K3S
"Migrate the Ubuntu server to VirtualBox" → rift migrate H26-U-S --from vmware --to vbox
"Create 3 web servers on AWS" → rift aws create --ami <id> --name web --count 3


Model reads rift --help and command documentation as context
rift serve JSON API is the interface between model and tool
OpenClaw integration for agentic coding against the rift codebase
Safety: rift plan shows proposed actions before execution, user approves


Ongoing — Quality and Polish

Unit test coverage for all non-hypervisor logic
Integration test suites per hypervisor (run on dedicated machines)
README maintenance with each release
GitHub releases with compiled binaries (Windows + Linux)
Error message improvements based on user feedback
Performance profiling and optimization
Documentation site (GitHub Pages or docs folder)
Contributing guide for open source contributions