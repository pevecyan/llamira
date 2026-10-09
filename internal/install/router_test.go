package install

import (
	"strings"
	"testing"
)

func TestRemoveArg(t *testing.T) {
	argv := strings.Fields("/bin/llama-server --host 0.0.0.0 --models-max 1 --sleep-idle-seconds=300 --no-models-autoload --api-key-file /etc/k")
	got := removeArg(argv, []string{"models-max"}, true)
	if strings.Join(got, " ") != "/bin/llama-server --host 0.0.0.0 --sleep-idle-seconds=300 --no-models-autoload --api-key-file /etc/k" {
		t.Errorf("got %v", got)
	}
	got = removeArg(argv, []string{"sleep-idle-seconds"}, true) // --flag=value form
	if strings.Contains(strings.Join(got, " "), "sleep") || len(got) != len(argv)-1 {
		t.Errorf("got %v", got)
	}
	got = removeArg(argv, []string{"models-autoload", "no-models-autoload"}, false)
	if strings.Contains(strings.Join(got, " "), "autoload") || len(got) != len(argv)-1 {
		t.Errorf("got %v", got)
	}
	got = removeArg(argv, []string{"api-key", "api-key-file"}, true)
	if strings.Contains(strings.Join(got, " "), "/etc/k") {
		t.Errorf("got %v", got)
	}
}
