package main

import (
	"gitlab.com/gitlab-org/fleeting/fleeting/plugin"
	vsphere "gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere"
)

func main() {
	plugin.Main(&vsphere.InstanceGroup{}, vsphere.Version)
}
