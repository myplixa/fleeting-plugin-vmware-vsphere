package vsphere

import (
	"context"
	"net/url"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/simulator"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

const (
	TestTemplateVM = "/DC0/vm/DC0_H0_VM0"
	TestVMFolder   = "/DC0/vm"
)

func setupSim(t *testing.T, dc, cluster, pool, ds, host, folder int) (*simulator.Server, func()) {
	model := simulator.VPX()

	model.Datacenter = dc
	model.Cluster = cluster
	model.Pool = pool
	model.Datastore = ds
	model.Host = host
	model.Folder = folder

	if err := model.Create(); err != nil {
		t.Fatalf("failed to create simulator: %v", err)
	}

	s := model.Service.NewServer()

	return s, func() {
		defer model.Remove()
		defer s.Close()
	}
}

func markAsTemplate(t *testing.T, url *url.URL, template string) {
	ctx := context.Background()

	gc, err := govmomi.NewClient(ctx, url, true)
	if err != nil {
		t.Fatalf("%v", err)
	}

	finder := find.NewFinder(gc.Client)

	vm, err := finder.VirtualMachine(ctx, template)
	if err != nil {
		t.Fatalf("%v", err)
	}

	task, err := vm.PowerOff(ctx)
	if err != nil {
		t.Fatalf("%v", err)
	}
	task.Wait(ctx)

	err = vm.MarkAsTemplate(ctx)
	if err != nil {
		t.Fatalf("%v", err)
	}
}

func TestInit(t *testing.T) {
	t.Run("init fails provided vm not found", func(t *testing.T) {
		s, cleanup := setupSim(t, 1, 0, 0, 1, 1, 0)
		defer cleanup()

		g := InstanceGroup{
			VsphereUrl:         s.URL.String(),
			InsecureConnection: true,
			Folder:             TestVMFolder,
			Name:               "Test-Vsphere",
			Template:           "/DC0/vm/VM_NOT_FOUND",
		}

		ctx := context.Background()
		_, err := g.Init(ctx, hclog.Default(), provider.Settings{})

		require.Error(t, err)
	})

	t.Run("init fails provide vm is not a template", func(t *testing.T) {
		s, cleanup := setupSim(t, 1, 0, 0, 1, 1, 0)
		defer cleanup()

		g := InstanceGroup{
			VsphereUrl:         s.URL.String(),
			InsecureConnection: true,
			Folder:             TestVMFolder,
			Name:               "Test-Vsphere",
			Template:           TestTemplateVM,
		}

		ctx := context.Background()
		_, err := g.Init(ctx, hclog.Default(), provider.Settings{})

		require.Error(t, err)
	})

	t.Run("context cancels init", func(t *testing.T) {
		s, cleanup := setupSim(t, 1, 0, 0, 1, 1, 0)
		defer cleanup()

		markAsTemplate(t, s.URL, TestTemplateVM)

		g := InstanceGroup{
			VsphereUrl:         s.URL.String(),
			InsecureConnection: true,
			Folder:             TestVMFolder,
			Name:               "Test-Vsphere",
			Template:           TestTemplateVM,
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := g.Init(ctx, hclog.Default(), provider.Settings{})

		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("context cancels after init still has working client", func(t *testing.T) {
		s, cleanup := setupSim(t, 1, 0, 0, 1, 1, 0)
		defer cleanup()

		markAsTemplate(t, s.URL, TestTemplateVM)

		g := InstanceGroup{
			VsphereUrl:         s.URL.String(),
			InsecureConnection: true,
			Folder:             TestVMFolder,
			Name:               "Test-Vsphere",
			Template:           TestTemplateVM,
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		_, err := g.Init(ctx, hclog.Default(), provider.Settings{})
		require.NoError(t, err)

		cancel()

		err = g.Update(ctx, func(instance string, state provider.State) {})
		require.NoError(t, err)
	})
}
