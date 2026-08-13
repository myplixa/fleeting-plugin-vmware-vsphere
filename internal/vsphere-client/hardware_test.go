package vsphereclient

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
)

// TestHardwareConfigSpec covers the merge logic that turns numCPUs/memoryMB/
// diskChange overrides into the VirtualMachineConfigSpec fragment sent as
// part of the clone request. This is asserted at the unit level (rather than
// against vcsim) because vcsim's CloneVMTask does not apply NumCPUs/MemoryMB
// overrides — see the comment on TestProvisioning_ResourceSizing in
// provider_integration_test.go for details.
func TestHardwareConfigSpec(t *testing.T) {
	t.Run("no overrides configured returns nil", func(t *testing.T) {
		c := &client{}
		require.Nil(t, c.hardwareConfigSpec())
	})

	t.Run("num cpus only", func(t *testing.T) {
		c := &client{numCPUs: 4}
		spec := c.hardwareConfigSpec()
		require.NotNil(t, spec)
		require.EqualValues(t, 4, spec.NumCPUs)
		require.Zero(t, spec.MemoryMB)
		require.Nil(t, spec.DeviceChange)
	})

	t.Run("memory only", func(t *testing.T) {
		c := &client{memoryMB: 8192}
		spec := c.hardwareConfigSpec()
		require.NotNil(t, spec)
		require.EqualValues(t, 8192, spec.MemoryMB)
		require.Zero(t, spec.NumCPUs)
	})

	t.Run("disk change only", func(t *testing.T) {
		change := &types.VirtualDeviceConfigSpec{Operation: types.VirtualDeviceConfigSpecOperationEdit}
		c := &client{diskChange: change}
		spec := c.hardwareConfigSpec()
		require.NotNil(t, spec)
		require.Len(t, spec.DeviceChange, 1)
		require.Same(t, change, spec.DeviceChange[0])
	})

	t.Run("all overrides combined", func(t *testing.T) {
		change := &types.VirtualDeviceConfigSpec{Operation: types.VirtualDeviceConfigSpecOperationEdit}
		c := &client{numCPUs: 2, memoryMB: 2048, diskChange: change}
		spec := c.hardwareConfigSpec()
		require.NotNil(t, spec)
		require.EqualValues(t, 2, spec.NumCPUs)
		require.EqualValues(t, 2048, spec.MemoryMB)
		require.Len(t, spec.DeviceChange, 1)
	})
}

// TestResolveDiskResize exercises the grow / no-op / shrink-rejected paths
// against a real (simulated) VM with an actual disk device, since the
// capacity comparison depends on a live property fetch of the VM's devices.
func TestResolveDiskResize(t *testing.T) {
	model := simulator.VPX()
	defer model.Remove()

	model.Datacenter = 1
	model.Host = 1
	model.Datastore = 1
	model.Cluster = 1
	model.Pool = 1
	model.Folder = 0

	require.NoError(t, model.Create())

	s := model.Service.NewServer()
	defer s.Close()

	ctx := context.Background()

	gc, err := govmomi.NewClient(ctx, s.URL, true)
	require.NoError(t, err)

	finder := find.NewFinder(gc.Client)

	dc, err := finder.Datacenter(ctx, "/DC0")
	require.NoError(t, err)
	finder.SetDatacenter(dc)

	folder, err := finder.Folder(ctx, "/DC0/vm")
	require.NoError(t, err)

	pool, err := finder.ResourcePool(ctx, "/DC0/host/DC0_H0/Resources")
	require.NoError(t, err)

	host, err := finder.HostSystem(ctx, "/DC0/host/DC0_H0/DC0_H0")
	require.NoError(t, err)

	ds, err := finder.Datastore(ctx, "/DC0/datastore/LocalDS_0")
	require.NoError(t, err)

	spec := types.VirtualMachineConfigSpec{
		Name: "disk-resize-source",
		Files: &types.VirtualMachineFileInfo{
			VmPathName: "[" + ds.Name() + "]",
		},
	}

	task, err := folder.CreateVM(ctx, spec, pool, host)
	require.NoError(t, err)

	info, err := task.WaitForResult(ctx)
	require.NoError(t, err)

	vm := object.NewVirtualMachine(gc.Client, info.Result.(types.ManagedObjectReference))

	var devices object.VirtualDeviceList

	controller, err := devices.CreateSCSIController("pvscsi")
	require.NoError(t, err)
	devices = append(devices, controller)

	disk := devices.CreateDisk(controller.(types.BaseVirtualController), ds.Reference(), "")
	disk.CapacityInKB = 10 * 1024 * 1024 // 10GB
	devices = append(devices, disk)

	changes, err := devices.ConfigSpec(types.VirtualDeviceConfigSpecOperationAdd)
	require.NoError(t, err)

	reconfigTask, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{DeviceChange: changes})
	require.NoError(t, err)
	require.NoError(t, reconfigTask.Wait(ctx))

	c := &client{client: gc}

	t.Run("growing returns an edit device change", func(t *testing.T) {
		change, err := c.resolveDiskResize(ctx, vm.Reference(), 20)
		require.NoError(t, err)
		require.NotNil(t, change)
		require.Equal(t, types.VirtualDeviceConfigSpecOperationEdit, change.Operation)

		resized, ok := change.Device.(*types.VirtualDisk)
		require.True(t, ok)
		require.EqualValues(t, 20*1024*1024, resized.CapacityInKB)
	})

	t.Run("same size is a no-op", func(t *testing.T) {
		change, err := c.resolveDiskResize(ctx, vm.Reference(), 10)
		require.NoError(t, err)
		require.Nil(t, change)
	})

	t.Run("shrinking is rejected", func(t *testing.T) {
		_, err := c.resolveDiskResize(ctx, vm.Reference(), 5)
		require.Error(t, err)
	})
}
