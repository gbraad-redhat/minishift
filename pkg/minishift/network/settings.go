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
	"text/template"

	"github.com/docker/machine/libmachine/drivers"
	minishiftConfig "github.com/minishift/minishift/pkg/minishift/config"
	"github.com/minishift/minishift/pkg/util/os/atexit"
)

const (
	configureIPAddressMessage                   = "-- Attempting to set network settings ..."
	configureIPAddressFailure                   = "FAIL\n   not supported on this platform or hypervisor"
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
)

type NetworkSettings struct {
	Device    string
	IPAddress string
	Netmask   string
	Gateway   string
	DNS1      string
	DNS2      string
	UseDHCP   bool
}

// This will return the address as used by libmachine or assigned by us
func GetIP(driver drivers.Driver) (string, error) {
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
		atexit.ExitWithMessage(1, "The Minishift VM does not support network assignment")
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

	fmt.Println("Please restart the instance using 'stop' and 'start'")
}

func ConfigureStaticAssignment(driver drivers.Driver) {
	if checkSupportForAddressAssignment() {
		fmt.Println("Static assignment of IP address")
	}

	if minishiftConfig.IsKVM() {
		fmt.Println("kvm")
	}
	if minishiftConfig.IsHyperV() {
		fmt.Println("hyperv")
	}
	if minishiftConfig.IsXhyve() {
		fmt.Println("xhyve")
	}
	if minishiftConfig.IsVirtualBox() {
		fmt.Println("vbox")
	}
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

	if networkSettings.UseDHCP {
		tmpl.Parse(configureNetworkScriptDynamicAddressTemplate)
	} else {
		tmpl.Parse(configureNetworkScriptStaticAddressTemplate)
	}

	err := tmpl.Execute(result, networkSettings)
	if err != nil {
		atexit.ExitWithMessage(1, fmt.Sprintf("Error executing network script template:: %s", err.Error()))
	}

	return result.String()
}

func GetNetworkSettingsForHost(driver drivers.Driver) NetworkSettings {
	instanceip, err := driver.GetIP()

	if err != nil {
		fmt.Println("Err")
	}

	networkSettings := NetworkSettings{
		Device:    "eth0", // based on hypervisor
		IPAddress: instanceip,
		Netmask:   "24",
		Gateway:   "10.0.15.1", // based on hypervisor
		DNS1:      "10.0.15.3",
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

func parseResolveConf() {

}
