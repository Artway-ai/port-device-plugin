package plugin

import (
	"fmt"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"net"
	"strconv"
	"time"
)

type Device struct {
	pluginapi.Device
	// the main port as device
	Port int
	// the ports can be assign if main port is occupied
	Backup []int
}

func NewFreeDevice() *Device {
	p, err := NewFreePort()
	if err != nil {
		return nil
	}
	d := Device{}
	d.ID = strconv.Itoa(p)
	d.Health = pluginapi.Healthy
	d.Port = p
	return &d
}

func NewDevice(id int, back []int) *Device {
	d := Device{}
	d.ID = strconv.Itoa(id)
	d.Health = pluginapi.Healthy
	d.Port = id
	d.Backup = back
	return &d
}

func (d *Device) availablePort() (string, error) {
	if d.isAvailable(d.ID) {
		return d.ID, nil
	}

	for _, p := range d.Backup {
		pt := strconv.Itoa(p)
		if d.isAvailable(pt) {
			return pt, nil
		}
	}
	return "", fmt.Errorf("invalid port '%s'", d.ID)
}

func (d *Device) isAvailable(port string) bool {
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
