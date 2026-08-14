package main

import (
	vsphere "github.com/myplixa/vmware-fleeting-plugin"
	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"
)

func main() {
	plugin.Main(&vsphere.InstanceGroup{}, vsphere.Version)
}
