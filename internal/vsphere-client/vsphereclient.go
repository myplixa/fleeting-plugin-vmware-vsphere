package vsphereclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/hashicorp/go-hclog"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

type Client interface {
	DeleteVMs(ctx context.Context, vmNames []string, log hclog.Logger) ([]string, error)
	TemplateClone(ctx context.Context, count uint, log hclog.Logger) (uint, error)
	GetVMs(ctx context.Context, logger hclog.Logger) (map[string]provider.State, error)
	NetInfo(ctx context.Context, vmName string) (string, error)
}

type ClientOption func(ctx context.Context, c *client, finder *find.Finder) error

type client struct {
	client     *govmomi.Client
	datacenter types.ManagedObjectReference
	pool       types.ManagedObjectReference
	host       types.ManagedObjectReference
	datastore  types.ManagedObjectReference
	folder     types.ManagedObjectReference
	template   types.ManagedObjectReference
	namePrefix string
}

func NewClient(ctx context.Context, vsphereUrl string, insecure bool, template string, options ...ClientOption) (*client, error) {
	url, err := url.Parse(vsphereUrl)
	if err != nil {
		return nil, err
	}

	gc, err := govmomi.NewClient(ctx, url, insecure)
	if err != nil {
		return nil, err
	}

	finder := find.NewFinder(gc.Client)

	c := client{
		client: gc,
	}

	for _, option := range options {
		err := option(ctx, &c, finder)
		if err != nil {
			return nil, err
		}
	}

	if c.datacenter == (types.ManagedObjectReference{}) {
		dc, err := finder.DefaultDatacenter(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to setup default Datacenter: %w", err)
		}

		c.datacenter = dc.Reference()
	}

	finder.SetDatacenter(object.NewDatacenter(c.client.Client, c.datacenter))

	templateVM, err := finder.VirtualMachine(ctx, template)
	if err != nil {
		return nil, fmt.Errorf("failed to find source template %s: %w", template, err)
	}

	if isTemp, err := templateVM.IsTemplate(ctx); err != nil {
		return nil, fmt.Errorf("failed to confirm %s is a template: %w", template, err)
	} else if !isTemp {
		return nil, fmt.Errorf("%s should be a template", template)
	}
	c.template = templateVM.Reference()

	if c.folder == (types.ManagedObjectReference{}) {
		folder, err := finder.DefaultFolder(ctx)
		if err != nil {
			return nil, err
		}

		c.folder = folder.Reference()
	}

	if c.host == (types.ManagedObjectReference{}) {
		host, err := finder.DefaultHostSystem(ctx)
		if err != nil {
			return nil, err
		}

		c.host = host.Reference()
	}

	if c.pool == (types.ManagedObjectReference{}) {
		pool, err := finder.DefaultResourcePool(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to setup default Resource Pool: %w", err)
		}

		c.pool = pool.Reference()
	}

	if c.datastore == (types.ManagedObjectReference{}) {
		ds, err := finder.DefaultDatastore(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to setup default Datastore: %w", err)
		}

		c.datastore = ds.Reference()
	}

	if c.namePrefix == "" {
		c.namePrefix = uuid.NewString()
	}

	return &c, nil
}

func WithFolder(folder string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		folder, err := finder.Folder(ctx, folder)
		if err != nil {
			return fmt.Errorf("failed to find folder %s: %w", folder, err)
		}

		c.folder = folder.Reference()
		return nil
	}
}

func WithDatacenter(datacenter string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		ds, err := finder.Datacenter(ctx, datacenter)
		if err != nil {
			return fmt.Errorf("failed to find the datacenter '%s': %w", datacenter, err)
		}

		c.datacenter = ds.Reference()
		return nil
	}
}

func WithPool(pool string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		pool, err := finder.ResourcePool(ctx, pool)
		if err != nil {
			return fmt.Errorf("failed to find the resource pool %s: %w", pool, err)
		}

		c.pool = pool.Reference()
		return nil
	}
}

func WithHost(host string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		host, err := finder.HostSystem(ctx, host)
		if err != nil {
			return fmt.Errorf("failed to find the host %s: %w", host, err)
		}

		c.host = host.Reference()
		return nil
	}
}

func WithDatastore(datastore string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		ds, err := finder.Datastore(ctx, datastore)
		if err != nil {
			return fmt.Errorf("failed to find the datastore %s: %w", datastore, err)
		}

		c.datastore = ds.Reference()
		return nil
	}
}

func WithVMNamePrefix(prefix string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		c.namePrefix = prefix
		return nil
	}
}

type taskResult struct {
	name      string
	isSuccess bool
	err       error
}

func (c *client) TemplateClone(ctx context.Context, count uint, log hclog.Logger) (uint, error) {
	if c == nil {
		return 0, fmt.Errorf("client needs to be initialized before cloning")
	}

	var wg sync.WaitGroup
	resultChan := make(chan taskResult, count)

	for range count {
		wg.Add(1)

		go func() {
			defer wg.Done()

			name, err := c.templateClone(ctx, c.template)
			resultChan <- taskResult{
				name:      name,
				isSuccess: err == nil,
				err:       err,
			}
		}()
	}

	wg.Wait()
	close(resultChan)

	var newClones uint
	for result := range resultChan {
		if result.isSuccess {
			newClones++
			continue
		}

		log.Error("failure in vm clone", "error", result.err, "name", result.name)
	}

	return newClones, nil
}

func (c *client) GetVMs(ctx context.Context, logger hclog.Logger) (map[string]provider.State, error) {
	folder := object.NewFolder(c.client.Client, c.folder)

	var folderProps mo.Folder
	folder.Properties(ctx, folder.Reference(), []string{"childEntity"}, &folderProps)

	vms := make(map[string]provider.State)
	for _, mor := range folderProps.ChildEntity {
		if mor.Type != "VirtualMachine" {
			continue
		}

		vm := object.NewVirtualMachine(c.client.Client, mor)

		name, err := vm.ObjectName(ctx)
		if err != nil {
			logger.Error("failed to get vm name", "error", err, "mor", vm.Reference())
			continue
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		if !strings.HasPrefix(name, c.namePrefix) {
			continue
		}

		state, err := c.getVMState(ctx, mor)
		if err != nil {
			logger.Error("failed to get vm state", "error", err, "name", name, "mor", vm.Reference())
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		vms[name] = state
	}

	return vms, nil
}

func (c *client) DeleteVMs(ctx context.Context, vmNames []string, log hclog.Logger) ([]string, error) {
	folder := object.NewFolder(c.client.Client, c.folder)

	var folderProps mo.Folder
	folder.Properties(ctx, folder.Reference(), []string{"childEntity"}, &folderProps)

	vms := make(map[string]types.ManagedObjectReference, len(vmNames))
	for _, mor := range folderProps.ChildEntity {
		if mor.Type != "VirtualMachine" {
			continue
		}

		vm := object.NewVirtualMachine(c.client.Client, mor)
		name, err := vm.ObjectName(ctx)
		if err != nil {
			continue
		}

		if slices.Contains(vmNames, name) {
			vms[name] = vm.Reference()
		}
	}

	for _, vmName := range vmNames {
		if _, ok := vms[vmName]; !ok {
			log.Error("failure in vm deletion", "error", "failed to find VM in the destFolder", "name", vmName)
		}
	}

	var wg sync.WaitGroup
	resultChan := make(chan taskResult, len(vms))

	for name, vm := range vms {
		wg.Add(1)

		go func(vm types.ManagedObjectReference, name string) {
			defer wg.Done()

			err := c.deleteVM(ctx, vm, name)
			resultChan <- taskResult{
				name:      name,
				isSuccess: err == nil,
				err:       err,
			}
		}(vm, name)
	}

	wg.Wait()
	close(resultChan)

	deletedVms := make([]string, 0, len(vms))
	for result := range resultChan {
		if result.isSuccess {
			deletedVms = append(deletedVms, result.name)
			continue
		}

		log.Error("failure in vm deletion", "error", result.err, "name", result.name)
	}

	return deletedVms, nil
}

func (c *client) templateClone(ctx context.Context, src types.ManagedObjectReference) (string, error) {
	spec := types.VirtualMachineCloneSpec{
		Location: types.VirtualMachineRelocateSpec{
			Folder:    &c.folder,
			Pool:      &c.pool,
			Host:      &c.host,
			Datastore: &c.datastore,
		},
		PowerOn:  true, // This field is ignored when cloning from a template
		Template: false,
	}
	srcVM := object.NewVirtualMachine(c.client.Client, src)

	id := uuid.New()
	targetName := fmt.Sprintf("%s-%s", c.namePrefix, id)

	folder := object.NewFolder(c.client.Client, c.folder)

	task, err := srcVM.Clone(ctx, folder, targetName, spec)
	if err != nil {
		return "", fmt.Errorf("failed to clone VM from template: %w", err)
	}

	err = task.Wait(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to wait for VM cloning to complete: %w", err)
	}

	var folderProps mo.Folder
	folder.Properties(ctx, folder.Reference(), []string{"childEntity"}, &folderProps)

	var clonedVM *object.VirtualMachine
	for _, mor := range folderProps.ChildEntity {
		if mor.Type != "VirtualMachine" {
			continue
		}

		vm := object.NewVirtualMachine(c.client.Client, mor)
		name, err := vm.ObjectName(ctx)
		if err != nil {
			continue
		}

		if name == targetName {
			clonedVM = vm
			break
		}
	}

	if clonedVM == nil {
		return targetName, fmt.Errorf("failed to find the newly cloned VM '%s'", targetName)
	}

	if err := c.powerOnVM(ctx, clonedVM.Reference(), targetName); err != nil {
		if derr := c.deleteVM(ctx, clonedVM.Reference(), targetName); derr != nil {
			return targetName, derr
		}

		return targetName, err
	}

	return targetName, nil
}

func (c *client) powerOnVM(ctx context.Context, vmMOR types.ManagedObjectReference, vmName string) error {
	vm := object.NewVirtualMachine(c.client.Client, vmMOR)

	task, err := vm.PowerOn(ctx)
	if err != nil {
		return fmt.Errorf("failed to start VM '%s': %w", vmName, err)
	}

	err = task.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for VM '%s' startup: %w", vmName, err)
	}

	return nil
}

func (c *client) powerOffVM(ctx context.Context, vmMOR types.ManagedObjectReference, vmName string) error {
	vm := object.NewVirtualMachine(c.client.Client, vmMOR)

	task, err := vm.PowerOff(ctx)
	if err != nil {
		return fmt.Errorf("failed to power off VM '%s': %w", vmName, err)
	}

	err = task.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for VM '%s' power off: %w", vmName, err)
	}

	return nil
}

func (c *client) deleteVM(ctx context.Context, vmMOR types.ManagedObjectReference, vmName string) error {
	vm := object.NewVirtualMachine(c.client.Client, vmMOR)

	state, err := vm.PowerState(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete VM '%s': %w", vmName, err)
	}

	if state == types.VirtualMachinePowerStatePoweredOn {
		if err := c.powerOffVM(ctx, vmMOR, vmName); err != nil {
			return fmt.Errorf("failed to delete VM '%s': %w", vmName, err)
		}
	}

	task, err := vm.Destroy(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete VM '%s'", vmName)
	}

	err = task.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for VM '%s' deletion", vmName)
	}

	return nil
}

func (c *client) getVMState(ctx context.Context, vmMOR types.ManagedObjectReference) (provider.State, error) {
	vm := object.NewVirtualMachine(c.client.Client, vmMOR)

	vmName, err := vm.ObjectName(ctx)
	if err != nil {
		return provider.StateDeleting, fmt.Errorf("failed to get vm name: %w", err)
	}

	var vmInfo mo.VirtualMachine
	err = vm.Properties(ctx, vm.Reference(), []string{
		"runtime.powerState",
		"guest.net",
	}, &vmInfo)
	if err != nil {
		return provider.StateDeleting, fmt.Errorf("failed to virtual machine information: %w", err)
	}

	if vmInfo.Runtime.PowerState != types.VirtualMachinePowerStatePoweredOn {
		return provider.StateDeleting, nil
	}

	if vmInfo.Guest == nil {
		return provider.StateDeleting, fmt.Errorf("no guest info found for VM '%s'", vmName)
	}

	for _, nic := range vmInfo.Guest.Net {
		if nic.MacAddress == "" || nic.IpConfig == nil {
			continue
		}

		for _, ip := range nic.IpAddress {
			if vmip := net.ParseIP(ip).String(); vmip == "nil" {
				continue
			}

			return provider.StateRunning, nil
		}
	}

	return provider.StateCreating, nil
}

func (c *client) NetInfo(ctx context.Context, vmName string) (string, error) {
	folder := object.NewFolder(c.client.Client, c.folder)

	var folderProps mo.Folder
	folder.Properties(ctx, folder.Reference(), []string{"childEntity"}, &folderProps)

	var vm *object.VirtualMachine
	for _, mor := range folderProps.ChildEntity {
		if mor.Type != "VirtualMachine" {
			continue
		}

		v := object.NewVirtualMachine(c.client.Client, mor)
		name, err := v.ObjectName(ctx)
		if err != nil {
			continue
		}

		if name != vmName {
			continue
		}

		vm = v
		break
	}

	if vm == nil {
		return "", fmt.Errorf("failed to find vm '%s'", vmName)
	}

	var vmNetInfo mo.VirtualMachine
	err := vm.Properties(ctx, vm.Reference(), []string{"guest.net"}, &vmNetInfo)
	if err != nil {
		return "", err
	}

	if vmNetInfo.Guest == nil || vmNetInfo.Guest.Net == nil {
		return "", fmt.Errorf("failed to fetch the vm guest os net info")
	}

	var internalIP string
	for _, nic := range vmNetInfo.Guest.Net {
		if nic.MacAddress == "" || nic.IpConfig == nil {
			continue
		}

		for _, ip := range nic.IpAddress {
			if vmip := net.ParseIP(ip).String(); vmip == "nil" {
				continue
			} else {
				internalIP = vmip
				break
			}
		}
	}

	if internalIP == "" {
		return "", fmt.Errorf("failed to get ip address of vm '%s'", vmName)
	}

	return internalIP, nil
}
