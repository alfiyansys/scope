package docker

import (
	"context"
	"sync"
	"time"

	"github.com/armon/go-radix"
	"github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	log "github.com/sirupsen/logrus"

	"github.com/weaveworks/scope/probe/controls"
	"github.com/weaveworks/scope/report"
)

// Consts exported for testing.
const (
	CreateEvent            = "create"
	DestroyEvent           = "destroy"
	RenameEvent            = "rename"
	StartEvent             = "start"
	DieEvent               = "die"
	PauseEvent             = "pause"
	UnpauseEvent           = "unpause"
	NetworkConnectEvent    = "network:connect"
	NetworkDisconnectEvent = "network:disconnect"
)

// Vars exported for testing.
var (
	NewDockerClientStub = newDockerClient
	NewContainerStub    = NewContainer
)

// Registry keeps track of running docker containers and their images
type Registry interface {
	Stop()
	LockedPIDLookup(f func(func(int) Container))
	WalkContainers(f func(Container))
	WalkImages(f func(image.Summary))
	WalkNetworks(f func(network.Summary))
	WatchContainerUpdates(ContainerUpdateWatcher)
	GetContainer(string) (Container, bool)
	GetContainerByPrefix(string) (Container, bool)
	GetContainerImage(string) (image.Summary, bool)
}

// ContainerUpdateWatcher is the type of functions that get called when containers are updated.
type ContainerUpdateWatcher func(report.Node)

type registry struct {
	sync.RWMutex
	quit                   chan chan struct{}
	interval               time.Duration
	collectStats           bool
	client                 Client
	pipes                  controls.PipeClient
	hostID                 string
	handlerRegistry        *controls.HandlerRegistry
	noCommandLineArguments bool
	noEnvironmentVariables bool

	watchers        []ContainerUpdateWatcher
	containers      *radix.Tree
	containersByPID map[int]Container
	images          map[string]image.Summary
	networks        []network.Summary
	pipeIDToexecID  map[string]string
}

// Client is the little bit of the official Docker SDK client we need.
// Modeled directly on github.com/docker/docker/client.APIClient so a real
// *dockerclient.Client satisfies it without an adapter.
type Client interface {
	ContainerList(ctx context.Context, options containertypes.ListOptions) ([]containertypes.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (containertypes.InspectResponse, error)
	ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
	NetworkList(ctx context.Context, options network.ListOptions) ([]network.Summary, error)
	Events(ctx context.Context, options events.ListOptions) (<-chan events.Message, <-chan error)

	ContainerStop(ctx context.Context, containerID string, options containertypes.StopOptions) error
	ContainerStart(ctx context.Context, containerID string, options containertypes.StartOptions) error
	ContainerRestart(ctx context.Context, containerID string, options containertypes.StopOptions) error
	ContainerPause(ctx context.Context, containerID string) error
	ContainerUnpause(ctx context.Context, containerID string) error
	ContainerRemove(ctx context.Context, containerID string, options containertypes.RemoveOptions) error
	ContainerAttach(ctx context.Context, containerID string, options containertypes.AttachOptions) (types.HijackedResponse, error)
	ContainerExecCreate(ctx context.Context, containerID string, options containertypes.ExecOptions) (containertypes.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, options containertypes.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecResize(ctx context.Context, execID string, options containertypes.ResizeOptions) error
	ContainerStats(ctx context.Context, containerID string, stream bool) (containertypes.StatsResponseReader, error)
}

func newDockerClient(endpoint string) (Client, error) {
	opts := []dockerclient.Opt{dockerclient.WithAPIVersionNegotiation()}
	if endpoint == "" {
		opts = append(opts, dockerclient.FromEnv)
	} else {
		opts = append(opts, dockerclient.WithHost(endpoint))
	}
	return dockerclient.NewClientWithOpts(opts...)
}

// RegistryOptions are used to initialize the Registry
type RegistryOptions struct {
	Interval               time.Duration
	Pipes                  controls.PipeClient
	CollectStats           bool
	HostID                 string
	HandlerRegistry        *controls.HandlerRegistry
	DockerEndpoint         string
	NoCommandLineArguments bool
	NoEnvironmentVariables bool
}

// NewRegistry returns a usable Registry. Don't forget to Stop it.
func NewRegistry(options RegistryOptions) (Registry, error) {
	client, err := NewDockerClientStub(options.DockerEndpoint)
	if err != nil {
		return nil, err
	}

	r := &registry{
		containers:      radix.New(),
		containersByPID: map[int]Container{},
		images:          map[string]image.Summary{},
		pipeIDToexecID:  map[string]string{},

		client:                 client,
		pipes:                  options.Pipes,
		interval:               options.Interval,
		collectStats:           options.CollectStats,
		hostID:                 options.HostID,
		handlerRegistry:        options.HandlerRegistry,
		quit:                   make(chan chan struct{}),
		noCommandLineArguments: options.NoCommandLineArguments,
		noEnvironmentVariables: options.NoEnvironmentVariables,
	}

	r.registerControls()
	go r.loop()
	return r, nil
}

// Stop stops the Docker registry's event subscriber.
func (r *registry) Stop() {
	r.deregisterControls()
	ch := make(chan struct{})
	r.quit <- ch
	<-ch
}

// WatchContainerUpdates registers a callback to be called
// whenever a container is updated.
func (r *registry) WatchContainerUpdates(f ContainerUpdateWatcher) {
	r.Lock()
	defer r.Unlock()
	r.watchers = append(r.watchers, f)
}

func (r *registry) loop() {
	for {
		// NB listenForEvents blocks.
		// Returning false means we should exit.
		if !r.listenForEvents() {
			return
		}

		// Sleep here so we don't hammer the
		// logs if docker is down
		time.Sleep(r.interval)
	}
}

func (r *registry) listenForEvents() bool {
	// First we empty the store lists.
	// This ensure any containers that went away in between calls to
	// listenForEvents don't hang around.
	r.reset()

	// Next, start listening for events.  We do this before fetching
	// the list of containers so we don't miss containers created
	// after listing but before listening for events.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eventCh, errCh := r.client.Events(ctx, events.ListOptions{})

	if err := r.updateContainers(ctx); err != nil {
		log.Errorf("docker registry: %s", err)
		return true
	}

	if err := r.updateImages(ctx); err != nil {
		log.Errorf("docker registry: %s", err)
		return true
	}

	if err := r.updateNetworks(ctx); err != nil {
		log.Errorf("docker registry: %s", err)
		return true
	}

	otherUpdates := time.Tick(r.interval)
	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				log.Errorf("docker registry: event listener unexpectedly disconnected")
				return true
			}
			r.handleEvent(ctx, event)

		case err, ok := <-errCh:
			if !ok || err == nil {
				continue
			}
			log.Errorf("docker registry: event stream error: %s", err)
			return true

		case <-otherUpdates:
			if err := r.updateImages(ctx); err != nil {
				log.Errorf("docker registry: %s", err)
				return true
			}
			if err := r.updateNetworks(ctx); err != nil {
				log.Errorf("docker registry: %s", err)
				return true
			}

		case ch := <-r.quit:
			r.Lock()
			defer r.Unlock()

			if r.collectStats {
				r.containers.Walk(func(_ string, c interface{}) bool {
					c.(Container).StopGatheringStats()
					return false
				})
			}
			close(ch)
			return false
		}
	}
}

func (r *registry) reset() {
	r.Lock()
	defer r.Unlock()

	if r.collectStats {
		r.containers.Walk(func(_ string, c interface{}) bool {
			c.(Container).StopGatheringStats()
			return false
		})
	}

	r.containers = radix.New()
	r.containersByPID = map[int]Container{}
	r.images = map[string]image.Summary{}
	r.networks = r.networks[:0]
}

func (r *registry) updateContainers(ctx context.Context) error {
	apiContainers, err := r.client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return err
	}

	for _, apiContainer := range apiContainers {
		r.updateContainerState(ctx, apiContainer.ID)
	}

	return nil
}

func (r *registry) updateImages(ctx context.Context) error {
	images, err := r.client.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return err
	}

	r.Lock()
	defer r.Unlock()

	for _, img := range images {
		r.images[trimImageID(img.ID)] = img
	}

	return nil
}

func (r *registry) updateNetworks(ctx context.Context) error {
	networks, err := r.client.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return err
	}

	r.Lock()
	r.networks = networks
	r.Unlock()

	return nil
}

func (r *registry) handleEvent(ctx context.Context, event events.Message) {
	// TODO: Send shortcut reports on networks being created/destroyed?
	action := string(event.Action)
	if event.Type == events.NetworkEventType {
		action = "network:" + action
	}
	switch action {
	case CreateEvent, RenameEvent, StartEvent, DieEvent, PauseEvent, UnpauseEvent, NetworkConnectEvent, NetworkDisconnectEvent:
		r.updateContainerState(ctx, event.Actor.ID)
	case DestroyEvent:
		r.Lock()
		r.deleteContainer(event.Actor.ID)
		r.Unlock()
		r.sendDeletedUpdate(event.Actor.ID)
	}
}

func (r *registry) updateContainerState(ctx context.Context, containerID string) {
	r.Lock()
	defer r.Unlock()

	dockerContainer, err := r.client.ContainerInspect(ctx, containerID)
	if err != nil {
		if dockerclient.IsErrNotFound(err) {
			// Docker says the container doesn't exist - remove it from our data
			r.deleteContainer(containerID)
			return
		}
		log.Errorf("Unable to get status for container %s: %v", containerID, err)
		return
	}

	// Container exists, ensure we have it
	o, ok := r.containers.Get(containerID)
	var c Container
	if !ok {
		c = NewContainerStub(&dockerContainer, r.hostID, r.noCommandLineArguments, r.noEnvironmentVariables)
		r.containers.Insert(containerID, c)
	} else {
		c = o.(Container)
		// potentially remove existing pid mapping.
		delete(r.containersByPID, c.PID())
		c.UpdateState(&dockerContainer)
	}

	// Update PID index
	if c.PID() > 1 {
		r.containersByPID[c.PID()] = c
	}

	// Trigger anyone watching for updates
	node := c.GetNode()
	for _, f := range r.watchers {
		f(node)
	}

	// And finally, ensure we gather stats for it
	if r.collectStats {
		if dockerContainer.State != nil && dockerContainer.State.Running {
			if err := c.StartGatheringStats(r.client); err != nil {
				log.Errorf("Error gathering stats for container %s: %s", containerID, err)
				return
			}
		} else {
			c.StopGatheringStats()
		}
	}
}

func (r *registry) deleteContainer(containerID string) {
	// Container doesn't exist anymore, so lets stop and remove it
	c, ok := r.containers.Get(containerID)
	if !ok {
		return
	}
	container := c.(Container)

	r.containers.Delete(containerID)
	delete(r.containersByPID, container.PID())
	if r.collectStats {
		container.StopGatheringStats()
	}
}

func (r *registry) sendDeletedUpdate(containerID string) {
	node := report.MakeNodeWith(report.MakeContainerNodeID(containerID), map[string]string{
		ContainerID:    containerID,
		ContainerState: report.StateDeleted,
	})
	// Trigger anyone watching for updates
	for _, f := range r.watchers {
		f(node)
	}
}

// LockedPIDLookup runs f under a read lock, and gives f a function for
// use doing pid->container lookups.
func (r *registry) LockedPIDLookup(f func(func(int) Container)) {
	r.RLock()
	defer r.RUnlock()

	lookup := func(pid int) Container {
		return r.containersByPID[pid]
	}

	f(lookup)
}

// WalkContainers runs f on every running containers the registry knows of.
func (r *registry) WalkContainers(f func(Container)) {
	r.RLock()
	defer r.RUnlock()

	r.containers.Walk(func(_ string, c interface{}) bool {
		f(c.(Container))
		return false
	})
}

func (r *registry) GetContainer(id string) (Container, bool) {
	r.RLock()
	defer r.RUnlock()
	c, ok := r.containers.Get(id)
	if ok {
		return c.(Container), true
	}
	return nil, false
}

func (r *registry) GetContainerByPrefix(prefix string) (Container, bool) {
	r.RLock()
	defer r.RUnlock()
	out := []interface{}{}
	r.containers.WalkPrefix(prefix, func(_ string, v interface{}) bool {
		out = append(out, v)
		return false
	})
	if len(out) == 1 {
		return out[0].(Container), true
	}
	return nil, false
}

func (r *registry) GetContainerImage(id string) (image.Summary, bool) {
	r.RLock()
	defer r.RUnlock()
	img, ok := r.images[id]
	return img, ok
}

// WalkImages runs f on every image of running containers the registry
// knows of.  f may be run on the same image more than once.
func (r *registry) WalkImages(f func(image.Summary)) {
	r.RLock()
	defer r.RUnlock()

	// Loop over containers so we only emit images for running containers.
	r.containers.Walk(func(_ string, c interface{}) bool {
		img, ok := r.images[c.(Container).Image()]
		if ok {
			f(img)
		}
		return false
	})
}

// WalkNetworks runs f on every network the registry knows of.
func (r *registry) WalkNetworks(f func(network.Summary)) {
	r.RLock()
	defer r.RUnlock()

	for _, n := range r.networks {
		f(n)
	}
}
