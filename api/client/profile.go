/*
Copyright 2021 Gravitational, Inc.

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

package client

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/gravitational/trace"

	"gopkg.in/yaml.v2"
)

// CurrentProfileFilename is a file which stores the name of the
// currently active profile.
const CurrentProfileFilename = "current-profile"

// Profile is a collection of most frequently used CLI flags
// for "tsh".
//
// Profiles can be stored in a profile file, allowing TSH users to
// type fewer CLI args.
//
type Profile struct {
	// WebProxyAddr is the host:port the web proxy can be accessed at.
	WebProxyAddr string `yaml:"web_proxy_addr,omitempty"`

	// SSHProxyAddr is the host:port the SSH proxy can be accessed at.
	SSHProxyAddr string `yaml:"ssh_proxy_addr,omitempty"`

	// KubeProxyAddr is the host:port the Kubernetes proxy can be accessed at.
	KubeProxyAddr string `yaml:"kube_proxy_addr,omitempty"`

	// Username is the Teleport username for the client.
	Username string `yaml:"user,omitempty"`

	// AuthType (like "google")
	AuthType string `yaml:"auth_type,omitempty"`

	// SiteName is equivalient to --cluster argument
	SiteName string `yaml:"cluster,omitempty"`

	// ForwardedPorts is the list of ports to forward to the target node.
	ForwardedPorts []string `yaml:"forward_ports,omitempty"`

	// DynamicForwardedPorts is a list of ports to use for dynamic port
	// forwarding (SOCKS5).
	DynamicForwardedPorts []string `yaml:"dynamic_forward_ports,omitempty"`

	// Dir is the directory of this profile.
	Dir string
}

// Name returns the name of the profile.
func (cp *Profile) Name() string {
	addr, _, err := net.SplitHostPort(cp.WebProxyAddr)
	if err != nil {
		return cp.WebProxyAddr
	}

	return addr
}

// TLS attempts to load credentials from the specified tls certificates path.
func (cp *Profile) TLS() (*tls.Config, error) {
	credsPath := filepath.Join(cp.Dir, sessionKeyDir, cp.Name())

	certFile := filepath.Join(credsPath, cp.Username+fileExtTLSCert)
	keyFile := filepath.Join(credsPath, cp.Username)
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	caCertsFile := filepath.Join(credsPath, fileNameTLSCerts)
	caCerts, err := ioutil.ReadFile(caCertsFile)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCerts) {
		return nil, trace.BadParameter("invalid CA cert PEM")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
	}, nil
}

// SetCurrentProfileName attempts to set the current profile name.
func SetCurrentProfileName(dir string, name string) error {
	if dir == "" {
		return trace.BadParameter("cannot set current profile: missing dir")
	}

	path := filepath.Join(dir, CurrentProfileFilename)
	if err := ioutil.WriteFile(path, []byte(strings.TrimSpace(name)+"\n"), 0660); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// GetCurrentProfileName attempts to load the current profile name.
func GetCurrentProfileName(dir string) (name string, err error) {
	if dir == "" {
		return "", trace.BadParameter("cannot get current profile: missing dir")
	}

	data, err := ioutil.ReadFile(filepath.Join(dir, CurrentProfileFilename))
	if err != nil {
		if os.IsNotExist(err) {
			return "", trace.NotFound("current-profile is not set")
		}
		return "", trace.ConvertSystemError(err)
	}
	name = strings.TrimSpace(string(data))
	if name == "" {
		return "", trace.NotFound("current-profile is not set")
	}
	return name, nil
}

// ListProfileNames lists all available profiles.
func ListProfileNames(dir string) ([]string, error) {
	if dir == "" {
		return nil, trace.BadParameter("cannot list profiles: missing dir")
	}
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	var names []string
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		if file.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !strings.HasSuffix(file.Name(), ".yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(file.Name(), ".yaml"))
	}
	return names, nil
}

// FullProfilePath returns the full path to the user profile directory.
// If the parameter is empty, it returns expanded "~/.tsh", otherwise
// returns its unmodified parameter
func FullProfilePath(dir string) string {
	if dir != "" {
		return dir
	}
	return defaultProfilePath()
}

// defaultProfilePath retrieves the default path the the .tsh profile.
func defaultProfilePath() string {
	var dirPath string
	if u, err := user.Current(); err != nil {
		dirPath = os.TempDir()
	} else {
		dirPath = u.HomeDir
	}
	return filepath.Join(dirPath, ProfileDir)
}

// ProfileFromDir reads the user (yaml) profile from a given directory. If
// name is empty, this function defaults to loading the currently active
// profile (if any).
func ProfileFromDir(dir string, name string) (*Profile, error) {
	if dir == "" {
		return nil, trace.BadParameter("cannot load profile: missing dir")
	}
	var err error
	if name == "" {
		name, err = GetCurrentProfileName(dir)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	cp, err := profileFromFile(filepath.Join(dir, name+".yaml"))
	if err != nil {
		return nil, trace.Wrap(err)
	}
	cp.Dir = dir
	return cp, nil
}

// profileFromFile loads the profile from a YAML file.
func profileFromFile(filePath string) (*Profile, error) {
	bytes, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, trace.ConvertSystemError(err)
	}
	var cp *Profile
	if err := yaml.Unmarshal(bytes, &cp); err != nil {
		return nil, trace.Wrap(err)
	}
	return cp, nil
}

// SaveToDir saves Profile to the target directory.
// if makeCurrent is true, attempt to make it the current profile.
func (cp *Profile) SaveToDir(dir string, makeCurrent bool) error {
	if dir == "" {
		return trace.BadParameter("cannot save profile: missing dir")
	}
	if err := cp.SaveToFile(filepath.Join(dir, cp.Name()+".yaml")); err != nil {
		return trace.Wrap(err)
	}
	if makeCurrent {
		return trace.Wrap(SetCurrentProfileName(dir, cp.Name()))
	}
	return nil
}

// SaveToFile saves Profile to the target file.
func (cp *Profile) SaveToFile(filepath string) error {
	bytes, err := yaml.Marshal(&cp)
	if err != nil {
		return trace.Wrap(err)
	}
	if err = ioutil.WriteFile(filepath, bytes, 0660); err != nil {
		return trace.Wrap(err)
	}
	return nil
}
