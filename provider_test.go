package vsphere

import (
	"context"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
	vsphereclient "gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere/internal/vsphere-client"
	"gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere/internal/vsphere-client/fake"
)

func setupFakeClient(t *testing.T, setup func(client *fake.Client)) *InstanceGroup {
	t.Helper()

	oldClient := newClient
	t.Cleanup(func() {
		newClient = oldClient
	})

	newClient = func(ctx context.Context, vsphereUrl string, insecure bool, template string, options ...vsphereclient.ClientOption) (vsphereclient.Client, error) {
		client := fake.New()

		if setup != nil {
			setup(client)
		}

		return client, nil
	}

	return &InstanceGroup{
		Name:       "test-vsphere",
		Datacenter: "test-datacenter",
		Folder:     "test-folder",
		Template:   "test-template",
	}
}

func TestInit(t *testing.T) {
	t.Run("context cancels init", func(t *testing.T) {
		group := setupFakeClient(t, nil)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		settings := provider.Settings{
			ConnectorConfig: provider.ConnectorConfig{
				UseStaticCredentials: true,
			},
		}

		_, err := group.Init(ctx, hclog.Default(), settings)

		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("context cancels after init still has working client", func(t *testing.T) {
		group := setupFakeClient(t, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		settings := provider.Settings{
			ConnectorConfig: provider.ConnectorConfig{
				UseStaticCredentials: true,
			},
		}

		_, err := group.Init(ctx, hclog.Default(), settings)
		require.NoError(t, err)

		cancel()

		err = group.Update(ctx, func(instance string, state provider.State) {})
		require.NoError(t, err)
	})
}

func TestIncrease(t *testing.T) {
	group := setupFakeClient(t, nil)

	ctx := context.Background()

	settings := provider.Settings{
		ConnectorConfig: provider.ConnectorConfig{
			UseStaticCredentials: true,
		},
	}

	var count int
	_, err := group.Init(ctx, hclog.Default(), settings)
	require.NoError(t, err)

	require.NoError(t, group.Update(ctx, func(instance string, state provider.State) {
		count++
	}))
	require.Equal(t, 0, count)

	num, err := group.Increase(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, 2, num)

	count = 0
	require.NoError(t, group.Update(ctx, func(instance string, state provider.State) {
		require.Equal(t, provider.StateRunning, state)
		count++
	}))
	require.Equal(t, 2, count)
	require.Equal(t, 2, int(group.size))
}
