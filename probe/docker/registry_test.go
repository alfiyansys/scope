package docker_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"

	"github.com/weaveworks/common/mtime"
	"github.com/weaveworks/scope/probe/controls"
	"github.com/weaveworks/scope/probe/docker"
	"github.com/weaveworks/scope/report"
	"github.com/weaveworks/scope/test"
)

func testRegistry() docker.Registry {
	hr := controls.NewDefaultHandlerRegistry()
	registry, _ := docker.NewRegistry(docker.RegistryOptions{
		Interval:        10 * time.Second,
		CollectStats:    true,
		HandlerRegistry: hr,
	})
	return registry
}

type mockContainer struct {
	c *containertypes.InspectResponse
}

func (c *mockContainer) UpdateState(_ *containertypes.InspectResponse) {}

func (c *mockContainer) ID() string {
	return c.c.ID
}

func (c *mockContainer) PID() int {
	return c.c.State.Pid
}

func (c *mockContainer) Image() string {
	return c.c.Image
}

func (c *mockContainer) Hostname() string {
	return ""
}

func (c *mockContainer) State() string {
	return "Up 3 minutes"
}

func (c *mockContainer) StateString() string {
	return report.StateRunning
}

func (c *mockContainer) StartGatheringStats(docker.StatsGatherer) error {
	return nil
}

func (c *mockContainer) StopGatheringStats() {}

func (c *mockContainer) GetNode() report.Node {
	return report.MakeNodeWith(report.MakeContainerNodeID(c.c.ID), map[string]string{
		docker.ContainerID:   c.c.ID,
		docker.ContainerName: c.c.Name,
		docker.ImageID:       c.c.Image,
	}).WithParents(report.MakeSets().
		Add(report.ContainerImage, report.MakeStringSet(report.MakeContainerImageNodeID(c.c.Image))),
	)
}

func (c *mockContainer) NetworkMode() (string, bool) {
	return "", false
}
func (c *mockContainer) NetworkInfo([]net.IP) report.Sets {
	return report.MakeSets()
}

func (c *mockContainer) Container() *containertypes.InspectResponse {
	return c.c
}

func (c *mockContainer) HasTTY() bool { return true }

// notFoundError satisfies containerd/errdefs' notFound marker interface, so
// client.IsErrNotFound(err) recognizes it the same way it would a real
// Docker Engine 404 response.
type notFoundError struct{}

func (notFoundError) Error() string { return "no such container" }
func (notFoundError) NotFound()     {}

type mockDockerClient struct {
	sync.RWMutex
	apiContainers []containertypes.Summary
	containers    map[string]*containertypes.InspectResponse
	apiImages     []image.Summary
	networks      []network.Summary
	events        []chan events.Message
}

func (m *mockDockerClient) ContainerList(context.Context, containertypes.ListOptions) ([]containertypes.Summary, error) {
	m.RLock()
	defer m.RUnlock()
	return m.apiContainers, nil
}

func (m *mockDockerClient) ContainerInspect(_ context.Context, id string) (containertypes.InspectResponse, error) {
	m.RLock()
	defer m.RUnlock()
	c, ok := m.containers[id]
	if !ok {
		return containertypes.InspectResponse{}, notFoundError{}
	}
	return *c, nil
}

func (m *mockDockerClient) ImageList(context.Context, image.ListOptions) ([]image.Summary, error) {
	m.RLock()
	defer m.RUnlock()
	return m.apiImages, nil
}

func (m *mockDockerClient) NetworkList(context.Context, network.ListOptions) ([]network.Summary, error) {
	m.RLock()
	defer m.RUnlock()
	return m.networks, nil
}

func (m *mockDockerClient) Events(ctx context.Context, _ events.ListOptions) (<-chan events.Message, <-chan error) {
	ch := make(chan events.Message, 1024)
	errCh := make(chan error, 1)
	m.Lock()
	m.events = append(m.events, ch)
	m.Unlock()
	go func() {
		<-ctx.Done()
		m.Lock()
		defer m.Unlock()
		for i, c := range m.events {
			if c == ch {
				m.events = append(m.events[:i], m.events[i+1:]...)
				break
			}
		}
		close(ch)
	}()
	return ch, errCh
}

func (m *mockDockerClient) ContainerStart(context.Context, string, containertypes.StartOptions) error {
	return fmt.Errorf("started")
}

func (m *mockDockerClient) ContainerStop(context.Context, string, containertypes.StopOptions) error {
	return fmt.Errorf("stopped")
}

func (m *mockDockerClient) ContainerRestart(context.Context, string, containertypes.StopOptions) error {
	return fmt.Errorf("restarted")
}

func (m *mockDockerClient) ContainerPause(context.Context, string) error {
	return fmt.Errorf("paused")
}

func (m *mockDockerClient) ContainerUnpause(context.Context, string) error {
	return fmt.Errorf("unpaused")
}

func (m *mockDockerClient) ContainerRemove(context.Context, string, containertypes.RemoveOptions) error {
	return fmt.Errorf("remove")
}

func (m *mockDockerClient) ContainerStats(context.Context, string, bool) (containertypes.StatsResponseReader, error) {
	return containertypes.StatsResponseReader{}, fmt.Errorf("stats")
}

func (m *mockDockerClient) ContainerExecResize(context.Context, string, containertypes.ResizeOptions) error {
	return fmt.Errorf("resizeExecTTY")
}

// emptyHijackedResponse gives pumpHijackedStream a Reader it can safely call
// Read on (immediate EOF) instead of a nil *bufio.Reader, which would panic.
func emptyHijackedResponse() types.HijackedResponse {
	return types.HijackedResponse{Reader: bufio.NewReader(strings.NewReader(""))}
}

func (m *mockDockerClient) ContainerAttach(context.Context, string, containertypes.AttachOptions) (types.HijackedResponse, error) {
	return emptyHijackedResponse(), nil
}

func (m *mockDockerClient) ContainerExecCreate(context.Context, string, containertypes.ExecOptions) (containertypes.ExecCreateResponse, error) {
	return containertypes.ExecCreateResponse{ID: "id"}, nil
}

func (m *mockDockerClient) ContainerExecAttach(context.Context, string, containertypes.ExecAttachOptions) (types.HijackedResponse, error) {
	return emptyHijackedResponse(), nil
}

func (m *mockDockerClient) send(event events.Message) {
	m.RLock()
	defer m.RUnlock()
	for _, c := range m.events {
		c <- event
	}
}

var (
	startTime  = time.Date(2009, time.November, 10, 23, 0, 0, 0, time.UTC)
	container1 = &containertypes.InspectResponse{
		ContainerJSONBase: &containertypes.ContainerJSONBase{
			ID:    "ping",
			Name:  "pong",
			Image:   "baz",
			Path:    "ping",
			Created: time.Time{}.Format(time.RFC3339Nano),
			Args: []string{
				"foo.bar.local",
			},
			State: &containertypes.State{
				Status:    containertypes.StateRunning,
				Pid:       2,
				Running:   true,
				StartedAt: startTime.Format(time.RFC3339Nano),
			},
		},
		NetworkSettings: &containertypes.NetworkSettings{
			NetworkSettingsBase: containertypes.NetworkSettingsBase{
				Ports: map[nat.Port][]nat.PortBinding{
					nat.Port("80/tcp"): {
						{
							HostIP:   "1.2.3.4",
							HostPort: "80",
						},
					},
					nat.Port("81/tcp"): {},
				},
			},
			DefaultNetworkSettings: containertypes.DefaultNetworkSettings{
				IPAddress: "1.2.3.4",
			},
			Networks: map[string]*network.EndpointSettings{
				"network1": {
					IPAddress: "5.6.7.8",
				},
			},
		},
		Config: &containertypes.Config{
			Env: []string{
				"FOO=secret-bar",
			},
			Labels: map[string]string{
				"foo1": "bar1",
				"foo2": "bar2",
			},
		},
	}
	container2 = &containertypes.InspectResponse{
		ContainerJSONBase: &containertypes.ContainerJSONBase{
			ID:    "wiff",
			Name:  "waff",
			Image: "baz",
			State: &containertypes.State{Status: containertypes.StateRunning, Pid: 1, Running: true},
		},
		Config: &containertypes.Config{
			Labels: map[string]string{
				"foo1": "bar1",
				"foo2": "bar2",
			},
		},
	}
	renamedContainer = &containertypes.InspectResponse{
		ContainerJSONBase: &containertypes.ContainerJSONBase{
			ID:    "renamed",
			Name:  "renamed",
			Image: "baz",
			State: &containertypes.State{Status: containertypes.StateRunning, Pid: 1, Running: true},
		},
		Config: &containertypes.Config{
			Labels: map[string]string{
				"foo1": "bar1",
				"foo2": "bar2",
			},
		},
	}
	apiContainer1       = containertypes.Summary{ID: "ping"}
	apiContainer2       = containertypes.Summary{ID: "wiff"}
	renamedAPIContainer = containertypes.Summary{ID: "renamed"}
	apiImage1           = image.Summary{
		ID:       "baz",
		RepoTags: []string{"bang", "not-chosen"},
		Labels: map[string]string{
			"imgfoo1": "bar1",
			"imgfoo2": "bar2",
		},
	}
	network1 = network.Summary{
		ID:    "deadbeef",
		Name:  "network1",
		Scope: "local",
		IPAM: network.IPAM{
			Config: []network.IPAMConfig{{Subnet: "5.6.7.8/24"}},
		},
	}
)

func newMockClient() *mockDockerClient {
	return &mockDockerClient{
		apiContainers: []containertypes.Summary{apiContainer1},
		containers:    map[string]*containertypes.InspectResponse{"ping": container1},
		apiImages:     []image.Summary{apiImage1},
		networks:      []network.Summary{network1},
	}
}

func setupStubs(mdc *mockDockerClient, f func()) {
	oldDockerClient, oldNewContainer := docker.NewDockerClientStub, docker.NewContainerStub
	defer func() { docker.NewDockerClientStub, docker.NewContainerStub = oldDockerClient, oldNewContainer }()

	docker.NewDockerClientStub = func(endpoint string) (docker.Client, error) {
		return mdc, nil
	}

	docker.NewContainerStub = func(c *containertypes.InspectResponse, _ string, _ bool, _ bool) docker.Container {
		return &mockContainer{c}
	}

	f()
}

type containers []docker.Container

func (c containers) Len() int           { return len(c) }
func (c containers) Swap(i, j int)      { c[i], c[j] = c[j], c[i] }
func (c containers) Less(i, j int) bool { return c[i].ID() < c[j].ID() }

func allContainers(r docker.Registry) []docker.Container {
	result := []docker.Container{}
	r.WalkContainers(func(c docker.Container) {
		result = append(result, c)
	})
	sort.Sort(containers(result))
	return result
}

func allImages(r docker.Registry) []image.Summary {
	result := []image.Summary{}
	r.WalkImages(func(i image.Summary) {
		result = append(result, i)
	})
	return result
}

func allNetworks(r docker.Registry) []network.Summary {
	result := []network.Summary{}
	r.WalkNetworks(func(i network.Summary) {
		result = append(result, i)
	})
	return result
}

func TestRegistry(t *testing.T) {
	mdc := newMockClient()
	setupStubs(mdc, func() {
		registry := testRegistry()
		defer registry.Stop()
		runtime.Gosched()

		{
			want := []docker.Container{&mockContainer{container1}}
			test.Poll(t, 100*time.Millisecond, want, func() interface{} {
				return allContainers(registry)
			})
		}

		{
			want := []image.Summary{apiImage1}
			test.Poll(t, 100*time.Millisecond, want, func() interface{} {
				return allImages(registry)
			})
		}

		{
			want := []network.Summary{network1}
			test.Poll(t, 100*time.Millisecond, want, func() interface{} {
				return allNetworks(registry)
			})
		}

	})
}

func TestLookupByPID(t *testing.T) {
	mdc := newMockClient()
	setupStubs(mdc, func() {
		registry := testRegistry()
		defer registry.Stop()

		want := docker.Container(&mockContainer{container1})
		test.Poll(t, 100*time.Millisecond, want, func() interface{} {
			var have docker.Container
			registry.LockedPIDLookup(func(lookup func(int) docker.Container) {
				have = lookup(2)
			})
			return have
		})
	})
}

func TestRegistryEvents(t *testing.T) {
	mdc := newMockClient()
	setupStubs(mdc, func() {
		registry := testRegistry()
		defer registry.Stop()
		runtime.Gosched()

		check := func(want []docker.Container) {
			test.Poll(t, 100*time.Millisecond, want, func() interface{} {
				return allContainers(registry)
			})
		}

		{
			mdc.Lock()
			mdc.apiContainers = []containertypes.Summary{apiContainer1, apiContainer2}
			mdc.containers["wiff"] = container2
			mdc.Unlock()
			mdc.send(events.Message{Type: events.ContainerEventType, Action: events.ActionStart, Actor: events.Actor{ID: "wiff"}})
			runtime.Gosched()

			want := []docker.Container{&mockContainer{container1}, &mockContainer{container2}}
			check(want)
		}

		{
			mdc.Lock()
			mdc.apiContainers = []containertypes.Summary{apiContainer1}
			delete(mdc.containers, "wiff")
			mdc.Unlock()
			mdc.send(events.Message{Type: events.ContainerEventType, Action: events.ActionDestroy, Actor: events.Actor{ID: "wiff"}})
			runtime.Gosched()

			want := []docker.Container{&mockContainer{container1}}
			check(want)
		}

		{
			mdc.Lock()
			mdc.apiContainers = []containertypes.Summary{}
			delete(mdc.containers, "ping")
			mdc.Unlock()
			mdc.send(events.Message{Type: events.ContainerEventType, Action: events.ActionDie, Actor: events.Actor{ID: "ping"}})
			runtime.Gosched()

			want := []docker.Container{}
			check(want)
		}

		{
			mdc.send(events.Message{Type: events.ContainerEventType, Action: events.ActionDie, Actor: events.Actor{ID: "doesntexist"}})
			runtime.Gosched()

			want := []docker.Container{}
			check(want)
		}

		{
			mdc.Lock()
			mdc.apiContainers = []containertypes.Summary{renamedAPIContainer}
			mdc.containers[renamedContainer.ID] = renamedContainer
			mdc.Unlock()
			mdc.send(events.Message{Type: events.ContainerEventType, Action: events.ActionRename, Actor: events.Actor{ID: renamedContainer.ID}})
			runtime.Gosched()

			want := []docker.Container{&mockContainer{renamedContainer}}
			check(want)
		}
	})
}

func TestRegistryDelete(t *testing.T) {
	mtime.NowForce(mtime.Now())
	defer mtime.NowReset()

	mdc := newMockClient()
	setupStubs(mdc, func() {
		registry := testRegistry()
		defer registry.Stop()
		time.Sleep(time.Millisecond * 100) // Allow for goroutines to get started

		// Collect all the events.
		mtx := sync.Mutex{}
		nodes := []report.Node{}
		registry.WatchContainerUpdates(func(n report.Node) {
			mtx.Lock()
			defer mtx.Unlock()
			nodes = append(nodes, n)
		})

		check := func(want []docker.Container) {
			test.Poll(t, 100*time.Millisecond, want, func() interface{} {
				return allContainers(registry)
			})
		}

		want := []docker.Container{&mockContainer{container1}}
		check(want)

		{
			mdc.Lock()
			mdc.apiContainers = []containertypes.Summary{}
			delete(mdc.containers, "ping")
			mdc.Unlock()
			mdc.send(events.Message{Type: events.ContainerEventType, Action: events.ActionDestroy, Actor: events.Actor{ID: "ping"}})

			check([]docker.Container{})

			want := []report.Node{
				report.MakeNodeWith(report.MakeContainerNodeID("ping"), map[string]string{
					docker.ContainerID:    "ping",
					docker.ContainerState: "deleted",
				}),
			}
			test.Poll(t, 100*time.Millisecond, want, func() interface{} {
				mtx.Lock()
				nodesCopy := make([]report.Node, len(nodes))
				copy(nodesCopy, nodes)
				mtx.Unlock()
				return nodesCopy
			})
			mtx.Lock()
			nodes = []report.Node{}
			mtx.Unlock()
		}
	})
}
