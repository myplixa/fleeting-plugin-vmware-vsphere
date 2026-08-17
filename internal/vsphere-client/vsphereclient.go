package vsphereclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"text/template"

	"github.com/google/uuid"
	"github.com/hashicorp/go-hclog"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

type Client interface {
	DeleteVMs(ctx context.Context, vmNames []string, log hclog.Logger) ([]string, error)
	TemplateClone(ctx context.Context, count uint, log hclog.Logger, guestopts *GuestOsOpts) (uint, error)
	GetVMs(ctx context.Context, logger hclog.Logger) (map[string]provider.State, error)
	NetInfo(ctx context.Context, vmName string) (string, error)
	GuestOs(ctx context.Context) (string, error)
}

type ClientOption func(ctx context.Context, c *client, finder *find.Finder) error

type client struct {
	client        *govmomi.Client
	datacenter    types.ManagedObjectReference
	pool          types.ManagedObjectReference
	host          *types.ManagedObjectReference
	datastore     types.ManagedObjectReference
	folder        types.ManagedObjectReference
	template      types.ManagedObjectReference
	namePrefix    string
	hostShortName string
	linkedClone   bool
	snapshotName  string
	snapshot      *types.ManagedObjectReference

	numCPUs    int32
	memoryMB   int64
	diskSizeGB int64
	diskChange *types.VirtualDeviceConfigSpec

	cloudInitExtraTemplate *template.Template
	cloudInitVars          map[string]string
}

func NewClient(ctx context.Context, vsphereUrl string, insecure bool, template string, username string, password string, options ...ClientOption) (Client, error) {
	targetURL, err := parseURL(vsphereUrl, username, password)
	if err != nil {
		return nil, err
	}

	gc, err := govmomi.NewClient(ctx, targetURL, insecure)
	if err != nil {
		return nil, err
	}

	finder := find.NewFinder(gc.Client)

	c := client{
		client: gc,
	}

	// WithDatacenter scopes the finder, so it must run before any option that
	// resolves an inventory path. InstanceGroup.Init already orders it first.
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
		return nil, fmt.Errorf("failed to find source VM/template %s: %w", template, err)
	}

	isTemplate, err := templateVM.IsTemplate(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to confirm %s is a template: %w", template, err)
	}
	if c.linkedClone && isTemplate {
		return nil, fmt.Errorf("linked clone requires a VM with snapshots, but %s is a template", template)
	}
	if !c.linkedClone && !isTemplate {
		return nil, fmt.Errorf("%s should be a template", template)
	}
	c.template = templateVM.Reference()

	if c.linkedClone {
		snapshot, err := c.resolveSnapshot(ctx, c.template)
		if err != nil {
			return nil, err
		}
		c.snapshot = snapshot
	}

	if c.diskSizeGB > 0 {
		diskChange, err := c.resolveDiskResize(ctx, c.template, c.diskSizeGB)
		if err != nil {
			return nil, err
		}
		c.diskChange = diskChange
	}

	if c.folder == (types.ManagedObjectReference{}) {
		folder, err := finder.DefaultFolder(ctx)
		if err != nil {
			return nil, err
		}

		c.folder = folder.Reference()
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

		// Scope the finder to this datacenter. Without this, every later lookup
		// (folder, host, resource pool, datastore, template) runs without a
		// datacenter context, so relative paths fail with errors such as
		// "failed to find the resource pool <nil>: please specify a datacenter"
		// and users are forced to spell out absolute inventory paths.
		finder.SetDatacenter(ds)

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
		hostSystem, err := finder.HostSystem(ctx, host)
		if err != nil {
			return fmt.Errorf("failed to find the host %s: %w", host, err)
		}

		ref := hostSystem.Reference()
		c.host = &ref
		c.hostShortName = shortHostName(host)
		return nil
	}
}

func shortHostName(host string) string {
	base := path.Base(host)
	if i := strings.Index(base, "."); i >= 0 {
		base = base[:i]
	}
	return base
}

func (c *client) instancePrefix() string {
	if c.hostShortName == "" {
		return c.namePrefix
	}
	return c.hostShortName + "-" + c.namePrefix
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
		if len(prefix) == 0 {
			return errors.New("instance group name is required")
		}
		c.namePrefix = prefix
		return nil
	}
}

func WithLinkedClone(snapshotName string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		c.linkedClone = true
		c.snapshotName = snapshotName
		return nil
	}
}

func WithNumCPUs(numCPUs int32) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		if numCPUs <= 0 {
			return fmt.Errorf("num_cpus must be greater than 0, got %d", numCPUs)
		}
		c.numCPUs = numCPUs
		return nil
	}
}

func WithMemoryMB(memoryMB int64) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		if memoryMB <= 0 {
			return fmt.Errorf("memory_mb must be greater than 0, got %d", memoryMB)
		}
		c.memoryMB = memoryMB
		return nil
	}
}

func WithDiskSizeGB(diskSizeGB int64) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		if diskSizeGB <= 0 {
			return fmt.Errorf("disk_size_gb must be greater than 0, got %d", diskSizeGB)
		}
		c.diskSizeGB = diskSizeGB
		return nil
	}
}

// WithCloudInitExtraFile reads and parses the given file once at startup, so a
// broken template fails the plugin's Init immediately instead of failing every
// subsequent clone. The file is treated as a Go text/template producing one or
// more #cloud-config documents; see cloudInitExtraData for the fields it can
// reference.
func WithCloudInitExtraFile(path string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading cloud_init_extra_file %s: %w", path, err)
		}

		tmpl, err := template.New(filepath.Base(path)).Parse(string(content))
		if err != nil {
			return fmt.Errorf("parsing cloud_init_extra_file %s: %w", path, err)
		}

		c.cloudInitExtraTemplate = tmpl
		return nil
	}
}

func WithCloudInitVars(vars map[string]string) ClientOption {
	return func(ctx context.Context, c *client, finder *find.Finder) error {
		c.cloudInitVars = vars
		return nil
	}
}

type taskResult struct {
	name      string
	isSuccess bool
	err       error
}

type GuestOsOpts struct {
	Username string
	PubKey   []byte
}

func (c *client) TemplateClone(ctx context.Context, count uint, log hclog.Logger, guestopts *GuestOsOpts) (uint, error) {
	if c == nil {
		return 0, fmt.Errorf("client needs to be initialized before cloning")
	}

	var wg sync.WaitGroup
	resultChan := make(chan taskResult, count)

	for range count {
		wg.Add(1)

		go func() {
			defer wg.Done()

			name, err := c.templateClone(ctx, c.template, guestopts)
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

		if !strings.HasPrefix(name, c.instancePrefix()) {
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

func (c *client) templateClone(ctx context.Context, src types.ManagedObjectReference, guestopts *GuestOsOpts) (string, error) {
	shortID := uuid.New().String()[:8]
	targetName := fmt.Sprintf("%s-%s", c.instancePrefix(), shortID)

	config := c.hardwareConfigSpec()

	if guestopts != nil {
		userOptions, err := c.encodeUserData(guestopts.Username, guestopts.PubKey, targetName)
		if err != nil {
			return "", err
		}

		if config == nil {
			config = &types.VirtualMachineConfigSpec{}
		}

		config.ExtraConfig = userOptions
	}

	spec := types.VirtualMachineCloneSpec{
		Location: types.VirtualMachineRelocateSpec{
			Folder:    &c.folder,
			Pool:      &c.pool,
			Host:      c.host,
			Datastore: &c.datastore,
		},
		Config:   config,
		PowerOn:  false, // This field is ignored when cloning from a template
		Template: false,
	}

	if c.linkedClone {
		spec.Snapshot = c.snapshot
		spec.Location.DiskMoveType = string(types.VirtualMachineRelocateDiskMoveOptionsCreateNewChildDiskBacking)
	}
	srcVM := object.NewVirtualMachine(c.client.Client, src)

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

func (c *client) resolveSnapshot(ctx context.Context, vmRef types.ManagedObjectReference) (*types.ManagedObjectReference, error) {
	vm := object.NewVirtualMachine(c.client.Client, vmRef)

	var vmMo mo.VirtualMachine
	err := vm.Properties(ctx, vm.Reference(), []string{"snapshot"}, &vmMo)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve snapshot info: %w", err)
	}

	if vmMo.Snapshot == nil {
		return nil, fmt.Errorf("linked clone requires the source VM to have at least one snapshot")
	}

	if c.snapshotName == "" {
		if vmMo.Snapshot.CurrentSnapshot == nil {
			return nil, fmt.Errorf("no current snapshot found on source VM")
		}
		return vmMo.Snapshot.CurrentSnapshot, nil
	}

	queue := vmMo.Snapshot.RootSnapshotList
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		if s.Name == c.snapshotName {
			ref := s.Snapshot
			return &ref, nil
		}
		queue = append(queue, s.ChildSnapshotList...)
	}

	return nil, fmt.Errorf("snapshot '%s' not found on source VM", c.snapshotName)
}

func (c *client) resolveDiskResize(ctx context.Context, vmRef types.ManagedObjectReference, sizeGB int64) (*types.VirtualDeviceConfigSpec, error) {
	vm := object.NewVirtualMachine(c.client.Client, vmRef)

	devices, err := vm.Device(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list template devices: %w", err)
	}

	disks := devices.SelectByType((*types.VirtualDisk)(nil))
	if len(disks) == 0 {
		return nil, fmt.Errorf("template has no virtual disks to resize")
	}

	disk, ok := disks[0].(*types.VirtualDisk)
	if !ok {
		return nil, fmt.Errorf("unexpected device type for template's primary disk")
	}

	requestedKB := sizeGB * 1024 * 1024

	switch {
	case requestedKB < disk.CapacityInKB:
		return nil, fmt.Errorf(
			"disk_size_gb (%dGB) is smaller than the template's primary disk (%dGB); virtual disks can only grow",
			sizeGB, disk.CapacityInKB/(1024*1024),
		)
	case requestedKB == disk.CapacityInKB:
		return nil, nil
	}

	disk.CapacityInKB = requestedKB

	return &types.VirtualDeviceConfigSpec{
		Operation: types.VirtualDeviceConfigSpecOperationEdit,
		Device:    disk,
	}, nil
}

func (c *client) hardwareConfigSpec() *types.VirtualMachineConfigSpec {
	if c.numCPUs == 0 && c.memoryMB == 0 && c.diskChange == nil {
		return nil
	}

	config := &types.VirtualMachineConfigSpec{}

	if c.numCPUs > 0 {
		config.NumCPUs = c.numCPUs
	}

	if c.memoryMB > 0 {
		config.MemoryMB = c.memoryMB
	}

	if c.diskChange != nil {
		config.DeviceChange = []types.BaseVirtualDeviceConfigSpec{c.diskChange}
	}

	return config
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

// pickGuestIP returns a usable address from the NICs reported by VMware Tools,
// preferring IPv4 and falling back to a non-link-local IPv6 address.
//
// Three problems are handled here:
//
//  1. Link-local and loopback addresses are not usable by the runner. A Windows
//     guest with IPv6 enabled typically reports its fe80::/10 address first, and
//     returning it leaves the runner dialing an address it can never reach: the
//     job hangs on "Dialing instance..." with no error to explain why.
//  2. IPv4 is preferred because it is what most environments actually route. A
//     non-link-local IPv6 address is only used when no IPv4 address exists.
//  3. Selection stops at the first usable address. Breaking out of the inner
//     loop alone let a later NIC overwrite an address that was already good, so
//     on a multi-NIC guest the last NIC won.
//
// net.ParseIP returns nil for malformed input, so the parse result is checked
// directly rather than comparing its String() against a literal.
func pickGuestIP(nics []types.GuestNicInfo) string {
	var fallbackIP string

	for _, nic := range nics {
		if nic.MacAddress == "" || nic.IpConfig == nil {
			continue
		}

		for _, ip := range nic.IpAddress {
			parsed := net.ParseIP(ip)
			if parsed == nil || parsed.IsUnspecified() || parsed.IsLoopback() ||
				parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast() {
				continue
			}

			if parsed.To4() != nil {
				return parsed.String()
			}

			if fallbackIP == "" {
				fallbackIP = parsed.String()
			}
		}
	}

	return fallbackIP
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

	internalIP := pickGuestIP(vmNetInfo.Guest.Net)

	if internalIP == "" {
		return "", fmt.Errorf("failed to get ip address of vm '%s'", vmName)
	}

	return internalIP, nil
}

func (c *client) GuestOs(ctx context.Context) (string, error) {
	vm := object.NewVirtualMachine(c.client.Client, c.template)
	pc := property.DefaultCollector(c.client.Client)

	var vmMo mo.VirtualMachine
	err := pc.RetrieveOne(ctx, vm.Reference(), []string{"config.guestId"}, &vmMo)
	if err != nil {
		return "", err
	}

	if vmMo.Config == nil {
		return "", fmt.Errorf("failed to retrieve guest os info")
	}

	return vmMo.Config.GuestId, nil
}

// parseURL parses the given URL and combines the result with the given username and password.
// It ensures the username and password are encoded in a url-safe way.
func parseURL(vsphereUrl string, username string, password string) (*url.URL, error) {
	serverURL, err := url.Parse(vsphereUrl)
	if err != nil {
		return nil, err
	}

	if len(username) > 0 && len(password) > 0 {
		serverURL.User = url.UserPassword(username, password)
	}
	return serverURL, nil
}
