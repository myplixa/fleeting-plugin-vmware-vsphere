package vsphere

import (
	"context"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"github.com/vmware/govmomi/simulator"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
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

func TestInit_WithDefaultValues(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(model *simulator.Model)
		assert func(t *testing.T, p provider.ProviderInfo, err error)
	}{
		{
			name: "init ok only root resource pool, datastore, in a datacenter cluster",
			setup: func(model *simulator.Model) {
				model.Datacenter = 1
				model.Cluster = 0
				model.Pool = 0
				model.Host = 1
				model.Datastore = 1
				model.Folder = 0
			},
			assert: func(t *testing.T, p provider.ProviderInfo, err error) {
				require.NoError(t, err)
				require.NotNil(t, p)
			},
		},
		{
			name: "init fails when multiple pools in a datacenter cluster",
			setup: func(model *simulator.Model) {
				model.Datacenter = 1
				model.Cluster = 1
				model.Pool = 2
				model.Host = 1
				model.Datastore = 1
				model.Folder = 0
			},
			assert: func(t *testing.T, p provider.ProviderInfo, err error) {
				require.NotNil(t, err)
			},
		},
		{
			name: "init fails when multiple datacenters in env",
			setup: func(model *simulator.Model) {
				model.Datacenter = 2
				model.Cluster = 0
				model.Pool = 0
				model.Host = 1
				model.Datastore = 1
				model.Folder = 0
			},
			assert: func(t *testing.T, p provider.ProviderInfo, err error) {
				require.NotNil(t, err)
			},
		},
		{
			name: "init fails when multiple datastores in a datacenter cluster",
			setup: func(model *simulator.Model) {
				model.Datacenter = 1
				model.Cluster = 0
				model.Pool = 0
				model.Host = 1
				model.Datastore = 2
				model.Folder = 0
			},
			assert: func(t *testing.T, p provider.ProviderInfo, err error) {
				require.NotNil(t, err)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := simulator.VPX()
			defer model.Remove()

			tc.setup(model)

			if err := model.Create(); err != nil {
				t.Fatalf("failed to create simulator: %v", err)
			}

			s := model.Service.NewServer()
			defer s.Close()

			g := InstanceGroup{
				VsphereUrl:         s.URL.String(),
				InsecureConnection: true,
				Folder:             "DC0/vm/",
				Name:               "Test-Vsphere",
			}

			p, err := g.Init(context.Background(), hclog.Default(), provider.Settings{})

			tc.assert(t, p, err)
		})
	}
}

func TestInit_FolderNot_Found(t *testing.T) {
	t.Run("init fails no folder found", func(t *testing.T) {
		s, cleanup := setupSim(t, 1, 0, 0, 1, 1, 0)
		defer cleanup()

		g := InstanceGroup{
			VsphereUrl:         s.URL.String(),
			InsecureConnection: true,
			Folder:             "DC0/vm/FOLDER_NOT_HERE",
			Name:               "Test-Vsphere",
		}

		_, err := g.Init(context.Background(), hclog.Default(), provider.Settings{})

		require.NotNil(t, err)
	})
}

func TestInit_Context(t *testing.T) {
	t.Run("context cancels init", func(t *testing.T) {
		s, cleanup := setupSim(t, 1, 0, 0, 1, 1, 0)
		defer cleanup()

		g := InstanceGroup{
			VsphereUrl:         s.URL.String(),
			InsecureConnection: true,
			Folder:             "DC0/vm",
			Name:               "Test-Vsphere",
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := g.Init(ctx, hclog.Default(), provider.Settings{})

		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("context cancels after init still has working client", func(t *testing.T) {
		s, cleanup := setupSim(t, 1, 0, 0, 1, 1, 0)
		defer cleanup()

		g := InstanceGroup{
			VsphereUrl:         s.URL.String(),
			InsecureConnection: true,
			Folder:             "DC0/vm",
			Name:               "Test-Vsphere",
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
