package vsphere

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
	"gitlab.com/gitlab-org/fleeting/fleeting/integration"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

const (
	datacenter     = "/DC0"
	host           = "/DC0/host/DC0_H0/DC0_H0"
	pool           = "/DC0/host/DC0_H0/Resources"
	datastore      = "/DC0/datastore/LocalDS_0"
	vmFolder       = "/DC0/vm"
	destFolderName = "fleeting-test"
	templateName   = "test-template"
	vmPathName     = "[LocalDS_0] fleeting-test"
)

func TestProvisioning(t *testing.T) {
	model := simulator.VPX()
	defer model.Remove()

	model.Datacenter = 1
	model.Host = 1
	model.Datastore = 1
	model.Cluster = 1
	model.Pool = 1
	model.Folder = 0

	err := model.Create()
	if err != nil {
		t.Fatalf("simulating vsphere: %s", err)
	}

	s := model.Service.NewServer()
	defer s.Close()

	setupTestEnv(t, s.URL)
	if err != nil {
		t.Fatalf("setting up test environment: %s", err)
	}

	integration.TestProvisioning(t,
		integration.BuildPluginBinary(t, "cmd/fleeting-plugin-vsphere", "fleeting-plugin-vsphere"),
		integration.Config{
			PluginConfig: InstanceGroup{
				VsphereUrl:         s.URL.String(),
				Template:           templateName,
				Folder:             vmFolder,
				Datacenter:         datacenter,
				Host:               host,
				ResourcePool:       pool,
				Datastore:          datastore,
				InsecureConnection: true,
				Name:               "fleeting-plugin-test",
			},
			ConnectorConfig: provider.ConnectorConfig{
				Timeout: 10 * time.Minute,
			},
			MaxInstances:    3,
			UseExternalAddr: false,
		})
}

func setupTestEnv(t *testing.T, url *url.URL) error {
	t.Helper()
	ctx := context.Background()

	c, err := govmomi.NewClient(ctx, url, true)
	if err != nil {
		return err
	}

	finder := find.NewFinder(c.Client)

	dc, err := finder.Datacenter(ctx, datacenter)
	if err != nil {
		return err
	}
	finder.SetDatacenter(dc)

	f, err := finder.Folder(ctx, vmFolder)
	if err != nil {
		return err
	}

	vf, err := f.CreateFolder(ctx, destFolderName)
	if err != nil {
		return err
	}

	pool, err := finder.ResourcePool(ctx, pool)
	if err != nil {
		return err
	}

	host, err := finder.HostSystem(ctx, host)
	if err != nil {
		return err
	}

	spec := types.VirtualMachineConfigSpec{
		Name: templateName,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: vmPathName,
		},
	}

	task, err := vf.CreateVM(ctx, spec, pool, host)
	if err != nil {
		return err
	}

	info, err := task.WaitForResult(ctx)
	if err != nil {
		return err
	}

	vm := object.NewVirtualMachine(c.Client, info.Result.(types.ManagedObjectReference))
	err = vm.MarkAsTemplate(ctx)
	if err != nil {
		return err
	}

	return nil
}
