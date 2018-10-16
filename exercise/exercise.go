package exercise

import (
	"errors"
	"regexp"

	"github.com/aau-network-security/go-ntp/store"
	"github.com/aau-network-security/go-ntp/virtual"
	"github.com/aau-network-security/go-ntp/virtual/docker"
	"github.com/aau-network-security/go-ntp/virtual/vbox"
)

var (
	DuplicateTagErr = errors.New("Tag already exists")
	MissingTagsErr  = errors.New("No tags, need atleast one tag")
	UnknownTagErr   = errors.New("Unknown tag")

	tagRawRegexp = `^[a-z0-9][a-z0-9-]*[a-z0-9]$`
	tagRegex     = regexp.MustCompile(tagRawRegexp)
)

type DockerHost interface {
	CreateContainer(conf docker.ContainerConfig) (docker.Container, error)
}

type dockerHost struct{}

func (dockerHost) CreateContainer(conf docker.ContainerConfig) (docker.Container, error) {
	return docker.NewContainer(conf)
}

type exercise struct {
	conf       *store.Exercise
	net        *docker.Network
	flags      []store.Flag
	machines   []virtual.Instance
	ips        []int
	dnsIP      string
	dnsRecords []RecordConfig
	dockerHost DockerHost
	lib        vbox.Library
}

func (e *exercise) Create() error {
	containers, records := e.conf.ContainerOpts()

	var machines []virtual.Instance
	var newIps []int
	for i, spec := range containers {
		spec.DNS = []string{e.dnsIP}

		c, err := e.dockerHost.CreateContainer(spec)
		if err != nil {
			return err
		}

		var lastDigit int
		// Example: 216

		if e.ips != nil {
			// Containers need specific ips
			lastDigit, err = e.net.Connect(c, e.ips[i])
			if err != nil {
				return err
			}
		} else {
			// Let network assign ips
			lastDigit, err = e.net.Connect(c)
			if err != nil {
				return err
			}

			newIps = append(newIps, lastDigit)
		}

		ipaddr := e.net.FormatIP(lastDigit)
		// Example: 172.16.5.216

		for _, record := range records[i] {
			if record.RData == "" {
				record.RData = ipaddr
			}
			e.dnsRecords = append(e.dnsRecords, record)
		}

		machines = append(machines, c)
	}

	for _, spec := range e.conf.VBoxConfig {
		vm, err := e.lib.GetCopy(
			spec.Image,
			vbox.SetBridge(e.net.Interface()),
		)
		if err != nil {
			return err
		}
		machines = append(machines, vm)
	}

	if e.ips == nil {
		e.ips = newIps
	}

	e.machines = machines

	return nil
}

func (e *exercise) Start() error {
	for _, m := range e.machines {
		if err := m.Start(); err != nil {
			return err
		}
	}
	return nil
}

func (e *exercise) Stop() error {
	for _, m := range e.machines {
		if err := m.Stop(); err != nil {
			return err
		}
	}

	return nil
}

func (e *exercise) Close() error {
	for _, m := range e.machines {
		if err := m.Close(); err != nil {
			return err
		}
	}
	e.machines = nil
	return nil
}

func (e *exercise) Restart() error {
	if err := e.Stop(); err != nil {
		return err
	}

	if err := e.Start(); err != nil {
		return err
	}

	return nil
}

func (e *exercise) Reset() error {
	if err := e.Close(); err != nil {
		return err
	}

	if err := e.Create(); err != nil {
		return err
	}

	if err := e.Start(); err != nil {
		return err
	}

	return nil
}
