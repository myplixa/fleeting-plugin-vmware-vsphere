package vsphere

import (
	"context"
	"fmt"
	"path"

	"github.com/hashicorp/go-hclog"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
	vsphereclient "gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere/internal/vsphere-client"
)

type InstanceGroup struct {
	VsphereUrl         string `json:"vsphere_url"`
	Template           string `json:"template"`
	Folder             string `json:"folder"`
	Datacenter         string `json:"datacenter"`
	Host               string `json:"host"`
	Datastore          string `json:"datastore"`
	ResourcePool       string `json:"resource_pool"`
	InsecureConnection bool   `json:"allow_insecure_connection"`
	Name               string `json:"name"`

	size     uint
	client   vsphereclient.Client
	settings provider.Settings
	log      hclog.Logger
}

func (g *InstanceGroup) Init(ctx context.Context, logger hclog.Logger, settings provider.Settings) (provider.ProviderInfo, error) {
	var options []vsphereclient.ClientOption

	if g.Datacenter != "" {
		options = append(options, vsphereclient.WithDatacenter(g.Datacenter))
	}

	if g.Folder != "" {
		options = append(options, vsphereclient.WithFolder(g.Folder))
	}

	if g.Host != "" {
		options = append(options, vsphereclient.WithHost(g.Host))
	}

	if g.ResourcePool != "" {
		options = append(options, vsphereclient.WithPool(g.ResourcePool))
	}

	if g.Datastore != "" {
		options = append(options, vsphereclient.WithDatastore(g.Datastore))
	}

	if g.Name != "" {
		options = append(options, vsphereclient.WithVMNamePrefix(g.Name))
	}

	client, err := vsphereclient.NewClient(ctx, g.VsphereUrl, g.InsecureConnection, g.Template, options...)
	if err != nil {
		return provider.ProviderInfo{}, err
	}

	g.client = client
	g.settings = settings
	g.log = logger.With("data Center", g.Datacenter, "folder", g.Folder, "template", g.Template)

	return provider.ProviderInfo{
		ID:        path.Join("vsphere", g.Name, g.Datacenter),
		MaxSize:   50,
		Version:   Version.String(),
		BuildInfo: Version.BuildInfo(),
	}, nil
}

func (g *InstanceGroup) ConnectInfo(ctx context.Context, id string) (provider.ConnectInfo, error) {
	return provider.ConnectInfo{}, fmt.Errorf("Not implemented")
}

func (g *InstanceGroup) Update(ctx context.Context, update func(instance string, state provider.State)) error {
	vms, err := g.client.GetVMs(ctx, g.log)
	if err != nil {
		return err
	}

	for name, state := range vms {
		update(name, state)
	}

	return nil
}

func (g *InstanceGroup) Increase(ctx context.Context, delta int) (int, error) {
	return 0, fmt.Errorf("Not implemented")
}

func (g *InstanceGroup) Decrease(ctx context.Context, instances []string) ([]string, error) {
	return nil, fmt.Errorf("Not implemented")
}

func (g *InstanceGroup) Shutdown(ctx context.Context) error {
	return fmt.Errorf("Not implemented")
}
