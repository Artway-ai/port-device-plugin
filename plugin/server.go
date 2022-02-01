/*
Copyright 2022 kuizhiqing.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package plugin

import (
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
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
}

// PortDevicePlugin implements the Kubernetes device plugin API
type PortDevicePlugin struct {
	pluginapi.UnimplementedDevicePluginServer
	resourceName          string
	deviceListEnvvar      string
	deviceListAnnotations string
	socket                string

	portOpt *PortOpt

	// alternative ports cursor
	cursor int

	server        *grpc.Server
	cachedDevices []*pluginapi.Device
	health        chan *pluginapi.Device
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

		cursor: portOpt.Delim,

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
	klog.Infof("cached device %d", len(m.cachedDevices))
	m.server = grpc.NewServer([]grpc.ServerOption{}...)
	m.health = make(chan *pluginapi.Device)
	m.stop = make(chan interface{})

	err := m.Serve()
	if err != nil {
		klog.Fatalf("Could not start device plugin for '%s': %s", m.resourceName, err)
		m.cleanup()
		return err
	}
	klog.Infof("Starting to serve '%s' on %s", m.resourceName, m.socket)

	err = m.Register()
	if err != nil {
		klog.Fatalf("Could not register device plugin: %s", err)
		m.Stop()
		return err
	}
	klog.Infof("Registered device plugin for '%s' with Kubelet", m.resourceName)

	<-m.stop

	m.Stop()

	return nil
}

// Stop stops the gRPC server.
func (m *PortDevicePlugin) Stop() error {
	if m == nil || m.server == nil {
		return nil
	}
	klog.Infof("Stopping to serve '%s' on %s", m.resourceName, m.socket)
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
			klog.Infof("Starting GRPC server for '%s'", m.resourceName)
			err := m.server.Serve(sock)
			if err == nil {
				break
			}

			klog.Infof("GRPC server for '%s' crashed with error: %v", m.resourceName, err)

			// restart if it has not been too often
			// i.e. if server has crashed more than 5 times and it didn't last more than one hour each time
			if restartCount > 5 {
				// quit
				klog.Fatalf("GRPC server for '%s' has repeatedly crashed recently. Quitting", m.resourceName)
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

// GetDevicePluginOptions returns the values of the optional settings for this plugin
func (m *PortDevicePlugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	options := &pluginapi.DevicePluginOptions{
		PreStartRequired:                false,
		GetPreferredAllocationAvailable: false,
	}
	return options, nil
}

// ListAndWatch lists devices and update that list according to the health status
func (m *PortDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	s.Send(&pluginapi.ListAndWatchResponse{Devices: m.cachedDevices})

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
		if m.portOpt.Min == 0 {
			ids = NewFreePorts(len(req.DevicesIDs))
		} else {
			ids = m.validatedPorts(req.DevicesIDs)
		}

		response := pluginapi.ContainerAllocateResponse{}
		response.Envs = m.apiEnvs(ids)
		response.Mounts = m.apiMounts(ids)
		response.Annotations = m.apiAnnotations(ids)

		responses.ContainerResponses = append(responses.ContainerResponses, &response)

		klog.Info("Ports allocated ", ids)
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

func (m *PortDevicePlugin) Devices() []*pluginapi.Device {
	var devs []*pluginapi.Device

	for i := m.portOpt.Min; i < m.portOpt.Delim; i++ {
		devs = append(devs,
			&pluginapi.Device{
				ID:     strconv.Itoa(i),
				Health: pluginapi.Healthy,
			})
	}
	return devs
}

func (m *PortDevicePlugin) validatedPorts(rids []string) []string {
	var ids []string
	for _, id := range rids {
		if !IsAvailable(id) {
			id = m.alternatePort()
		}
		ids = append(ids, id)
	}
	return ids
}

func (m *PortDevicePlugin) alternatePort() string {
	for {
		p := strconv.Itoa(m.cursor)
		m.cursor++
		if IsAvailable(p) {
			return p
		}
		if m.cursor >= m.portOpt.Max {
			m.cursor = m.portOpt.Delim
			time.Sleep(time.Second)
		}
	}
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

func NewFreePort() (port int, err error) {
	if addr, err := net.ResolveTCPAddr("tcp", "0.0.0.0:0"); err == nil {
		if l, err := net.ListenTCP("tcp", addr); err == nil {
			defer l.Close()
			return l.Addr().(*net.TCPAddr).Port, nil
		}
		return 0, err
	}
	return 0, err
}

func NewFreePorts(n int) []string {
	ps := make([]string, n)
	i := 0
	for i < n {
		p, err := NewFreePort()
		if err == nil {
			ps[i] = strconv.Itoa(p)
			i++
		}
	}
	return ps
}

func IsAvailable(port string) bool {
	address := net.JoinHostPort("", port)
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if conn != nil {
		conn.Close()
	}

	if err == nil {
		return false
	} else {
		return true
	}
}
