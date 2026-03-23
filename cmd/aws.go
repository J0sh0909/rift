package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/J0sh0909/rift/internal"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// AWS flags
// ---------------------------------------------------------------------------

var (
	awsAllFlag    bool
	awsHardFlag   bool
	awsYesFlag    bool
	awsAMIFlag    string
	awsNameFlag   string
	awsTypeFlag   string
	awsRegionFlag string
	awsUserFlag   string
	awsKeyFlag    string
)

// ---------------------------------------------------------------------------
// Lazy AWS backend
// ---------------------------------------------------------------------------

var (
	awsBackend  *internal.AWSBackend
	awsSettings internal.Settings
)

func requireAWS() {
	if awsBackend != nil {
		return
	}
	// Load settings for AWS_REGION/AWS_KEY_DIR (best-effort; .env is optional for AWS).
	awsSettings, _ = internal.LoadSettings()
	region := awsRegionFlag
	if region == "" {
		region = awsSettings.AWSRegion
	}
	var err error
	awsBackend, err = internal.NewAWSBackend(region)
	if err != nil {
		internal.LogError(internal.ErrAWS, "", "initializing AWS: %s", err)
		os.Exit(1)
	}
}

// awsKeyPath returns the full path for a .pem file, using AWS_KEY_DIR if set.
func awsKeyPath(keyName string) string {
	filename := keyName + ".pem"
	if awsSettings.AWSKeyDir != "" {
		return filepath.Join(awsSettings.AWSKeyDir, filename)
	}
	return filename
}

// ---------------------------------------------------------------------------
// State management — rift-state.json
// ---------------------------------------------------------------------------

// RiftState is the top-level state file structure.
type RiftState struct {
	Instances []RiftInstance `json:"instances"`
}

// RiftInstance tracks resources created by a single rift aws create.
type RiftInstance struct {
	InstanceID      string `json:"instance_id"`
	Name            string `json:"name"`
	KeyPairName     string `json:"key_pair_name"`
	SecurityGroupID string `json:"security_group_id"`
	EIPAllocationID string `json:"eip_allocation_id,omitempty"`
	SubnetID        string `json:"subnet_id"`
	AMI             string `json:"ami"`
	InstanceType    string `json:"instance_type"`
	CreatedAt       string `json:"created_at"`
}

func statePath() string {
	if awsSettings.RiftStatePath != "" {
		return awsSettings.RiftStatePath
	}
	return "rift-state.json"
}

func loadState() RiftState {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return RiftState{}
	}
	var s RiftState
	json.Unmarshal(data, &s)
	return s
}

func saveState(s RiftState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(), data, 0644)
}

func stateAddInstance(ri RiftInstance) {
	s := loadState()
	s.Instances = append(s.Instances, ri)
	if err := saveState(s); err != nil {
		fmt.Fprintf(os.Stderr, "warning: saving state: %s\n", err)
	}
}

func stateRemoveInstance(instanceID string) RiftInstance {
	s := loadState()
	var removed RiftInstance
	var kept []RiftInstance
	for _, ri := range s.Instances {
		if ri.InstanceID == instanceID {
			removed = ri
		} else {
			kept = append(kept, ri)
		}
	}
	s.Instances = kept
	if err := saveState(s); err != nil {
		fmt.Fprintf(os.Stderr, "warning: saving state: %s\n", err)
	}
	return removed
}

// keyPairInUse returns true if any other instance in state uses this key pair.
func keyPairInUse(keyName, excludeInstanceID string) bool {
	s := loadState()
	for _, ri := range s.Instances {
		if ri.KeyPairName == keyName && ri.InstanceID != excludeInstanceID {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// rift aws
// ---------------------------------------------------------------------------

var awsCmd = &cobra.Command{
	Use:   "aws",
	Short: "Manage AWS EC2 instances",
}

// ---------------------------------------------------------------------------
// rift aws list
// ---------------------------------------------------------------------------

var awsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List EC2 instances",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		requireAWS()
		instances, err := awsBackend.ListInstances(awsAllFlag)
		if err != nil {
			internal.LogError(internal.ErrAWS, "", "listing instances: %s", err)
			os.Exit(1)
		}
		if len(instances) == 0 {
			fmt.Println("No instances found.")
			return
		}
		fmt.Printf("%-20s %-20s %-12s %-12s %-16s %-16s %s\n",
			"INSTANCE ID", "NAME", "STATE", "TYPE", "PUBLIC IP", "PRIVATE IP", "LAUNCHED")
		fmt.Println(strings.Repeat("-", 110))
		for _, i := range instances {
			launched := ""
			if !i.LaunchTime.IsZero() {
				launched = i.LaunchTime.Format("2006-01-02 15:04")
			}
			pub := i.PublicIP
			if pub == "" {
				pub = "-"
			}
			name := i.Name
			if name == "" {
				name = "-"
			}
			fmt.Printf("%-20s %-20s %-12s %-12s %-16s %-16s %s\n",
				i.ID, name, i.State, i.Type, pub, i.PrivateIP, launched)
		}
	},
}

// ---------------------------------------------------------------------------
// rift aws start
// ---------------------------------------------------------------------------

var awsStartCmd = &cobra.Command{
	Use:   "start <instance-id> [instance-id...]",
	Short: "Start stopped EC2 instances",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireAWS()
		if err := awsBackend.StartInstances(args); err != nil {
			internal.LogError(internal.ErrAWSStartFailed, "", "%s", err)
			os.Exit(1)
		}
		for _, id := range args {
			fmt.Printf("%s → starting...\n", id)
		}
		for _, id := range args {
			if err := awsBackend.WaitUntilRunning(id, 5*time.Minute); err != nil {
				internal.LogError(internal.ErrAWSStartFailed, id, "waiting for running: %s", err)
				continue
			}
			inst, err := awsBackend.GetInstance(id)
			if err != nil {
				internal.LogError(internal.ErrAWSNotFound, id, "%s", err)
				continue
			}
			pub := inst.PublicIP
			if pub == "" {
				pub = "(no public IP)"
			}
			fmt.Printf("%s → running — %s\n", id, pub)
		}
	},
}

// ---------------------------------------------------------------------------
// rift aws stop
// ---------------------------------------------------------------------------

var awsStopCmd = &cobra.Command{
	Use:   "stop <instance-id> [instance-id...]",
	Short: "Stop running EC2 instances",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireAWS()
		if err := awsBackend.StopInstances(args, awsHardFlag); err != nil {
			internal.LogError(internal.ErrAWSStopFailed, "", "%s", err)
			os.Exit(1)
		}
		for _, id := range args {
			fmt.Printf("%s → stopping...\n", id)
		}
		for _, id := range args {
			if err := awsBackend.WaitUntilStopped(id, 5*time.Minute); err != nil {
				internal.LogError(internal.ErrAWSStopFailed, id, "waiting for stopped: %s", err)
				continue
			}
			fmt.Printf("%s → stopped\n", id)
		}
	},
}

// ---------------------------------------------------------------------------
// rift aws create
// ---------------------------------------------------------------------------

var awsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Launch a new EC2 instance",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		requireAWS()
		if awsAMIFlag == "" || awsNameFlag == "" {
			fmt.Fprintln(os.Stderr, "error: --ami and --name are required")
			os.Exit(1)
		}
		instType := awsTypeFlag
		if instType == "" {
			instType = "t2.micro"
		}

		// 1. Key pair — reuse via --key or create new.
		keyName := awsKeyFlag
		pemPath := ""
		if keyName != "" {
			// Reuse existing key pair.
			pemPath = awsKeyPath(keyName)
			fmt.Printf("key pair → %s (reusing)\n", keyName)
		} else {
			keyName = "rift-" + awsNameFlag
			pemPath = awsKeyPath(keyName)
			// If .pem already exists locally, assume the AWS key pair exists too.
			if _, err := os.Stat(pemPath); err == nil {
				fmt.Printf("key pair → %s (existing)\n", pemPath)
			} else {
				pemData, err := awsBackend.CreateKeyPair(keyName)
				if err != nil {
					internal.LogError(internal.ErrAWSCreateFailed, "", "creating key pair: %s", err)
					os.Exit(1)
				}
				if err := os.WriteFile(pemPath, []byte(pemData), 0600); err != nil {
					internal.LogError(internal.ErrAWSCreateFailed, "", "writing key file: %s", err)
					os.Exit(1)
				}
				fmt.Printf("key pair → %s\n", pemPath)
			}
		}

		// 2. Get default VPC + subnet.
		vpcID, err := awsBackend.GetDefaultVPC()
		if err != nil {
			internal.LogError(internal.ErrAWSCreateFailed, "", "finding default VPC: %s", err)
			os.Exit(1)
		}
		subnetID, err := awsBackend.GetFirstSubnet(vpcID)
		if err != nil {
			internal.LogError(internal.ErrAWSCreateFailed, "", "finding subnet: %s", err)
			os.Exit(1)
		}

		// 3. Security group.
		sgID, err := awsBackend.EnsureSecurityGroup(vpcID)
		if err != nil {
			internal.LogError(internal.ErrAWSCreateFailed, "", "security group: %s", err)
			os.Exit(1)
		}

		// 4. Launch instance.
		fmt.Printf("launching %s (%s)...\n", awsNameFlag, instType)
		instanceID, err := awsBackend.CreateInstance(awsAMIFlag, awsNameFlag, instType, keyName, sgID, subnetID)
		if err != nil {
			internal.LogError(internal.ErrAWSCreateFailed, "", "%s", err)
			os.Exit(1)
		}

		// 5. Wait for running.
		if err := awsBackend.WaitUntilRunning(instanceID, 5*time.Minute); err != nil {
			internal.LogError(internal.ErrAWSCreateFailed, instanceID, "waiting for running: %s", err)
			os.Exit(1)
		}

		// 6. Elastic IP.
		var eipAllocID string
		publicIP, allocID, eipErr := awsBackend.AllocateAndAssociateEIP(instanceID)
		if eipErr != nil {
			internal.LogError(internal.ErrAWSCreateFailed, instanceID, "elastic IP: %s", eipErr)
		} else {
			eipAllocID = allocID
		}

		inst, _ := awsBackend.GetInstance(instanceID)
		if publicIP == "" {
			publicIP = inst.PublicIP
		}

		// 7. Save to state file.
		stateAddInstance(RiftInstance{
			InstanceID:      instanceID,
			Name:            awsNameFlag,
			KeyPairName:     keyName,
			SecurityGroupID: sgID,
			EIPAllocationID: eipAllocID,
			SubnetID:        subnetID,
			AMI:             awsAMIFlag,
			InstanceType:    instType,
			CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		})

		fmt.Println(strings.Repeat("-", 50))
		fmt.Printf("instance:  %s\n", instanceID)
		fmt.Printf("public IP: %s\n", publicIP)
		fmt.Printf("key file:  %s\n", pemPath)
		user := internal.GuessSSHUser(inst.Platform)
		if publicIP != "" {
			fmt.Printf("ssh:       ssh -i %s %s@%s\n", pemPath, user, publicIP)
		}
	},
}

// ---------------------------------------------------------------------------
// rift aws terminate
// ---------------------------------------------------------------------------

var awsTerminateCmd = &cobra.Command{
	Use:   "terminate <instance-id> [instance-id...]",
	Short: "Terminate EC2 instances and clean up resources",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireAWS()
		if !awsYesFlag {
			fmt.Fprintf(os.Stderr, "error: --yes is required to confirm termination\n")
			os.Exit(1)
		}
		for _, id := range args {
			awsTerminateOne(id)
		}
	},
}

func awsTerminateOne(id string) {
	ri := stateRemoveInstance(id)

	// Release EIP from state if tracked, otherwise try discovery.
	if ri.EIPAllocationID != "" {
		if err := awsBackend.ReleaseEIP(ri.EIPAllocationID); err != nil {
			fmt.Fprintf(os.Stderr, "%s: warning: releasing EIP: %s\n", id, err)
		} else {
			fmt.Printf("%s → released Elastic IP\n", id)
		}
	} else {
		if err := awsBackend.ReleaseInstanceEIPs(id); err != nil {
			fmt.Fprintf(os.Stderr, "%s: warning: releasing EIP: %s\n", id, err)
		}
	}

	// Delete key pair from AWS if no other instances use it.
	if ri.KeyPairName != "" && !keyPairInUse(ri.KeyPairName, id) {
		if err := awsBackend.DeleteKeyPair(ri.KeyPairName); err != nil {
			fmt.Fprintf(os.Stderr, "%s: warning: deleting key pair: %s\n", id, err)
		} else {
			fmt.Printf("%s → deleted key pair %s\n", id, ri.KeyPairName)
		}
	}

	// Terminate.
	if err := awsBackend.TerminateInstances([]string{id}); err != nil {
		internal.LogError(internal.ErrAWSTermFailed, id, "%s", err)
		return
	}
	fmt.Printf("%s → terminated\n", id)
}

// ---------------------------------------------------------------------------
// rift aws destroy
// ---------------------------------------------------------------------------

var awsDestroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Terminate ALL rift-managed instances and clean up resources",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		requireAWS()
		if !awsYesFlag {
			fmt.Fprintf(os.Stderr, "error: --yes is required to confirm destruction\n")
			os.Exit(1)
		}
		state := loadState()
		if len(state.Instances) == 0 {
			fmt.Println("No rift-managed instances found.")
			return
		}
		fmt.Printf("Destroying %d rift-managed instance(s)...\n", len(state.Instances))

		// Collect unique security groups before removing state entries.
		sgIDs := map[string]bool{}
		for _, ri := range state.Instances {
			if ri.SecurityGroupID != "" {
				sgIDs[ri.SecurityGroupID] = true
			}
		}

		for _, ri := range state.Instances {
			awsTerminateOne(ri.InstanceID)
		}

		// Try to delete security groups now that no instances reference them.
		for sgID := range sgIDs {
			if err := awsBackend.DeleteSecurityGroup(sgID); err != nil {
				fmt.Fprintf(os.Stderr, "warning: deleting security group %s: %s\n", sgID, err)
			} else {
				fmt.Printf("deleted security group %s\n", sgID)
			}
		}
	},
}

// ---------------------------------------------------------------------------
// rift aws state
// ---------------------------------------------------------------------------

var awsStateCmd = &cobra.Command{
	Use:   "state",
	Short: "Show rift-managed AWS resources",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		requireAWS()
		state := loadState()
		if len(state.Instances) == 0 {
			fmt.Println("No rift-managed instances.")
			return
		}
		fmt.Printf("%-20s %-20s %-14s %-20s %-16s %s\n",
			"INSTANCE ID", "NAME", "TYPE", "KEY PAIR", "EIP ALLOC", "CREATED")
		fmt.Println(strings.Repeat("-", 115))
		for _, ri := range state.Instances {
			eip := ri.EIPAllocationID
			if eip == "" {
				eip = "-"
			}
			fmt.Printf("%-20s %-20s %-14s %-20s %-16s %s\n",
				ri.InstanceID, ri.Name, ri.InstanceType, ri.KeyPairName, eip, ri.CreatedAt)
		}
	},
}

// ---------------------------------------------------------------------------
// rift aws ssh
// ---------------------------------------------------------------------------

var awsSSHCmd = &cobra.Command{
	Use:   "ssh <instance-id>",
	Short: "SSH into an EC2 instance",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireAWS()
		inst, err := awsBackend.GetInstance(args[0])
		if err != nil {
			internal.LogError(internal.ErrAWSNotFound, args[0], "%s", err)
			os.Exit(1)
		}
		if inst.PublicIP == "" {
			fmt.Fprintln(os.Stderr, "error: instance has no public IP")
			os.Exit(1)
		}
		user := awsUserFlag
		if user == "" {
			user = internal.GuessSSHUser(inst.Platform)
		}
		pemPath := awsKeyPath(inst.KeyName)
		// If that doesn't exist, try without the rift- prefix.
		if _, err := os.Stat(pemPath); err != nil {
			pemPath = awsKeyPath(strings.TrimPrefix(inst.KeyName, "rift-"))
		}
		sshCmd := fmt.Sprintf("ssh -i %s %s@%s", pemPath, user, inst.PublicIP)
		fmt.Println(sshCmd)

		// Connect directly.
		c := exec.Command("ssh", "-i", pemPath, fmt.Sprintf("%s@%s", user, inst.PublicIP))
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			os.Exit(1)
		}
	},
}

// ---------------------------------------------------------------------------
// rift aws ip
// ---------------------------------------------------------------------------

var awsIPCmd = &cobra.Command{
	Use:   "ip <instance-id>",
	Short: "Show public and private IP of an EC2 instance",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireAWS()
		inst, err := awsBackend.GetInstance(args[0])
		if err != nil {
			internal.LogError(internal.ErrAWSNotFound, args[0], "%s", err)
			os.Exit(1)
		}
		pub := inst.PublicIP
		if pub == "" {
			pub = "(none)"
		}
		fmt.Printf("public:  %s\n", pub)
		fmt.Printf("private: %s\n", inst.PrivateIP)
	},
}

// ---------------------------------------------------------------------------
// Registration
// ---------------------------------------------------------------------------

func init() {
	rootCmd.AddCommand(awsCmd)
	awsCmd.AddCommand(awsListCmd)
	awsCmd.AddCommand(awsStartCmd)
	awsCmd.AddCommand(awsStopCmd)
	awsCmd.AddCommand(awsCreateCmd)
	awsCmd.AddCommand(awsTerminateCmd)
	awsCmd.AddCommand(awsDestroyCmd)
	awsCmd.AddCommand(awsStateCmd)
	awsCmd.AddCommand(awsSSHCmd)
	awsCmd.AddCommand(awsIPCmd)

	awsListCmd.Flags().BoolVar(&awsAllFlag, "all", false, "Include terminated instances")
	awsStopCmd.Flags().BoolVarP(&awsHardFlag, "hard", "H", false, "Force stop")
	awsTerminateCmd.Flags().BoolVarP(&awsYesFlag, "yes", "y", false, "Confirm termination")
	awsDestroyCmd.Flags().BoolVarP(&awsYesFlag, "yes", "y", false, "Confirm destruction")
	awsCreateCmd.Flags().StringVar(&awsAMIFlag, "ami", "", "AMI ID (required)")
	awsCreateCmd.Flags().StringVar(&awsNameFlag, "name", "", "Instance name (required)")
	awsCreateCmd.Flags().StringVar(&awsTypeFlag, "type", "t2.micro", "Instance type")
	awsCreateCmd.Flags().StringVar(&awsKeyFlag, "key", "", "Reuse existing AWS key pair by name")
	awsCmd.PersistentFlags().StringVar(&awsRegionFlag, "region", "", "AWS region (overrides AWS_REGION from .env)")
	awsSSHCmd.Flags().StringVarP(&awsUserFlag, "user", "u", "", "SSH username (default: auto-detect from AMI)")
}
