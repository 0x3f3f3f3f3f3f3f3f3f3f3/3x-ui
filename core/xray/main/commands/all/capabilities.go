package all

import (
	"encoding/json"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/main/commands/base"
)

var cmdCapabilities = &base.Command{
	UsageLine: "{{.Exec}} capabilities",
	Short:     "Report compiled Custom Xray features as JSON without starting an instance",
	Long:      "Print the binary version, compatibility and compiled native/shared-policy features. This does not initialize execution state or report a configured instance's durability.",
}

func init() {
	cmdCapabilities.Run = func(_ *base.Command, args []string) {
		if len(args) != 0 {
			base.Fatalf("capabilities accepts no positional arguments")
		}
		arm := ""
		if runtime.GOARCH == "arm" {
			if info, ok := debug.ReadBuildInfo(); ok {
				for _, setting := range info.Settings {
					if setting.Key == "GOARM" {
						arm = strings.Split(setting.Value, ",")[0]
					}
				}
			}
		}
		report := struct {
			FormatVersion  int    `json:"formatVersion"`
			APIVersion     uint32 `json:"apiVersion"`
			Compatibility  string `json:"compatibility"`
			SourceRevision string `json:"sourceRevision"`
			Target         struct {
				OS   string `json:"os"`
				Arch string `json:"arch"`
				ARM  string `json:"arm,omitempty"`
			} `json:"target"`
			GoVersion    string   `json:"goVersion"`
			CoreVersion  string   `json:"coreVersion"`
			Capabilities []string `json:"capabilities"`
		}{FormatVersion: 1, APIVersion: 1, Compatibility: "traffic-control-v1", SourceRevision: core.BuildRevision(), GoVersion: runtime.Version(), CoreVersion: core.VersionStatement()[0], Capabilities: command.BuiltinCapabilities()}
		report.Target.OS, report.Target.Arch, report.Target.ARM = runtime.GOOS, runtime.GOARCH, arm
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			base.Fatalf("write compiled capabilities: %s", err)
		}
	}
	base.RootCommand.Commands = append(base.RootCommand.Commands, cmdCapabilities)
}
