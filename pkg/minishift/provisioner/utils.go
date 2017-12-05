/*
Copyright (C) 2016 Red Hat, Inc.

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

// This include functions and variable used by provisioner
package provisioner

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/docker/machine/libmachine/auth"
	"github.com/docker/machine/libmachine/cert"
	"github.com/docker/machine/libmachine/engine"
	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/mcnutils"
	"github.com/docker/machine/libmachine/provision"
	"github.com/docker/machine/libmachine/provision/serviceaction"
	"github.com/minishift/minishift/pkg/minikube/assets"
	"github.com/minishift/minishift/pkg/minikube/sshutil"
	minishiftConfig "github.com/minishift/minishift/pkg/minishift/config"
	"github.com/pkg/errors"
)

var (
	engineConfigTemplateRHEL = `[Service]
ExecStart=
ExecStart=/usr/bin/dockerd-current -H tcp://0.0.0.0:{{.DockerPort}} -H unix:///var/run/docker.sock \
           --authorization-plugin rhel-push-plugin \
           --selinux-enabled \
           --log-driver=journald \
           --add-runtime docker-runc=/usr/libexec/docker/docker-runc-current \
           --default-runtime=docker-runc \
           --exec-opt native.cgroupdriver=systemd \
           --userland-proxy-path=/usr/libexec/docker/docker-proxy-current \
           --add-registry registry.access.redhat.com \
           --storage-driver {{.EngineOptions.StorageDriver}} --tlsverify --tlscacert {{.AuthOptions.CaCertRemotePath}} \
           --tlscert {{.AuthOptions.ServerCertRemotePath}} --tlskey {{.AuthOptions.ServerKeyRemotePath}} \
           {{ range .EngineOptions.Labels }}--label {{.}} {{ end }}{{ range .EngineOptions.InsecureRegistry }}--insecure-registry {{.}} {{ end }}{{ range .EngineOptions.RegistryMirror }}--registry-mirror {{.}} {{ end }}{{ range .EngineOptions.ArbitraryFlags }}--{{.}} {{ end }}
Environment={{range .EngineOptions.Env}}{{ printf "%q" . }} {{end}}
`
	engineConfigTemplateCentOS = `[Service]
ExecStart=
ExecStart=/usr/bin/dockerd-current -H tcp://0.0.0.0:{{.DockerPort}} -H unix:///var/run/docker.sock \
           --selinux-enabled \
           --log-driver=journald \
           --signature-verification=false \
           --add-runtime docker-runc=/usr/libexec/docker/docker-runc-current \
           --default-runtime=docker-runc \
           --exec-opt native.cgroupdriver=systemd \
           --userland-proxy-path=/usr/libexec/docker/docker-proxy-current \
           --storage-driver {{.EngineOptions.StorageDriver}} --tlsverify --tlscacert {{.AuthOptions.CaCertRemotePath}} \
           --tlscert {{.AuthOptions.ServerCertRemotePath}} --tlskey {{.AuthOptions.ServerKeyRemotePath}} \
           {{ range .EngineOptions.Labels }}--label {{.}} {{ end }}{{ range .EngineOptions.InsecureRegistry }}--insecure-registry {{.}} {{ end }}{{ range .EngineOptions.RegistryMirror }}--registry-mirror {{.}} {{ end }}{{ range .EngineOptions.ArbitraryFlags }}--{{.}} {{ end }}
Environment={{range .EngineOptions.Env}}{{ printf "%q" . }} {{end}}
`
	engineConfigTemplateBuildRoot = `[Unit]
Description=Docker Application Container Engine
Documentation=https://docs.docker.com
After=network.target docker.socket
Requires=docker.socket

[Service]
Type=notify

# DOCKER_RAMDISK disables pivot_root in Docker, using MS_MOVE instead.
Environment=DOCKER_RAMDISK=yes
{{range .EngineOptions.Env}}Environment={{.}}
{{end}}

# This file is a systemd drop-in unit that inherits from the base dockerd configuration.
# The base configuration already specifies an 'ExecStart=...' command. The first directive
# here is to clear out that command inherited from the base configuration. Without this,
# the command from the base configuration and the command specified here are treated as
# a sequence of commands, which is not the desired behavior, nor is it valid -- systemd
# will catch this invalid input and refuse to start the service with an error like:
#  Service has more than one ExecStart= setting, which is only allowed for Type=oneshot services.
ExecStart=
ExecStart=/usr/bin/docker daemon -H tcp://0.0.0.0:{{.DockerPort}} -H unix:///var/run/docker.sock --tlsverify --tlscacert {{.AuthOptions.CaCertRemotePath}} --tlscert {{.AuthOptions.ServerCertRemotePath}} --tlskey {{.AuthOptions.ServerKeyRemotePath}} {{ range .EngineOptions.Labels }}--label {{.}} {{ end }}{{ range .EngineOptions.InsecureRegistry }}--insecure-registry {{.}} {{ end }}{{ range .EngineOptions.RegistryMirror }}--registry-mirror {{.}} {{ end }}{{ range .EngineOptions.ArbitraryFlags }}--{{.}} {{ end }}
ExecReload=/bin/kill -s HUP $MAINPID

# Having non-zero Limit*s causes performance problems due to accounting overhead
# in the kernel. We recommend using cgroups to do container-local accounting.
LimitNOFILE=infinity
LimitNPROC=infinity
LimitCORE=infinity

# Uncomment TasksMax if your systemd version supports it.
# Only systemd 226 and above support this version.
TasksMax=infinity
TimeoutStartSec=0

# set delegate yes so that systemd does not reset the cgroups of docker containers
Delegate=yes

# kill only the docker process, not all processes in the cgroup
KillMode=process

[Install]
WantedBy=multi-user.target`
)

func makeDockerOptionsDir(p provision.Provisioner) error {
	dockerDir := p.GetDockerOptionsDir()
	if _, err := p.SSHCommand(fmt.Sprintf("sudo mkdir -p %s", dockerDir)); err != nil {
		return err
	}

	return nil
}

func setRemoteAuthOptions(p provision.Provisioner) auth.Options {
	dockerDir := p.GetDockerOptionsDir()
	authOptions := p.GetAuthOptions()

	// due to windows clients, we cannot use filepath.Join as the paths
	// will be mucked on the linux hosts
	authOptions.CaCertRemotePath = path.Join(dockerDir, "ca.pem")
	authOptions.ServerCertRemotePath = path.Join(dockerDir, "server.pem")
	authOptions.ServerKeyRemotePath = path.Join(dockerDir, "server-key.pem")

	return authOptions
}

func decideStorageDriver(p provision.Provisioner, defaultDriver, suppliedDriver string) (string, error) {
	if suppliedDriver != "" {
		return suppliedDriver, nil
	}
	bestSuitedDriver := ""

	defer func() {
		if bestSuitedDriver != "" {
			log.Debugf("No storage driver specified, instead using %s\n", bestSuitedDriver)
		}
	}()

	if defaultDriver != "aufs" {
		bestSuitedDriver = defaultDriver
	} else {
		remoteFilesystemType, err := getFilesystemType(p, "/var/lib")
		if err != nil {
			return "", err
		}
		if remoteFilesystemType == "btrfs" {
			bestSuitedDriver = "btrfs"
		} else {
			bestSuitedDriver = "aufs"
		}
	}
	return bestSuitedDriver, nil

}

func getFilesystemType(p provision.Provisioner, directory string) (string, error) {
	statCommandOutput, err := p.SSHCommand("stat -f -c %T " + directory)
	if err != nil {
		err = fmt.Errorf("Error looking up file system type: %s", err)
		return "", err
	}

	fstype := strings.TrimSpace(statCommandOutput)
	return fstype, nil
}

func configureAuth(p *BuildrootProvisioner) error {
	driver := p.GetDriver()
	machineName := driver.GetMachineName()
	authOptions := p.GetAuthOptions()
	org := mcnutils.GetUsername() + "." + machineName
	bits := 2048

	ip, err := driver.GetIP()
	if err != nil {
		return errors.Wrap(err, "error getting ip during provisioning")
	}

	hostCerts := map[string]string{
		authOptions.CaCertPath:     filepath.Join(authOptions.StorePath, "ca.pem"),
		authOptions.ClientCertPath: filepath.Join(authOptions.StorePath, "cert.pem"),
		authOptions.ClientKeyPath:  filepath.Join(authOptions.StorePath, "key.pem"),
	}

	for src, dst := range hostCerts {
		f, err := assets.NewFileAsset(src, filepath.Dir(dst), filepath.Base(dst), "0777")
		if err != nil {
			return errors.Wrapf(err, "open cert file: %s", src)
		}
		if err := assets.CopyFileLocal(f); err != nil {
			return errors.Wrapf(err, "transferring file: %+v", f)
		}
	}

	// The Host IP is always added to the certificate's SANs list
	hosts := append(authOptions.ServerCertSANs, ip, "localhost")
	log.Debugf("generating server cert: %s ca-key=%s private-key=%s org=%s san=%s",
		authOptions.ServerCertPath,
		authOptions.CaCertPath,
		authOptions.CaPrivateKeyPath,
		org,
		hosts,
	)

	err = cert.GenerateCert(&cert.Options{
		Hosts:     hosts,
		CertFile:  authOptions.ServerCertPath,
		KeyFile:   authOptions.ServerKeyPath,
		CAFile:    authOptions.CaCertPath,
		CAKeyFile: authOptions.CaPrivateKeyPath,
		Org:       org,
		Bits:      bits,
	})

	if err != nil {
		return fmt.Errorf("error generating server cert: %s", err)
	}

	remoteCerts := map[string]string{
		authOptions.CaCertPath:     authOptions.CaCertRemotePath,
		authOptions.ServerCertPath: authOptions.ServerCertRemotePath,
		authOptions.ServerKeyPath:  authOptions.ServerKeyRemotePath,
	}

	sshClient, err := sshutil.NewSSHClient(driver)
	if err != nil {
		return errors.Wrap(err, "provisioning: error getting ssh client")
	}

	for src, dst := range remoteCerts {
		f, err := assets.NewFileAsset(src, filepath.Dir(dst), filepath.Base(dst), "0640")
		if err != nil {
			return errors.Wrapf(err, "error copying %s to %s", src, dst)
		}
		if err := sshutil.TransferFile(f, sshClient); err != nil {
			return errors.Wrapf(err, "transfering file to machine %v", f)
		}
	}

	dockerCfg, err := p.GenerateDockerOptions(engine.DefaultPort)
	if err != nil {
		return errors.Wrap(err, "generating docker options")
	}

	log.Info("Setting Docker configuration on the remote daemon...")

	if _, err = p.SSHCommand(fmt.Sprintf("sudo mkdir -p %s && printf %%s \"%s\" | sudo tee %s", path.Dir(dockerCfg.EngineOptionsPath), dockerCfg.EngineOptions, dockerCfg.EngineOptionsPath)); err != nil {
		return err
	}

	if err := p.Service("docker", serviceaction.Restart); err != nil {
		return err
	}

	return nil
}

func doFeatureDetection(p provision.Provisioner) error {
	if out, err := p.SSHCommand(fmt.Sprintf("test -f %s && echo '1' || echo '0' ", "/usr/local/bin/minishift-set-ipaddress")); err != nil {
		return err
	} else {
		if strings.Trim(out, "\n") == "1" {
			minishiftConfig.InstanceConfig.SupportsNetworkAssignment = true
		} else {
			minishiftConfig.InstanceConfig.SupportsNetworkAssignment = false
		}
	}

	minishiftConfig.InstanceConfig.Write()

	return nil
}
