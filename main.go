package main

import (
	"flag"
	"k8s.io/klog/v2"
	"path/filepath"

	"github.com/kuizhiqing/port-device-plugin/plugin"

	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

var (
	resourceName          = "github.com/port"
	deviceListAnnotations = "github.com/port"
	deviceListEnvvar      = "HOST_PORTS"
)

func main() {

	var minPort int
	var maxPort int
	var backup int

	flag.IntVar(&minPort, "min", 0, "the smallest port can be be allocated")
	flag.IntVar(&maxPort, "max", 0, "the largest  port can be be allocated")
	flag.IntVar(&backup, "backup", 1, "the number of backup")
	flag.Parse()

	klog.V(1).Infof("Port device plugin started")

	var opt *plugin.PortOpt
	if minPort > 0 && maxPort > minPort {
		delim := maxPort
		if backup > 0 {
			delim = (maxPort + minPort) / (backup + 1)
		}
		opt = &plugin.PortOpt{
			Min:    minPort,
			Max:    maxPort,
			Delim:  delim,
			Backup: backup,
		}
	}

	plug := plugin.NewPortDevicePlugin(
		resourceName,
		deviceListEnvvar,
		deviceListAnnotations,
		filepath.Join(pluginapi.DevicePluginPath, "port.sock"),
		opt)
	plug.Start()
}
