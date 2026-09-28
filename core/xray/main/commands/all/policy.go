package all

import (
	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/main/commands/base"
)

var cmdPolicyInit = &base.Command{
	UsageLine: "{{.Exec}} policy-init [-file /private/path/policy.db] [-instance node-instance-id]",
	Short:     "Initialize a new durable client-policy store without overwriting existing state",
	Long:      "Create a new execution-state store using a panel-assigned instance ID. Never use initialization to replace lost or rolled-back state; reconcile outstanding budgets with the panel first.",
}

var policyStateFile = cmdPolicyInit.Flag.String("file", "", "New private state file")
var policyInstanceID = cmdPolicyInit.Flag.String("instance", "", "Panel-assigned stable core instance ID")

func init() {
	cmdPolicyInit.Run = func(_ *base.Command, args []string) {
		if len(args) != 0 || *policyStateFile == "" || *policyInstanceID == "" {
			base.Fatalf("file and instance are required; positional arguments are not accepted")
		}
		if err := clientpolicy.CreateStore(*policyStateFile, *policyInstanceID); err != nil {
			base.Fatalf("initialize client policy: %s", err)
		}
	}
	base.RootCommand.Commands = append(base.RootCommand.Commands, cmdPolicyInit)
}
