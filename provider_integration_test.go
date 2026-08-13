package vsphere

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/mo"
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

const (
	linkedCloneVMName   = "linked-clone-source"
	linkedCloneSnapshot = "Base_1"
)

func TestProvisioning_LinkedClone(t *testing.T) {
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

	setupLinkedCloneEnv(t, s.URL)

	integration.TestProvisioning(t,
		integration.BuildPluginBinary(t, "cmd/fleeting-plugin-vsphere", "fleeting-plugin-vsphere"),
		integration.Config{
			PluginConfig: InstanceGroup{
				VsphereUrl:         s.URL.String(),
				Template:           linkedCloneVMName,
				Folder:             vmFolder,
				Datacenter:         datacenter,
				Host:               host,
				ResourcePool:       pool,
				Datastore:          datastore,
				InsecureConnection: true,
				LinkedClone:        true,
				Snapshot:           linkedCloneSnapshot,
				Name:               "fleeting-linked-test",
			},
			ConnectorConfig: provider.ConnectorConfig{
				Timeout: 10 * time.Minute,
			},
			MaxInstances:    3,
			UseExternalAddr: false,
		})
}

func setupLinkedCloneEnv(t *testing.T, url *url.URL) {
	t.Helper()
	ctx := context.Background()

	c, err := govmomi.NewClient(ctx, url, true)
	require.NoError(t, err)

	finder := find.NewFinder(c.Client)

	dc, err := finder.Datacenter(ctx, datacenter)
	require.NoError(t, err)
	finder.SetDatacenter(dc)

	f, err := finder.Folder(ctx, vmFolder)
	require.NoError(t, err)

	_, err = f.CreateFolder(ctx, destFolderName)
	require.NoError(t, err)

	rp, err := finder.ResourcePool(ctx, pool)
	require.NoError(t, err)

	hs, err := finder.HostSystem(ctx, host)
	require.NoError(t, err)

	spec := types.VirtualMachineConfigSpec{
		Name: linkedCloneVMName,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: vmPathName,
		},
	}

	task, err := f.CreateVM(ctx, spec, rp, hs)
	require.NoError(t, err)

	info, err := task.WaitForResult(ctx)
	require.NoError(t, err)

	vm := object.NewVirtualMachine(c.Client, info.Result.(types.ManagedObjectReference))

	// Create a snapshot for linked clone
	snapTask, err := vm.CreateSnapshot(ctx, linkedCloneSnapshot, "base snapshot for linked clones", false, false)
	require.NoError(t, err)

	err = snapTask.Wait(ctx)
	require.NoError(t, err)
}

func TestProvisioning_LinkedClone_CurrentSnapshot(t *testing.T) {
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

	// Create a VM with two snapshots — the current snapshot should be used
	setupLinkedCloneEnvWithMultipleSnapshots(t, s.URL)

	integration.TestProvisioning(t,
		integration.BuildPluginBinary(t, "cmd/fleeting-plugin-vsphere", "fleeting-plugin-vsphere"),
		integration.Config{
			PluginConfig: InstanceGroup{
				VsphereUrl:         s.URL.String(),
				Template:           linkedCloneVMName,
				Folder:             vmFolder,
				Datacenter:         datacenter,
				Host:               host,
				ResourcePool:       pool,
				Datastore:          datastore,
				InsecureConnection: true,
				LinkedClone:        true,
				Snapshot:           "", // empty = use current snapshot
				Name:               "fleeting-linked-current-test",
			},
			ConnectorConfig: provider.ConnectorConfig{
				Timeout: 10 * time.Minute,
			},
			MaxInstances:    3,
			UseExternalAddr: false,
		})
}

func setupLinkedCloneEnvWithMultipleSnapshots(t *testing.T, url *url.URL) {
	t.Helper()
	ctx := context.Background()

	c, err := govmomi.NewClient(ctx, url, true)
	require.NoError(t, err)

	finder := find.NewFinder(c.Client)

	dc, err := finder.Datacenter(ctx, datacenter)
	require.NoError(t, err)
	finder.SetDatacenter(dc)

	f, err := finder.Folder(ctx, vmFolder)
	require.NoError(t, err)

	_, err = f.CreateFolder(ctx, destFolderName)
	require.NoError(t, err)

	rp, err := finder.ResourcePool(ctx, pool)
	require.NoError(t, err)

	hs, err := finder.HostSystem(ctx, host)
	require.NoError(t, err)

	spec := types.VirtualMachineConfigSpec{
		Name: linkedCloneVMName,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: vmPathName,
		},
	}

	task, err := f.CreateVM(ctx, spec, rp, hs)
	require.NoError(t, err)

	info, err := task.WaitForResult(ctx)
	require.NoError(t, err)

	vm := object.NewVirtualMachine(c.Client, info.Result.(types.ManagedObjectReference))

	// Create first snapshot
	snapTask1, err := vm.CreateSnapshot(ctx, "Old_Snapshot", "first snapshot", false, false)
	require.NoError(t, err)
	err = snapTask1.Wait(ctx)
	require.NoError(t, err)

	// Create second snapshot — this becomes the current snapshot
	snapTask2, err := vm.CreateSnapshot(ctx, "Current_Snapshot", "second snapshot", false, false)
	require.NoError(t, err)
	err = snapTask2.Wait(ctx)
	require.NoError(t, err)
}

func TestProvisioning_LinkedClone_NestedSnapshot(t *testing.T) {
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

	// Create a VM with a nested snapshot tree: Root → Child → Target
	setupLinkedCloneEnvWithNestedSnapshots(t, s.URL)

	integration.TestProvisioning(t,
		integration.BuildPluginBinary(t, "cmd/fleeting-plugin-vsphere", "fleeting-plugin-vsphere"),
		integration.Config{
			PluginConfig: InstanceGroup{
				VsphereUrl:         s.URL.String(),
				Template:           linkedCloneVMName,
				Folder:             vmFolder,
				Datacenter:         datacenter,
				Host:               host,
				ResourcePool:       pool,
				Datastore:          datastore,
				InsecureConnection: true,
				LinkedClone:        true,
				Snapshot:           "Nested_Target",
				Name:               "fleeting-linked-nested-test",
			},
			ConnectorConfig: provider.ConnectorConfig{
				Timeout: 10 * time.Minute,
			},
			MaxInstances:    3,
			UseExternalAddr: false,
		})
}

func setupLinkedCloneEnvWithNestedSnapshots(t *testing.T, url *url.URL) {
	t.Helper()
	ctx := context.Background()

	c, err := govmomi.NewClient(ctx, url, true)
	require.NoError(t, err)

	finder := find.NewFinder(c.Client)

	dc, err := finder.Datacenter(ctx, datacenter)
	require.NoError(t, err)
	finder.SetDatacenter(dc)

	f, err := finder.Folder(ctx, vmFolder)
	require.NoError(t, err)

	_, err = f.CreateFolder(ctx, destFolderName)
	require.NoError(t, err)

	rp, err := finder.ResourcePool(ctx, pool)
	require.NoError(t, err)

	hs, err := finder.HostSystem(ctx, host)
	require.NoError(t, err)

	spec := types.VirtualMachineConfigSpec{
		Name: linkedCloneVMName,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: vmPathName,
		},
	}

	task, err := f.CreateVM(ctx, spec, rp, hs)
	require.NoError(t, err)

	info, err := task.WaitForResult(ctx)
	require.NoError(t, err)

	vm := object.NewVirtualMachine(c.Client, info.Result.(types.ManagedObjectReference))

	// Create root snapshot
	snapTask1, err := vm.CreateSnapshot(ctx, "Root_Snapshot", "root", false, false)
	require.NoError(t, err)
	err = snapTask1.Wait(ctx)
	require.NoError(t, err)

	// Create child snapshot (child of Root_Snapshot since it's the current snapshot)
	snapTask2, err := vm.CreateSnapshot(ctx, "Child_Snapshot", "child", false, false)
	require.NoError(t, err)
	err = snapTask2.Wait(ctx)
	require.NoError(t, err)

	// Create grandchild snapshot (child of Child_Snapshot)
	snapTask3, err := vm.CreateSnapshot(ctx, "Nested_Target", "grandchild target", false, false)
	require.NoError(t, err)
	err = snapTask3.Wait(ctx)
	require.NoError(t, err)
}

func TestProvisioning_MissingName(t *testing.T) {
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

	err = setupTestEnv(t, s.URL)
	if err != nil {
		t.Fatalf("setting up test environment: %s", err)
	}

	ig := InstanceGroup{
		VsphereUrl:         s.URL.String(),
		Template:           templateName,
		Folder:             vmFolder,
		Datacenter:         datacenter,
		Host:               host,
		ResourcePool:       pool,
		Datastore:          datastore,
		InsecureConnection: true,
		Name:               "", // Intentionally empty
	}

	settings := provider.Settings{
		ConnectorConfig: provider.ConnectorConfig{
			Timeout: 10 * time.Minute,
		},
	}

	_, err = ig.Init(context.Background(), nil, settings)
	require.Error(t, err, "expected error when InstanceGroup name is empty, got nil")
}

const resourceSizingTemplateName = "resource-sizing-template"

// TestProvisioning_ResourceSizing verifies that DiskSizeGB, when set on the
// InstanceGroup, actually grows the cloned VM's disk instead of being
// silently ignored.
//
// NumCPUs/MemoryMB are intentionally NOT asserted here: govmomi's vcsim
// simulator has a bug in CloneVMTask (simulator/virtual_machine.go) where it
// computes the merged NumCPUs/MemoryMB into a local copy ("dst") of the
// config spec but then creates the clone from the original, unmodified
// config — so vcsim silently drops these two fields on clone, unlike real
// vCenter. The construction of the clone's Config spec (the part this
// plugin actually controls) is covered instead by the pure unit test
// TestHardwareConfigSpec in internal/vsphere-client.
func TestProvisioning_ResourceSizing(t *testing.T) {
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

	setupResourceSizingTemplate(t, s.URL, 10 /* GB */)

	ig := InstanceGroup{
		VsphereUrl:         s.URL.String(),
		Template:           resourceSizingTemplateName,
		Folder:             vmFolder,
		Datacenter:         datacenter,
		Host:               host,
		ResourcePool:       pool,
		Datastore:          datastore,
		InsecureConnection: true,
		Name:               "fleeting-sizing-test",
		NumCPUs:            4,
		MemoryMB:           8192,
		DiskSizeGB:         20, // grows the template's 10GB disk
	}

	settings := provider.Settings{
		ConnectorConfig: provider.ConnectorConfig{
			Timeout:              10 * time.Minute,
			UseStaticCredentials: true,
		},
	}

	ctx := context.Background()

	_, err = ig.Init(ctx, hclog.NewNullLogger(), settings)
	require.NoError(t, err)

	created, err := ig.Increase(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, created)

	vmMo := findVMByPrefix(t, s.URL, vmFolder, "fleeting-sizing-test-")
	require.NotNil(t, vmMo.Config)

	var diskCapacityKB int64
	for _, dev := range vmMo.Config.Hardware.Device {
		if disk, ok := dev.(*types.VirtualDisk); ok {
			diskCapacityKB = disk.CapacityInKB
			break
		}
	}
	require.EqualValues(t, 20*1024*1024, diskCapacityKB, "disk size was not grown")
}

// TestResourceSizing_DiskShrinkRejected verifies that requesting a
// DiskSizeGB smaller than the template's current disk fails fast at Init,
// instead of silently truncating data or being ignored.
func TestResourceSizing_DiskShrinkRejected(t *testing.T) {
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

	setupResourceSizingTemplate(t, s.URL, 20 /* GB */)

	ig := InstanceGroup{
		VsphereUrl:         s.URL.String(),
		Template:           resourceSizingTemplateName,
		Folder:             vmFolder,
		Datacenter:         datacenter,
		Host:               host,
		ResourcePool:       pool,
		Datastore:          datastore,
		InsecureConnection: true,
		Name:               "fleeting-shrink-test",
		DiskSizeGB:         10, // smaller than the template's 20GB disk
	}

	settings := provider.Settings{
		ConnectorConfig: provider.ConnectorConfig{
			Timeout:              10 * time.Minute,
			UseStaticCredentials: true,
		},
	}

	_, err = ig.Init(context.Background(), hclog.NewNullLogger(), settings)
	require.Error(t, err, "expected error when DiskSizeGB is smaller than the template's disk")
}

// setupResourceSizingTemplate creates a template VM with a single SCSI disk
// of the given size, used to exercise CPU/memory/disk overrides on clone.
func setupResourceSizingTemplate(t *testing.T, url *url.URL, diskGB int64) {
	t.Helper()
	ctx := context.Background()

	c, err := govmomi.NewClient(ctx, url, true)
	require.NoError(t, err)

	finder := find.NewFinder(c.Client)

	dc, err := finder.Datacenter(ctx, datacenter)
	require.NoError(t, err)
	finder.SetDatacenter(dc)

	f, err := finder.Folder(ctx, vmFolder)
	require.NoError(t, err)

	vf, err := f.CreateFolder(ctx, "resource-sizing-source")
	require.NoError(t, err)

	rp, err := finder.ResourcePool(ctx, pool)
	require.NoError(t, err)

	hs, err := finder.HostSystem(ctx, host)
	require.NoError(t, err)

	ds, err := finder.Datastore(ctx, datastore)
	require.NoError(t, err)

	spec := types.VirtualMachineConfigSpec{
		Name: resourceSizingTemplateName,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: vmPathName,
		},
	}

	task, err := vf.CreateVM(ctx, spec, rp, hs)
	require.NoError(t, err)

	info, err := task.WaitForResult(ctx)
	require.NoError(t, err)

	vm := object.NewVirtualMachine(c.Client, info.Result.(types.ManagedObjectReference))

	var devices object.VirtualDeviceList

	controller, err := devices.CreateSCSIController("pvscsi")
	require.NoError(t, err)
	devices = append(devices, controller)

	disk := devices.CreateDisk(controller.(types.BaseVirtualController), ds.Reference(), "")
	disk.CapacityInKB = diskGB * 1024 * 1024
	devices = append(devices, disk)

	changes, err := devices.ConfigSpec(types.VirtualDeviceConfigSpecOperationAdd)
	require.NoError(t, err)

	reconfigTask, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{DeviceChange: changes})
	require.NoError(t, err)
	require.NoError(t, reconfigTask.Wait(ctx))

	require.NoError(t, vm.MarkAsTemplate(ctx))
}

// findVMByPrefix looks up the first VM whose name has the given prefix in
// folderPath, and returns its full "config" property tree.
func findVMByPrefix(t *testing.T, url *url.URL, folderPath, prefix string) mo.VirtualMachine {
	t.Helper()
	ctx := context.Background()

	c, err := govmomi.NewClient(ctx, url, true)
	require.NoError(t, err)

	finder := find.NewFinder(c.Client)

	dc, err := finder.Datacenter(ctx, datacenter)
	require.NoError(t, err)
	finder.SetDatacenter(dc)

	f, err := finder.Folder(ctx, folderPath)
	require.NoError(t, err)

	var folderProps mo.Folder
	require.NoError(t, f.Properties(ctx, f.Reference(), []string{"childEntity"}, &folderProps))

	for _, ref := range folderProps.ChildEntity {
		if ref.Type != "VirtualMachine" {
			continue
		}

		vm := object.NewVirtualMachine(c.Client, ref)
		name, err := vm.ObjectName(ctx)
		require.NoError(t, err)

		if !strings.HasPrefix(name, prefix) {
			continue
		}

		var vmMo mo.VirtualMachine
		require.NoError(t, vm.Properties(ctx, vm.Reference(), []string{"config"}, &vmMo))
		return vmMo
	}

	t.Fatalf("no VM found with prefix %q in folder %q", prefix, folderPath)
	return mo.VirtualMachine{}
}
