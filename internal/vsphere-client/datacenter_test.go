package vsphereclient

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
)

// newSimulator starts a vcsim instance and creates a template named
// "test-template" in the VM folder of DC0.
//
// Two datacenters are simulated on purpose. With a single one, govmomi's finder
// resolves relative paths even when no datacenter is set, because it falls back
// to the only one that exists; the ambiguity that makes the missing
// SetDatacenter observable only shows up from the second datacenter onwards.
func newSimulator(t *testing.T) *url.URL {
	t.Helper()

	model := simulator.VPX()
	model.Datacenter = 2
	model.Host = 1
	model.Datastore = 1
	model.Cluster = 1
	model.Pool = 1
	model.Folder = 0

	require.NoError(t, model.Create(), "creating the simulated vsphere")
	t.Cleanup(model.Remove)

	s := model.Service.NewServer()
	t.Cleanup(s.Close)

	createTemplate(t, s.URL)

	return s.URL
}

func createTemplate(t *testing.T, u *url.URL) {
	t.Helper()
	ctx := context.Background()

	c, err := govmomi.NewClient(ctx, u, true)
	require.NoError(t, err)

	finder := find.NewFinder(c.Client)

	dc, err := finder.Datacenter(ctx, "/DC0")
	require.NoError(t, err)
	finder.SetDatacenter(dc)

	folder, err := finder.Folder(ctx, "/DC0/vm")
	require.NoError(t, err)

	pool, err := finder.ResourcePool(ctx, "/DC0/host/DC0_H0/Resources")
	require.NoError(t, err)

	host, err := finder.HostSystem(ctx, "/DC0/host/DC0_H0/DC0_H0")
	require.NoError(t, err)

	task, err := folder.CreateVM(ctx, types.VirtualMachineConfigSpec{
		Name:  "test-template",
		Files: &types.VirtualMachineFileInfo{VmPathName: "[LocalDS_0] test-template"},
	}, pool, host)
	require.NoError(t, err)

	info, err := task.WaitForResult(ctx)
	require.NoError(t, err)

	vm := object.NewVirtualMachine(c.Client, info.Result.(types.ManagedObjectReference))
	require.NoError(t, vm.MarkAsTemplate(ctx))
}

// TestNewClient_relativeInventoryPaths covers the case where the inventory
// objects are named relative to the datacenter instead of by absolute path.
//
// WithDatacenter has to scope the finder, otherwise every option that runs after
// it resolves its path without a datacenter context and fails with
// "please specify a datacenter", forcing users to spell out absolute paths.
func TestNewClient_relativeInventoryPaths(t *testing.T) {
	u := newSimulator(t)

	tests := []struct {
		name      string
		folder    string
		host      string
		pool      string
		datastore string
	}{
		{
			name:      "absolute paths",
			folder:    "/DC0/vm",
			host:      "/DC0/host/DC0_H0/DC0_H0",
			pool:      "/DC0/host/DC0_H0/Resources",
			datastore: "/DC0/datastore/LocalDS_0",
		},
		{
			name:      "paths relative to the datacenter",
			folder:    "vm",
			host:      "DC0_H0/DC0_H0",
			pool:      "DC0_H0/Resources",
			datastore: "LocalDS_0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(context.Background(), u.String(), true, "test-template", "user", "pass",
				WithDatacenter("/DC0"),
				WithFolder(tt.folder),
				WithHost(tt.host),
				WithPool(tt.pool),
				WithDatastore(tt.datastore),
				WithVMNamePrefix("fleeting-test"),
			)
			require.NoError(t, err)
			require.NotNil(t, c)
		})
	}
}

// TestNewClient_datacenterByName checks that the datacenter itself can be given
// by name, not only by absolute path.
func TestNewClient_datacenterByName(t *testing.T) {
	u := newSimulator(t)

	c, err := NewClient(context.Background(), u.String(), true, "test-template", "user", "pass",
		WithDatacenter("DC0"),
		WithPool("DC0_H0/Resources"),
		WithVMNamePrefix("fleeting-test"),
	)
	require.NoError(t, err)
	require.NotNil(t, c)
}
