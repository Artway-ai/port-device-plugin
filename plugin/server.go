package plugin

import (
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// Constants for use by the 'volume-mounts' device list strategy
const (
	deviceListAsVolumeMountsHostPath          = "/dev/null"
	deviceListAsVolumeMountsContainerPathRoot = "/var/run/port-container-devices"
)

type PortOpt struct {
	// smallest port can be allocated
	Min int
	// largest port can be allocated
	Max int
	// delimitation, [min, delim) for allocation and [delim, max) for backup
	Delim int
	// number of backup
	Backup int
}

// PortDevicePlugin implements the Kubernetes device plugin API
type PortDevicePlugin struct {
	pluginapi.UnimplementedDevicePluginServer
	resourceName          string
	deviceListEnvvar      string
	deviceListAnnotations string
	socket                string

	portOpt *PortOpt

	server        *grpc.Server
	cachedDevices []*Device
	health        chan *Device
	stop          chan interface{}
}

// NewPortDevicePlugin returns an initialized PortDevicePlugin
func NewPortDevicePlugin(resourceName string, deviceListEnvvar string, deviceListAnnotations string, socket string, portOpt *PortOpt) *PortDevicePlugin {
	return &PortDevicePlugin{
		resourceName:          resourceName,
		deviceListEnvvar:      deviceListEnvvar,
		deviceListAnnotations: deviceListAnnotations,
		socket:                socket,
		portOpt:               portOpt,

		// These will be reinitialized every
		// time the plugin server is restarted.
		server:        nil,
		cachedDevices: nil,
		health:        nil,
		stop:          nil,
	}
}

func (m *PortDevicePlugin) cleanup() {
	close(m.stop)
	m.cachedDevices = nil
	m.server = nil
	m.health = nil
	m.stop = nil
}

// Start starts the gRPC server, registers the device plugin with the Kubelet,
// and starts the device healthchecks.
func (m *PortDevicePlugin) Start() error {
	m.cachedDevices = m.Devices()
	klog.V(1).Infof("cached device %d", len(m.cachedDevices))
	m.server = grpc.NewServer([]grpc.ServerOption{}...)
	m.health = make(chan *Device)
	m.stop = make(chan interface{})

	err := m.Serve()
	if err != nil {
		log.Printf("Could not start device plugin for '%s': %s", m.resourceName, err)
		m.cleanup()
		return err
	}
	log.Printf("Starting to serve '%s' on %s", m.resourceName, m.socket)

	err = m.Register()
	if err != nil {
		log.Printf("Could not register device plugin: %s", err)
		m.Stop()
		return err
	}
	log.Printf("Registered device plugin for '%s' with Kubelet", m.resourceName)

	<-m.stop

	m.Stop()

	return nil
}

// Stop stops the gRPC server.
func (m *PortDevicePlugin) Stop() error {
	if m == nil || m.server == nil {
		return nil
	}
	log.Printf("Stopping to serve '%s' on %s", m.resourceName, m.socket)
	m.server.Stop()
	if err := os.Remove(m.socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	m.cleanup()
	return nil
}

// Serve starts the gRPC server of the device plugin.
func (m *PortDevicePlugin) Serve() error {
	os.Remove(m.socket)
	sock, err := net.Listen("unix", m.socket)
	if err != nil {
		return err
	}

	pluginapi.RegisterDevicePluginServer(m.server, m)

	go func() {
		lastCrashTime := time.Now()
		restartCount := 0
		for {
			log.Printf("Starting GRPC server for '%s'", m.resourceName)
			err := m.server.Serve(sock)
			if err == nil {
				break
			}

			log.Printf("GRPC server for '%s' crashed with error: %v", m.resourceName, err)

			// restart if it has not been too often
			// i.e. if server has crashed more than 5 times and it didn't last more than one hour each time
			if restartCount > 5 {
				// quit
				log.Fatalf("GRPC server for '%s' has repeatedly crashed recently. Quitting", m.resourceName)
			}
			timeSinceLastCrash := time.Since(lastCrashTime).Seconds()
			lastCrashTime = time.Now()
			if timeSinceLastCrash > 3600 {
				// it has been one hour since the last crash.. reset the count
				// to reflect on the frequency
				restartCount = 1
			} else {
				restartCount++
			}
		}
	}()

	// Wait for server to start by launching a blocking connexion
	conn, err := m.dial(m.socket, 5*time.Second)
	if err != nil {
		return err
	}
	conn.Close()

	return nil
}

// Register registers the device plugin for the given resourceName with Kubelet.
func (m *PortDevicePlugin) Register() error {
	conn, err := m.dial(pluginapi.KubeletSocket, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	opts, _ := m.GetDevicePluginOptions(context.Background(), nil)
	req := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(m.socket),
		ResourceName: m.resourceName,
		Options:      opts,
	}

	_, err = client.Register(context.Background(), req)
	if err != nil {
		return err
	}
	return nil
}

func (m *PortDevicePlugin) backupPolicy(id int) []int {
	shift := m.portOpt.Delim - m.portOpt.Min
	back := []int{}
	for i := 0; i < m.portOpt.Backup; i++ {
		b := shift * i
		if b < m.portOpt.Max {
			back = append(back, b)
		} else {
			break
		}
	}
	return back
}

func (m *PortDevicePlugin) Devices() []*Device {
	var devs []*Device
	if m.portOpt == nil {
		for i := 0; i < 1000; i++ {
			d := NewDevice(i, nil)
			devs = append(devs, d)
		}
	} else {
		for i := m.portOpt.Min; i < m.portOpt.Delim; i++ {
			d := NewDevice(i, m.backupPolicy(i))
			devs = append(devs, d)
		}
	}
	return devs
}

func (m *PortDevicePlugin) getPortsByIDs(plist []string) ([]string, error) {
	devs, err := m.getDevicesByIDs(plist)
	if err != nil {
		return nil, err
	}
	ids, err := m.getPortsByDevices(devs)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (m *PortDevicePlugin) getDevicesByIDs(plist []string) ([]*Device, error) {
	var devs []*Device
	for _, pd := range plist {
		for j, d := range m.cachedDevices {
			if d.ID == pd {
				devs = append(devs, m.cachedDevices[j])
				continue
			}
		}
	}
	if len(devs) != len(plist) {
		return nil, fmt.Errorf("device not found %v", plist)
	}
	return devs, nil
}

func (m *PortDevicePlugin) getPortsByDevices(devs []*Device) ([]string, error) {
	ps := make([]string, len(devs))
	for i, dv := range devs {
		p, err := dv.availablePort()
		if err != nil {
			return nil, err
		}
		ps[i] = p
	}
	return ps, nil
}

// GetDevicePluginOptions returns the values of the optional settings for this plugin
func (m *PortDevicePlugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	options := &pluginapi.DevicePluginOptions{
		PreStartRequired:                true,
		GetPreferredAllocationAvailable: true,
	}
	return options, nil
}

// ListAndWatch lists devices and update that list according to the health status
func (m *PortDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	s.Send(&pluginapi.ListAndWatchResponse{Devices: m.apiDevices()})

	for {
		select {
		case <-m.stop:
			return nil
		case d := <-m.health:
			d.Health = pluginapi.Unhealthy
		}
	}
}

// GetPreferredAllocation returns the preferred allocation from the set of devices specified in the request
func (m *PortDevicePlugin) GetPreferredAllocation(ctx context.Context, r *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	response := &pluginapi.PreferredAllocationResponse{}
	for _, req := range r.ContainerRequests {
		deviceIds := req.AvailableDeviceIDs[:req.AllocationSize]
		resp := &pluginapi.ContainerPreferredAllocationResponse{
			DeviceIDs: deviceIds,
		}

		response.ContainerResponses = append(response.ContainerResponses, resp)
	}
	return response, nil
}

// Allocate which return list of devices.
func (m *PortDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	responses := pluginapi.AllocateResponse{}
	for _, req := range reqs.ContainerRequests {
		var ids []string
		if m.portOpt == nil {
			ids = NewFreePorts(len(req.DevicesIDs))
		} else {
			var err error
			ids, err = m.getPortsByIDs(req.DevicesIDs)
			if err != nil {
				return nil, err
			}
		}

		response := pluginapi.ContainerAllocateResponse{}
		response.Envs = m.apiEnvs(ids)
		response.Mounts = m.apiMounts(ids)
		response.Annotations = m.apiAnnotations(ids)

		responses.ContainerResponses = append(responses.ContainerResponses, &response)

		log.Println("Ports allocated", ids)
	}
	return &responses, nil
}

// PreStartContainer is unimplemented for this plugin
func (m *PortDevicePlugin) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

// dial establishes the gRPC communication with the registered device plugin.
func (m *PortDevicePlugin) dial(unixSocketPath string, timeout time.Duration) (*grpc.ClientConn, error) {
	c, err := grpc.Dial(unixSocketPath, grpc.WithInsecure(), grpc.WithBlock(),
		grpc.WithTimeout(timeout),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)

	if err != nil {
		return nil, err
	}

	return c, nil
}

func (m *PortDevicePlugin) apiDevices() []*pluginapi.Device {
	var pdevs []*pluginapi.Device
	for _, d := range m.cachedDevices {
		pdevs = append(pdevs, &d.Device)
	}
	return pdevs
}

func (m *PortDevicePlugin) apiEnvs(deviceIDs []string) map[string]string {
	return map[string]string{
		m.deviceListEnvvar: strings.Join(deviceIDs, ","),
	}
}

func (m *PortDevicePlugin) apiAnnotations(deviceIDs []string) map[string]string {
	return map[string]string{
		m.deviceListAnnotations: strings.Join(deviceIDs, ","),
	}
}

func (m *PortDevicePlugin) apiMounts(deviceIDs []string) []*pluginapi.Mount {
	var mounts []*pluginapi.Mount

	for _, id := range deviceIDs {
		mount := &pluginapi.Mount{
			HostPath:      deviceListAsVolumeMountsHostPath,
			ContainerPath: filepath.Join(deviceListAsVolumeMountsContainerPathRoot, id),
		}
		mounts = append(mounts, mount)
	}

	return mounts
}
