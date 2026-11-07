package cmd

import (
	"os"

	log "github.com/sirupsen/logrus"

	"github.com/nektos/act/pkg/tart"
)

// tartSSHPasswordEnv supplies --tart-ssh-password without putting it on
// the command line.
const tartSSHPasswordEnv = "ACT_TART_SSH_PASSWORD"

func (i *Input) tartHostConfig() tart.HostConfig {
	password := i.tartSSHPassword
	if password == "" {
		password = os.Getenv(tartSSHPasswordEnv)
	}
	return tart.HostConfig{
		SSHUser:     i.tartSSHUser,
		SSHPassword: password,
		SSHKeyPath:  i.tartSSHKey,
		MaxVMs:      i.tartMaxVMs,
		KeepFailed:  i.tartKeepFailed,
	}
}

// warnIgnoredForTart warns once about Docker-only flags when a platform
// runs in Tart.
func warnIgnoredForTart(i *Input, platforms map[string]string) {
	usesTart := false
	for _, image := range platforms {
		if tart.HasScheme(image) {
			usesTart = true
			break
		}
	}
	if !usesTart {
		return
	}
	var ignored []string
	if i.containerArchitecture != "" {
		ignored = append(ignored, "--container-architecture")
	}
	if i.privileged {
		ignored = append(ignored, "--privileged")
	}
	if i.containerOptions != "" {
		ignored = append(ignored, "--container-options")
	}
	for _, f := range ignored {
		log.Warnf("%s is ignored for tart platforms", f)
	}
}
