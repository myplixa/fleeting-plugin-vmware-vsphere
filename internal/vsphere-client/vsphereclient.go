package vsphereclient

import (
	"context"
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
	TemplateClone(ctx context.Context, template string, count uint, log hclog.Logger) (uint, error)
	GetVMs(ctx context.Context, logger hclog.Logger) map[string]provider.State
}

type client struct {
	client         *govmomi.Client
	destPool       types.ManagedObjectReference
	destDatastore  types.ManagedObjectReference
	destFolder     types.ManagedObjectReference
	destDataCenter types.ManagedObjectReference
	namePrefix     string
}

func NewClient(ctx context.Context, vsphereUrl string, insecure bool, destDataCenter string, destPool string, destDatastore string, destFolder string, namePrefix string) (*client, error) {
	if namePrefix == "" {
		return nil, fmt.Errorf("no prefix name provided for VM")
	}
	url, err := url.Parse(vsphereUrl)
	if err != nil {
		return nil, err
	}

	c, err := govmomi.NewClient(ctx, url, insecure)
	if err != nil {
		return nil, err
	}

	finder := find.NewFinder(c.Client)

	dcMOR, err := initDataCenter(ctx, finder, destDataCenter)
	if err != nil {
		return nil, err
	}

	poolMOR, err := initResourcePool(ctx, finder, destPool)
	if err != nil {
		return nil, err
	}

	dsMOR, err := initDatastore(ctx, finder, destDatastore)
	if err != nil {
		return nil, err
	}

	folder, err := finder.Folder(ctx, destFolder)
	if err != nil {
		return nil, fmt.Errorf("failed to find folder %s: %w", destFolder, err)
	}
	folderMOR := folder.Reference()

	return &client{
		client:         c,
		destDataCenter: *dcMOR,
		destPool:       *poolMOR,
		destDatastore:  *dsMOR,
		destFolder:     folderMOR,
		namePrefix:     namePrefix,
	}, nil
}

func initDataCenter(ctx context.Context, finder *find.Finder, destDataCenter string) (*types.ManagedObjectReference, error) {
	if destDataCenter == "" {
		dc, err := finder.DefaultDatacenter(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to setup default Datacenter: %w", err)
		}

		dcMOR := dc.Reference()
		return &dcMOR, nil
	}

	ds, err := finder.Datacenter(ctx, destDataCenter)
	if err != nil {
		return nil, fmt.Errorf("failed to find the Datacenter '%s': %w", destDataCenter, err)
	}
	dsMOR := ds.Reference()

	return &dsMOR, nil
}

func initResourcePool(ctx context.Context, finder *find.Finder, destPool string) (*types.ManagedObjectReference, error) {
	if destPool == "" {
		pool, err := finder.DefaultResourcePool(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to setup default Resource Pool: %w", err)
		}

		poolMOR := pool.Reference()
		return &poolMOR, nil
	}

	pool, err := finder.ResourcePool(ctx, destPool)
	if err != nil {
		return nil, fmt.Errorf("failed to find the Resource Pool %s: %w", destPool, err)
	}
	poolMOR := pool.Reference()

	return &poolMOR, nil
}

func initDatastore(ctx context.Context, finder *find.Finder, destDatastore string) (*types.ManagedObjectReference, error) {
	if destDatastore == "" {
		ds, err := finder.DefaultDatastore(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to setup default Datastore: %w", err)
		}

		dsMOR := ds.Reference()
		return &dsMOR, nil
	}

	ds, err := finder.Datastore(ctx, destDatastore)
	if err != nil {
		return nil, fmt.Errorf("failed to find the Datastore %s: %w", destDatastore, err)
	}
	dsMOR := ds.Reference()

	return &dsMOR, nil
}

type taskResult struct {
	name      string
	isSuccess bool
	err       error
}

func (c *client) TemplateClone(ctx context.Context, template string, count uint, log hclog.Logger) (uint, error) {
	if c == nil {
		return 0, fmt.Errorf("client needs to be initialized before cloning")
	}

	finder := c.newFinder()

	srcVM, err := finder.VirtualMachine(ctx, template)
	if err != nil {
		return 0, fmt.Errorf("failed to find source template: %w", err)
	}

	srcMOR := srcVM.Reference()
	if isTemp, err := srcVM.IsTemplate(ctx); err != nil {
		return 0, fmt.Errorf("failed to confirm %s is a template: %w", template, err)
	} else if !isTemp {
		return 0, fmt.Errorf("%s should be a template", template)
	}

	var wg sync.WaitGroup
	resultChan := make(chan taskResult, count)

	for range count {
		wg.Add(1)

		go func(srcMOR types.ManagedObjectReference) {
			defer wg.Done()

			name, err := c.templateClone(ctx, srcMOR, template)
			resultChan <- taskResult{
				name:      name,
				isSuccess: err == nil,
				err:       err,
			}
		}(srcMOR)
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

func (c *client) GetVMs(ctx context.Context, logger hclog.Logger) map[string]provider.State {
	folder := object.NewFolder(c.client.Client, c.destFolder)

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
		}

		if !strings.HasPrefix(name, c.namePrefix) {
			continue
		}

		state, err := c.getVMState(ctx, mor)
		if err != nil {
			logger.Error("failed to get vm state", "error", err, "name", name, "mor", vm.Reference())
		}

		vms[name] = state
	}

	return vms
}

func (c *client) DeleteVMs(ctx context.Context, vmNames []string, log hclog.Logger) ([]string, error) {
	folder := object.NewFolder(c.client.Client, c.destFolder)

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

func (c *client) templateClone(ctx context.Context, src types.ManagedObjectReference, template string) (string, error) {
	spec := types.VirtualMachineCloneSpec{
		Location: types.VirtualMachineRelocateSpec{
			Folder:    &c.destFolder,
			Pool:      &c.destPool,
			Datastore: &c.destDatastore,
		},
		PowerOn:  true, // This field is ignored when cloning from a template
		Template: false,
	}
	srcVM := object.NewVirtualMachine(c.client.Client, src)

	id := uuid.New()
	targetName := fmt.Sprintf("%s-%s", c.namePrefix, id)

	folder := object.NewFolder(c.client.Client, c.destFolder)

	task, err := srcVM.Clone(ctx, folder, targetName, spec)
	if err != nil {
		return "", fmt.Errorf("failed to clone VM from template %s", template)
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

func (c *client) newFinder() *find.Finder {
	finder := find.NewFinder(c.client.Client)

	dc := object.NewDatacenter(c.client.Client, c.destDataCenter)
	finder.SetDatacenter(dc)

	return finder
}
