package docker

import "testing"

func TestHostConfigCarriesMemory(t *testing.T) {
	spec := ContainerSpec{
		Mounts:     []Mount{{Source: "/a", Target: "/b", ReadOnly: true}},
		ShmSize:    1 << 30,
		Memory:     3 << 30,
		MemorySwap: 3 << 30,
		Restart:    "unless-stopped",
	}
	hc := hostConfig(spec)
	if hc.Memory != spec.Memory || hc.MemorySwap != spec.MemorySwap {
		t.Errorf("Resources memory = %d/%d, want %d/%d",
			hc.Memory, hc.MemorySwap, spec.Memory, spec.MemorySwap)
	}
	if hc.ShmSize != spec.ShmSize {
		t.Errorf("ShmSize = %d, want %d", hc.ShmSize, spec.ShmSize)
	}
	if len(hc.Mounts) != 1 || hc.Mounts[0].Source != "/a" || hc.Mounts[0].Target != "/b" || !hc.Mounts[0].ReadOnly {
		t.Errorf("Mounts = %+v, want the one read-only /a -> /b bind", hc.Mounts)
	}
	if string(hc.RestartPolicy.Name) != "unless-stopped" {
		t.Errorf("RestartPolicy = %q, want unless-stopped", hc.RestartPolicy.Name)
	}
}
