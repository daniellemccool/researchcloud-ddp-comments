package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ExecCall is one recorded Exec invocation with its full argv, env and stdin.
type ExecCall struct {
	Container string
	Cmd, Env  []string
	Stdin     []byte
}

// Fake is an in-memory Client for other packages' tests. It records every
// call in Calls (before any injected failure is applied), can fail selected
// calls through Fail, and hands out canned Exec results through ExecResults.
// All state is guarded by a mutex, so tests may drive it from goroutines.
type Fake struct {
	Calls       []string                 // one entry per method call, carrying names not ids
	Fail        map[string]error         // key "<Method> <name>"; when present the method returns it and does nothing
	ExecResults map[string]Exec          // key cmd[0]+" "+cmd[1]; missing key -> Exec{ExitCode: 0}
	Execs       []ExecCall               // every Exec call in order, with its full argv
	Created     []ContainerSpec          // every created spec, in creation order
	Specs       map[string]ContainerSpec // created specs by name, removed on ContainerRemove

	mu         sync.Mutex
	networks   map[string]*fakeNetwork
	containers map[string]*fakeContainer
	netOrder   []string // network names in creation order
	ctrOrder   []string // container names in creation order
	nextSubnet int      // next subnet index, subnets are 10.200.<n>.0/24 from n=1
}

type fakeNetwork struct {
	id, name, subnet string
	labels           map[string]string
	options          map[string]string
	attached         map[string]bool // container names
	nextHost         int             // next host index K in <prefix>.K, starts at 2
}

type fakeContainer struct {
	id, name, image string
	running         bool
	exitCode        int
	exitSet         bool // SetExited pinned the exit code
	labels          map[string]string
	networks        map[string]string // network name -> IP
}

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{
		Fail:        map[string]error{},
		ExecResults: map[string]Exec{},
		Specs:       map[string]ContainerSpec{},
		networks:    map[string]*fakeNetwork{},
		containers:  map[string]*fakeContainer{},
		nextSubnet:  1,
	}
}

// record appends the call string and returns the injected failure, if any.
// Every method calls it before touching state, so failed calls are recorded.
func (f *Fake) record(call, failKey string) error {
	f.Calls = append(f.Calls, call)
	if err, ok := f.Fail[failKey]; ok {
		return err
	}
	return nil
}

// SetExited marks a container not running with the given exit code. Like the
// daemon, it releases the container's address on every attached network.
func (f *Fake) SetExited(name string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[name]; ok {
		c.running = false
		c.exitCode = code
		c.exitSet = true
		for netName := range c.networks {
			c.networks[netName] = ""
		}
	}
}

// MarkRunning marks a container running, as if started: it assigns the next
// address on every attached network, exactly as ContainerStart does.
func (f *Fake) MarkRunning(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[name]; ok {
		c.running = true
		for netName := range c.networks {
			f.assignIP(f.networks[netName], c)
		}
	}
}

func copyLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// copyOptions copies driver options like copyLabels, but keeps nil nil (the
// daemon reports no options as a nil map, not an empty one).
func copyOptions(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	return copyLabels(in)
}

// matchLabels reports whether have carries every wanted label.
func matchLabels(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// subnetPrefix turns "10.200.1.0/24" into "10.200.1".
func subnetPrefix(subnet string) string {
	prefix := strings.SplitN(subnet, "/", 2)[0]
	return strings.TrimSuffix(prefix, ".0")
}

func (f *Fake) networkView(n *fakeNetwork) Network {
	return Network{ID: n.id, Name: n.name, Subnet: n.subnet, Labels: copyLabels(n.labels), Options: copyOptions(n.options)}
}

func (f *Fake) containerView(c *fakeContainer) Container {
	out := Container{
		ID:       c.id,
		Name:     c.name,
		Image:    c.image,
		Running:  c.running,
		ExitCode: c.exitCode,
		Labels:   copyLabels(c.labels),
		IPs:      map[string]string{},
	}
	for netName, ip := range c.networks {
		out.Networks = append(out.Networks, netName)
		if ip != "" {
			out.IPs[netName] = ip
		}
	}
	sort.Strings(out.Networks)
	return out
}

// attach adds the container to the network without an address, matching the
// daemon: the address is assigned when the container starts (or when a running
// container is connected) and cleared on stop/remove.
func (f *Fake) attach(n *fakeNetwork, c *fakeContainer) {
	n.attached[c.name] = true
	c.networks[n.name] = ""
}

// assignIP gives the container the next address on the network. K starts at 2
// and is never reused within a fake.
func (f *Fake) assignIP(n *fakeNetwork, c *fakeContainer) {
	if c.networks[n.name] != "" {
		return
	}
	c.networks[n.name] = fmt.Sprintf("%s.%d", subnetPrefix(n.subnet), n.nextHost)
	n.nextHost++
}

func (f *Fake) NetworkCreate(ctx context.Context, name string, labels, opts map[string]string) (Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("NetworkCreate "+name, "NetworkCreate "+name); err != nil {
		return Network{}, err
	}
	if _, ok := f.networks[name]; ok {
		return Network{}, fmt.Errorf("%w: network %s already exists", ErrConflict, name)
	}
	n := &fakeNetwork{
		id:       fmt.Sprintf("fake-net-%d", f.nextSubnet),
		name:     name,
		subnet:   fmt.Sprintf("10.200.%d.0/24", f.nextSubnet),
		labels:   copyLabels(labels),
		options:  copyOptions(opts),
		attached: map[string]bool{},
		nextHost: 2,
	}
	f.nextSubnet++
	f.networks[name] = n
	f.netOrder = append(f.netOrder, name)
	return f.networkView(n), nil
}

func (f *Fake) NetworkInspect(ctx context.Context, name string) (Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("NetworkInspect "+name, "NetworkInspect "+name); err != nil {
		return Network{}, err
	}
	n, ok := f.networks[name]
	if !ok {
		return Network{}, fmt.Errorf("%w: network %s", ErrNotFound, name)
	}
	return f.networkView(n), nil
}

func (f *Fake) NetworkList(ctx context.Context, labels map[string]string) ([]Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("NetworkList", "NetworkList"); err != nil {
		return nil, err
	}
	var out []Network
	for _, name := range f.netOrder {
		n := f.networks[name]
		if matchLabels(n.labels, labels) {
			out = append(out, f.networkView(n))
		}
	}
	return out, nil
}

func (f *Fake) NetworkRemove(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("NetworkRemove "+name, "NetworkRemove "+name); err != nil {
		return err
	}
	n, ok := f.networks[name]
	if !ok {
		return fmt.Errorf("%w: network %s", ErrNotFound, name)
	}
	if len(n.attached) > 0 {
		return fmt.Errorf("%w: network %s still has attached containers", ErrConflict, name)
	}
	delete(f.networks, name)
	for i, got := range f.netOrder {
		if got == name {
			f.netOrder = append(f.netOrder[:i], f.netOrder[i+1:]...)
			break
		}
	}
	return nil
}

func (f *Fake) NetworkConnect(ctx context.Context, networkName, containerName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := "NetworkConnect " + networkName + " " + containerName
	if err := f.record(call, call); err != nil {
		return err
	}
	n, ok := f.networks[networkName]
	if !ok {
		return fmt.Errorf("%w: network %s", ErrNotFound, networkName)
	}
	c, ok := f.containers[containerName]
	if !ok {
		return fmt.Errorf("%w: container %s", ErrNotFound, containerName)
	}
	if n.attached[containerName] {
		return fmt.Errorf("%w: container %s already exists in network %s", ErrConflict, containerName, networkName)
	}
	f.attach(n, c)
	if c.running {
		f.assignIP(n, c)
	}
	return nil
}

func (f *Fake) NetworkDisconnect(ctx context.Context, networkName, containerName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := "NetworkDisconnect " + networkName + " " + containerName
	if err := f.record(call, call); err != nil {
		return err
	}
	n, ok := f.networks[networkName]
	if !ok {
		return fmt.Errorf("%w: network %s", ErrNotFound, networkName)
	}
	c, ok := f.containers[containerName]
	if !ok {
		return fmt.Errorf("%w: container %s", ErrNotFound, containerName)
	}
	if !n.attached[containerName] {
		return fmt.Errorf("%w: container %s is not attached to network %s", ErrNotFound, containerName, networkName)
	}
	delete(n.attached, containerName)
	delete(c.networks, networkName)
	return nil
}

func (f *Fake) ContainerCreate(ctx context.Context, spec ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := "ContainerCreate " + spec.Name
	if err := f.record(call, call); err != nil {
		return "", err
	}
	if _, ok := f.containers[spec.Name]; ok {
		return "", fmt.Errorf("%w: container %s already exists", ErrConflict, spec.Name)
	}
	// Validate every endpoint's network before attaching any: an unknown
	// network fails with nothing attached and no counter touched.
	for _, ep := range spec.Endpoints {
		if _, ok := f.networks[ep.Network]; !ok {
			return "", fmt.Errorf("%w: network %s", ErrNotFound, ep.Network)
		}
	}
	c := &fakeContainer{
		id:       fmt.Sprintf("fake-ctr-%s", spec.Name),
		name:     spec.Name,
		image:    spec.Image,
		labels:   copyLabels(spec.Labels),
		networks: map[string]string{},
	}
	for _, ep := range spec.Endpoints {
		n := f.networks[ep.Network]
		if n.attached[c.name] {
			// Duplicate endpoints on the same network fold into one, like
			// the SDK's EndpointsConfig map.
			continue
		}
		f.attach(n, c)
	}
	f.containers[spec.Name] = c
	f.ctrOrder = append(f.ctrOrder, spec.Name)
	f.Created = append(f.Created, spec)
	f.Specs[spec.Name] = spec
	return c.id, nil
}

func (f *Fake) ContainerStart(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ContainerStart "+name, "ContainerStart "+name); err != nil {
		return err
	}
	c, ok := f.containers[name]
	if !ok {
		return fmt.Errorf("%w: container %s", ErrNotFound, name)
	}
	c.running = true
	c.exitCode = 0
	c.exitSet = false
	// The daemon assigns the container's address on each attached network
	// when it starts.
	for netName := range c.networks {
		f.assignIP(f.networks[netName], c)
	}
	return nil
}

func (f *Fake) ContainerStop(ctx context.Context, name string, timeout time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ContainerStop "+name, "ContainerStop "+name); err != nil {
		return err
	}
	c, ok := f.containers[name]
	if !ok {
		return fmt.Errorf("%w: container %s", ErrNotFound, name)
	}
	c.running = false
	if !c.exitSet {
		c.exitCode = 0
	}
	// The daemon releases the container's addresses on stop.
	for netName := range c.networks {
		c.networks[netName] = ""
	}
	return nil
}

func (f *Fake) ContainerRemove(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ContainerRemove "+name, "ContainerRemove "+name); err != nil {
		return err
	}
	c, ok := f.containers[name]
	if !ok {
		return fmt.Errorf("%w: container %s", ErrNotFound, name)
	}
	for netName := range c.networks {
		if n, ok := f.networks[netName]; ok {
			delete(n.attached, name)
		}
	}
	delete(f.containers, name)
	delete(f.Specs, name)
	for i, got := range f.ctrOrder {
		if got == name {
			f.ctrOrder = append(f.ctrOrder[:i], f.ctrOrder[i+1:]...)
			break
		}
	}
	return nil
}

func (f *Fake) ContainerInspect(ctx context.Context, name string) (Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ContainerInspect "+name, "ContainerInspect "+name); err != nil {
		return Container{}, err
	}
	c, ok := f.containers[name]
	if !ok {
		return Container{}, fmt.Errorf("%w: container %s", ErrNotFound, name)
	}
	return f.containerView(c), nil
}

func (f *Fake) ContainerList(ctx context.Context, labels map[string]string) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ContainerList", "ContainerList"); err != nil {
		return nil, err
	}
	var out []Container
	for _, name := range f.ctrOrder {
		c := f.containers[name]
		if matchLabels(c.labels, labels) {
			v := f.containerView(c)
			v.ExitCode = 0 // the list API cannot report an exit code
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *Fake) Exec(ctx context.Context, containerName string, cmd []string, env []string, stdin []byte) (Exec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	words := cmd
	if len(words) > 2 {
		words = words[:2]
	}
	call := "Exec " + containerName + " " + strings.Join(words, " ")
	f.Calls = append(f.Calls, call)
	// Record the call in Execs first, as the other methods record in Calls.
	f.Execs = append(f.Execs, ExecCall{
		Container: containerName,
		Cmd:       append([]string(nil), cmd...),
		Env:       append([]string(nil), env...),
		Stdin:     append([]byte(nil), stdin...),
	})
	if err, ok := f.Fail["Exec "+containerName]; ok {
		return Exec{}, err
	}
	c, ok := f.containers[containerName]
	if !ok {
		return Exec{}, fmt.Errorf("%w: container %s", ErrNotFound, containerName)
	}
	if !c.running {
		return Exec{}, fmt.Errorf("%w: container %s is not running", ErrConflict, containerName)
	}
	if ex, ok := f.ExecResults[strings.Join(words, " ")]; ok {
		return ex, nil
	}
	return Exec{ExitCode: 0}, nil
}
