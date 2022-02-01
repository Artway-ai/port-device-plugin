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

	flag.IntVar(&minPort, "min", 0, "the smallest port can be be allocated")
	flag.IntVar(&maxPort, "max", 1000, "the largest port can be be allocated")
	flag.Parse()

	var opt *plugin.PortOpt
	delim := maxPort - (maxPort-minPort)/10
	opt = &plugin.PortOpt{
		Min:   minPort,
		Max:   maxPort,
		Delim: delim,
	}

	klog.Infof("Ports will be allocated between %d-%d, while %d-%d are reserved for replacing when conflict", minPort, delim, delim, maxPort)

	plug := plugin.NewPortDevicePlugin(
		resourceName,
		deviceListEnvvar,
		deviceListAnnotations,
		filepath.Join(pluginapi.DevicePluginPath, "port.sock"),
		opt)
	plug.Start()
}
