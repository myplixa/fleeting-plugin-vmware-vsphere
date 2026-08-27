package vsphere

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/hashicorp/go-hclog"
	vsphereclient "github.com/myplixa/vmware-fleeting-plugin/internal/vsphere-client"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

const MaxInstances = 50

var _ provider.InstanceGroup = (*InstanceGroup)(nil)

var newClient = vsphereclient.NewClient

type InstanceGroup struct {
	VsphereUrl         string `json:"vsphere_url"`
	Username           string `json:"vsphere_username"`
	Password           string `json:"vsphere_password"`
	CredentialsFile    string `json:"vsphere_credentials_file,omitempty"`
	Template           string `json:"vsphere_template"`
	Folder             string `json:"vsphere_folder"`
	Datacenter         string `json:"vsphere_datacenter"`
	Host               string `json:"vsphere_host"`
	Datastore          string `json:"vsphere_datastore"`
	ResourcePool       string `json:"vsphere_resource_pool"`
	InsecureConnection bool   `json:"vsphere_allow_insecure_connection"`
	LinkedClone        bool   `json:"vsphere_linked_clone"`
	Snapshot           string `json:"vsphere_snapshot"`
	Name               string `json:"name"`

	NumCPUs    int32 `json:"num_cpus"`
	MemoryMB   int64 `json:"memory_mb"`
	DiskSizeGB int64 `json:"disk_size_gb"`

	CloudInitExtraFile string            `json:"cloud_init_extra_file,omitempty"`
	CloudInitVars      map[string]string `json:"cloud_init_vars,omitempty"`

	size     uint
	client   vsphereclient.Client
	settings provider.Settings
	log      hclog.Logger

	sshPubKey []byte
}

func (g *InstanceGroup) Init(ctx context.Context, logger hclog.Logger, settings provider.Settings) (provider.ProviderInfo, error) {
	if g.CredentialsFile != "" {
		creds, err := loadCredentialsFile(g.CredentialsFile)
		if err != nil {
			return provider.ProviderInfo{}, err
		}

		if g.VsphereUrl == "" {
			g.VsphereUrl = creds.Url
		}
		if g.Username == "" {
			g.Username = creds.Username
		}
		if g.Password == "" {
			g.Password = creds.Password
		}
		if !g.InsecureConnection {
			g.InsecureConnection = creds.InsecureConnection
		}
	}

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

	// Name is required
	options = append(options, vsphereclient.WithVMNamePrefix(g.Name))

	if g.LinkedClone {
		options = append(options, vsphereclient.WithLinkedClone(g.Snapshot))
	}

	if g.NumCPUs > 0 {
		options = append(options, vsphereclient.WithNumCPUs(g.NumCPUs))
	}

	if g.MemoryMB > 0 {
		options = append(options, vsphereclient.WithMemoryMB(g.MemoryMB))
	}

	if g.DiskSizeGB > 0 {
		options = append(options, vsphereclient.WithDiskSizeGB(g.DiskSizeGB))
	}

	if g.CloudInitExtraFile != "" {
		options = append(options, vsphereclient.WithCloudInitExtraFile(g.CloudInitExtraFile))
	}

	if len(g.CloudInitVars) > 0 {
		options = append(options, vsphereclient.WithCloudInitVars(g.CloudInitVars))
	}

	client, err := newClient(ctx, g.VsphereUrl, g.InsecureConnection, g.Template, g.Username, g.Password, options...)
	if err != nil {
		return provider.ProviderInfo{}, err
	}

	g.client = client

	g.log = logger.With("data Center", g.Datacenter, "folder", g.Folder, "template", g.Template)
	g.settings = settings

	providerInfo := provider.ProviderInfo{
		ID:        path.Join("vsphere", g.Name, g.Datacenter),
		MaxSize:   MaxInstances,
		Version:   Version.String(),
		BuildInfo: Version.BuildInfo(),
	}

	if g.settings.UseStaticCredentials {
		return providerInfo, ctx.Err()
	}

	if g.settings.OS == "" {
		g.settings.OS = "linux"

		guestOsId, err := g.client.GuestOs(ctx)
		if err != nil {
			return providerInfo, nil
		}

		if strings.Contains(guestOsId, "win") {
			g.settings.OS = "windows"
		} else if strings.Contains(guestOsId, "darwin") {
			g.settings.OS = "darwin"
		}
	}

	if g.settings.Protocol == "" && g.settings.OS != "windows" {
		g.settings.Protocol = provider.ProtocolSSH
	}

	if g.settings.Username == "" {
		g.settings.Username = "fleeting"
	}

	if g.settings.Key == nil {
		key, err := g.generateSshKey()
		if err != nil {
			return provider.ProviderInfo{}, nil
		}

		g.settings.Key = key
	}

	pubKey, err := g.getSshPubKey(g.settings.Key)
	if err != nil {
		return provider.ProviderInfo{}, err
	}

	g.sshPubKey = pubKey

	return providerInfo, ctx.Err()
}

func (g *InstanceGroup) ConnectInfo(ctx context.Context, id string) (provider.ConnectInfo, error) {
	info := provider.ConnectInfo{
		ID:              id,
		ConnectorConfig: g.settings.ConnectorConfig,
	}

	internalIP, err := g.client.NetInfo(ctx, id)
	if err != nil {
		return provider.ConnectInfo{}, fmt.Errorf("fetching ip address: %w", err)
	}
	info.InternalAddr = internalIP

	if info.UseStaticCredentials {
		return info, nil
	}

	if info.OS == "windows" {
		return provider.ConnectInfo{}, fmt.Errorf("provisioning credential for windows is not supported")
	}

	if info.Protocol == "" {
		info.Protocol = provider.ProtocolSSH
	}

	if info.Username == "" {
		info.Username = g.settings.Username
	}

	switch info.Protocol {
	case provider.ProtocolSSH:
		if info.Key == nil {
			info.Key = g.settings.Key
		}
	}

	return info, nil
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
	var guestopts *vsphereclient.GuestOsOpts
	if !g.settings.UseStaticCredentials {
		guestopts = &vsphereclient.GuestOsOpts{
			Username: g.settings.Username,
			PubKey:   g.sshPubKey,
		}
	}
	count, err := g.client.TemplateClone(ctx, uint(delta), g.log, guestopts)
	if err != nil {
		return 0, fmt.Errorf("cloning from template: %w", err)
	}

	g.size += count

	return int(count), nil
}

func (g *InstanceGroup) Decrease(ctx context.Context, instances []string) ([]string, error) {
	deleted, err := g.client.DeleteVMs(ctx, instances, g.log)
	if err != nil {
		return nil, err
	}

	count := int(g.size) - len(deleted)

	if count < 0 {
		// g.size is a purely internal bookkeeping counter (not read by
		// anything outside Increase/Decrease); it goes negative when
		// Decrease is asked to delete VMs this process didn't itself
		// create via Increase - e.g. VMs orphaned by a previous process
		// that Update()/GetVMs picked back up after a restart. Clamp to
		// zero instead of wrapping to a huge value via uint(count), which
		// would otherwise keep this warning firing on every subsequent
		// Decrease call until the process restarts.
		g.log.Warn("out-of-sync size", "count", count, "size", g.size, "deleted", len(deleted))
		count = 0
	}

	g.size = uint(count)

	return deleted, nil
}

// Heartbeat is typically called by the taskscaler before connecting to the instance.
// TODO: Implement check related to VM health state (e.g., power state, guest heartbeat).
//
// HINT: Too many API calls should be avoided, as ConnectInfo is called subsequently.
func (g *InstanceGroup) Heartbeat(ctx context.Context, id string) error {
	return nil
}

func (g *InstanceGroup) Shutdown(ctx context.Context) error {
	remaining, err := g.client.GetVMs(ctx, g.log)
	if err != nil {
		return err
	}

	instances := make([]string, 0, len(remaining))
	for name := range remaining {
		instances = append(instances, name)
	}

	deleted, err := g.client.DeleteVMs(ctx, instances, g.log)
	if err != nil {
		return err
	}

	if len(deleted) == len(instances) {
		return nil
	}

	var notDeleted []string
	for _, name := range instances {
		if !slices.Contains(deleted, name) {
			notDeleted = append(notDeleted, name)
		}
	}

	return fmt.Errorf("failed to delete vm instances: %s", strings.Join(notDeleted, ","))
}
