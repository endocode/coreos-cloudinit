// Copyright 2015 CoreOS, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package initialize

import (
	"fmt"
	"net"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/coreos/coreos-cloudinit/Godeps/_workspace/src/github.com/docker/libcontainer/netlink"
	"github.com/coreos/coreos-cloudinit/config"
	"github.com/coreos/coreos-cloudinit/datasource"
	"github.com/coreos/coreos-cloudinit/system"
)

const DefaultSSHKeyName = "coreos-cloudinit"

type EnvVal struct {
	envName string
	val     string
}

type Environment struct {
	root          string
	configRoot    string
	workspace     string
	sshKeyName    string
	substitutions map[string]EnvVal
}

// TODO(jonboulle): this is getting unwieldy, should be able to simplify the interface somehow
func NewEnvironment(root, configRoot, workspace, sshKeyName string, metadata datasource.Metadata) *Environment {
	firstNonNull := func(ip net.IP, env string) string {
		if ip == nil {
			return env
		}
		return ip.String()
	}

	substitutions := make(map[string]EnvVal)

	envMap := map[string]string{
		"$public_ipv4":  "COREOS_PUBLIC_IPV4",
		"$private_ipv4": "COREOS_PRIVATE_IPV4",
		"$public_ipv6":  "COREOS_PUBLIC_IPV6",
		"$private_ipv6": "COREOS_PRIVATE_IPV6",
	}

	for key, val := range envMap {
		var envVal EnvVal
		switch key {
		case "$public_ipv4":
			envVal = EnvVal{envName: val, val: firstNonNull(metadata.PublicIPv4, os.Getenv(val))}
		case "$private_ipv4":
			envVal = EnvVal{envName: val, val: firstNonNull(metadata.PrivateIPv4, os.Getenv(val))}
		case "$public_ipv6":
			envVal = EnvVal{envName: val, val: firstNonNull(metadata.PublicIPv6, os.Getenv(val))}
		case "$private_ipv6":
			envVal = EnvVal{envName: val, val: firstNonNull(metadata.PrivateIPv6, os.Getenv(val))}
		}
		substitutions[key] = envVal
	}

	// Populate system network interfaces
	defaultIfaceName := getDefaultGatewayIfaceName()
	interfaces, err := net.Interfaces()
	if err == nil {
		fmt.Printf("Fetching network interfaces info\n")
		for _, iface := range interfaces {
			addrs, err := iface.Addrs()
			if err == nil {
				ipv4 := 0
				ipv6 := 0
				for _, addr := range addrs {
					ip, _, err := net.ParseCIDR(addr.String())
					if err != nil {
						fmt.Printf("Warning: Cannot parse '%s' CIDR\n", addr.String())
					} else {
						var varName string
						IPseq := ""
						if ip.To4() != nil {
							if ipv4 > 0 {
								IPseq = fmt.Sprintf("_%d", ipv4)
							}
							varName = fmt.Sprintf("IFACE_%s%s_IPV4", strings.Replace(strings.ToUpper(iface.Name), ".", "_", -1), IPseq)
							ipv4++
						} else if ip.To16() != nil {
							if ipv6 > 0 {
								IPseq = fmt.Sprintf("_%d", ipv6)
							}
							varName = fmt.Sprintf("IFACE_%s%s_IPV6", strings.Replace(strings.ToUpper(iface.Name), ".", "_", -1), IPseq)
							ipv6++
						} else {
							fmt.Printf("Warning: Incorrect IP address '%s', skipping\n", ip.String())
							continue
						}
						key := fmt.Sprintf("$%s", strings.ToLower(varName))
						substitutions[key] = EnvVal{envName: varName, val: ip.String()}
						if defaultIfaceName == iface.Name && ip.To4() != nil {
							substitutions["$iface_default_ipv4"] = EnvVal{envName: "IFACE_DEFAULT_IPV4", val: ip.String()}
						}
						fmt.Printf("Found '%s' network interface with '%s' IP address\n", iface.Name, ip.String())
					}
				}
			}
		}
	}

	return &Environment{root, configRoot, workspace, sshKeyName, substitutions}
}

func (e *Environment) Workspace() string {
	return path.Join(e.root, e.workspace)
}

func (e *Environment) Root() string {
	return e.root
}

func (e *Environment) ConfigRoot() string {
	return e.configRoot
}

func (e *Environment) SSHKeyName() string {
	return e.sshKeyName
}

func (e *Environment) SetSSHKeyName(name string) {
	e.sshKeyName = name
}

// Apply goes through the map of substitutions and replaces all instances of
// the keys with their respective values. It supports escaping substitutions
// with a leading '\'.
func (e *Environment) Apply(data string) string {
	for key, val := range e.substitutions {
		matchKey := strings.Replace(key, `$`, `\$`, -1)
		replKey := strings.Replace(key, `$`, `$$`, -1)

		// "key" -> "val"
		data = regexp.MustCompile(`([^\\]|^)`+matchKey).ReplaceAllString(data, `${1}`+val.val)
		// "\key" -> "key"
		data = regexp.MustCompile(`\\`+matchKey).ReplaceAllString(data, replKey)
	}
	return data
}

func (e *Environment) DefaultEnvironmentFile() *system.EnvFile {
	ef := system.EnvFile{
		File: &system.File{File: config.File{
			Path: "/etc/environment",
		}},
		Vars: map[string]string{},
	}
	for _, val := range e.substitutions {
		if len(val.val) > 0 {
			ef.Vars[val.envName] = val.val
		}
	}
	if len(ef.Vars) == 0 {
		return nil
	} else {
		return &ef
	}
}

func getDefaultGatewayIfaceName() string {
	routes, err := netlink.NetworkGetRoutes()
	if err != nil {
		fmt.Printf("%v\n", err)
		return ""
	}
	for _, route := range routes {
		if route.Default {
			if route.Iface == nil {
				fmt.Printf("Warning: found default route but could not determine interface\n")
				return ""
			}
			return route.Iface.Name
		}
	}
	fmt.Printf("Warning: unable to find default route\n")
	return ""
}
