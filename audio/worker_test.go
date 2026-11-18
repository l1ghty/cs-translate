package audio

import "testing"

func TestAutoSettingsLeaveCoresForTheGame(t *testing.T) {
	for _, tc := range []struct {
		cpus    int
		model   string
		threads int
	}{{2, "base", 1}, {4, "base", 1}, {6, "small", 2}, {8, "small", 2}, {12, "small", 3}, {32, "small", 4}} {
		if got := AutoModel("cpu", tc.cpus); got != tc.model {
			t.Errorf("AutoModel(cpu,%d) = %s, want %s", tc.cpus, got, tc.model)
		}
		if got := AutoThreads(tc.cpus); got != tc.threads {
			t.Errorf("AutoThreads(%d) = %d, want %d", tc.cpus, got, tc.threads)
		}
		if got := AutoThreads(tc.cpus); got*2 > tc.cpus && tc.cpus > 2 {
			t.Errorf("AutoThreads(%d) = %d uses more than half the cores", tc.cpus, got)
		}
	}
	if got := AutoModel("cuda", 4); got != "large-v3-turbo" {
		t.Errorf("AutoModel(cuda) = %s", got)
	}
}

func TestResolveDefaults(t *testing.T) {
	c := Config{Device: "gpu"}.Resolve()
	if c.Device != "cuda" || c.Model != "large-v3-turbo" || c.Threads < 1 || c.BufferSecs != 30 {
		t.Errorf("unexpected resolve: %+v", c)
	}
	c = Config{Model: "tiny", Threads: 3}.Resolve()
	if c.Device != "cpu" || c.Model != "tiny" || c.Threads != 3 {
		t.Errorf("explicit settings changed: %+v", c)
	}
}
