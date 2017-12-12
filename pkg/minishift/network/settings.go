/*
Copyright (C) 2017 Red Hat, Inc.

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

package network

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"text/template"

	"github.com/docker/machine/libmachine/drivers"
	minishiftConfig "github.com/minishift/minishift/pkg/minishift/config"
	"github.com/minishift/minishift/pkg/util/os/atexit"
)

const (
	configureIPAddressMessage                   = "-- Set the following network settings to VM ..."
	configureRestartNeededMessage               = "Network settings get applied to the instance on restart"
	configureIPAddressFailure                   = "FAIL\n   not supported on this platform or hypervisor"
	configureNetworkNotSupportedMessage         = "The Minishift VM does not support network assignment"
	configureNetworkScriptStaticAddressTemplate = `DEVICE={{.Device}}
IPADDR={{.IPAddress}}
NETMASK={{.Netmask}}
GATEWAY={{.Gateway}}
DNS1={{.DNS1}}
DNS2={{.DNS2}}
`
	configureNetworkScriptDynamicAddressTemplate = `DEVICE={{.Device}}
USEDHCP={{.UseDHCP}}
`
	configureNetworkScriptDisabledAddressTemplate = `DEVICE={{.Device}}
DISABLED={{.Disabled}}
`
)

type NetworkSettings struct {
	Device    string
	IPAddress string
	Netmask   string
	Gateway   string
	DNS1      string
	DNS2      string
	UseDHCP   bool
	Disabled  bool
}

// This will return the address as used by libmachine or assigned by us
func GetIP(driver drivers.Driver) (string, error) {
	configuredIP := minishiftConfig.InstanceConfig.IPAddress
	if configuredIP != "" {
		return configuredIP, nil
	}

	ip, err := driver.GetIP()
	if err != nil {
		return "", err
	}
	return ip, nil
}

func checkSupportForAddressAssignment() bool {
	if minishiftConfig.InstanceConfig.IsRHELBased &&
		minishiftConfig.InstanceConfig.SupportsNetworkAssignment {
		return true
	} else {
		atexit.ExitWithMessage(1, configureNetworkNotSupportedMessage)
	}
	return false
}

func ConfigureDynamicAssignment(driver drivers.Driver) {
	if checkSupportForAddressAssignment() {
		fmt.Println("Dynamic assignment of IP address")
	}

	networkSettingsEth0 := NetworkSettings{
		Device:  "eth0",
		UseDHCP: true,
	}
	WriteNetworkSettingsToHost(driver, networkSettingsEth0)

	networkSettingsEth1 := NetworkSettings{
		Device:  "eth1",
		UseDHCP: true,
	}
	WriteNetworkSettingsToHost(driver, networkSettingsEth1)

	fmt.Println(configureRestartNeededMessage)
}

func ConfigureStaticAssignment(driver drivers.Driver) {
	if checkSupportForAddressAssignment() {
		fmt.Println("Static assignment of IP address")
	}

	networkSettings := GetNetworkSettingsForHost(driver)

	// VirtualBox and KVM rely on twon interfaces
	// eth0 is used for host communication
	// eth1 is used for the external communication
	if minishiftConfig.IsVirtualBox() || minishiftConfig.IsKVM() {
		fmt.Println("vbox or kvm")
		dhcpNetworkSettings := NetworkSettings{
			Device:  "eth0",
			UseDHCP: true,
		}
		WriteNetworkSettingsToHost(driver, networkSettings)
		WriteNetworkSettingsToHost(driver, dhcpNetworkSettings)
	}

	// HyperV and Xhyve rely on a single interface
	// eth0 is used for hpst and external communication
	// eth1 is disabled
	if minishiftConfig.IsHyperV() || minishiftConfig.IsXhyve() {
		fmt.Println("hyperv or xhyve")
		disabledNetworkSettings := NetworkSettings{
			Device:   "eth1",
			Disabled: true,
		}
		WriteNetworkSettingsToHost(driver, networkSettings)
		WriteNetworkSettingsToHost(driver, disabledNetworkSettings)
	}

	printNetworkSettings(networkSettings)

	minishiftConfig.InstanceConfig.IPAddress = networkSettings.IPAddress
	minishiftConfig.InstanceConfig.Write()

	fmt.Println(configureRestartNeededMessage)
}

func printNetworkSettings(networkSettings NetworkSettings) {
	fmt.Println(configureIPAddressMessage)
	fmt.Println("   Device:     ", networkSettings.Device)
	fmt.Println("   IP Address: ", fmt.Sprintf("%s/%s", networkSettings.IPAddress, networkSettings.Netmask))
	if networkSettings.Gateway != "" {
		fmt.Println("   Gateway:    ", networkSettings.Gateway)
	}
	if networkSettings.DNS1 != "" {
		fmt.Println("   Nameservers:", fmt.Sprintf("%s %s", networkSettings.DNS1, networkSettings.DNS2))
	}
}

func fillNetworkSettingsScript(networkSettings NetworkSettings) string {
	result := &bytes.Buffer{}

	tmpl := template.New("networkScript")

	if networkSettings.Disabled {
		tmpl.Parse(configureNetworkScriptDisabledAddressTemplate)
	} else if networkSettings.UseDHCP {
		tmpl.Parse(configureNetworkScriptDynamicAddressTemplate)
	} else {
		tmpl.Parse(configureNetworkScriptStaticAddressTemplate)
	}

	err := tmpl.Execute(result, networkSettings)
	if err != nil {
		atexit.ExitWithMessage(1, fmt.Sprintf("Error executing network script template: %s", err.Error()))
	}

	return result.String()
}

func GetNetworkSettingsForHost(driver drivers.Driver) NetworkSettings {
	instanceip, err := driver.GetIP()
	if err != nil {
		atexit.ExitWithMessage(1, fmt.Sprintf("Error getting IP address: %s", err.Error()))
	}
	if instanceip == "" {
		atexit.ExitWithMessage(1, fmt.Sprintf("Error getting IP address: %s", "No address available"))
	}

	device, err := drivers.RunSSHCommandFromDriver(driver, fmt.Sprintf("ip a |grep -i '%s' | awk '{print $NF}' | tr -d '\n'", instanceip))
	if err != nil {
		atexit.ExitWithMessage(1, fmt.Sprintf("Error getting device: %s", err.Error()))
	}

	addressInfo, err := drivers.RunSSHCommandFromDriver(driver, fmt.Sprintf("ip -o -f inet addr show %s | head -n1 | awk '/scope global/ {print $4}'", device))
	if err != nil {
		atexit.ExitWithMessage(1, fmt.Sprintf("Error getting netmask: %s", err.Error()))
	}
	ipAddress := strings.Split(strings.TrimSpace(addressInfo), "/")[0]
	netmask := strings.Split(strings.TrimSpace(addressInfo), "/")[1]

	resolveInfo, err := drivers.RunSSHCommandFromDriver(driver, "cat /etc/resolv.conf |grep -i '^nameserver' | cut -d ' ' -f2 | tr '\n' ' '")
	if err != nil {
		atexit.ExitWithMessage(1, fmt.Sprintf("Error getting nameserver: %s", err.Error()))
	}
	nameservers := strings.Split(strings.TrimSpace(resolveInfo), " ")

	gateway, err := drivers.RunSSHCommandFromDriver(driver, "route -n | grep 'UG[ \t]' | awk '{print $2}' | tr -d '\n'")
	if err != nil {
		atexit.ExitWithMessage(1, fmt.Sprintf("Error getting gateway: %s", err.Error()))
	}

	networkSettings := NetworkSettings{
		Device:    device,
		IPAddress: ipAddress, // ~= instanceip
		Netmask:   netmask,
		Gateway:   gateway,
		DNS1:      nameservers[0],
	}
	if len(nameservers) > 1 {
		networkSettings.DNS2 = nameservers[1]
	}

	return networkSettings
}

func WriteNetworkSettingsToHost(driver drivers.Driver, networkSettings NetworkSettings) bool {
	networkScript := fillNetworkSettingsScript(networkSettings) // perhaps move this to the struct as a ToString()
	encodedScript := base64.StdEncoding.EncodeToString([]byte(networkScript))

	cmd := fmt.Sprintf(
		"echo %s | base64 --decode | sudo tee /var/lib/minishift/networking-%s > /dev/null",
		encodedScript,
		networkSettings.Device)

	if _, err := drivers.RunSSHCommandFromDriver(driver, cmd); err != nil {
		fmt.Println("FAIL")
		return false //fmt.Errorf("Error occured while writing network configuration", err)
	} else {
		fmt.Println("OK")
	}

	return true
}
