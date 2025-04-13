package fake

import (
	"context"

	"github.com/google/uuid"
	"github.com/hashicorp/go-hclog"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
	vsphereclient "gitlab.com/santhanuv/fleeting-plugin-vmware-vsphere/internal/vsphere-client"
)

type Client struct {
	Instances map[string]provider.State
}

func New() *Client {
	return &Client{
		Instances: map[string]provider.State{},
	}
}

func (c *Client) TemplateClone(ctx context.Context, count uint, log hclog.Logger, guestopts *vsphereclient.GuestOsOpts) (uint, error) {
	for range count {
		name := uuid.NewString()
		c.Instances[name] = provider.StateRunning
	}

	return uint(len(c.Instances)), nil
}

func (c *Client) GetVMs(ctx context.Context, logger hclog.Logger) (map[string]provider.State, error) {
	return c.Instances, nil
}

func (c *Client) NetInfo(ctx context.Context, vmName string) (string, error) {
	return "10.0.0.1", nil
}

func (c *Client) DeleteVMs(ctx context.Context, vmNames []string, log hclog.Logger) ([]string, error) {
	var deleted []string

	for _, name := range vmNames {
		if _, ok := c.Instances[name]; ok {
			delete(c.Instances, name)
			deleted = append(deleted, name)
		}
	}

	return deleted, nil
}
